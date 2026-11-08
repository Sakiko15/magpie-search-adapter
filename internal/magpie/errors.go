package magpie

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Kind classifies an upstream or adapter error so the HTTP layer can pick a
// status code and message.
type Kind string

const (
	KindConfig        Kind = "config"
	KindAuth          Kind = "upstream_auth"
	KindForbidden     Kind = "upstream_forbidden"
	KindRateLimited   Kind = "upstream_rate_limited"
	KindTimeout       Kind = "upstream_timeout"
	KindUpstream      Kind = "upstream_error"
	KindProtocol      Kind = "upstream_protocol"
	KindNoSearcher    Kind = "no_searcher"
	KindInvalidInput  Kind = "invalid_input"
	KindFetchDisabled Kind = "fetch_disabled"
)

// Error carries enough for the HTTP layer to answer without leaking
// secrets.
type Error struct {
	Kind       Kind
	StatusCode int
	Message    string
	Err        error
	// RetryAfter is set when the upstream asked us to wait, so the
	// resilience layer can honour it instead of its own backoff.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Kind)
}

func (e *Error) Unwrap() error { return e.Err }

// IsKind reports whether err is an *Error of kind k.
func IsKind(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}

// IsStatus reports whether err is an *Error with the given upstream status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == status
}

func upstreamError(status int, message string) *Error {
	e := &Error{StatusCode: status, Message: message}
	switch status {
	case 401:
		e.Kind = KindAuth
	case 403:
		e.Kind = KindForbidden
	case 429:
		e.Kind = KindRateLimited
	case 408, 504:
		e.Kind = KindTimeout
	default:
		e.Kind = KindUpstream
	}
	return e
}

// withRetryAfter records the upstream's Retry-After, when it sent one.
func withRetryAfter(e *Error, header http.Header) *Error {
	if e == nil || header == nil {
		return e
	}
	if d, ok := parseRetryAfter(header.Get("Retry-After")); ok {
		e.RetryAfter = d
	}
	return e
}

// parseRetryAfter reads either the seconds or the HTTP-date form of
// Retry-After.
func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay, true
		}
		return 0, false
	}
	return 0, false
}
