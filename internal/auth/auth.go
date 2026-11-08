// Package auth checks the adapter's static keys and buffers the JSON body
// so a legacy Tavily api_key in the body can be checked too.
package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"magpie-search-adapter/internal/apierr"
)

type bodyKey struct{}

// BodyFromContext is the request body buffered by Middleware.
func BodyFromContext(ctx context.Context) ([]byte, bool) {
	b, ok := ctx.Value(bodyKey{}).([]byte)
	return b, ok
}

// KeySet holds hashes, never the keys themselves.
type KeySet struct {
	hashes [][sha256.Size]byte
}

func NewKeySet(keys []string) (*KeySet, error) {
	k := &KeySet{}
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			k.hashes = append(k.hashes, sha256.Sum256([]byte(key)))
		}
	}
	if len(k.hashes) == 0 {
		return nil, errors.New("no adapter API keys configured")
	}
	return k, nil
}

// Valid compares candidate against every stored hash in constant time.
func (k *KeySet) Valid(candidate string) bool {
	if candidate == "" || len(k.hashes) == 0 {
		return false
	}
	h := sha256.Sum256([]byte(candidate))
	var ok int
	for _, want := range k.hashes {
		ok |= subtle.ConstantTimeCompare(h[:], want[:])
	}
	return ok == 1
}

type Middleware struct {
	keys    *KeySet
	maxBody int64
	// keyless holds networks whose requests skip the key check. Empty by
	// default: every request needs a key.
	keyless []netip.Prefix
}

// Option tunes the middleware.
type Option func(*Middleware)

// WithKeylessCIDRs lets requests coming from these networks through
// without a key, for clients that cannot send one (SearXNG, Jina).
func WithKeylessCIDRs(prefixes []netip.Prefix) Option {
	return func(m *Middleware) { m.keyless = prefixes }
}

func NewMiddleware(keys []string, maxBody int64, opts ...Option) (*Middleware, error) {
	set, err := NewKeySet(keys)
	if err != nil {
		return nil, err
	}
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	m := &Middleware{keys: set, maxBody: maxBody}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		body, err := readBody(w, r, m.maxBody)
		if err != nil {
			status := http.StatusBadRequest
			message := "the request body could not be read"
			if errors.Is(err, errTooLarge) {
				status = http.StatusRequestEntityTooLarge
				message = "the request body is too large"
			}
			apierr.Write(w, apierr.Detect(r, nil), status, message)
			return
		}
		if body != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			r = r.WithContext(context.WithValue(r.Context(), bodyKey{}, body))
		}
		if !m.authorized(r, body) {
			apierr.Write(w, apierr.Detect(r, body), http.StatusUnauthorized, "invalid or missing API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) authorized(r *http.Request, body []byte) bool {
	for _, candidate := range []string{
		bearerToken(r.Header.Get("Authorization")),
		strings.TrimSpace(r.Header.Get("X-API-KEY")),
		strings.TrimSpace(r.Header.Get("X-Subscription-Token")),
	} {
		if m.keys.Valid(candidate) {
			return true
		}
	}
	// Some clients can only pass the key in the query: Google Programmable
	// Search uses "key", others use api_key/apikey.
	query := r.URL.Query()
	for _, name := range []string{"key", "api_key", "apikey", "api-key"} {
		if m.keys.Valid(strings.TrimSpace(query.Get(name))) {
			return true
		}
	}
	if len(body) > 0 {
		var payload struct {
			APIKey string `json:"api_key"`
		}
		if json.Unmarshal(body, &payload) == nil && m.keys.Valid(payload.APIKey) {
			return true
		}
	}
	return m.keylessCaller(r)
}

// keylessCaller reports whether the request comes from a network that is
// allowed to search without a key. Only the connection address counts;
// X-Forwarded-For is never trusted, so a proxied deployment must not rely
// on this.
func (m *Middleware) keylessCaller(r *http.Request) bool {
	if len(m.keyless) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range m.keyless {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func bearerToken(header string) string {
	const prefix = "bearer "
	if len(header) >= len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

var errTooLarge = errors.New("request body too large")

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errTooLarge
		}
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	return body, nil
}
