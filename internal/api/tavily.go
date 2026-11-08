package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type tavilyRequest struct {
	Query             string          `json:"query"`
	MaxResults        *int            `json:"max_results"`
	IncludeRawContent json.RawMessage `json:"include_raw_content"`
	IncludeDomains    []string        `json:"include_domains"`
	ExcludeDomains    []string        `json:"exclude_domains"`
}

type tavilyResult struct {
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Content    string  `json:"content"`
	Score      float64 `json:"score"`
	RawContent *string `json:"raw_content"`
}

type tavilyResponse struct {
	Query        string         `json:"query"`
	Results      []tavilyResult `json:"results"`
	ResponseTime float64        `json:"response_time"`
	Answer       *string        `json:"answer"`
}

func (h *Handlers) handleTavily(w http.ResponseWriter, r *http.Request, body []byte) {
	var req tavilyRequest
	if err := decodeBody(body, &req); err != nil {
		writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := validQuery(req.Query, 2048); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	count := clampCount(5, 5, h.MaxCount)
	if req.MaxResults != nil {
		count = clampCount(*req.MaxResults, 5, h.MaxCount)
	}
	include, err := normalizeDomains(req.IncludeDomains)
	if err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	exclude, err := normalizeDomains(req.ExcludeDomains)
	if err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	upstream := count
	if len(include) > 0 || len(exclude) > 0 {
		upstream = h.MaxCount
	}

	start := time.Now()
	results, err := h.Backend.Search(r.Context(), strings.TrimSpace(req.Query), upstream, include)
	if err != nil {
		writeUpstreamError(w, r, body, err)
		return
	}
	results = filterDomains(results, include, exclude)
	if len(results) > count {
		results = results[:count]
	}

	includeRaw := boolOrString(req.IncludeRawContent)
	out := make([]tavilyResult, 0, len(results))
	for i, result := range results {
		content := result.Content
		if content == "" {
			content = result.Snippet
		}
		var raw *string
		if includeRaw {
			rc := content
			raw = &rc
		}
		score := result.Score
		if score <= 0 {
			score = tavilyScore(i)
		}
		out = append(out, tavilyResult{
			Title:      result.Title,
			URL:        result.URL,
			Content:    content,
			Score:      score,
			RawContent: raw,
		})
	}
	writeJSON(w, http.StatusOK, tavilyResponse{
		Query:        req.Query,
		Results:      out,
		ResponseTime: time.Since(start).Seconds(),
	})
}

type extractRequest struct {
	URLs          []string `json:"urls"`
	IncludeImages bool     `json:"include_images"`
	ExtractDepth  string   `json:"extract_depth"`
}

type extractResult struct {
	URL        string `json:"url"`
	RawContent string `json:"raw_content"`
}

type extractFailure struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

type extractResponse struct {
	Results       []extractResult  `json:"results"`
	FailedResults []extractFailure `json:"failed_results"`
}

// TavilyExtract is Tavily's POST /extract, implemented with Magpie's
// web_fetch.
func (h *Handlers) TavilyExtract(w http.ResponseWriter, r *http.Request) {
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	var req extractRequest
	if err := decodeBody(body, &req); err != nil {
		writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(req.URLs) == 0 {
		writeError(w, r, body, http.StatusBadRequest, "urls is required")
		return
	}
	if len(req.URLs) > 20 {
		writeError(w, r, body, http.StatusBadRequest, "at most 20 urls are accepted")
		return
	}
	if !h.FetchEnabled {
		writeError(w, r, body, http.StatusNotImplemented, "web_fetch is disabled (MAGPIE_FETCH_MODE=off)")
		return
	}

	success := make(map[string]string, len(req.URLs))
	// prefailed is finished before any worker starts, so the dispatch loop
	// below can read it while the workers fill failed/success.
	prefailed := make(map[string]string)
	for _, rawURL := range req.URLs {
		if err := validateFetchURL(rawURL); err != nil {
			prefailed[rawURL] = err.Error()
		}
	}
	failed := make(map[string]string)
	var mu sync.Mutex
	jobs := make(chan string, len(req.URLs))
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(r.Context(), h.extractTimeout())
	defer cancel()
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rawURL := range jobs {
				_, content, err := h.Backend.Fetch(ctx, rawURL, h.FetchMaxChars)
				mu.Lock()
				if err != nil {
					failed[rawURL] = redact(err.Error())
				} else {
					success[rawURL] = content
				}
				mu.Unlock()
			}
		}()
	}
	for _, rawURL := range req.URLs {
		if _, bad := prefailed[rawURL]; bad {
			continue
		}
		jobs <- rawURL
	}
	close(jobs)
	wg.Wait()

	out := extractResponse{
		Results:       make([]extractResult, 0, len(success)),
		FailedResults: make([]extractFailure, 0, len(prefailed)+len(failed)),
	}
	for _, rawURL := range req.URLs {
		if reason, ok := prefailed[rawURL]; ok {
			out.FailedResults = append(out.FailedResults, extractFailure{URL: rawURL, Error: reason})
			continue
		}
		if reason, ok := failed[rawURL]; ok {
			out.FailedResults = append(out.FailedResults, extractFailure{URL: rawURL, Error: reason})
			continue
		}
		if content, ok := success[rawURL]; ok {
			out.Results = append(out.Results, extractResult{URL: rawURL, RawContent: content})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ExtractBatchTimeout bounds one /extract batch: three workers, each page
// bounded by FETCH_TIMEOUT, so the worst case is three sequential fetches per
// worker. It is also used for the HTTP server's write timeout.
func ExtractBatchTimeout(fetch time.Duration) time.Duration {
	if fetch > 0 {
		return fetch * 3
	}
	return 110 * time.Second
}

func (h *Handlers) extractTimeout() time.Duration {
	return ExtractBatchTimeout(h.FetchTimeout)
}
