package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"magpie-search-adapter/internal/magpie"
)

type mixedFetchBackend struct {
	mu   sync.Mutex
	ok   map[string]string
	fail map[string]bool
}

func (b *mixedFetchBackend) Search(context.Context, string, int, []string) ([]magpie.Result, error) {
	return nil, nil
}

func (b *mixedFetchBackend) Fetch(_ context.Context, rawURL string, _ int) (string, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail[rawURL] {
		return "", "", errors.New("boom")
	}
	return "", b.ok[rawURL], nil
}

// TestExtractConcurrentMixed exercises the workers and the dispatch loop with
// several URLs at once. Run with -race: reading the failed map before
// wg.Wait() used to be a fatal concurrent map read/write.
func TestExtractConcurrentMixed(t *testing.T) {
	urls := []string{
		"https://a.example/1", "https://a.example/2", "https://a.example/3",
		"https://a.example/4", "https://a.example/5", "https://a.example/6",
		"not a url", "http://127.0.0.1/", "https://a.example/9", "https://a.example/10",
	}
	backend := &mixedFetchBackend{ok: map[string]string{}, fail: map[string]bool{}}
	valid := 0
	for _, rawURL := range urls {
		if !strings.HasPrefix(rawURL, "https://") {
			continue
		}
		valid++
		if valid%4 == 0 {
			backend.fail[rawURL] = true
		} else {
			backend.ok[rawURL] = "content"
		}
	}
	body, err := json.Marshal(map[string]any{"urls": urls})
	if err != nil {
		t.Fatal(err)
	}
	h := testHandlers(backend)
	rec := do(h.TavilyExtract, http.MethodPost, "/extract", string(body), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[extractResponse](t, rec.Body)
	if len(resp.Results)+len(resp.FailedResults) != len(urls) {
		t.Fatalf("results+failures = %d, want %d", len(resp.Results)+len(resp.FailedResults), len(urls))
	}
	if len(resp.Results) == 0 || len(resp.FailedResults) == 0 {
		t.Fatalf("want both successes and failures: %#v", resp)
	}
}

func TestValidateFetchURLRejectsEmptyHostAndCGNAT(t *testing.T) {
	for _, raw := range []string{
		"http://:8080/",
		"http://100.64.0.1/",
		"http://169.254.169.254/",
		"http://127.0.0.1/",
		"http://10.0.0.1/",
	} {
		if err := validateFetchURL(raw); err == nil {
			t.Errorf("validateFetchURL(%q) = nil, want an error", raw)
		}
	}
	for _, raw := range []string{
		"https://example.com/",
		"http://8.8.8.8/",
	} {
		if err := validateFetchURL(raw); err != nil {
			t.Errorf("validateFetchURL(%q) = %v, want nil", raw, err)
		}
	}
}

func TestFilterDomainsKeepsURLLessResult(t *testing.T) {
	results := []magpie.Result{{Title: "prose answer", Content: "no link parsed"}}
	got := filterDomains(results, []string{"example.com"}, nil)
	if len(got) != 1 {
		t.Fatalf("got %d results, want the URL-less fallback to survive", len(got))
	}
}

func TestNormalizeDomainsRejectsWildcardAndHugeLists(t *testing.T) {
	if _, err := normalizeDomains([]string{"*"}); err == nil {
		t.Fatal("a bare * must be rejected")
	}
	in := make([]string, maxDomains+1)
	for i := range in {
		in[i] = fmt.Sprintf("d%d.example", i)
	}
	if _, err := normalizeDomains(in); err == nil {
		t.Fatalf("%d domains must be rejected", len(in))
	}
}

func TestModelsHideDisabledFetch(t *testing.T) {
	h := testHandlers(&fakeBackend{})
	h.FetchEnabled = false
	rec := do(h.Models, http.MethodGet, "/v1/models", "", nil)
	resp := decode[modelList](t, rec.Body)
	if len(resp.Data) != 1 || resp.Data[0].ID != "deepseek-search" {
		t.Fatalf("models = %#v, want only deepseek-search", resp.Data)
	}
}

func TestReadyCoalescesProbes(t *testing.T) {
	h := testHandlers(&fakeBackend{})
	var calls atomic.Int32
	release := make(chan struct{})
	h.ReadyFn = func(context.Context) error {
		if calls.Add(1) == 1 {
			<-release
		}
		return nil
	}

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			h.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			codes <- rec.Code
		}()
	}
	close(start)
	time.Sleep(20 * time.Millisecond) // let the leader claim the probe
	close(release)
	wg.Wait()
	close(codes)

	if calls.Load() != 1 {
		t.Fatalf("probes = %d, want 1", calls.Load())
	}
	for code := range codes {
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
	}
}

func TestSharedSearchPathPrefersExaShape(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.Search, http.MethodPost, "/search", `{"query":"go","q":"go","numResults":2}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[exaResponse](t, rec.Body)
	if len(resp.Results) != 2 {
		t.Fatalf("results = %#v, want the Exa shape", resp.Results)
	}
}
