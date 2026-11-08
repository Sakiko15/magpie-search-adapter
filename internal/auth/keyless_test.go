package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestQueryParamKeys(t *testing.T) {
	mw, err := NewMiddleware([]string{"adapter-key"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	handler := mw.Wrap(okHandler())

	for _, name := range []string{"key", "api_key", "apikey", "api-key"} {
		req := httptest.NewRequest("GET", "/customsearch/v1?q=go&"+name+"=adapter-key", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", name, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/customsearch/v1?q=go&key=wrong", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong query key: status = %d, want 401", rec.Code)
	}
}

func TestKeylessCIDRs(t *testing.T) {
	mw, err := NewMiddleware([]string{"adapter-key"}, 1024,
		WithKeylessCIDRs([]netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/8"),
			netip.MustParsePrefix("::1/128"),
		}))
	if err != nil {
		t.Fatal(err)
	}
	handler := mw.Wrap(okHandler())

	tests := []struct {
		remote string
		want   int
	}{
		{"10.1.2.3:4444", http.StatusOK},
		{"[::1]:4444", http.StatusOK},
		{"192.168.1.5:4444", http.StatusUnauthorized},
		{"bogus", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		req := httptest.NewRequest("GET", "/searxng/search?q=go&format=json", nil)
		req.RemoteAddr = tt.remote
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("%s: status = %d, want %d", tt.remote, rec.Code, tt.want)
		}
	}
}

func TestKeylessOffByDefault(t *testing.T) {
	mw, err := NewMiddleware([]string{"adapter-key"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/searxng/search?q=go", nil)
	req.RemoteAddr = "10.1.2.3:4444"
	rec := httptest.NewRecorder()
	mw.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
