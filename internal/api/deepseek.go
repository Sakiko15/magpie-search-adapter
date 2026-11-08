package api

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"magpie-search-adapter/internal/magpie"
)

type deepseekSearchRequest struct {
	Query        string  `json:"query"`
	Count        *int    `json:"count"`
	NeedContent  bool    `json:"need_content"`
	OriQuestion  *string `json:"ori_question"`
	Localization string  `json:"localization"`
	Model        string  `json:"model"`
}

type deepseekResult struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content"`
	SiteName    *string `json:"site_name"`
	SiteIcon    *string `json:"site_icon"`
	Date        *string `json:"date"`
	Authority   int     `json:"authority"`
}

type deepseekSearchResponse struct {
	Results []deepseekResult `json:"results"`
}

// DeepSeekSearch is POST /v1/tools/search, the DeepSeek Search API shape.
func (h *Handlers) DeepSeekSearch(w http.ResponseWriter, r *http.Request) {
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	var req deepseekSearchRequest
	if err := decodeBody(body, &req); err != nil {
		writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := validQuery(req.Query, 128); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	count := 10
	if req.Count != nil {
		count = *req.Count
	}
	if count < 1 || count > 50 {
		writeError(w, r, body, http.StatusBadRequest, "count must be between 1 and 50")
		return
	}
	if req.OriQuestion != nil {
		ori := strings.TrimSpace(*req.OriQuestion)
		if ori != "" && utf8.RuneCountInString(ori) > 128 {
			writeError(w, r, body, http.StatusBadRequest, "ori_question is longer than 128 characters")
			return
		}
	}
	upstream := count
	if upstream > h.MaxCount {
		upstream = h.MaxCount
	}

	results, err := h.Backend.Search(r.Context(), strings.TrimSpace(req.Query), upstream, nil)
	if err != nil {
		writeUpstreamError(w, r, body, err)
		return
	}
	if len(results) > upstream {
		results = results[:upstream]
	}
	out := deepseekSearchResponse{Results: make([]deepseekResult, 0, len(results))}
	for i, result := range results {
		content := result.Content
		snippet := magpie.Snippet(content, 200)
		if snippet == "" {
			snippet = result.Snippet
		}
		summary := magpie.Snippet(content, 800)
		var siteName *string
		if host := magpie.HostOf(result.URL); host != "" {
			siteName = &host
		}
		out.Results = append(out.Results, deepseekResult{
			RerankScore: deepseekScore(i),
			Title:       result.Title,
			URL:         result.URL,
			Snippet:     snippet,
			Summary:     summary,
			Content:     pick(req.NeedContent, content),
			SiteName:    siteName,
			SiteIcon:    nil,
			Date:        nil,
			Authority:   -1,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type deepseekFetchRequest struct {
	URL   string `json:"url"`
	Model string `json:"model"`
}

type deepseekFetchResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// DeepSeekFetch is POST /v1/tools/fetch.
func (h *Handlers) DeepSeekFetch(w http.ResponseWriter, r *http.Request) {
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	var req deepseekFetchRequest
	if err := decodeBody(body, &req); err != nil {
		writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := validateFetchURL(req.URL); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	if !h.FetchEnabled {
		writeError(w, r, body, http.StatusNotImplemented, "web_fetch is disabled (MAGPIE_FETCH_MODE=off)")
		return
	}
	title, content, err := h.Backend.Fetch(r.Context(), strings.TrimSpace(req.URL), h.FetchMaxChars)
	if err != nil {
		writeUpstreamError(w, r, body, err)
		return
	}
	writeJSON(w, http.StatusOK, deepseekFetchResponse{Title: title, Content: content})
}

func pick(ok bool, value string) string {
	if ok {
		return value
	}
	return ""
}
