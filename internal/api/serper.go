package api

import (
	"net/http"
	"strings"
)

type serperRequest struct {
	Q   string `json:"q"`
	Num *int   `json:"num"`
}

type serperResult struct {
	Title    string `json:"title"`
	Link     string `json:"link"`
	Snippet  string `json:"snippet"`
	Position int    `json:"position"`
}

type serperResponse struct {
	SearchParameters struct {
		Q      string `json:"q"`
		Type   string `json:"type"`
		Engine string `json:"engine"`
	} `json:"searchParameters"`
	Organic []serperResult `json:"organic"`
}

func (h *Handlers) handleSerper(w http.ResponseWriter, r *http.Request, body []byte) {
	var req serperRequest
	if err := decodeBody(body, &req); err != nil {
		writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := validQuery(req.Q, 2048); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	count := clampCount(10, 10, h.MaxCount)
	if req.Num != nil {
		count = clampCount(*req.Num, 10, h.MaxCount)
	}
	results, err := h.Backend.Search(r.Context(), strings.TrimSpace(req.Q), count, nil)
	if err != nil {
		writeUpstreamError(w, r, body, err)
		return
	}
	if len(results) > count {
		results = results[:count]
	}
	out := serperResponse{Organic: make([]serperResult, 0, len(results))}
	out.SearchParameters.Q = req.Q
	out.SearchParameters.Type = "search"
	out.SearchParameters.Engine = "google"
	for i, result := range results {
		snippet := result.Snippet
		if snippet == "" {
			snippet = result.Content
		}
		out.Organic = append(out.Organic, serperResult{
			Title:    result.Title,
			Link:     result.URL,
			Snippet:  snippet,
			Position: i + 1,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
