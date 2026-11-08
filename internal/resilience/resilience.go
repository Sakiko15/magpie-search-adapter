// Package resilience wraps a magpie.Searcher with upstream retries, a
// per-request time budget, a short-lived result cache and singleflight
// coalescing. Handlers keep talking to the same magpie.Searcher interface.
package resilience

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"magpie-search-adapter/internal/magpie"
)

// Config tunes the wrapper. A zero Retries and a zero CacheTTL turn the
// corresponding behaviour off.
type Config struct {
	Retries   int
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// Budget bounds one Search including its retries. Zero means no
	// wrapper-imposed deadline (the client's own timeout still applies).
	Budget    time.Duration
	CacheTTL  time.Duration
	CacheSize int
	Coalesce  bool
}

// Searcher decorates another magpie.Searcher.
type Searcher struct {
	inner magpie.Searcher
	cfg   Config
	log   *slog.Logger
	cache *ttlCache
	group *group
}

// Wrap returns inner with retries, caching and coalescing applied.
func Wrap(inner magpie.Searcher, cfg Config, logger *slog.Logger) *Searcher {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Searcher{inner: inner, cfg: cfg, log: logger}
	if cfg.CacheTTL > 0 && cfg.CacheSize > 0 {
		s.cache = newTTLCache(cfg.CacheTTL, cfg.CacheSize)
	}
	if cfg.Coalesce {
		s.group = newGroup()
	}
	return s
}

// Search serves a cached or coalesced result when it can, and otherwise
// asks Magpie with retries inside one time budget.
func (s *Searcher) Search(ctx context.Context, query string, count int, includeDomains []string) ([]magpie.Result, error) {
	key := cacheKey(query, count, includeDomains)
	if s.cache != nil {
		if results, ok := s.cache.get(key); ok {
			return results, nil
		}
	}
	load := func() (any, error) {
		// With a budget the shared call gets its own deadline, so one
		// waiter giving up does not cancel the others. Without one the
		// caller's context must stay attached, or nothing would stop it.
		base := ctx
		if s.cfg.Budget > 0 {
			base = context.WithoutCancel(ctx)
		}
		results, err := s.attempt(base, query, count, includeDomains)
		if err != nil {
			return nil, err
		}
		if s.cache != nil {
			s.cache.set(key, results)
		}
		return results, nil
	}

	var (
		value any
		err   error
	)
	if s.group == nil {
		value, err = load()
	} else {
		value, err, _ = s.group.do(key, load, ctx)
	}
	if err != nil {
		return nil, err
	}
	results, ok := value.([]magpie.Result)
	if !ok {
		return nil, errors.New("resilience: unexpected cached value")
	}
	return results, nil
}

// Fetch is passed through: page content is volatile and usually large.
func (s *Searcher) Fetch(ctx context.Context, url string, maxChars int) (string, string, error) {
	return s.inner.Fetch(ctx, url, maxChars)
}

func (s *Searcher) attempt(ctx context.Context, query string, count int, includeDomains []string) ([]magpie.Result, error) {
	attempts := s.cfg.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	if s.cfg.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.Budget)
		defer cancel()
	}
	if attempts == 1 {
		return s.inner.Search(ctx, query, count, includeDomains)
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		results, err := s.inner.Search(ctx, query, count, includeDomains)
		if err == nil {
			return results, nil
		}
		lastErr = err
		if i == attempts-1 || !retryable(err, ctx) {
			break
		}
		delay := s.backoff(i, err)
		if deadline, ok := ctx.Deadline(); ok && delay >= time.Until(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

// backoff is an exponentially growing window with full jitter, raised to
// the upstream's Retry-After when it sent a longer one.
func (s *Searcher) backoff(attempt int, err error) time.Duration {
	base := s.cfg.BaseDelay
	if base <= 0 {
		base = 200 * time.Millisecond
	}
	ceiling := s.cfg.MaxDelay
	if ceiling <= 0 {
		ceiling = 2 * time.Second
	}
	window := base << uint(attempt)
	if window > ceiling || window <= 0 {
		window = ceiling
	}
	delay := time.Duration(rand.Int64N(int64(window) + 1))
	var upstream *magpie.Error
	if errors.As(err, &upstream) && upstream.RetryAfter > delay {
		delay = upstream.RetryAfter
	}
	return delay
}

// retryable reports whether err is worth another attempt while ctx still
// has budget: upstream 5xx and 429, a timeout with budget left, and
// transport errors with no classification.
func retryable(err error, ctx context.Context) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var upstream *magpie.Error
	if errors.As(err, &upstream) {
		switch upstream.Kind {
		case magpie.KindRateLimited, magpie.KindTimeout:
			return true
		case magpie.KindUpstream:
			return upstream.StatusCode == 0 ||
				upstream.StatusCode == http.StatusRequestTimeout ||
				upstream.StatusCode >= 500
		default:
			return false
		}
	}
	return true
}

// cacheKey identifies one search. The query is kept as written; only
// surrounding space and the domain order are normalised.
func cacheKey(query string, count int, includeDomains []string) string {
	domains := make([]string, 0, len(includeDomains))
	domains = append(domains, includeDomains...)
	sortStrings(domains)

	var b strings.Builder
	b.WriteString(strings.TrimSpace(query))
	b.WriteByte(0)
	b.WriteString(strconv.Itoa(count))
	b.WriteByte(0)
	b.WriteString(strings.Join(domains, ","))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// sortStrings is a tiny insertion sort, so this file needs no extra
// dependency on the sort package's interface machinery.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
