package api

import (
	"net/http"

	"magpie-search-adapter/internal/apierr"
)

// Route is one client-facing endpoint: its ServeMux pattern, the error
// envelope it answers with, and its handler.
type Route struct {
	Pattern string
	Vendor  apierr.Vendor
	// Dispatch marks the shared search path, whose vendor depends on the
	// request body rather than on the path alone.
	Dispatch bool
	Handler  http.HandlerFunc
}

// Routes lists the adapter's whole public surface in registration order.
func (h *Handlers) Routes() []Route {
	return []Route{
		{Pattern: "POST /search", Vendor: apierr.Tavily, Dispatch: true, Handler: h.Search},
		{Pattern: "GET /search", Vendor: apierr.SearXNG, Handler: h.SearxngSearch},
		{Pattern: "POST /extract", Vendor: apierr.Tavily, Handler: h.TavilyExtract},
		{Pattern: "GET /res/v1/web/search", Vendor: apierr.Brave, Handler: h.BraveSearch},
		{Pattern: "POST /v1/tools/search", Vendor: apierr.OpenAI, Handler: h.DeepSeekSearch},
		{Pattern: "POST /v1/tools/fetch", Vendor: apierr.OpenAI, Handler: h.DeepSeekFetch},
		{Pattern: "GET /v1/models", Vendor: apierr.OpenAI, Handler: h.Models},

		{Pattern: "GET /searxng/search", Vendor: apierr.SearXNG, Handler: h.SearxngSearch},
		{Pattern: "POST /searxng/search", Vendor: apierr.SearXNG, Handler: h.SearxngSearch},
		{Pattern: "GET /google/customsearch/v1", Vendor: apierr.Google, Handler: h.GoogleSearch},
		{Pattern: "GET /customsearch/v1", Vendor: apierr.Google, Handler: h.GoogleSearch},
		{Pattern: "POST /bocha/v1/web-search", Vendor: apierr.Bocha, Handler: h.BochaSearch},
		{Pattern: "POST /v1/web-search", Vendor: apierr.Bocha, Handler: h.BochaSearch},
		{Pattern: "POST /zhipu/v4/web_search", Vendor: apierr.Zhipu, Handler: h.ZhipuSearch},
		{Pattern: "POST /api/paas/v4/web_search", Vendor: apierr.Zhipu, Handler: h.ZhipuSearch},
		{Pattern: "GET /jina", Vendor: apierr.Jina, Handler: h.JinaSearch},
		{Pattern: "POST /jina", Vendor: apierr.Jina, Handler: h.JinaSearch},
		{Pattern: "GET /jina/", Vendor: apierr.Jina, Handler: h.JinaSearch},
		{Pattern: "POST /jina/", Vendor: apierr.Jina, Handler: h.JinaSearch},

		{Pattern: "POST /exa/search", Vendor: apierr.Exa, Handler: h.ExaSearch},
		{Pattern: "POST /firecrawl/v1/search", Vendor: apierr.Firecrawl, Handler: h.FirecrawlV1},
		{Pattern: "POST /firecrawl/v2/search", Vendor: apierr.Firecrawl, Handler: h.FirecrawlV2},
		{Pattern: "POST /v1/search", Vendor: apierr.Firecrawl, Handler: h.V1Search},
		{Pattern: "POST /v2/search", Vendor: apierr.Firecrawl, Handler: h.FirecrawlV2},
		{Pattern: "GET /v1/search", Vendor: apierr.YouCom, Handler: h.YouComSearch},
		{Pattern: "POST /you/v1/search", Vendor: apierr.YouCom, Handler: h.YouComSearch},
		{Pattern: "GET /you/v1/search", Vendor: apierr.YouCom, Handler: h.YouComSearch},
	}
}

// Path returns the route's path without its method.
func (r Route) Path() string {
	if _, path, ok := cutPattern(r.Pattern); ok {
		return path
	}
	return r.Pattern
}

// Method returns the route's HTTP method.
func (r Route) Method() string {
	if method, _, ok := cutPattern(r.Pattern); ok {
		return method
	}
	return ""
}

func cutPattern(pattern string) (method, path string, ok bool) {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == ' ' {
			return pattern[:i], pattern[i+1:], true
		}
	}
	return "", "", false
}
