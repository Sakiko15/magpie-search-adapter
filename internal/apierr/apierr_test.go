package apierr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		header map[string]string
		body   string
		want   Vendor
	}{
		{name: "tavily search", method: "POST", path: "/search", body: `{"query":"x"}`, want: Tavily},
		{name: "serper search", method: "POST", path: "/search", body: `{"q":"x"}`, want: Serper},
		{name: "serper header", method: "POST", path: "/search", header: map[string]string{"X-API-KEY": "k"}, body: `{}`, want: Serper},
		{name: "searxng get", method: "GET", path: "/search", want: SearXNG},
		{name: "searxng form", method: "POST", path: "/search", header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, body: "q=x", want: SearXNG},
		{name: "brave", method: "GET", path: "/res/v1/web/search", want: Brave},
		{name: "deepseek", method: "POST", path: "/v1/tools/search", want: OpenAI},
		{name: "exa", method: "POST", path: "/exa/search", want: Exa},
		{name: "firecrawl v1 native", method: "POST", path: "/v1/search", body: `{"query":"x","limit":3}`, want: Firecrawl},
		{name: "you.com native", method: "POST", path: "/v1/search", header: map[string]string{"X-API-Key": "k"}, body: `{"query":"x"}`, want: YouCom},
		{name: "you.com legacy get", method: "GET", path: "/v1/search", want: YouCom},
		{name: "google", method: "GET", path: "/customsearch/v1", want: Google},
		{name: "bocha", method: "POST", path: "/v1/web-search", want: Bocha},
		{name: "zhipu", method: "POST", path: "/api/paas/v4/web_search", want: Zhipu},
		{name: "jina", method: "GET", path: "/jina/", want: Jina},
		{name: "unknown", method: "GET", path: "/nope", want: OpenAI},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			if got := Detect(req, []byte(tt.body)); got != tt.want {
				t.Fatalf("Detect = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWriteShapes(t *testing.T) {
	tests := []struct {
		vendor Vendor
		status int
		want   []string
	}{
		{Tavily, http.StatusBadRequest, []string{"detail"}},
		{Serper, http.StatusBadRequest, []string{"message"}},
		{Brave, http.StatusBadRequest, []string{"type", "error"}},
		{Exa, http.StatusBadRequest, []string{"requestId", "error", "tag"}},
		{Firecrawl, http.StatusBadRequest, []string{"success", "code", "error"}},
		{SearXNG, http.StatusBadRequest, []string{"error"}},
		{Google, http.StatusBadRequest, []string{"error"}},
		{YouCom, http.StatusUnauthorized, []string{"detail"}},
		{YouCom, http.StatusBadRequest, []string{"error"}},
		{Bocha, http.StatusBadRequest, []string{"code", "msg", "log_id", "data"}},
		{Zhipu, http.StatusBadRequest, []string{"error"}},
		{Jina, http.StatusBadRequest, []string{"code", "status", "data", "message"}},
		{OpenAI, http.StatusBadRequest, []string{"error"}},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		Write(rec, tt.vendor, tt.status, "boom")
		if rec.Code != tt.status {
			t.Errorf("vendor %v: status = %d, want %d", tt.vendor, rec.Code, tt.status)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("vendor %v: %v", tt.vendor, err)
		}
		if len(body) != len(tt.want) {
			t.Errorf("vendor %v: keys = %v, want %v", tt.vendor, keysOf(body), tt.want)
			continue
		}
		for _, key := range tt.want {
			if _, ok := body[key]; !ok {
				t.Errorf("vendor %v: missing key %q in %v", tt.vendor, key, keysOf(body))
			}
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
