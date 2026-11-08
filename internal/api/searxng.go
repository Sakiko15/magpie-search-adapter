package api

import (
	"net/http"
	"strconv"
	"strings"
)

type searxngResult struct {
	URL           string   `json:"url"`
	Title         string   `json:"title"`
	Content       string   `json:"content"`
	Engine        string   `json:"engine"`
	Engines       []string `json:"engines"`
	Positions     []int    `json:"positions"`
	Score         float64  `json:"score"`
	Category      string   `json:"category"`
	PrettyURL     string   `json:"pretty_url"`
	PublishedDate *string  `json:"publishedDate"`
	Thumbnail     *string  `json:"thumbnail"`
	Template      string   `json:"template"`
	ParsedURL     []string `json:"parsed_url"`
}

type searxngResponse struct {
	Query               string          `json:"query"`
	NumberOfResults     int             `json:"number_of_results"`
	Results             []searxngResult `json:"results"`
	Answers             []any           `json:"answers"`
	Corrections         []any           `json:"corrections"`
	Infoboxes           []any           `json:"infoboxes"`
	Suggestions         []any           `json:"suggestions"`
	UnresponsiveEngines []any           `json:"unresponsive_engines"`
}

// SearxngSearch serves GET/POST /searxng/search and the native GET /search.
// Only the first results page exists: pageno > 1 answers an empty page.
func (h *Handlers) SearxngSearch(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Method == http.MethodPost {
		var err error
		body, err = requestBody(r)
		if err != nil {
			writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
			return
		}
	}
	values, err := formValues(r, body)
	if err != nil {
		writeError(w, r, body, http.StatusBadRequest, "the form body could not be read")
		return
	}
	query := strings.TrimSpace(values.Get("q"))
	if err := validQuery(query, 2048); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	count, err := h.resolveCount(intParam(values, "count", "limit", "num"), countSpec{Default: h.DefaultCount})
	if err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}

	out := searxngResponse{
		Query:               query,
		Results:             []searxngResult{},
		Answers:             []any{},
		Corrections:         []any{},
		Infoboxes:           []any{},
		Suggestions:         []any{},
		UnresponsiveEngines: []any{},
	}
	if page, _ := strconv.Atoi(values.Get("pageno")); page > 1 {
		writeJSON(w, http.StatusOK, out)
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
	for i, result := range results {
		out.Results = append(out.Results, searxngResult{
			URL:           result.URL,
			Title:         result.Title,
			Content:       contentOf(result),
			Engine:        "magpie",
			Engines:       []string{"magpie"},
			Positions:     []int{i + 1},
			Score:         tavilyScore(i),
			Category:      "general",
			PrettyURL:     prettyURL(result.URL),
			PublishedDate: nil,
			Thumbnail:     nil,
			Template:      "default.html",
			ParsedURL:     parsedURL(result.URL),
		})
	}
	out.NumberOfResults = len(out.Results)
	writeJSON(w, http.StatusOK, out)
}
