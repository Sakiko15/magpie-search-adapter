// Package config reads the adapter's environment.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is everything the adapter needs at startup.
type Config struct {
	ListenAddr string
	// AdapterAPIKeys are the static keys clients (and new-api) send.
	AdapterAPIKeys []string

	MagpieBaseURL string
	MagpieAPIKey  string

	// SearchMode is "mcp" (documented, count and fetch) or "alpha"
	// (Codex-compatible HTTP, no MCP for search).
	SearchMode     string
	MCPPath        string
	AlphaModel     string
	FetchMode      string
	InitTimeout    time.Duration
	RequestTimeout time.Duration
	FetchTimeout   time.Duration

	DefaultCount  int
	MaxCount      int
	FetchMaxChars int

	MaxBodyBytes   int64
	MaxConcurrency int

	// AllowKeylessCIDRs lets clients from these networks search without a
	// key (SearXNG and Jina clients cannot send one). Empty by default.
	AllowKeylessCIDRs []netip.Prefix

	// Resilience: upstream retries, their backoff, the per-request search
	// budget, and the result cache.
	UpstreamRetries int
	RetryBaseDelay  time.Duration
	RetryMaxDelay   time.Duration
	SearchBudget    time.Duration
	SearchCacheTTL  time.Duration
	SearchCacheSize int

	LogLevel slog.Level
}

