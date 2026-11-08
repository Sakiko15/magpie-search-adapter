// Package api serves Tavily, Serper, Brave, and DeepSeek search shapes.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"magpie-search-adapter/internal/apierr"
	"magpie-search-adapter/internal/auth"
	"magpie-search-adapter/internal/config"
	"magpie-search-adapter/internal/magpie"
)

// Handlers maps the vendor HTTP shapes onto a Magpie searcher.
type Handlers struct {
	Backend       magpie.Searcher
	MaxCount      int
	DefaultCount  int
	FetchMaxChars int
	FetchTimeout  time.Duration
	FetchEnabled  bool
	Mode          string
	Logger        *slog.Logger
	ReadyFn       func(context.Context) error

	readyMu   sync.Mutex
	readyAt   time.Time
	readyErr  error
	probing   bool
	probeDone chan struct{}
}

func NewHandlers(backend magpie.Searcher, cfg *config.Config, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{
		Backend:       backend,
		MaxCount:      cfg.MaxCount,
		DefaultCount:  cfg.DefaultCount,
		FetchMaxChars: cfg.FetchMaxChars,
		FetchTimeout:  cfg.FetchTimeout,
		FetchEnabled:  cfg.FetchMode == "mcp",
		Mode:          cfg.SearchMode,
		Logger:        logger,
	}
}

func requestBody(r *http.Request) ([]byte, error) {
	if body, ok := auth.BodyFromContext(r.Context()); ok {
		return body, nil
	}
	if r.Body == nil {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}

func decodeBody(body []byte, dst any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("request body is empty")
	}
	return json.Unmarshal(body, dst)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, body []byte, status int, message string) {
	apierr.Write(w, apierr.Detect(r, body), status, redact(message))
}

// writeUpstreamError maps a Magpie error to a client status without ever
// presenting an upstream 401 as the client's own key failure.
func writeUpstreamError(w http.ResponseWriter, r *http.Request, body []byte, err error) {
	status := http.StatusBadGateway
	var me *magpie.Error
	if errors.As(err, &me) {
		switch me.Kind {
		case magpie.KindInvalidInput:
			status = http.StatusBadRequest
		case magpie.KindNoSearcher:
			status = http.StatusServiceUnavailable
		case magpie.KindRateLimited:
			status = http.StatusTooManyRequests
		case magpie.KindTimeout:
			status = http.StatusGatewayTimeout
		case magpie.KindFetchDisabled:
			status = http.StatusNotImplemented
		case magpie.KindProtocol, magpie.KindUpstream, magpie.KindAuth, magpie.KindForbidden:
			status = http.StatusBadGateway
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
	}
	writeError(w, r, body, status, err.Error())
}

func clampCount(n, def, max int) int {
	if n <= 0 {
		n = def
	}
	if n > max {
		n = max
	}
	if n < 1 {
		n = 1
	}
	return n
}

// maxDomains caps one request's domain filters: they are only used to build
// the result filter and the cache key, so a huge list is never useful.
const maxDomains = 100

func normalizeDomains(in []string) ([]string, error) {
	if len(in) > maxDomains {
		return nil, fmt.Errorf("at most %d domains are accepted", maxDomains)
	}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d == "" {
			continue
		}
		if strings.Contains(d, "://") {
			u, err := url.Parse(d)
			if err != nil || u.Hostname() == "" {
				return nil, fmt.Errorf("invalid domain %q", raw)
			}
			d = u.Hostname()
		}
		d = strings.TrimPrefix(d, "*.")
		d = strings.TrimPrefix(d, ".")
		// Results are compared by HostOf, which drops a leading www.
		d = strings.TrimPrefix(d, "www.")
		// A bare host must not carry a port or brackets: filterDomains
		// compares against Hostname(), which never has either. "*" would
		// never match anything, so it is rejected rather than silently
		// returning no results.
		if d == "" || d == "*" || strings.ContainsAny(d, " /\\?#@:") {
			return nil, fmt.Errorf("invalid domain %q", raw)
		}
		out = append(out, d)
	}
	return out, nil
}

func filterDomains(results []magpie.Result, include, exclude []string) []magpie.Result {
	out := make([]magpie.Result, 0, len(results))
	for _, result := range results {
		host := strings.ToLower(magpie.HostOf(result.URL))
		if host == "" {
			// A model's prose answer can come back without a parseable URL.
			// Keeping the one fallback result beats answering with nothing.
			out = append(out, result)
			continue
		}
		if len(include) > 0 && !matchesAnyHost(host, include) {
			continue
		}
		if len(exclude) > 0 && matchesAnyHost(host, exclude) {
			continue
		}
		out = append(out, result)
	}
	return out
}

func matchesAnyHost(host string, domains []string) bool {
	for _, domain := range domains {
		if hostMatches(host, domain) {
			return true
		}
	}
	return false
}

func hostMatches(host, domain string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return host == domain
	}
	if _, err := netip.ParseAddr(domain); err == nil {
		return host == domain
	}
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func boolOrString(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "", "false", "0", "no":
			return false
		default:
			return true
		}
	}
	return false
}

func validQuery(q string, maxRunes int) error {
	q = strings.TrimSpace(q)
	if q == "" {
		return errors.New("query is required")
	}
	if maxRunes > 0 && utf8.RuneCountInString(q) > maxRunes {
		return fmt.Errorf("query is longer than %d characters", maxRunes)
	}
	return nil
}

// cgnat is 100.64.0.0/10, the shared carrier-grade NAT range. netip does not
// count it as private, but it reaches cloud-internal and metadata services.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// isPublicAddr reports whether ip is a global unicast address that is safe to
// fetch: not private, loopback, link-local, multicast, unspecified or CGNAT.
func isPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	return !cgnat.Contains(ip)
}

func validateFetchURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("url is required")
	}
	if len(raw) > 8192 {
		return errors.New("url is longer than 8192 characters")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return errors.New("url contains control characters")
		}
	}
	u, err := url.Parse(raw)
	// Hostname() rather than Host: "http://:8080/" has a host but no name,
	// and net.Dial would read that as the local machine.
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("url must be an absolute http(s) URL")
	}
	if u.User != nil {
		return errors.New("url must not contain a username or password")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !isPublicAddr(ip) {
		return errors.New("url points to a private or special address")
	}
	return nil
}

func tavilyScore(rank int) float64 {
	s := 1.0 - 0.05*float64(rank)
	if s < 0.05 {
		return 0.05
	}
	return s
}

func deepseekScore(rank int) float64 {
	s := 3.0 - 0.15*float64(rank)
	if s < 0.1 {
		return 0.1
	}
	return s
}

var secretPattern = regexp.MustCompile(`sk-magpie-key-[A-Za-z0-9_-]+`)

func redact(s string) string {
	return secretPattern.ReplaceAllString(s, "sk-magpie-key-***")
}
