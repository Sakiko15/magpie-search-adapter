package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type exaRequest struct {
	Query          string       `json:"query"`
	NumResults     *int         `json:"numResults"`
	IncludeDomains []string     `json:"includeDomains"`
	ExcludeDomains []string     `json:"excludeDomains"`
	Contents       *exaContents `json:"contents"`
}

type exaContents struct {
	Text       json.RawMessage `json:"text"`
	Highlights json.RawMessage `json:"highlights"`
	Summary    json.RawMessage `json:"summary"`
}

type exaResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	PublishedDate *string  `json:"publishedDate"`
	Author        *string  `json:"author"`
	ID            string   `json:"id"`
	Image         *string  `json:"image"`
	Favicon       *string  `json:"favicon"`
	Text          string   `json:"text,omitempty"`
	Highlights    []string `json:"highlights,omitempty"`
	Summary       string   `json:"summary,omitempty"`
}

type exaResponse struct {
	RequestID   string      `json:"requestId"`
	Results     []exaResult `json:"results"`
	CostDollars struct {
		Total float64 `json:"total"`
	} `json:"costDollars"`
	SearchTime float64 `json:"searchTime"`
}

// ExaSearch serves POST /exa/search.
func (h *Handlers) ExaSearch(w http.ResponseWriter, r *http.Request) {
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	h.exaSearch(w, r, body)
}

func (h *Handlers) exaSearch(w http.ResponseWriter, r *http.Request, body []byte) {
	var req exaRequest
	if err := decodeBody(body, &req); err != nil {
		writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	query := strings.TrimSpace(req.Query)
	if err := validQuery(query, 2048); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	count, err := h.resolveCount(req.NumResults, countSpec{Default: 10, Max: 100})
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
	upstream := count
	if len(include) > 0 || len(exclude) > 0 {
		upstream = h.MaxCount
	}

	began := time.Now()
	results, err := h.Backend.Search(r.Context(), query, upstream, include)
	if err != nil {
		writeUpstreamError(w, r, body, err)
		return
	}
	results = filterDomains(results, include, exclude)
	if len(results) > count {
		results = results[:count]
	}

	wantText := req.Contents != nil && jsonWants(req.Contents.Text)
	wantHighlights := req.Contents != nil && jsonWants(req.Contents.Highlights)
	wantSummary := req.Contents != nil && jsonWants(req.Contents.Summary)

	out := exaResponse{RequestID: randomID(12)}
	out.Results = make([]exaResult, 0, len(results))
	for _, result := range results {
		item := exaResult{
			Title:         result.Title,
			URL:           result.URL,
			PublishedDate: nil,
			Author:        nil,
			ID:            resultID(result.URL),
			Image:         nil,
			Favicon:       nil,
		}
		if wantText {
			item.Text = contentOf(result)
		}
		if wantHighlights {
			item.Highlights = []string{snippetOf(result)}
		}
		if wantSummary {
			item.Summary = contentOf(result)
		}
		out.Results = append(out.Results, item)
	}
	out.SearchTime = time.Since(began).Seconds()
	writeJSON(w, http.StatusOK, out)
}

// jsonWants reports whether a raw JSON flag asks for content, accepting
// both true and an options object.
func jsonWants(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch string(trimmed) {
	case "false", "null":
		return false
	}
	return true
}

func resultID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
