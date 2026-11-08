package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

type jinaRequest struct {
	Q     string `json:"q"`
	Query string `json:"query"`
	Num   *int   `json:"num"`
	Count *int   `json:"count"`
}

type jinaResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Content     string `json:"content"`
	Description string `json:"description"`
}

type jinaResponse struct {
	Code   int          `json:"code"`
	Status int          `json:"status"`
	Data   []jinaResult `json:"data"`
}

// JinaSearch serves GET/POST /jina and /jina/search. Jina's own search
// endpoint is a bare GET with ?q=, so this path prefix is the base URL.
func (h *Handlers) JinaSearch(w http.ResponseWriter, r *http.Request) {
	var (
		body []byte
		req  jinaRequest
	)
	values := r.URL.Query()
	if r.Method == http.MethodPost {
		var err error
		body, err = requestBody(r)
		if err != nil {
			writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
			return
		}
		if len(body) > 0 && json.Valid(body) {
			if err := decodeBody(body, &req); err != nil {
				writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
				return
			}
		} else if len(body) > 0 {
			form, err := url.ParseQuery(string(body))
			if err != nil {
				writeError(w, r, body, http.StatusBadRequest, "the form body could not be read")
				return
			}
			for key, list := range form {
				values[key] = append(values[key], list...)
			}
		}
	}

	query := strings.TrimSpace(req.Q)
	if query == "" {
		query = strings.TrimSpace(req.Query)
	}
	if query == "" {
		query = strings.TrimSpace(values.Get("q"))
	}
	if err := validQuery(query, 2048); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	requested := req.Count
	if requested == nil {
		requested = req.Num
	}
	if requested == nil {
		requested = intParam(values, "num", "count")
	}
	count, err := h.resolveCount(requested, countSpec{Default: 5, Max: 20})
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
	out := jinaResponse{Code: 200, Status: 20000, Data: make([]jinaResult, 0, len(results))}
	for _, result := range results {
		out.Data = append(out.Data, jinaResult{
			Title:       result.Title,
			URL:         result.URL,
			Content:     contentOf(result),
			Description: snippetOf(result),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
