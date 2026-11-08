package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type bochaRequest struct {
	Query     string `json:"query"`
	Q         string `json:"q"`
	Count     *int   `json:"count"`
	Summary   bool   `json:"summary"`
	Freshness string `json:"freshness"`
}

type bochaValue struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	URL             string  `json:"url"`
	DisplayURL      string  `json:"displayUrl"`
	Snippet         string  `json:"snippet"`
	Summary         string  `json:"summary,omitempty"`
	SiteName        string  `json:"siteName"`
	SiteIcon        *string `json:"siteIcon"`
	DatePublished   *string `json:"datePublished"`
	DateLastCrawled *string `json:"dateLastCrawled"`
	CachedPageURL   *string `json:"cachedPageUrl"`
}

type bochaQueryContext struct {
	OriginalQuery string `json:"originalQuery"`
}

type bochaWebPages struct {
	WebSearchURL          string       `json:"webSearchUrl"`
	TotalEstimatedMatches int          `json:"totalEstimatedMatches"`
	Value                 []bochaValue `json:"value"`
}

type bochaData struct {
	Type         string            `json:"_type"`
	QueryContext bochaQueryContext `json:"queryContext"`
	WebPages     bochaWebPages     `json:"webPages"`
}

type bochaResponse struct {
	Code  int       `json:"code"`
	Msg   any       `json:"msg"`
	LogID string    `json:"log_id"`
	Data  bochaData `json:"data"`
}

// BochaSearch serves POST /bocha/v1/web-search and its native alias.
func (h *Handlers) BochaSearch(w http.ResponseWriter, r *http.Request) {
	body, err := requestBody(r)
	if err != nil {
		writeError(w, r, nil, http.StatusBadRequest, "the request body could not be read")
		return
	}
	var req bochaRequest
	if err := decodeBody(body, &req); err != nil {
		writeError(w, r, body, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		query = strings.TrimSpace(req.Q)
	}
	if err := validQuery(query, 2048); err != nil {
		writeError(w, r, body, http.StatusBadRequest, err.Error())
		return
	}
	count, err := h.resolveCount(req.Count, countSpec{Default: 10, Max: 50})
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

	out := bochaResponse{Code: 200, Msg: nil, LogID: randomID(8)}
	out.Data.Type = "SearchResponse"
	out.Data.QueryContext.OriginalQuery = query
	out.Data.WebPages.WebSearchURL = "https://bochaai.com/search?q=" + url.QueryEscape(query)
	out.Data.WebPages.TotalEstimatedMatches = len(results)
	out.Data.WebPages.Value = make([]bochaValue, 0, len(results))
	for i, result := range results {
		summary := ""
		if req.Summary {
			summary = contentOf(result)
		}
		out.Data.WebPages.Value = append(out.Data.WebPages.Value, bochaValue{
			ID:              "https://api.bochaai.com/v1/#WebPages." + strconv.Itoa(i),
			Name:            result.Title,
			URL:             result.URL,
			DisplayURL:      displayURL(result.URL),
			Snippet:         snippetOf(result),
			Summary:         summary,
			SiteName:        hostOf(result.URL),
			SiteIcon:        nil,
			DatePublished:   nil,
			DateLastCrawled: nil,
			CachedPageURL:   nil,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
