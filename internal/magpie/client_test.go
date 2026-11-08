package magpie

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeMCP struct {
	initCount      atomic.Int32
	callCount      atomic.Int32
	lastCount      atomic.Int32
	missingSession atomic.Bool
	sse            bool
	toolError      string
	stateless      bool
	noFetch        bool
	emptyCall      bool
	stallBody      time.Duration
}

func newFakeMCP(t *testing.T, f *fakeMCP) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		switch msg.Method {
		case "initialize":
			f.initCount.Add(1)
			if !f.stateless {
				w.Header().Set("Mcp-Session-Id", "test-session")
			}
			writeRPC(w, msg.ID, map[string]any{"protocolVersion": "2025-06-18"})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") == "" {
				f.missingSession.Store(true)
			}
			tools := []map[string]any{{"name": "web_search"}, {"name": "web_fetch"}}
			if f.noFetch {
				tools = tools[:1]
			}
			writeRPC(w, msg.ID, map[string]any{"tools": tools})
		case "tools/call":
			f.callCount.Add(1)
			if f.stallBody > 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				time.Sleep(f.stallBody)
				return
			}
			if f.emptyCall {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			if r.Header.Get("Mcp-Session-Id") == "" {
				f.missingSession.Store(true)
			}
			var p struct {
				Name      string `json:"name"`
				Arguments struct {
					Query string `json:"query"`
					Count int    `json:"count"`
				} `json:"arguments"`
			}
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				http.Error(w, "bad params", http.StatusBadRequest)
				return
			}
			f.lastCount.Store(int32(p.Arguments.Count))
			if p.Name == "web_fetch" {
				result := map[string]any{"content": []map[string]any{{
					"type": "text",
					"text": "URL: https://example.com/\nTitle: Example\n\nHello\nworld",
				}}}
				if f.sse {
					writeSSE(w, msg.ID, result)
				} else {
					writeRPC(w, msg.ID, result)
				}
				return
			}
			result := map[string]any{"content": []map[string]any{{
				"type": "text",
				"text": "1. Go — https://go.dev/dl/\ngo1.27.1 is the latest release.\n\n" +
					"2. Go blog — https://go.dev/blog/\nThe Go blog.\n",
			}}}
			if f.toolError != "" {
				result = map[string]any{
					"content": []map[string]any{{"type": "text", "text": f.toolError}},
					"isError": true,
				}
			}
			if f.sse {
				writeSSE(w, msg.ID, result)
			} else {
				writeRPC(w, msg.ID, result)
			}
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
		}
	}))
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func writeSSE(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "text/event-stream")
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
}

func newTestClient(t *testing.T, baseURL, mode string, opts func(*Options)) *Client {
	t.Helper()
	o := Options{
		BaseURL:        baseURL,
		APIKey:         "test-key",
		MCPPath:        "/mcp/magpie/web-search",
		SearchMode:     mode,
		AlphaModel:     "magpie-search-adapter",
		FetchMode:      "mcp",
		InitTimeout:    2 * time.Second,
		RequestTimeout: 5 * time.Second,
		FetchTimeout:   5 * time.Second,
		MaxCount:       20,
		Logger:         testLogger(),
	}
	if opts != nil {
		opts(&o)
	}
	client, err := NewClient(o)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestStatelessMCPInitializesOnce(t *testing.T) {
	f := &fakeMCP{stateless: true}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "mcp", nil)
	defer client.Close()

	for i := 0; i < 2; i++ {
		if _, err := client.Search(context.Background(), "go", 2, nil); err != nil {
			t.Fatalf("search %d: %v", i, err)
		}
	}
	if got := f.initCount.Load(); got != 1 {
		t.Fatalf("initialize count = %d, want 1 for a stateless server", got)
	}
}

func TestAlphaReadyChecksFetchMCP(t *testing.T) {
	f := &fakeMCP{noFetch: true}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "alpha", nil)
	defer client.Close()

	if err := client.Ready(context.Background()); err == nil {
		t.Fatal("Ready must fail when alpha search is up but MCP has no web_fetch")
	}
}

func TestClientSearchMCP(t *testing.T) {
	f := &fakeMCP{}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "mcp", nil)
	defer client.Close()

	results, err := client.Search(context.Background(), "go", 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Title != "Go" || results[1].URL != "https://go.dev/blog/" {
		t.Fatalf("results = %#v", results)
	}
	if f.initCount.Load() != 1 {
		t.Fatalf("initialize count = %d, want 1", f.initCount.Load())
	}
	if f.missingSession.Load() {
		t.Fatal("a tools call omitted Mcp-Session-Id")
	}
	if f.lastCount.Load() != 2 {
		t.Fatalf("count = %d, want 2", f.lastCount.Load())
	}
}

func TestClientConcurrentSearchInitializesOnce(t *testing.T) {
	f := &fakeMCP{}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "mcp", nil)
	defer client.Close()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.Search(context.Background(), "go", 1, nil); err != nil {
				t.Errorf("search: %v", err)
			}
		}()
	}
	wg.Wait()
	if f.initCount.Load() != 1 {
		t.Fatalf("initialize count = %d, want 1", f.initCount.Load())
	}
}

