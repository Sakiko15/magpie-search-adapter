package api

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type googleItem struct {
	Kind             string  `json:"kind"`
	Title            string  `json:"title"`
	HTMLTitle        string  `json:"htmlTitle"`
	Link             string  `json:"link"`
	DisplayLink      string  `json:"displayLink"`
	Snippet          string  `json:"snippet"`
	HTMLSnippet      string  `json:"htmlSnippet"`
	FormattedURL     string  `json:"formattedUrl"`
	HTMLFormattedURL string  `json:"htmlFormattedUrl"`
	Mime             *string `json:"mime"`
	FileFormat       *string `json:"fileFormat"`
	Image            any     `json:"image"`
	Pagemap          any     `json:"pagemap"`
}

type googleQuery struct {
	Title          string `json:"title"`
	TotalResults   string `json:"totalResults"`
	SearchTerms    string `json:"searchTerms"`
	Count          int    `json:"count"`
	StartIndex     int    `json:"startIndex"`
	StartPage      int    `json:"startPage"`
	Language       string `json:"language"`
	InputEncoding  string `json:"inputEncoding"`
	OutputEncoding string `json:"outputEncoding"`
	Safe           string `json:"safe"`
	CX             string `json:"cx"`
	Filter         string `json:"filter"`
}

type googleResponse struct {
	Kind string `json:"kind"`
	URL  struct {
		Type     string `json:"type"`
		Template string `json:"template"`
	} `json:"url"`
	Queries struct {
		Request  []googleQuery `json:"request"`
		NextPage []googleQuery `json:"nextPage,omitempty"`
	} `json:"queries"`
	Context struct {
		Title string `json:"title"`
	} `json:"context"`
	SearchInformation struct {
		SearchTime            float64 `json:"searchTime"`
		FormattedSearchTime   string  `json:"formattedSearchTime"`
		TotalResults          string  `json:"totalResults"`
		FormattedTotalResults string  `json:"formattedTotalResults"`
	} `json:"searchInformation"`
	Items []googleItem `json:"items"`
}

// GoogleSearch serves GET /customsearch/v1 and /google/customsearch/v1.
func (h *Handlers) GoogleSearch(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	query := strings.TrimSpace(values.Get("q"))
	if err := validQuery(query, 2048); err != nil {
		writeError(w, r, nil, http.StatusBadRequest, err.Error())
		return
	}
	count, err := h.resolveCount(intParam(values, "num"), countSpec{Default: 10, Max: 10, HardMax: 10})
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, err.Error())
		return
	}
	start := 1
	if raw := strings.TrimSpace(values.Get("start")); raw != "" {
		start, err = strconv.Atoi(raw)
		if err != nil || start < 1 {
			writeError(w, r, nil, http.StatusBadRequest, "start must be a positive integer")
			return
		}
	}
	if start > 100 {
		writeError(w, r, nil, http.StatusBadRequest, "start must not exceed 100")
		return
	}
	if start+count-1 > 100 {
		writeError(w, r, nil, http.StatusBadRequest, "start + num must not exceed 100")
		return
	}

	began := time.Now()
	var include, exclude []string
	include, exclude, err = googleDomainFilter(values)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, err.Error())
		return
	}
	upstream := start - 1 + count
	if len(include) > 0 || len(exclude) > 0 || upstream > h.MaxCount {
		upstream = h.MaxCount
	}
	results, err := h.Backend.Search(r.Context(), query, upstream, include)
	if err != nil {
		writeUpstreamError(w, r, nil, err)
		return
	}
	results = filterDomains(results, include, exclude)

	out := googleResponse{Kind: "customsearch#search"}
	out.URL.Type = "application/json"
	out.URL.Template = ""
	out.Context.Title = "Magpie Search"
	out.Items = []googleItem{}
	offset := start - 1
	if offset < len(results) {
		end := offset + count
		if end > len(results) {
			end = len(results)
		}
		for _, result := range results[offset:end] {
			snippet := snippetOf(result)
			// Google's displayLink is the bare host; formattedUrl keeps the
			// full link.
			display := result.URL
			if parsed, err := url.Parse(result.URL); err == nil && parsed.Host != "" {
				display = parsed.Host
			}
			out.Items = append(out.Items, googleItem{
				Kind:             "customsearch#result",
				Title:            result.Title,
				HTMLTitle:        html.EscapeString(result.Title),
				Link:             result.URL,
				DisplayLink:      display,
				Snippet:          snippet,
				HTMLSnippet:      html.EscapeString(snippet),
				FormattedURL:     result.URL,
				HTMLFormattedURL: html.EscapeString(result.URL),
				Mime:             nil,
				FileFormat:       nil,
				Image:            nil,
				Pagemap:          nil,
			})
		}
	}
	safe := values.Get("safe")
	if safe == "" {
		safe = "off"
	}
	request := googleQuery{
		Title:          "Google Custom Search - " + query,
		TotalResults:   strconv.Itoa(len(results)),
		SearchTerms:    query,
		Count:          len(out.Items),
		StartIndex:     start,
		StartPage:      (start-1)/count + 1,
		Language:       values.Get("hl"),
		InputEncoding:  "utf8",
		OutputEncoding: "utf8",
		Safe:           safe,
		CX:             values.Get("cx"),
		Filter:         "1",
	}
	out.Queries.Request = []googleQuery{request}
	if offset+count < len(results) {
		next := request
		next.StartIndex = start + count
		next.StartPage++
		out.Queries.NextPage = []googleQuery{next}
	}
	out.SearchInformation.SearchTime = time.Since(began).Seconds()
	out.SearchInformation.FormattedSearchTime = fmt.Sprintf("%.2f", out.SearchInformation.SearchTime)
	out.SearchInformation.TotalResults = strconv.Itoa(len(results))
	out.SearchInformation.FormattedTotalResults = strconv.Itoa(len(results))
	writeJSON(w, http.StatusOK, out)
}

// googleDomainFilter maps siteSearch/siteSearchFilter onto our domain
// filter; e means exclude, i means include. Entries are normalised the
// same way as every other vendor's domain list.
func googleDomainFilter(values url.Values) (include, exclude []string, err error) {
	site := strings.TrimSpace(values.Get("siteSearch"))
	if site == "" {
		return nil, nil, nil
	}
	domains, err := normalizeDomains([]string{site})
	if err != nil {
		return nil, nil, err
	}
	if values.Get("siteSearchFilter") == "e" {
		return nil, domains, nil
	}
	return domains, nil, nil
}
