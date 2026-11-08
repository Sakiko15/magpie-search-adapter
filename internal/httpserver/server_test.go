package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"magpie-search-adapter/internal/config"
	"magpie-search-adapter/internal/magpie"
)

type testBackend struct{}

func (testBackend) Search(ctx context.Context, query string, count int, domains []string) ([]magpie.Result, error) {
	return []magpie.Result{{Title: "A", URL: "https://a.example/x", Content: "alpha"}}, nil
}

func (testBackend) Fetch(ctx context.Context, url string, maxChars int) (string, string, error) {
	return "Example", "Hello", nil
}

func TestServerEndToEnd(t *testing.T) {
	cfg := &config.Config{
		ListenAddr:     "127.0.0.1:0",
		AdapterAPIKeys: []string{"adapter-key"},
		MagpieBaseURL:  "http://127.0.0.1:1",
		MagpieAPIKey:   "test-key",
		SearchMode:     "mcp",
		FetchMode:      "mcp",
		MaxCount:       20,
		FetchMaxChars:  100000,
		MaxBodyBytes:   1 << 20,
		MaxConcurrency: 4,
		LogLevel:       slog.LevelError,
	}
	srv, err := New(cfg, testBackend{}, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	get := func(path, key string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	post := func(path, key, header, body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if header != "" {
			req.Header.Set("X-API-KEY", header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if resp := get("/healthz", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
	if resp := get("/readyz", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d", resp.StatusCode)
	}
	if resp := post("/v1/tools/search", "", "", `{"query":"go"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated search = %d", resp.StatusCode)
	}
	if resp := post("/v1/tools/search", "adapter-key", "", `{"query":"go"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("deepseek search = %d", resp.StatusCode)
	}
	if resp := post("/search", "", "adapter-key", `{"q":"go"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("serper search = %d", resp.StatusCode)
	}
	if resp := get("/v1/models", "adapter-key"); resp.StatusCode != http.StatusOK {
		t.Fatalf("models = %d", resp.StatusCode)
	}
	if resp := post("/v1/tools/fetch", "adapter-key", "", `{"url":"https://example.com/"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch = %d", resp.StatusCode)
	}
}

func TestWriteTimeoutIncludesExtractBatch(t *testing.T) {
	cfg := &config.Config{
		ListenAddr:     "127.0.0.1:0",
		AdapterAPIKeys: []string{"adapter-key"},
		MagpieBaseURL:  "http://127.0.0.1:1",
		MagpieAPIKey:   "test-key",
		SearchMode:     "mcp",
		FetchMode:      "mcp",
		MaxCount:       20,
		FetchMaxChars:  100000,
		MaxBodyBytes:   1 << 20,
		MaxConcurrency: 4,
		LogLevel:       slog.LevelError,
		FetchTimeout:   90 * time.Second,
		RequestTimeout: 90 * time.Second,
		SearchBudget:   90 * time.Second,
	}
	srv, err := New(cfg, testBackend{}, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if want := 300 * time.Second; srv.WriteTimeout != want {
		t.Fatalf("WriteTimeout = %s, want %s", srv.WriteTimeout, want)
	}
}
