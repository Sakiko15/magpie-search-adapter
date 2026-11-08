package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"magpie-search-adapter/internal/magpie"
)

// countSpec describes how one vendor counts results.
type countSpec struct {
	Default int
	Max     int // vendor's own cap; 0 means only the global MaxCount applies
	HardMax int // >0 rejects requests outside 1..HardMax instead of clamping
}

// resolveCount applies the vendor's default and cap, then the global cap.
func (h *Handlers) resolveCount(requested *int, spec countSpec) (int, error) {
	max := h.MaxCount
	if spec.Max > 0 && spec.Max < max {
		max = spec.Max
	}
	if requested == nil || *requested == 0 {
		return clampCount(spec.Default, spec.Default, max), nil
	}
	if spec.HardMax > 0 && (*requested < 1 || *requested > spec.HardMax) {
		return 0, fmt.Errorf("count must be 1..%d", spec.HardMax)
	}
	return clampCount(*requested, spec.Default, max), nil
}

// intParam reads the first present integer query/form parameter.
func intParam(values url.Values, names ...string) *int {
	for _, name := range names {
		raw := strings.TrimSpace(values.Get(name))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		return &n
	}
	return nil
}

// formValues reads an application/x-www-form-urlencoded body when there is
// one, and the URL query otherwise.
func formValues(r *http.Request, body []byte) (url.Values, error) {
	if len(body) > 0 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return url.ParseQuery(string(body))
	}
	return r.URL.Query(), nil
}

// contentOf prefers the page text and falls back to the snippet.
func contentOf(result magpie.Result) string {
	if result.Content != "" {
		return result.Content
	}
	return result.Snippet
}

// snippetOf prefers the snippet and falls back to the page text.
func snippetOf(result magpie.Result) string {
	if result.Snippet != "" {
		return result.Snippet
	}
	return result.Content
}

// hostOf is a result URL's host without a leading www.
func hostOf(raw string) string {
	return magpie.HostOf(raw)
}

// displayURL is a result's URL without its scheme, as Bing-style vendors
// present it.
func displayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host + u.RequestURI()
}

// prettyURL is SearXNG's URL without the scheme.
func prettyURL(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		return raw[i+3:]
	}
	return raw
}

// parsedURL is SearXNG's six-element URL breakdown.
func parsedURL(raw string) []string {
	u, err := url.Parse(raw)
	if err != nil {
		return []string{"", "", "", "", "", ""}
	}
	return []string{u.Scheme, u.Host, u.Path, "", u.RawQuery, ""}
}

// randomID is a short opaque identifier for vendors that require one.
func randomID(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", size*2)
	}
	return hex.EncodeToString(buf)
}
