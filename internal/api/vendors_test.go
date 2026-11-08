package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"magpie-search-adapter/internal/magpie"
)

func vendorBackend() *fakeBackend {
	return &fakeBackend{results: []magpie.Result{
		{Title: "A", URL: "https://a.example/x", Content: "alpha", Snippet: "alpha snippet"},
		{Title: "B", URL: "https://b.example/y", Content: "beta", Snippet: "beta snippet"},
	}}
}

func decode[T any](t *testing.T, rec interface{ Bytes() []byte }) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	return out
}

func TestExaShape(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.ExaSearch, http.MethodPost, "/exa/search",
		`{"query":"go","numResults":2,"contents":{"text":true,"highlights":true}}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[exaResponse](t, rec.Body)
	if len(resp.Results) != 2 || resp.Results[0].URL != "https://a.example/x" {
		t.Fatalf("results = %#v", resp.Results)
	}
	if resp.Results[0].Text != "alpha" || len(resp.Results[0].Highlights) != 1 {
		t.Fatalf("content = %#v", resp.Results[0])
	}
	if resp.RequestID == "" || resp.CostDollars.Total != 0 {
		t.Fatalf("envelope = %#v", resp)
	}
}

func TestExaOnSharedSearchPath(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.Search, http.MethodPost, "/search", `{"query":"go","numResults":2}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[exaResponse](t, rec.Body)
	if len(resp.Results) != 2 {
		t.Fatalf("results = %#v", resp.Results)
	}
	if resp.Results[0].Text != "" {
		t.Fatalf("text must be omitted unless asked for: %#v", resp.Results[0])
	}
}

func TestFirecrawlShapes(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.FirecrawlV1, http.MethodPost, "/firecrawl/v1/search", `{"query":"go","limit":2}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("v1 status = %d: %s", rec.Code, rec.Body.String())
	}
	v1 := decode[firecrawlV1Response](t, rec.Body)
	if !v1.Success || len(v1.Data) != 2 || v1.Data[0].Markdown != nil {
		t.Fatalf("v1 = %#v", v1)
	}

	rec = do(h.FirecrawlV2, http.MethodPost, "/firecrawl/v2/search", `{"query":"go","limit":2}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("v2 status = %d: %s", rec.Code, rec.Body.String())
	}
	v2 := decode[firecrawlV2Response](t, rec.Body)
	if !v2.Success || len(v2.Data.Web) != 2 || len(v2.Data.Images) != 0 || v2.CreditsUsed != 0 || v2.ID == "" {
		t.Fatalf("v2 = %#v", v2)
	}
}

