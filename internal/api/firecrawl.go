package api

import (
	"net/http"
	"strings"

	"magpie-search-adapter/internal/apierr"
	"magpie-search-adapter/internal/magpie"
)

type firecrawlRequest struct {
	Query          string   `json:"query"`
	Limit          *int     `json:"limit"`
	IncludeDomains []string `json:"includeDomains"`
	ExcludeDomains []string `json:"excludeDomains"`
}

type firecrawlMetadata struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	SourceURL   string  `json:"sourceURL"`
	URL         string  `json:"url"`
	StatusCode  *int    `json:"statusCode,omitempty"`
	NumPages    int     `json:"numPages"`
	TotalPages  int     `json:"totalPages"`
	Error       *string `json:"error"`
}

type firecrawlWeb struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	URL         string            `json:"url"`
	Markdown    *string           `json:"markdown"`
	HTML        *string           `json:"html"`
	RawHTML     *string           `json:"rawHtml"`
	Links       []string          `json:"links"`
	Screenshot  *string           `json:"screenshot"`
	Audio       *string           `json:"audio"`
	Video       *string           `json:"video"`
	Metadata    firecrawlMetadata `json:"metadata"`
}

type firecrawlV1Response struct {
	Success bool           `json:"success"`
	Data    []firecrawlWeb `json:"data"`
}

type firecrawlV2Response struct {
	Success bool `json:"success"`
	Data    struct {
		Web    []firecrawlWeb `json:"web"`
		Images []any          `json:"images"`
		News   []any          `json:"news"`
	} `json:"data"`
	Warning     *string `json:"warning"`
	ID          string  `json:"id"`
	CreditsUsed int     `json:"creditsUsed"`
}

// FirecrawlV1 serves POST /firecrawl/v1/search.
func (h *Handlers) FirecrawlV1(w http.ResponseWriter, r *http.Request) {
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	h.firecrawlV1(w, r, body)
}

// V1Search serves the native POST /v1/search: You.com owns it when it
// sends X-API-Key, otherwise it is Firecrawl v1.
func (h *Handlers) V1Search(w http.ResponseWriter, r *http.Request) {
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	if apierr.Detect(r, body) == apierr.YouCom {
		h.youComSearch(w, r, body)
		return
	}
	h.firecrawlV1(w, r, body)
}

func (h *Handlers) firecrawlV1(w http.ResponseWriter, r *http.Request, body []byte) {
	results, ok := h.firecrawlResults(w, r, body)
	if !ok {
		return
	}
	out := firecrawlV1Response{Success: true, Data: make([]firecrawlWeb, 0, len(results))}
	for _, result := range results {
		out.Data = append(out.Data, firecrawlWebFrom(result))
	}
	writeJSON(w, http.StatusOK, out)
}

// FirecrawlV2 serves POST /firecrawl/v2/search and POST /v2/search.
func (h *Handlers) FirecrawlV2(w http.ResponseWriter, r *http.Request) {
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	results, ok := h.firecrawlResults(w, r, body)
	if !ok {
		return
	}
	out := firecrawlV2Response{Success: true, ID: randomID(12), CreditsUsed: 0}
	out.Data.Web = make([]firecrawlWeb, 0, len(results))
	out.Data.Images = []any{}
	out.Data.News = []any{}
	for _, result := range results {
		out.Data.Web = append(out.Data.Web, firecrawlWebFrom(result))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) firecrawlResults(w http.ResponseWriter, r *http.Request, body []byte) ([]magpie.Result, bool) {
	var req firecrawlRequest
	if err := decodeBody(body, &req); err != nil {
		writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return nil, false
	}
	query := strings.TrimSpace(req.Query)
	if err := validQuery(query, 500); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return nil, false
	}
	count, err := h.resolveCount(req.Limit, countSpec{Default: 10, Max: 100})
	if err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return nil, false
	}
	include, err := normalizeDomains(req.IncludeDomains)
	if err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return nil, false
	}
	exclude, err := normalizeDomains(req.ExcludeDomains)
	if err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return nil, false
	}
	upstream := count
	if len(include) > 0 || len(exclude) > 0 {
		upstream = h.MaxCount
	}
	results, err := h.Backend.Search(r.Context(), query, upstream, include)
	if err != nil {
		writeUpstreamError(w, r, body, err)
		return nil, false
	}
	results = filterDomains(results, include, exclude)
	if len(results) > count {
		results = results[:count]
	}
	return results, true
}

func firecrawlWebFrom(result magpie.Result) firecrawlWeb {
	description := snippetOf(result)
	return firecrawlWeb{
		Title:       result.Title,
		Description: description,
		URL:         result.URL,
		Markdown:    nil,
		HTML:        nil,
		RawHTML:     nil,
		Links:       []string{},
		Screenshot:  nil,
		Audio:       nil,
		Video:       nil,
		Metadata: firecrawlMetadata{
			Title:       result.Title,
			Description: description,
			SourceURL:   result.URL,
			URL:         result.URL,
			NumPages:    0,
			TotalPages:  0,
			Error:       nil,
		},
	}
}
