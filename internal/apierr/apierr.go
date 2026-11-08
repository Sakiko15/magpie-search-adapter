// Package apierr writes errors in each search vendor's shape.
package apierr

import (
	"encoding/json"
	"net/http"
	"strings"
)

type Vendor int

const (
	OpenAI Vendor = iota
	Tavily
	Serper
	Brave
	Exa
	Firecrawl
	SearXNG
	Google
	YouCom
	Bocha
	Zhipu
	Jina
)

type pathRule struct {
	path   string
	prefix bool
	vendor Vendor
}

// pathRules maps client-facing paths to the error envelope they answer
// with. Exact paths win over prefixes, so order matters only within the
// same kind.
var pathRules = []pathRule{
	{path: "/res/v1/web/search", vendor: Brave},
	{path: "/exa/search", vendor: Exa},
	{path: "/firecrawl/", prefix: true, vendor: Firecrawl},
	{path: "/v2/search", vendor: Firecrawl},
	{path: "/searxng/search", vendor: SearXNG},
	{path: "/google/customsearch/v1", vendor: Google},
	{path: "/customsearch/v1", vendor: Google},
	{path: "/you/v1/search", vendor: YouCom},
	{path: "/bocha/v1/web-search", vendor: Bocha},
	{path: "/v1/web-search", vendor: Bocha},
	{path: "/zhipu/", prefix: true, vendor: Zhipu},
	{path: "/api/paas/v4/web_search", vendor: Zhipu},
	{path: "/jina", vendor: Jina},
	{path: "/jina/", prefix: true, vendor: Jina},
	{path: "/v1/tools/", prefix: true, vendor: OpenAI},
	{path: "/v1/models", vendor: OpenAI},
	{path: "/extract", vendor: Tavily},
}

// Detect picks the vendor from the path and, for the shared search paths,
// from the request body and headers.
func Detect(r *http.Request, body []byte) Vendor {
	path := r.URL.Path
	for _, rule := range pathRules {
		if rule.prefix {
			if strings.HasPrefix(path, rule.path) {
				return rule.vendor
			}
			continue
		}
		if path == rule.path {
			return rule.vendor
		}
	}
	switch path {
	case "/search":
		return searchVendor(r, body)
	case "/v1/search":
		if r.Method == http.MethodGet || r.Header.Get("X-API-Key") != "" || bodyHas(body, "freshness") {
			return YouCom
		}
		return Firecrawl
	}
	return OpenAI
}

// searchVendor separates the shapes that share a path: SearXNG (GET or a
// form body), Exa (its own body keys), and Tavily/Serper (query or q).
func searchVendor(r *http.Request, body []byte) Vendor {
	if r.Method == http.MethodGet ||
		strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return SearXNG
	}
	if len(body) > 0 {
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) == nil {
			for _, key := range []string{"numResults", "contents", "additionalQueries"} {
				if _, ok := fields[key]; ok {
					return Exa
				}
			}
			_, hasQuery := fields["query"]
			_, hasQ := fields["q"]
			if hasQuery && hasQ {
				if r.Header.Get("X-API-KEY") != "" {
					return Serper
				}
				return Tavily
			}
			if hasQuery {
				return Tavily
			}
			if hasQ {
				return Serper
			}
		}
	}
	if r.Header.Get("X-API-KEY") != "" {
		return Serper
	}
	return Tavily
}

func bodyHas(body []byte, key string) bool {
	if len(body) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	_, ok := fields[key]
	return ok
}

// Write writes message in vendor's shape with status.
func Write(w http.ResponseWriter, vendor Vendor, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	var body any
	switch vendor {
	case Tavily:
		body = map[string]any{"detail": map[string]any{"error": message}}
	case Serper:
		body = map[string]any{"message": message}
	case Brave:
		body = map[string]any{
			"type": "ErrorResponse",
			"error": map[string]any{
				"code":   status,
				"detail": message,
			},
		}
	case Exa:
		body = map[string]any{"requestId": "", "error": message, "tag": exaTag(status)}
	case Firecrawl:
		body = map[string]any{"success": false, "code": firecrawlCode(status), "error": message}
	case SearXNG:
		body = map[string]any{"error": message}
	case Google:
		body = map[string]any{
			"error": map[string]any{
				"code":    status,
				"message": message,
				"errors": []any{map[string]any{
					"message": message,
					"domain":  "global",
					"reason":  googleReason(status),
				}},
			},
		}
	case YouCom:
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			body = map[string]any{"detail": message}
		} else {
			body = map[string]any{"error": message}
		}
	case Bocha:
		body = map[string]any{"code": status, "msg": message, "log_id": nil, "data": nil}
	case Zhipu:
		body = map[string]any{"error": map[string]any{"code": zhipuCode(status), "message": message}}
	case Jina:
		body = map[string]any{"code": status, "status": status * 100, "data": nil, "message": message}
	default:
		body = map[string]any{
			"error": map[string]any{
				"message": message,
				"type":    errorType(status),
				"param":   nil,
				"code":    errorCode(status),
			},
		}
	}
	_ = json.NewEncoder(w).Encode(body)
}

func errorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status == http.StatusBadRequest:
		return "invalid_request_error"
	default:
		return "upstream_error"
	}
}

func errorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusUnauthorized:
		return "invalid_api_key"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case http.StatusServiceUnavailable:
		return "upstream_unavailable"
	default:
		return "upstream_error"
	}
}

func exaTag(status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "INVALID_REQUEST"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "UNAUTHORIZED"
	case status == http.StatusTooManyRequests:
		return "RATE_LIMIT_EXCEEDED"
	default:
		return "UPSTREAM_ERROR"
	}
}

func firecrawlCode(status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "BAD_REQUEST"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "UNAUTHORIZED"
	case status == http.StatusTooManyRequests:
		return "RATE_LIMIT_EXCEEDED"
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return "REQUEST_TIMEOUT"
	default:
		return "UNKNOWN_ERROR"
	}
}

func googleReason(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return "invalidParameter"
	case http.StatusTooManyRequests:
		return "rateLimitExceeded"
	default:
		return "backendError"
	}
}

func zhipuCode(status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "1210"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "1002"
	case status == http.StatusTooManyRequests:
		return "1302"
	default:
		return "500"
	}
}