func TestClientSearchSSE(t *testing.T) {
	f := &fakeMCP{sse: true}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "mcp", nil)
	defer client.Close()

	results, err := client.Search(context.Background(), "go", 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
}

func TestClientToolErrorNoSearcher(t *testing.T) {
	f := &fakeMCP{toolError: "magpie couldn't search the web: add a search API in magpie's Settings"}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "mcp", nil)
	defer client.Close()

	_, err := client.Search(context.Background(), "go", 1, nil)
	if !IsKind(err, KindNoSearcher) {
		t.Fatalf("err = %v, want KindNoSearcher", err)
	}
}

func TestClientFetch(t *testing.T) {
	f := &fakeMCP{}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "mcp", nil)
	defer client.Close()

	title, content, err := client.Fetch(context.Background(), "https://example.com/", 100000)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Example" || content != "Hello\nworld" {
		t.Fatalf("got title=%q content=%q", title, content)
	}
}

func TestClientReady(t *testing.T) {
	f := &fakeMCP{}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "mcp", nil)
	defer client.Close()

	if err := client.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientCountClamped(t *testing.T) {
	f := &fakeMCP{}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "mcp", func(o *Options) { o.MaxCount = 5 })
	defer client.Close()

	if _, err := client.Search(context.Background(), "go", 50, nil); err != nil {
		t.Fatal(err)
	}
	if f.lastCount.Load() != 5 {
		t.Fatalf("count = %d, want 5", f.lastCount.Load())
	}
}

func TestClientFetchDisabled(t *testing.T) {
	client := newTestClient(t, "http://127.0.0.1:1", "mcp", func(o *Options) { o.FetchMode = "off" })
	_, _, err := client.Fetch(context.Background(), "https://example.com/", 100000)
	if !IsKind(err, KindFetchDisabled) {
		t.Fatalf("err = %v, want KindFetchDisabled", err)
	}
}

type fakeAlphaState struct {
	mu      sync.Mutex
	model   string
	query   string
	domains []string
}

func TestClientAlphaSearchAndReady(t *testing.T) {
	state := &fakeAlphaState{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer test-key" {
				http.Error(w, "bad key", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		case "/v1/alpha/search":
			if r.Header.Get("Authorization") != "Bearer test-key" {
				http.Error(w, "bad key", http.StatusUnauthorized)
				return
			}
			var req alphaRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			state.mu.Lock()
			state.model = req.Model
			if len(req.Commands.SearchQuery) > 0 {
				state.query = req.Commands.SearchQuery[0].Q
				state.domains = req.Commands.SearchQuery[0].Domains
			}
			state.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":"1. Go — https://go.dev/dl/\ngo1.27.1\n"}`))
		case "/mcp/magpie/web-search":
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(body, &msg); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			switch msg.Method {
			case "initialize":
				w.Header().Set("Mcp-Session-Id", "test-session")
				writeRPC(w, msg.ID, map[string]any{"protocolVersion": "2025-06-18"})
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
			case "tools/list":
				writeRPC(w, msg.ID, map[string]any{"tools": []map[string]any{
					{"name": "web_search"},
					{"name": "web_fetch"},
				}})
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL, "alpha", nil)
	defer client.Close()
	if err := client.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, err := client.Search(context.Background(), "go", 1, []string{"go.dev"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].URL != "https://go.dev/dl/" {
		t.Fatalf("results = %#v", results)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.model != "magpie-search-adapter" || state.query != "go" || len(state.domains) != 1 || state.domains[0] != "go.dev" {
		t.Fatalf("alpha request = %#v", state)
	}
}

func TestClientAlphaEncryptedOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":"","encrypted_output":"abc"}`))
	}))
	defer srv.Close()
	client := newTestClient(t, srv.URL, "alpha", nil)
	defer client.Close()
	_, err := client.Search(context.Background(), "go", 1, nil)
	if !IsKind(err, KindProtocol) {
		t.Fatalf("err = %v, want KindProtocol", err)
	}
}

func TestClientEmptyToolResponseIsError(t *testing.T) {
	f := &fakeMCP{emptyCall: true}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "mcp", nil)
	defer client.Close()

	if _, err := client.Search(context.Background(), "go", 1, nil); err == nil {
		t.Fatal("a 202 with no JSON-RPC result must be an error, not an empty success")
	}
}

func TestClientReadTimeoutIsKindTimeout(t *testing.T) {
	f := &fakeMCP{stallBody: 300 * time.Millisecond}
	srv := newFakeMCP(t, f)
	defer srv.Close()
	client := newTestClient(t, srv.URL, "mcp", func(o *Options) {
		o.RequestTimeout = 100 * time.Millisecond
	})
	defer client.Close()

	_, err := client.Search(context.Background(), "go", 1, nil)
	if !IsKind(err, KindTimeout) {
		t.Fatalf("err = %v, want KindTimeout", err)
	}
}

func TestRedact(t *testing.T) {
	got := redact("key sk-magpie-key-abc123 is bad")
	if strings.Contains(got, "abc123") {
		t.Fatalf("redact left a key: %q", got)
	}
}
