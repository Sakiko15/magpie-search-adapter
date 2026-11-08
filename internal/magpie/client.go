package magpie

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Options is everything the Magpie client needs.
type Options struct {
	BaseURL        string
	APIKey         string
	MCPPath        string
	SearchMode     string // mcp | alpha
	AlphaModel     string
	FetchMode      string // mcp | off
	InitTimeout    time.Duration
	RequestTimeout time.Duration
	FetchTimeout   time.Duration
	MaxCount       int
	Logger         *slog.Logger
}

// Client searches and fetches through a Magpie gateway.
type Client struct {
	baseURL        string
	mode           string
	fetchMode      string
	maxCount       int
	requestTimeout time.Duration
	fetchTimeout   time.Duration
	initTimeout    time.Duration
	httpClient     *http.Client
	mcp            *mcpTransport
	alpha          *alphaSearcher
	log            *slog.Logger
}

var _ Searcher = (*Client)(nil)

func NewClient(opts Options) (*Client, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("MAGPIE_BASE_URL must be an absolute http(s) URL, not %q", opts.BaseURL)
	}
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, fmt.Errorf("MAGPIE_API_KEY is required")
	}
	if opts.SearchMode = strings.ToLower(strings.TrimSpace(opts.SearchMode)); opts.SearchMode == "" {
		opts.SearchMode = "mcp"
	}
	if opts.SearchMode != "mcp" && opts.SearchMode != "alpha" {
		return nil, fmt.Errorf("MAGPIE_SEARCH_MODE must be mcp or alpha, not %q", opts.SearchMode)
	}
	if opts.FetchMode = strings.ToLower(strings.TrimSpace(opts.FetchMode)); opts.FetchMode == "" {
		opts.FetchMode = "mcp"
	}
	if opts.FetchMode != "mcp" && opts.FetchMode != "off" {
		return nil, fmt.Errorf("MAGPIE_FETCH_MODE must be mcp or off, not %q", opts.FetchMode)
	}
	if opts.MCPPath == "" {
		opts.MCPPath = "/mcp/magpie/web-search"
	}
	if !strings.HasPrefix(opts.MCPPath, "/") || strings.Contains(opts.MCPPath, "..") {
		return nil, fmt.Errorf("MAGPIE_MCP_PATH must be an absolute path without .., not %q", opts.MCPPath)
	}
	opts.AlphaModel = strings.TrimSpace(opts.AlphaModel)
	if opts.AlphaModel == "" {
		opts.AlphaModel = "magpie-search-adapter"
	}
	if opts.InitTimeout <= 0 {
		opts.InitTimeout = 10 * time.Second
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 90 * time.Second
	}
	if opts.FetchTimeout <= 0 {
		opts.FetchTimeout = 90 * time.Second
	}
	if opts.MaxCount <= 0 || opts.MaxCount > 20 {
		opts.MaxCount = 20
	}

	endpoint, err := url.JoinPath(base, strings.TrimPrefix(opts.MCPPath, "/"))
	if err != nil {
		return nil, fmt.Errorf("MAGPIE_MCP_PATH: %w", err)
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	httpClient := &http.Client{Transport: transport}

	c := &Client{
		baseURL:        base,
		mode:           opts.SearchMode,
		fetchMode:      opts.FetchMode,
		maxCount:       opts.MaxCount,
		requestTimeout: opts.RequestTimeout,
		fetchTimeout:   opts.FetchTimeout,
		initTimeout:    opts.InitTimeout,
		httpClient:     httpClient,
		log:            opts.Logger,
	}
	c.mcp = newMCPTransport(endpoint, opts.APIKey, httpClient, opts.InitTimeout, opts.Logger)
	c.alpha = newAlphaSearcher(base+"/v1/alpha/search", opts.APIKey, opts.AlphaModel, httpClient, opts.Logger)
	return c, nil
}

func (c *Client) Search(ctx context.Context, query string, count int, includeDomains []string) ([]Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	if count < 0 {
		count = 0
	}
	if count > c.maxCount {
		count = c.maxCount
	}
	if c.mode == "alpha" {
		results, err := c.alpha.Search(ctx, query, count, includeDomains)
		if err != nil {
			return nil, err
		}
		return limit(results, count), nil
	}
	text, err := c.mcp.webSearch(ctx, query, count)
	if err != nil {
		return nil, err
	}
	return limit(ParsePages(text), count), nil
}

func (c *Client) Fetch(ctx context.Context, rawURL string, maxChars int) (title, content string, err error) {
	if c.fetchMode == "off" {
		return "", "", &Error{
			Kind:       KindFetchDisabled,
			StatusCode: http.StatusNotImplemented,
			Message:    "web_fetch is disabled (MAGPIE_FETCH_MODE=off)",
		}
	}
	if maxChars <= 0 || maxChars > 100000 {
		maxChars = 100000
	}
	ctx, cancel := context.WithTimeout(ctx, c.fetchTimeout)
	defer cancel()
	text, err := c.mcp.webFetch(ctx, rawURL, maxChars)
	if err != nil {
		return "", "", err
	}
	return ParseFetchText(text)
}

// Ready checks the credentials and that Magpie's web search is usable.
func (c *Client) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if c.mode == "alpha" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.mcp.apiKey)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return &Error{Kind: KindUpstream, StatusCode: http.StatusBadGateway, Message: "could not reach magpie", Err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return upstreamError(resp.StatusCode, fmt.Sprintf("magpie /v1/models answered %d", resp.StatusCode))
		}
		if c.fetchMode == "mcp" {
			tools, err := c.mcp.toolsList(ctx)
			if err != nil {
				return err
			}
			hasFetch := false
			for _, tool := range tools {
				if tool.Name == "web_fetch" {
					hasFetch = true
					break
				}
			}
			if !hasFetch {
				return protocolError("magpie's MCP server does not offer web_fetch (check the gateway key's MCP permissions)")
			}
		}
		// Neither /v1/models nor tools/list proves that alpha search itself
		// works: a missing search source or an encrypted answer only shows up
		// on a real call. /readyz caches this probe for 30 seconds.
		results, err := c.alpha.Search(ctx, "magpie readiness probe", 1, nil)
		if err != nil {
			return err
		}
		if len(results) == 0 {
			return protocolError("magpie's alpha search returned no results")
		}
		return nil
	}
	tools, err := c.mcp.toolsList(ctx)
	if err != nil {
		return err
	}
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["web_search"] {
		return protocolError("magpie's MCP server does not offer web_search (check the gateway key's MCP permissions)")
	}
	if c.fetchMode == "mcp" && !names["web_fetch"] {
		return protocolError("magpie's MCP server does not offer web_fetch (check the gateway key's MCP permissions)")
	}
	return nil
}

func (c *Client) Mode() string { return c.mode }

func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}

func limit(results []Result, count int) []Result {
	if count > 0 && len(results) > count {
		return results[:count]
	}
	return results
}
