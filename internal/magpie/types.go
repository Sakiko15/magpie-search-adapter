// Package magpie talks to a Magpie gateway's web search.
package magpie

import "context"

// Result is one page a Magpie search found.
type Result struct {
	Title   string
	URL     string
	Content string
	Snippet string
	Score   float64
}

// Searcher is what the API handlers need: Magpie's web_search and web_fetch.
type Searcher interface {
	Search(ctx context.Context, query string, count int, includeDomains []string) ([]Result, error)
	Fetch(ctx context.Context, url string, maxChars int) (title, content string, err error)
}

// Readiness is the optional check the HTTP server exposes on /readyz.
type Readiness interface {
	Ready(ctx context.Context) error
}
