package resilience

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"magpie-search-adapter/internal/magpie"
)

type fakeSearcher struct {
	mu       sync.Mutex
	calls    int32
	failures int
	err      error
	results  []magpie.Result
	block    bool
}

func (f *fakeSearcher) Search(ctx context.Context, query string, count int, includeDomains []string) ([]magpie.Result, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	fail := f.failures > 0
	if fail {
		f.failures--
	}
	err := f.err
	results := f.results
	block := f.block
	f.mu.Unlock()

	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if fail {
		return nil, err
	}
	return results, nil
}

func (f *fakeSearcher) Fetch(ctx context.Context, url string, maxChars int) (string, string, error) {
	return "title", "content", nil
}

func (f *fakeSearcher) count() int { return int(atomic.LoadInt32(&f.calls)) }

var sample = []magpie.Result{{Title: "A", URL: "https://a.example/x", Content: "c"}}

func newFake() *fakeSearcher {
	return &fakeSearcher{results: sample}
}

func TestRetriesThenSucceeds(t *testing.T) {
	inner := newFake()
	inner.failures = 2
	inner.err = &magpie.Error{Kind: magpie.KindUpstream, StatusCode: 502, Message: "bad gateway"}
	s := Wrap(inner, Config{Retries: 2, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}, nil)

	results, err := s.Search(context.Background(), "go", 5, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
	if inner.count() != 3 {
		t.Fatalf("calls = %d, want 3", inner.count())
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	inner := newFake()
	inner.failures = 5
	inner.err = &magpie.Error{Kind: magpie.KindInvalidInput, StatusCode: 400, Message: "bad query"}
	s := Wrap(inner, Config{Retries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}, nil)

	if _, err := s.Search(context.Background(), "go", 5, nil); err == nil {
		t.Fatal("want an error")
	}
	if inner.count() != 1 {
		t.Fatalf("calls = %d, want 1", inner.count())
	}
}

func TestCacheHitAndExpiry(t *testing.T) {
	inner := newFake()
	s := Wrap(inner, Config{CacheTTL: 40 * time.Millisecond, CacheSize: 8}, nil)

	for i := 0; i < 3; i++ {
		if _, err := s.Search(context.Background(), "go", 5, nil); err != nil {
			t.Fatalf("err = %v", err)
		}
	}
	if inner.count() != 1 {
		t.Fatalf("calls = %d, want 1", inner.count())
	}
	if _, err := s.Search(context.Background(), "go", 6, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	if inner.count() != 2 {
		t.Fatalf("a different count must not hit the cache: calls = %d", inner.count())
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := s.Search(context.Background(), "go", 5, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	if inner.count() != 3 {
		t.Fatalf("expired entry must be refetched: calls = %d", inner.count())
	}
}

func TestSingleflightCoalesces(t *testing.T) {
	inner := newFake()
	inner.block = true
	s := Wrap(inner, Config{Coalesce: true, Budget: 80 * time.Millisecond}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_, _ = s.Search(ctx, "same query", 5, nil)
		}()
	}
	time.Sleep(20 * time.Millisecond)
	if got := inner.count(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	inner.mu.Lock()
	inner.block = false
	inner.mu.Unlock()
	wg.Wait()
}

func TestBudgetBoundsRetries(t *testing.T) {
	inner := newFake()
	inner.block = true
	s := Wrap(inner, Config{Retries: 5, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond, Budget: 40 * time.Millisecond}, nil)

	start := time.Now()
	if _, err := s.Search(context.Background(), "go", 5, nil); err == nil {
		t.Fatal("want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("took %s, the budget did not bound it", elapsed)
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	inner := newFake()
	inner.failures = 1
	inner.err = &magpie.Error{
		Kind:       magpie.KindRateLimited,
		StatusCode: 429,
		Message:    "slow down",
		RetryAfter: 40 * time.Millisecond,
	}
	s := Wrap(inner, Config{Retries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, Budget: time.Second}, nil)

	start := time.Now()
	if _, err := s.Search(context.Background(), "go", 5, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("waited %s, Retry-After was ignored", elapsed)
	}
}

type panicOnceSearcher struct {
	fakeSearcher
	panicked bool
}

func (p *panicOnceSearcher) Search(ctx context.Context, query string, count int, includeDomains []string) ([]magpie.Result, error) {
	if !p.panicked {
		p.panicked = true
		panic("boom")
	}
	return sample, nil
}

func TestSingleflightSurvivesPanic(t *testing.T) {
	inner := &panicOnceSearcher{}
	s := Wrap(inner, Config{Coalesce: true}, nil)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("want the panic to propagate")
			}
		}()
		_, _ = s.Search(context.Background(), "panic", 5, nil)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := s.Search(ctx, "panic", 5, nil); err != nil {
		t.Fatalf("second call after the panic: %v", err)
	}
}

func TestFetchPassesThrough(t *testing.T) {
	inner := newFake()
	s := Wrap(inner, Config{CacheTTL: time.Minute, CacheSize: 4}, nil)
	title, content, err := s.Fetch(context.Background(), "https://example.com", 100)
	if err != nil || title != "title" || content != "content" {
		t.Fatalf("got %q %q %v", title, content, err)
	}
}