func TestNativeV1SearchDispatch(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.V1Search, http.MethodPost, "/v1/search", `{"query":"go","limit":2}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("firecrawl status = %d: %s", rec.Code, rec.Body.String())
	}
	if v1 := decode[firecrawlV1Response](t, rec.Body); !v1.Success || len(v1.Data) != 2 {
		t.Fatalf("firecrawl = %#v", v1)
	}

	rec = do(h.V1Search, http.MethodPost, "/v1/search", `{"query":"go","count":2}`, map[string]string{"X-API-Key": "k"})
	if rec.Code != http.StatusOK {
		t.Fatalf("you status = %d: %s", rec.Code, rec.Body.String())
	}
	you := decode[youResponse](t, rec.Body)
	if len(you.Results.Web) != 2 || you.Metadata.Query != "go" {
		t.Fatalf("you = %#v", you)
	}
}

func TestSearxngShape(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.SearxngSearch, http.MethodGet, "/searxng/search?q=go&format=json&count=2", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[searxngResponse](t, rec.Body)
	if resp.NumberOfResults != 2 || len(resp.Results) != 2 {
		t.Fatalf("results = %#v", resp)
	}
	if resp.Results[0].Engine != "magpie" || len(resp.Results[0].ParsedURL) != 6 {
		t.Fatalf("result = %#v", resp.Results[0])
	}

	rec = do(h.SearxngSearch, http.MethodGet, "/searxng/search?q=go&pageno=2", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("page 2 status = %d", rec.Code)
	}
	page2 := decode[searxngResponse](t, rec.Body)
	if len(page2.Results) != 0 || page2.NumberOfResults != 0 {
		t.Fatalf("page 2 = %#v", page2)
	}
}

func TestSearxngOnNativeGET(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.SearxngSearch, http.MethodGet, "/search?q=go&format=json", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if resp := decode[searxngResponse](t, rec.Body); len(resp.Results) == 0 {
		t.Fatalf("results = %#v", resp)
	}
}

func TestGoogleShape(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.GoogleSearch, http.MethodGet, "/customsearch/v1?q=go&num=2&cx=abc", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[googleResponse](t, rec.Body)
	if len(resp.Items) != 2 || resp.Items[0].Link != "https://a.example/x" {
		t.Fatalf("items = %#v", resp.Items)
	}
	if resp.SearchInformation.TotalResults != "2" || resp.Queries.Request[0].CX != "abc" {
		t.Fatalf("envelope = %#v", resp)
	}

	rec = do(h.GoogleSearch, http.MethodGet, "/customsearch/v1?q=go&num=11", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("num=11 status = %d", rec.Code)
	}
	if body := decode[map[string]any](t, rec.Body); body["error"] == nil {
		t.Fatalf("error shape = %#v", body)
	}
}

func TestGooglePaging(t *testing.T) {
	backend := vendorBackend()
	h := testHandlers(backend)
	rec := do(h.GoogleSearch, http.MethodGet, "/customsearch/v1?q=go&num=2&start=2", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[googleResponse](t, rec.Body)
	if len(resp.Items) != 1 || resp.Items[0].Link != "https://b.example/y" {
		t.Fatalf("items = %#v", resp.Items)
	}
	if backend.lastCount != 3 {
		t.Fatalf("upstream count = %d, want start-1+num = 3", backend.lastCount)
	}
}

func TestYouComOffset(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.YouComSearch, http.MethodPost, "/you/v1/search", `{"query":"go","count":1,"offset":1}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[youResponse](t, rec.Body)
	if len(resp.Results.Web) != 1 || resp.Results.Web[0].URL != "https://b.example/y" {
		t.Fatalf("page 2 = %#v", resp.Results.Web)
	}
	for _, body := range []string{
		`{"query":"go","offset":10}`,
		`{"query":"go","offset":-1}`,
		`{"query":"go","offset":9223372036854775807}`,
	} {
		rec := do(h.YouComSearch, http.MethodPost, "/you/v1/search", body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestGoogleStartBounds(t *testing.T) {
	h := testHandlers(vendorBackend())
	for _, target := range []string{
		"/customsearch/v1?q=go&num=2&start=101",
		"/customsearch/v1?q=go&num=2&start=9223372036854775807",
	} {
		rec := do(h.GoogleSearch, http.MethodGet, target, "", nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", target, rec.Code)
		}
	}
}

func TestGoogleSiteSearchCase(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.GoogleSearch, http.MethodGet, "/customsearch/v1?q=go&num=2&siteSearch=WWW.A.EXAMPLE", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[googleResponse](t, rec.Body)
	if len(resp.Items) != 1 || resp.Items[0].Link != "https://a.example/x" {
		t.Fatalf("items = %#v", resp.Items)
	}
}

func TestExaErrorShapeOnSharedPath(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.Search, http.MethodPost, "/search", `{"query":"","numResults":5}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"requestId", "error", "tag"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing %q in %s", key, rec.Body.String())
		}
	}
}

func TestJinaPostWithEmptyBody(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.JinaSearch, http.MethodPost, "/jina/?q=go&num=1", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[jinaResponse](t, rec.Body)
	if len(resp.Data) != 1 {
		t.Fatalf("data = %#v", resp.Data)
	}
}

func TestExtractMixedFailures(t *testing.T) {
	backend := &fakeBackend{fetchErr: errors.New("boom")}
	h := testHandlers(backend)
	rec := do(h.TavilyExtract, http.MethodPost, "/extract",
		`{"urls":["not a url","https://example.com/x"]}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[extractResponse](t, rec.Body)
	if len(resp.Results) != 0 || len(resp.FailedResults) != 2 {
		t.Fatalf("response = %#v", resp)
	}
}

func TestDomainWithPortRejected(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.Search, http.MethodPost, "/search", `{"query":"go","include_domains":["example.com:443"]}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBochaShape(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.BochaSearch, http.MethodPost, "/bocha/v1/web-search",
		`{"query":"go","count":2,"summary":true}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[bochaResponse](t, rec.Body)
	if resp.Code != 200 || len(resp.Data.WebPages.Value) != 2 {
		t.Fatalf("response = %#v", resp)
	}
	if resp.Data.WebPages.Value[0].Summary != "alpha" || resp.LogID == "" {
		t.Fatalf("value = %#v", resp.Data.WebPages.Value[0])
	}
}

func TestZhipuShape(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.ZhipuSearch, http.MethodPost, "/api/paas/v4/web_search",
		`{"search_query":"go","search_engine":"search_std","count":2}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[zhipuResponse](t, rec.Body)
	if len(resp.SearchResult) != 2 || resp.SearchResult[0].Refer != "1" {
		t.Fatalf("response = %#v", resp)
	}
	if len(resp.SearchIntent) != 1 || resp.SearchIntent[0].Intent != "SEARCH_ALWAYS" {
		t.Fatalf("intent = %#v", resp.SearchIntent)
	}
}

func TestJinaShape(t *testing.T) {
	h := testHandlers(vendorBackend())
	rec := do(h.JinaSearch, http.MethodGet, "/jina/?q=go&num=2", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	resp := decode[jinaResponse](t, rec.Body)
	if resp.Code != 200 || resp.Status != 20000 || len(resp.Data) != 2 {
		t.Fatalf("response = %#v", resp)
	}
}

func TestNewVendorErrorShapes(t *testing.T) {
	h := testHandlers(vendorBackend())
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		method  string
		target  string
		body    string
		key     string
	}{
		{"bocha", h.BochaSearch, http.MethodPost, "/bocha/v1/web-search", `{}`, "code"},
		{"zhipu", h.ZhipuSearch, http.MethodPost, "/api/paas/v4/web_search", `{}`, "error"},
		{"jina", h.JinaSearch, http.MethodGet, "/jina/", "", "code"},
		{"searxng", h.SearxngSearch, http.MethodGet, "/searxng/search", "", "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(tt.handler, tt.method, tt.target, tt.body, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if _, ok := body[tt.key]; !ok {
				t.Fatalf("missing %q in %s", tt.key, rec.Body.String())
			}
		})
	}
}
