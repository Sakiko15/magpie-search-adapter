package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type zhipuRequest struct {
	SearchQuery         string `json:"search_query"`
	Query               string `json:"query"`
	SearchEngine        string `json:"search_engine"`
	SearchIntent        *bool  `json:"search_intent"`
	Count               *int   `json:"count"`
	SearchDomainFilter  any    `json:"search_domain_filter"`
	SearchRecencyFilter string `json:"search_recency_filter"`
	ContentSize         string `json:"content_size"`
}

type zhipuIntent struct {
	Query    string `json:"query"`
	Intent   string `json:"intent"`
	Keywords string `json:"keywords"`
}

type zhipuResult struct {
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Link        string  `json:"link"`
	Media       string  `json:"media"`
	Icon        *string `json:"icon"`
	Refer       string  `json:"refer"`
	PublishDate *string `json:"publish_date"`
}

type zhipuResponse struct {
	ID           string        `json:"id"`
	Created      int64         `json:"created"`
	RequestID    string        `json:"request_id"`
	SearchIntent []zhipuIntent `json:"search_intent"`
	SearchResult []zhipuResult `json:"search_result"`
}

// ZhipuSearch serves POST /api/paas/v4/web_search and /zhipu/v4/web_search.
func (h *Handlers) ZhipuSearch(w http.ResponseWriter, r *http.Request) {
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	var req zhipuRequest
	if err := decodeBody(body, &req); err != nil {
		writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	query := strings.TrimSpace(req.SearchQuery)
	if query == "" {
		query = strings.TrimSpace(req.Query)
	}
	if err := validQuery(query, 2048); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	count, err := h.resolveCount(req.Count, countSpec{Default: 10, Max: 50, HardMax: 50})
	if err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	results, err := h.Backend.Search(r.Context(), query, count, nil)
	if err != nil {
		writeUpstreamError(w, r, body, err)
		return
	}
	if len(results) > count {
		results = results[:count]
	}

	id := randomID(12)
	out := zhipuResponse{
		ID:        id,
		Created:   time.Now().Unix(),
		RequestID: id,
		SearchIntent: []zhipuIntent{{
			Query:    query,
			Intent:   "SEARCH_ALWAYS",
			Keywords: query,
		}},
		SearchResult: make([]zhipuResult, 0, len(results)),
	}
	for i, result := range results {
		out.SearchResult = append(out.SearchResult, zhipuResult{
			Title:       result.Title,
			Content:     contentOf(result),
			Link:        result.URL,
			Media:       hostOf(result.URL),
			Icon:        nil,
			Refer:       strconv.Itoa(i + 1),
			PublishDate: nil,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
