package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// Search dispatches /search to Tavily or Serper by the request body, with
// the X-API-KEY header as a tie-breaker when both fields are present. Form
// bodies and GET /search belong to SearXNG.
func (h *Handlers) Search(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		h.SearxngSearch(w, r)
		return
	}
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	var fields map[string]json.RawMessage
	if len(bytes.TrimSpace(body)) > 0 {
		if json.Unmarshal(body, &fields) != nil {
			writeError(w, r, body, http.StatusBadRequest, "the request body is not a JSON object")
			return
		}
	}
	_, hasQuery := fields["query"]
	_, hasQ := fields["q"]
	if hasQuery && !hasQ && exaRequested(fields) {
		h.exaSearch(w, r, body)
		return
	}
	switch {
	case hasQuery && hasQ:
		if r.Header.Get("X-API-KEY") != "" {
			h.handleSerper(w, r, body)
		} else if r.Header.Get("Authorization") != "" {
			h.handleTavily(w, r, body)
		} else {
			writeError(w, r, body, http.StatusBadRequest, "the request names both query and q; use one")
		}
	case hasQuery:
		h.handleTavily(w, r, body)
	case hasQ:
		h.handleSerper(w, r, body)
	case r.Header.Get("X-API-KEY") != "":
		h.handleSerper(w, r, body)
	default:
		h.handleTavily(w, r, body)
	}
}

// exaRequested spots an Exa body on the shared POST /search path by its
// own fields; Tavily and Serper do not use them.
func exaRequested(fields map[string]json.RawMessage) bool {
	for _, key := range []string{"numResults", "contents", "additionalQueries"} {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}
