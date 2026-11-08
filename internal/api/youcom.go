package api

import (
	"net/http"
	"strings"
)

type youRequest struct {
	Query          string   `json:"query"`
	Count          *int     `json:"count"`
	Offset         *int     `json:"offset"`
	Freshness      string   `json:"freshness"`
	Country        string   `json:"country"`
	Language       string   `json:"language"`
	Safesearch     string   `json:"safesearch"`
	IncludeDomains []string `json:"include_domains"`
	ExcludeDomains []string `json:"exclude_domains"`
}

type youWebResult struct {
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Snippets     []string `json:"snippets"`
	ThumbnailURL *string  `json:"thumbnail_url"`
	PageAge      *string  `json:"page_age"`
	FaviconURL   *string  `json:"favicon_url"`
}

type youResponse struct {
	Results struct {
		Web       []youWebResult `json:"web"`
		Knowledge []any          `json:"knowledge"`
	} `json:"results"`
	Metadata struct {
		SearchUUID string  `json:"search_uuid"`
		Query      string  `json:"query"`
		Latency    float64 `json:"latency"`
	} `json:"metadata"`
}

// YouComSearch serves POST/GET /you/v1/search and the native GET
// /v1/search (the POST alias dispatches between You.com and Firecrawl).
func (h *Handlers) YouComSearch(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Method == http.MethodPost {
		var err error
		body, err = requestBody(r)
		if err != nil {
			writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
			return
		}
	}
	h.youComSearch(w, r, body)
}

func (h *Handlers) youComSearch(w http.ResponseWriter, r *http.Request, body []byte) {
	var req youRequest
	if r.Method == http.MethodPost {
		if err := decodeBody(body, &req); err != nil {
			writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	} else {
		values := r.URL.Query()
		req.Query = values.Get("query")
		req.Count = intParam(values, "count")
		req.Offset = intParam(values, "offset")
	}
	query := strings.TrimSpace(req.Query)
	if err := validQuery(query, 2048); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	count, err := h.resolveCount(req.Count, countSpec{Default: 10, Max: 100})
	if err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
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

	// offset is a page index: offset*count results are skipped.
	if req.Offset != nil && (*req.Offset < 0 || *req.Offset > 9) {
		writeError(w, r, body, http.StatusBadRequest, "offset must be 0..9")
		return
	}
	skip := 0
	if req.Offset != nil && *req.Offset > 0 {
		skip = *req.Offset * count
	}
	if skip > h.MaxCount {
		skip = h.MaxCount
	}
	upstream := count + skip
	if len(include) > 0 || len(exclude) > 0 || upstream > h.MaxCount {
		upstream = h.MaxCount
	}
	results, err := h.Backend.Search(r.Context(), query, upstream, include)
	if err != nil {
		writeUpstreamError(w, r, body, err)
		return
	}
	results = filterDomains(results, include, exclude)
	if skip >= len(results) {
		results = nil
	} else {
		results = results[skip:]
	}
	if len(results) > count {
		results = results[:count]
	}

	out := youResponse{}
	out.Results.Web = make([]youWebResult, 0, len(results))
	out.Results.Knowledge = []any{}
	out.Metadata.SearchUUID = randomID(12)
	out.Metadata.Query = query
	for _, result := range results {
		out.Results.Web = append(out.Results.Web, youWebResult{
			URL:          result.URL,
			Title:        result.Title,
			Description:  contentOf(result),
			Snippets:     []string{snippetOf(result)},
			ThumbnailURL: nil,
			PageAge:      nil,
			FaviconURL:   nil,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
