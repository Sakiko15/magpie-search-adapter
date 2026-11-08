package api

import (
	"net/http"
	"strconv"
	"strings"
)

type braveResult struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Description string  `json:"description"`
	PageAge     *string `json:"page_age"`
}

type braveResponse struct {
	Type string `json:"type"`
	Web  struct {
		Results []braveResult `json:"results"`
	} `json:"web"`
}

// BraveSearch is GET /res/v1/web/search.
func (h *Handlers) BraveSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if err := validQuery(q, 2048); err != nil {
		writeError(w, r, nil, http.StatusBadRequest, err.Error())
		return
	}
	count := 20
	if raw := r.URL.Query().Get("count"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			count = clampCount(n, 20, h.MaxCount)
		} else {
			count = clampCount(20, 20, h.MaxCount)
		}
	}
	count = clampCount(count, 20, h.MaxCount)

	results, err := h.Backend.Search(r.Context(), strings.TrimSpace(q), count, nil)
	if err != nil {
		writeUpstreamError(w, r, nil, err)
		return
	}
	if len(results) > count {
		results = results[:count]
	}
	var out braveResponse
	out.Type = "search"
	out.Web.Results = make([]braveResult, 0, len(results))
	for _, result := range results {
		description := result.Snippet
		if description == "" {
			description = result.Content
		}
		out.Web.Results = append(out.Web.Results, braveResult{
			Title:       result.Title,
			URL:         result.URL,
			Description: description,
			PageAge:     nil,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
