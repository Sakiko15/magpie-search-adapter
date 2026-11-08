package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"magpie-search-adapter/internal/apierr"
)

// Search dispatches POST /search to the vendor shape that both the handler
// and the error envelope agree on. GET /search and form bodies belong to
// SearXNG; apierr.SearchVendor makes that decision for both paths.
func (h *Handlers) Search(w http.ResponseWriter, r *http.Request) {
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	vendor := apierr.SearchVendor(r, body)
	if vendor == apierr.Tavily || vendor == apierr.Serper {
		var fields map[string]json.RawMessage
		if len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &fields) == nil {
			_, hasQuery := fields["query"]
			_, hasQ := fields["q"]
			// Headers remain the tie-breaker when both fields are present.
			if hasQuery && hasQ && r.Header.Get("X-API-KEY") == "" && r.Header.Get("Authorization") == "" {
				writeError(w, r, body, http.StatusBadRequest, "the request names both query and q; use one")
				return
			}
		}
	}
	switch vendor {
	case apierr.Exa:
		h.exaSearch(w, r, body)
	case apierr.Serper:
		h.handleSerper(w, r, body)
	case apierr.SearXNG:
		h.SearxngSearch(w, r)
	default:
		h.handleTavily(w, r, body)
	}
}
