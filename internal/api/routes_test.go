package api

import (
	"net/http/httptest"
	"testing"

	"magpie-search-adapter/internal/apierr"
)

// TestRoutesMatchDetect keeps the route table and the error-envelope table
// in step: every registered path must be recognised by apierr.Detect.
func TestRoutesMatchDetect(t *testing.T) {
	h := testHandlers(&fakeBackend{})
	seen := make(map[string]bool)
	for _, route := range h.Routes() {
		if seen[route.Pattern] {
			t.Fatalf("duplicate route %q", route.Pattern)
		}
		seen[route.Pattern] = true
		if route.Dispatch {
			continue
		}
		req := httptest.NewRequest(route.Method(), route.Path(), nil)
		if got := apierr.Detect(req, nil); got != route.Vendor {
			t.Errorf("%s: Detect = %v, want %v", route.Pattern, got, route.Vendor)
		}
	}
}
