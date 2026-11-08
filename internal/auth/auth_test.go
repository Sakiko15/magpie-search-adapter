package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKeySet(t *testing.T) {
	set, err := NewKeySet([]string{"key-1", "key-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !set.Valid("key-1") || !set.Valid("key-2") {
		t.Fatal("a configured key was refused")
	}
	if set.Valid("key-3") || set.Valid("") {
		t.Fatal("an unknown key was accepted")
	}
	if _, err := NewKeySet(nil); err == nil {
		t.Fatal("an empty key set was accepted")
	}
}

func TestMiddlewareCredentials(t *testing.T) {
	mw, err := NewMiddleware([]string{"adapter-key"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	handler := mw.Wrap(next)

	tests := []struct {
		name    string
		body    string
		headers map[string]string
		want    int
	}{
		{name: "bearer", body: `{"query":"x"}`, headers: map[string]string{"Authorization": "Bearer adapter-key"}, want: http.StatusOK},
		{name: "x-api-key", body: `{"q":"x"}`, headers: map[string]string{"X-API-KEY": "adapter-key"}, want: http.StatusOK},
		{name: "brave", body: "", headers: map[string]string{"X-Subscription-Token": "adapter-key"}, want: http.StatusOK},
		{name: "body api_key", body: `{"query":"x","api_key":"adapter-key"}`, want: http.StatusOK},
		{name: "wrong key", body: `{"query":"x"}`, headers: map[string]string{"Authorization": "Bearer nope"}, want: http.StatusUnauthorized},
		{name: "none", body: `{"query":"x"}`, want: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(tt.body))
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want == http.StatusOK && tt.body != "" && rec.Body.String() != tt.body {
				t.Fatalf("body was not restored: %q", rec.Body.String())
			}
		})
	}
}

func TestMiddlewareHealthBypass(t *testing.T) {
	mw, err := NewMiddleware([]string{"adapter-key"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestMiddlewareVendorErrorShape(t *testing.T) {
	mw, err := NewMiddleware([]string{"adapter-key"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	tests := []struct {
		path   string
		header map[string]string
		body   string
		check  func(t *testing.T, payload map[string]any)
	}{
		{
			path: "/res/v1/web/search?q=x",
			check: func(t *testing.T, payload map[string]any) {
				if payload["type"] != "ErrorResponse" {
					t.Fatalf("brave shape = %#v", payload)
				}
			},
		},
		{
			path:   "/search",
			header: map[string]string{"X-API-KEY": "nope"},
			body:   `{"q":"x"}`,
			check: func(t *testing.T, payload map[string]any) {
				if _, ok := payload["message"]; !ok {
					t.Fatalf("serper shape = %#v", payload)
				}
			},
		},
		{
			path: "/search",
			body: `{"query":"x"}`,
			check: func(t *testing.T, payload map[string]any) {
				if _, ok := payload["detail"]; !ok {
					t.Fatalf("tavily shape = %#v", payload)
				}
			},
		},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
		for k, v := range tt.header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d", tt.path, rec.Code)
		}
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s body: %v", tt.path, err)
		}
		tt.check(t, payload)
	}
}

func TestMiddlewareBodyTooLarge(t *testing.T) {
	mw, err := NewMiddleware([]string{"adapter-key"}, 8)
	if err != nil {
		t.Fatal(err)
	}
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader([]byte(`{"query":"a long body"}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}