// Load reads .env when present, then validates the environment.
func Load() (*Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return nil, err
	}
	cfg := &Config{
		ListenAddr:    env("LISTEN_ADDR", "0.0.0.0:8787"),
		MagpieBaseURL: strings.TrimRight(env("MAGPIE_BASE_URL", "https://magpie.127001001.xyz"), "/"),
		MagpieAPIKey:  strings.TrimSpace(os.Getenv("MAGPIE_API_KEY")),
		SearchMode:    strings.ToLower(env("MAGPIE_SEARCH_MODE", "mcp")),
		MCPPath:       env("MAGPIE_MCP_PATH", "/mcp/magpie/web-search"),
		AlphaModel:    env("MAGPIE_ALPHA_MODEL", "magpie-search-adapter"),
		FetchMode:     strings.ToLower(env("MAGPIE_FETCH_MODE", "mcp")),
	}
	var err error
	if cfg.MaxBodyBytes, err = int64Env("MAX_BODY_BYTES", 1<<20); err != nil {
		return nil, err
	}
	if cfg.MaxConcurrency, err = intEnv("MAX_CONCURRENCY", 32); err != nil {
		return nil, err
	}
	if cfg.InitTimeout, err = durationEnv("INIT_TIMEOUT", 10*time.Second); err != nil {
		return nil, err
	}
	if cfg.RequestTimeout, err = durationEnv("REQUEST_TIMEOUT", 60*time.Second); err != nil {
		return nil, err
	}
	if cfg.FetchTimeout, err = durationEnv("FETCH_TIMEOUT", 90*time.Second); err != nil {
		return nil, err
	}
	if cfg.MaxCount, err = intEnv("MAX_COUNT", 20); err != nil {
		return nil, err
	}
	if cfg.DefaultCount, err = intEnv("DEFAULT_COUNT", 6); err != nil {
		return nil, err
	}
	if cfg.FetchMaxChars, err = intEnv("FETCH_MAX_CHARS", 100000); err != nil {
		return nil, err
	}
	cfg.LogLevel = levelEnv("LOG_LEVEL", slog.LevelInfo)

	if cfg.UpstreamRetries, err = intEnv("UPSTREAM_RETRIES", 2); err != nil {
		return nil, err
	}
	if cfg.RetryBaseDelay, err = durationEnv("RETRY_BASE_DELAY", 200*time.Millisecond); err != nil {
		return nil, err
	}
	if cfg.RetryMaxDelay, err = durationEnv("RETRY_MAX_DELAY", 2*time.Second); err != nil {
		return nil, err
	}
	if cfg.SearchCacheTTL, err = durationEnv("SEARCH_CACHE_TTL", 30*time.Second); err != nil {
		return nil, err
	}
	if cfg.SearchCacheSize, err = intEnv("SEARCH_CACHE_SIZE", 1024); err != nil {
		return nil, err
	}
	if raw := env("SEARCH_BUDGET", ""); raw == "" {
		cfg.SearchBudget = cfg.RequestTimeout
	} else if cfg.SearchBudget, err = time.ParseDuration(raw); err != nil {
		return nil, fmt.Errorf("SEARCH_BUDGET: %w", err)
	}
	if cfg.AllowKeylessCIDRs, err = parseCIDRs(env("ALLOW_KEYLESS_CIDRS", "")); err != nil {
		return nil, err
	}

	for _, k := range strings.Split(os.Getenv("ADAPTER_API_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			cfg.AdapterAPIKeys = append(cfg.AdapterAPIKeys, k)
		}
	}

	switch {
	case cfg.ListenAddr == "":
		return nil, errors.New("LISTEN_ADDR must not be empty")
	case len(cfg.AdapterAPIKeys) == 0:
		return nil, errors.New("ADAPTER_API_KEYS is required (comma-separated keys)")
	case cfg.MagpieAPIKey == "":
		return nil, errors.New("MAGPIE_API_KEY is required (a valid Magpie gateway key)")
	case cfg.SearchMode != "mcp" && cfg.SearchMode != "alpha":
		return nil, fmt.Errorf("MAGPIE_SEARCH_MODE must be mcp or alpha, not %q", cfg.SearchMode)
	case cfg.FetchMode != "mcp" && cfg.FetchMode != "off":
		return nil, fmt.Errorf("MAGPIE_FETCH_MODE must be mcp or off, not %q", cfg.FetchMode)
	case !strings.HasPrefix(cfg.MCPPath, "/") || strings.Contains(cfg.MCPPath, ".."):
		return nil, fmt.Errorf("MAGPIE_MCP_PATH must be an absolute path without .., not %q", cfg.MCPPath)
	case cfg.AlphaModel == "":
		return nil, errors.New("MAGPIE_ALPHA_MODEL must not be empty")
	case cfg.InitTimeout <= 0 || cfg.RequestTimeout <= 0 || cfg.FetchTimeout <= 0:
		return nil, errors.New("INIT_TIMEOUT, REQUEST_TIMEOUT and FETCH_TIMEOUT must be positive")
	case cfg.MaxCount < 1 || cfg.MaxCount > 20:
		return nil, fmt.Errorf("MAX_COUNT must be 1..20, not %d", cfg.MaxCount)
	case cfg.DefaultCount < 1 || cfg.DefaultCount > cfg.MaxCount:
		return nil, fmt.Errorf("DEFAULT_COUNT must be 1..MAX_COUNT, not %d", cfg.DefaultCount)
	case cfg.FetchMaxChars < 1 || cfg.FetchMaxChars > 100000:
		return nil, fmt.Errorf("FETCH_MAX_CHARS must be 1..100000, not %d", cfg.FetchMaxChars)
	case cfg.MaxBodyBytes < 1024:
		return nil, fmt.Errorf("MAX_BODY_BYTES must be at least 1024, not %d", cfg.MaxBodyBytes)
	case cfg.MaxConcurrency < 1:
		return nil, fmt.Errorf("MAX_CONCURRENCY must be positive, not %d", cfg.MaxConcurrency)
	case cfg.UpstreamRetries < 0 || cfg.UpstreamRetries > 10:
		return nil, fmt.Errorf("UPSTREAM_RETRIES must be 0..10, not %d", cfg.UpstreamRetries)
	case cfg.RetryBaseDelay <= 0:
		return nil, errors.New("RETRY_BASE_DELAY must be positive")
	case cfg.RetryMaxDelay < cfg.RetryBaseDelay:
		return nil, errors.New("RETRY_MAX_DELAY must not be smaller than RETRY_BASE_DELAY")
	case cfg.SearchBudget <= 0:
		return nil, errors.New("SEARCH_BUDGET must be positive")
	case cfg.SearchCacheTTL < 0:
		return nil, errors.New("SEARCH_CACHE_TTL must not be negative")
	case cfg.SearchCacheSize < 1:
		return nil, fmt.Errorf("SEARCH_CACHE_SIZE must be positive, not %d", cfg.SearchCacheSize)
	}

	u, err := url.Parse(cfg.MagpieBaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("MAGPIE_BASE_URL must be an absolute http(s) URL, not %q", cfg.MagpieBaseURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("MAGPIE_BASE_URL must not contain a query or fragment")
	}
	return cfg, nil
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// parseCIDRs reads a comma/space separated list of CIDRs.
func parseCIDRs(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(field)
		if err != nil {
			return nil, fmt.Errorf("ALLOW_KEYLESS_CIDRS: %q is not a CIDR like 10.0.0.0/8", field)
		}
		out = append(out, prefix.Masked())
	}
	return out, nil
}

// intEnv reads an integer setting. A value that does not parse is an error,
// not a silent fallback: MAX_COUNT=2O (letter O) must not look like the
// default was meant.
func intEnv(key string, def int) (int, error) {
	raw := env(key, "")
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}

func int64Env(key string, def int64) (int64, error) {
	raw := env(key, "")
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	raw := env(key, "")
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func levelEnv(key string, def slog.Level) slog.Level {
	switch strings.ToLower(env(key, "")) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return def
	}
}
