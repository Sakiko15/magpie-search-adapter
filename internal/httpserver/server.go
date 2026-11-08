// Package httpserver wires the adapter's routes and middleware.
package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"magpie-search-adapter/internal/api"
	"magpie-search-adapter/internal/apierr"
	"magpie-search-adapter/internal/auth"
	"magpie-search-adapter/internal/config"
	"magpie-search-adapter/internal/magpie"
)

type requestIDKey struct{}

// New builds the adapter's HTTP server.
func New(cfg *config.Config, backend magpie.Searcher, readyFn func(context.Context) error, logger *slog.Logger) (*http.Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	handlers := api.NewHandlers(backend, cfg, logger)
	handlers.ReadyFn = readyFn

	mw, err := auth.NewMiddleware(cfg.AdapterAPIKeys, cfg.MaxBodyBytes, auth.WithKeylessCIDRs(cfg.AllowKeylessCIDRs))
	if err != nil {
		return nil, err
	}
	if len(cfg.AllowKeylessCIDRs) > 0 {
		networks := make([]string, 0, len(cfg.AllowKeylessCIDRs))
		for _, prefix := range cfg.AllowKeylessCIDRs {
			networks = append(networks, prefix.String())
		}
		logger.Warn("search without an API key is allowed from these networks", "cidrs", strings.Join(networks, ","))
	}

	apiMux := http.NewServeMux()
	for _, route := range handlers.Routes() {
		apiMux.HandleFunc(route.Pattern, route.Handler)
	}

	// Concurrency sits inside auth: an unauthenticated caller must not be able
	// to hold a slot and starve real clients.
	protected := mw.Wrap(withConcurrency(apiMux, cfg.MaxConcurrency))

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", handlers.Health)
	root.HandleFunc("GET /readyz", handlers.Ready)
	root.Handle("/", protected)

	handler := withRecovery(withRequestID(withLogging(root, logger)), logger)
	// The response is written after Magpie answers; give it at least the
	// slowest configured upstream timeout plus a margin.
	writeTimeout := cfg.FetchTimeout
	if cfg.RequestTimeout > writeTimeout {
		writeTimeout = cfg.RequestTimeout
	}
	if cfg.SearchBudget > writeTimeout {
		writeTimeout = cfg.SearchBudget
	}
	// A /extract batch can run three sequential fetches per worker.
	if batch := api.ExtractBatchTimeout(cfg.FetchTimeout); batch > writeTimeout {
		writeTimeout = batch
	}
	writeTimeout += 30 * time.Second
	if writeTimeout < 120*time.Second {
		writeTimeout = 120 * time.Second
	}
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       120 * time.Second,
	}, nil
}

func withConcurrency(next http.Handler, max int) http.Handler {
	if max <= 0 {
		return next
	}
	sem := make(chan struct{}, max)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			next.ServeHTTP(w, r)
		default:
			// The auth middleware already buffered the body, so the vendor
			// shape of this 503 matches the one the handler would have used.
			body, _ := auth.BodyFromContext(r.Context())
			apierr.Write(w, apierr.Detect(r, body), http.StatusServiceUnavailable, "too many concurrent requests")
		}
	})
}

func withRecovery(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic serving request",
					"error", recovered,
					"path", r.URL.Path,
					"request_id", requestID(r.Context()),
					"stack", string(debug.Stack()),
				)
				apierr.Write(w, apierr.Detect(r, nil), http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := cleanRequestID(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func withLogging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", requestID(r.Context()),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func cleanRequestID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		value = value[:64]
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(buf[:])
}
