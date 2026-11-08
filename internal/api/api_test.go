package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"magpie-search-adapter/internal/magpie"
)

type fakeBackend struct {
	mu           sync.Mutex
	results      []magpie.Result
	searchErr    error
	fetchTitle   string
	fetchContent string
	fetchErr     error
	fetchCalls   int
	lastQuery    string
	lastCount    int
	lastDomains  []string
}

func (f *fakeBackend) Search(ctx context.Context, query string, count int, domains []string) ([]magpie.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastQuery, f.lastCount, f.lastDomains = query, count, domains
	return f.results, f.searchErr
}

func (f *fakeBackend) Fetch(ctx context.Context, rawURL string, maxChars int) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetchCalls++
	return f.fetchTitle, f.fetchContent, f.fetchErr
}

func TestHandlersTavily(t *testing.T) {
	backend := &fakeBackend{results: []magpie.Result{
		{Title: "A", URL: "https://a.example/x", Content: "alpha"},
		{Title: "B", URL: "https://b.example/y", Content: "beta"},
	}}
	h := &Handlers{Backend: backend, MaxCount: 20, FetchMaxChars: 100000, FetchEnabled: true, Mode: "mcp", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := do(h.Search, http.MethodPost, "/search", `{"query":"go","max_results":1,"include_raw_content":true}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp tavilyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://a.example/x" {
		t.Fatalf("results = %#v", resp.Results)
	}
	if resp.Results[0].RawContent == nil || *resp.Results[0].RawContent != "alpha" {
		t.Fatalf("raw_content = %#v", resp.Results[0].RawContent)
	}
	if resp.Answer != nil {
		t.Fatalf("answer = %#v, want null", resp.Answer)
	}
}

func TestHandlersSerper(t *testing.T) {
	backend := &fakeBackend{results: []magpie.Result{
		{Title: "A", URL: "https://a.example/x", Content: "alpha"},
		{Title: "B", URL: "https://b.example/y", Content: "beta"},
	}}
	h := testHandlers(backend)
	rec := do(h.Search, http.MethodPost, "/search", `{"q":"go","num":2}`, map[string]string{"X-API-KEY": "key"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp serperResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Organic) != 2 || resp.Organic[0].Position != 1 || resp.Organic[1].Link != "https://b.example/y" {
		t.Fatalf("organic = %#v", resp.Organic)
	}
}

func TestHandlersSearchAmbiguous(t *testing.T) {
	h := testHandlers(&fakeBackend{})
	rec := do(h.Search, http.MethodPost, "/search", `{"query":"go","q":"go"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlersBrave(t *testing.T) {
	backend := &fakeBackend{results: []magpie.Result{
		{Title: "A", URL: "https://a.example/x", Content: "alpha"},
	}}
	h := testHandlers(backend)
	rec := do(h.BraveSearch, http.MethodGet, "/res/v1/web/search?q=go&count=1", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp braveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type != "search" || len(resp.Web.Results) != 1 || resp.Web.Results[0].URL != "https://a.example/x" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestHandlersDeepSeekNeedContent(t *testing.T) {
	backend := &fakeBackend{results: []magpie.Result{
		{Title: "A", URL: "https://a.example/x", Content: "alpha text"},
	}}
	h := testHandlers(backend)

	rec := do(h.DeepSeekSearch, http.MethodPost, "/v1/tools/search", `{"query":"go","count":1}`, nil)
	var resp deepseekSearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %#v", resp.Results)
	}
	got := resp.Results[0]
	if got.Content != "" {
		t.Fatalf("content = %q, want empty when need_content is false", got.Content)
	}
	if got.Snippet == "" || got.Summary == "" {
		t.Fatalf("snippet/summary empty: %#v", got)
	}
	if got.SiteName == nil || *got.SiteName != "a.example" {
		t.Fatalf("site_name = %#v", got.SiteName)
	}
	if got.Date != nil || got.SiteIcon != nil || got.Authority != -1 {
		t.Fatalf("nullable fields = %#v", got)
	}

	rec = do(h.DeepSeekSearch, http.MethodPost, "/v1/tools/search", `{"query":"go","count":1,"need_content":true}`, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Results[0].Content != "alpha text" {
		t.Fatalf("content = %q", resp.Results[0].Content)
	}
}

func TestHandlersDeepSeekCountClamp(t *testing.T) {
	backend := &fakeBackend{results: []magpie.Result{
		{Title: "A", URL: "https://a.example/x", Content: "alpha"},
	}}
	h := testHandlers(backend)
	rec := do(h.DeepSeekSearch, http.MethodPost, "/v1/tools/search", `{"query":"go","count":50}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.lastCount != 20 {
		t.Fatalf("upstream count = %d, want 20", backend.lastCount)
	}
}

func TestHandlersDeepSeekQueryTooLong(t *testing.T) {
	h := testHandlers(&fakeBackend{})
	rec := do(h.DeepSeekSearch, http.MethodPost, "/v1/tools/search", `{"query":"`+strings.Repeat("a", 129)+`"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlersDeepSeekFetch(t *testing.T) {
	backend := &fakeBackend{fetchTitle: "Example", fetchContent: "Hello"}
	h := testHandlers(backend)
	rec := do(h.DeepSeekFetch, http.MethodPost, "/v1/tools/fetch", `{"url":"https://example.com/"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp deepseekFetchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Title != "Example" || resp.Content != "Hello" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestHandlersFetchPrivateURL(t *testing.T) {
	backend := &fakeBackend{}
	h := testHandlers(backend)
	rec := do(h.DeepSeekFetch, http.MethodPost, "/v1/tools/fetch", `{"url":"http://127.0.0.1/x"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if backend.fetchCalls != 0 {
		t.Fatalf("fetch calls = %d, want 0", backend.fetchCalls)
	}
}

func TestHandlersEmptyArrays(t *testing.T) {
	h := testHandlers(&fakeBackend{})
	rec := do(h.DeepSeekSearch, http.MethodPost, "/v1/tools/search", `{"query":"go"}`, nil)
	if !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	rec = do(h.Search, http.MethodPost, "/search", `{"query":"go"}`, nil)
	if !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Fatalf("tavily body = %s", rec.Body.String())
	}
	rec = do(h.Search, http.MethodPost, "/search", `{"q":"go"}`, map[string]string{"X-API-KEY": "key"})
	if !strings.Contains(rec.Body.String(), `"organic":[]`) {
		t.Fatalf("serper body = %s", rec.Body.String())
	}
	rec = do(h.BraveSearch, http.MethodGet, "/res/v1/web/search?q=go", "", nil)
	if !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Fatalf("brave body = %s", rec.Body.String())
	}
}

func TestHandlersErrorMapping(t *testing.T) {
	backend := &fakeBackend{searchErr: &magpie.Error{Kind: magpie.KindNoSearcher}}
	h := testHandlers(backend)
	rec := do(h.DeepSeekSearch, http.MethodPost, "/v1/tools/search", `{"query":"go"}`, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	backend.searchErr = &magpie.Error{Kind: magpie.KindInvalidInput}
	rec = do(h.DeepSeekSearch, http.MethodPost, "/v1/tools/search", `{"query":"go"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlersDomainFilter(t *testing.T) {
	backend := &fakeBackend{results: []magpie.Result{
		{Title: "A", URL: "https://a.example/x", Content: "alpha"},
		{Title: "B", URL: "https://b.example/y", Content: "beta"},
	}}
	h := testHandlers(backend)
	rec := do(h.Search, http.MethodPost, "/search", `{"query":"go","max_results":5,"include_domains":["a.example"]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp tavilyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://a.example/x" {
		t.Fatalf("results = %#v", resp.Results)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.lastCount != 20 {
		t.Fatalf("upstream count = %d, want 20 with domain filters", backend.lastCount)
	}
}

func TestHandlersExtract(t *testing.T) {
	backend := &fakeBackend{fetchContent: "Hello"}
	h := testHandlers(backend)
	rec := do(h.TavilyExtract, http.MethodPost, "/extract", `{"urls":["https://a.example/x","not a url"]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp extractResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || len(resp.FailedResults) != 1 {
		t.Fatalf("response = %#v", resp)
	}
	if resp.Results[0].RawContent != "Hello" {
		t.Fatalf("raw_content = %q", resp.Results[0].RawContent)
	}
}

func testHandlers(backend *fakeBackend) *Handlers {
	return &Handlers{
		Backend:       backend,
		MaxCount:      20,
		DefaultCount:  6,
		FetchMaxChars: 100000,
		FetchEnabled:  true,
		Mode:          "mcp",
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func do(handler func(http.ResponseWriter, *http.Request), method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}
