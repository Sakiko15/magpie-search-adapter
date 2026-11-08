package magpie

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mcpProtocolVersion = "2025-06-18"
	mcpClientName      = "magpie-search-adapter"
	mcpClientVersion   = "1.0.0"
	maxRPCBody         = 4 << 20
)

var errNoMatch = errors.New("no matching JSON-RPC response")

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpTransport struct {
	endpoint    string
	apiKey      string
	httpClient  *http.Client
	initTimeout time.Duration
	log         *slog.Logger

	mu           sync.Mutex
	session      string
	protocol     string
	initialized  bool
	initializing bool
	initDone     chan struct{}
	initErr      error

	nextID atomic.Int64
}

func newMCPTransport(endpoint, apiKey string, client *http.Client, initTimeout time.Duration, log *slog.Logger) *mcpTransport {
	if log == nil {
		log = slog.Default()
	}
	return &mcpTransport{
		endpoint:    endpoint,
		apiKey:      apiKey,
		httpClient:  client,
		initTimeout: initTimeout,
		log:         log,
	}
}

// ensureSession initializes once, without holding the lock over the
// network. Concurrent callers wait; a failed init lets the next caller
// try again.
func (t *mcpTransport) ensureSession(ctx context.Context) (session, protocol string, err error) {
	t.mu.Lock()
	// A stateless MCP server answers without Mcp-Session-Id; the empty
	// session is still a completed initialization.
	if t.initialized {
		s, p := t.session, t.protocol
		t.mu.Unlock()
		return s, p, nil
	}
	if t.initializing {
		done := t.initDone
		t.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
		t.mu.Lock()
		s, p, e := t.session, t.protocol, t.initErr
		t.mu.Unlock()
		return s, p, e
	}
	t.initializing = true
	t.initDone = make(chan struct{})
	done := t.initDone
	t.mu.Unlock()

	// Initialization is shared: one client's cancellation must not poison the
	// callers waiting behind it. The defer publishes the outcome on every
	// path, so a panic inside initialize cannot leave waiters stuck on done.
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("magpie MCP initialization panicked: %v", recovered)
			t.publishInit(done, "", "", err)
			panic(recovered)
		}
		t.publishInit(done, session, protocol, err)
	}()

	initCtx, cancel := context.WithTimeout(context.Background(), t.initTimeout)
	defer cancel()
	session, protocol, err = t.initialize(initCtx)
	return
}

// publishInit records the shared initialization outcome and wakes waiters.
func (t *mcpTransport) publishInit(done chan struct{}, session, protocol string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err == nil {
		t.session, t.protocol = session, protocol
		t.initialized = true
	}
	t.initErr = err
	t.initializing = false
	close(done)
}

func (t *mcpTransport) invalidate() {
	t.mu.Lock()
	t.session, t.protocol = "", ""
	t.initialized = false
	t.mu.Unlock()
}

func (t *mcpTransport) initialize(ctx context.Context) (string, string, error) {
	id := t.nextID.Add(1)
	params := map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    mcpClientName,
			"version": mcpClientVersion,
		},
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: "initialize", Params: params})
	if err != nil {
		return "", "", err
	}
	status, headers, data, err := t.post(ctx, body, "", "")
	if err != nil {
		return "", "", err
	}
	if status == http.StatusAccepted && len(bytes.TrimSpace(data)) == 0 {
		return "", "", protocolError("initialize returned no JSON-RPC response")
	}
	result, err := decodeResponse(data, headers.Get("Content-Type"), id)
	if err != nil {
		return "", "", err
	}
	protocol := mcpProtocolVersion
	if len(result) > 0 {
		var init struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(result, &init) == nil && init.ProtocolVersion != "" {
			protocol = init.ProtocolVersion
		}
	}
	session := headers.Get("Mcp-Session-Id")
	if err := t.notify(ctx, session, protocol); err != nil {
		// Magpie accepts the server without it; not worth failing the
		// whole initialization.
		t.log.Debug("magpie MCP initialized notification failed", "error", err)
	}
	return session, protocol, nil
}

func (t *mcpTransport) notify(ctx context.Context, session, protocol string) error {
	body := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	status, _, data, err := t.post(ctx, body, session, protocol)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("notifications/initialized answered %d: %s", status, snippetBytes(data))
	}
	return nil
}

func (t *mcpTransport) rpc(ctx context.Context, method string, params any, session, protocol string) (json.RawMessage, error) {
	id := t.nextID.Add(1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	status, headers, data, err := t.post(ctx, body, session, protocol)
	if err != nil {
		return nil, err
	}
	if status == http.StatusAccepted && len(bytes.TrimSpace(data)) == 0 {
		return nil, protocolError("magpie answered without a JSON-RPC result")
	}
	return decodeResponse(data, headers.Get("Content-Type"), id)
}

func (t *mcpTransport) post(ctx context.Context, body []byte, session, protocol string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", mcpClientName+"/"+mcpClientVersion)
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	if protocol != "" {
		req.Header.Set("MCP-Protocol-Version", protocol)
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, nil, nil, &Error{Kind: KindTimeout, StatusCode: http.StatusGatewayTimeout, Message: "magpie did not answer in time", Err: err}
		}
		return 0, nil, nil, &Error{Kind: KindUpstream, StatusCode: http.StatusBadGateway, Message: "could not reach magpie", Err: err}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRPCBody+1))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return resp.StatusCode, resp.Header, nil, &Error{Kind: KindTimeout, StatusCode: http.StatusGatewayTimeout, Message: "magpie did not answer in time", Err: err}
		}
		return resp.StatusCode, resp.Header, data, &Error{Kind: KindUpstream, StatusCode: http.StatusBadGateway, Message: "could not read magpie's answer", Err: err}
	}
	if len(data) > maxRPCBody {
		return resp.StatusCode, resp.Header, nil, protocolError("magpie's answer is too long")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, resp.Header, data, withRetryAfter(upstreamError(resp.StatusCode, errorMessage(data)), resp.Header)
	}
	return resp.StatusCode, resp.Header, data, nil
}

func decodeResponse(data []byte, contentType string, wantID int64) (json.RawMessage, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return decodeSSE(bytes.NewReader(data), wantID)
	}
	return decodeJSON(data, wantID)
}

func decodeJSON(data []byte, wantID int64) (json.RawMessage, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, protocolError("magpie sent an invalid JSON-RPC batch")
		}
		for _, raw := range batch {
			result, matched, err := decodeOne(raw, wantID)
			if err != nil {
				return nil, err
			}
			if matched {
				return result, nil
			}
		}
		return nil, errNoMatch
	}
	result, matched, err := decodeOne(data, wantID)
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, errNoMatch
	}
	return result, nil
}

func decodeOne(raw json.RawMessage, wantID int64) (json.RawMessage, bool, error) {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, false, protocolError("magpie sent an invalid JSON-RPC message")
	}
	if len(msg.Error) > 0 && !bytes.Equal(bytes.TrimSpace(msg.Error), []byte("null")) {
		var e struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(msg.Error, &e) != nil {
			return nil, true, protocolError("magpie sent an unreadable JSON-RPC error")
		}
		code := e.Code
		if code == 0 {
			code = -32603
		}
		return nil, true, protocolError(fmt.Sprintf("magpie JSON-RPC error %d: %s", code, redact(e.Message)))
	}
	if !idMatches(msg.ID, wantID) {
		return nil, false, nil
	}
	return msg.Result, true, nil
}

func decodeSSE(r io.Reader, wantID int64) (json.RawMessage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRPCBody)
	var data []string
	try := func() (json.RawMessage, bool, error) {
		if len(data) == 0 {
			return nil, false, nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		result, err := decodeJSON([]byte(payload), wantID)
		if errors.Is(err, errNoMatch) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return result, true, nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			result, matched, err := try()
			if err != nil {
				return nil, err
			}
			if matched {
				return result, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			v := strings.TrimPrefix(line, "data:")
			data = append(data, strings.TrimPrefix(v, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, protocolError("magpie's event stream could not be read")
	}
	result, matched, err := try()
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, protocolError("magpie's event stream had no JSON-RPC response")
	}
	return result, nil
}

func idMatches(raw json.RawMessage, want int64) bool {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return false
	}
	s = strings.Trim(s, `"`)
	return s == strconv.FormatInt(want, 10)
}

type mcpTool struct {
	Name string `json:"name"`
}

func (t *mcpTransport) toolsList(ctx context.Context) ([]mcpTool, error) {
	session, protocol, err := t.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	result, err := t.rpc(ctx, "tools/list", nil, session, protocol)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []mcpTool `json:"tools"`
	}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &out); err != nil {
			return nil, protocolError("magpie sent an unreadable tools list")
		}
	}
	return out.Tools, nil
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (t *mcpTransport) callTool(ctx context.Context, name string, args any) (string, error) {
	text, err := t.callToolOnce(ctx, name, args)
	if err != nil && IsStatus(err, http.StatusNotFound) {
		// A future server may reject an expired session; Magpie itself
		// never does. Reinitialize once and try again.
		t.invalidate()
		text, err = t.callToolOnce(ctx, name, args)
	}
	return text, err
}

func (t *mcpTransport) callToolOnce(ctx context.Context, name string, args any) (string, error) {
	session, protocol, err := t.ensureSession(ctx)
	if err != nil {
		return "", err
	}
	params := map[string]any{"name": name, "arguments": args}
	result, err := t.rpc(ctx, "tools/call", params, session, protocol)
	if err != nil {
		return "", err
	}
	var out toolResult
	if len(result) > 0 {
		if err := json.Unmarshal(result, &out); err != nil {
			return "", protocolError("magpie sent an unreadable tool result")
		}
	}
	var text strings.Builder
	for _, block := range out.Content {
		if block.Type != "text" || block.Text == "" {
			continue
		}
		if text.Len() > 0 {
			text.WriteString("\n")
		}
		text.WriteString(block.Text)
	}
	if out.IsError {
		return "", toolError(text.String())
	}
	return text.String(), nil
}

func (t *mcpTransport) webSearch(ctx context.Context, query string, count int) (string, error) {
	args := map[string]any{"query": query}
	if count > 0 {
		args["count"] = count
	}
	return t.callTool(ctx, "web_search", args)
}

func (t *mcpTransport) webFetch(ctx context.Context, url string, maxChars int) (string, error) {
	if maxChars <= 0 || maxChars > 100000 {
		maxChars = 100000
	}
	args := map[string]any{"url": url, "max_chars": maxChars, "offset": 0}
	return t.callTool(ctx, "web_fetch", args)
}

func toolError(text string) *Error {
	lower := strings.ToLower(text)
	message := redact(text)
	switch {
	case strings.Contains(lower, "couldn't search the web"),
		strings.Contains(lower, "add a search api"),
		strings.Contains(lower, "no provider that can search"):
		return &Error{Kind: KindNoSearcher, StatusCode: http.StatusServiceUnavailable, Message: message}
	case strings.Contains(lower, "private address"),
		strings.Contains(lower, "reads only http"),
		strings.Contains(lower, "offset can't be negative"),
		strings.Contains(lower, "couldn't fetch"):
		return &Error{Kind: KindInvalidInput, StatusCode: http.StatusBadRequest, Message: message}
	case strings.Contains(lower, "rate limit"),
		strings.Contains(lower, "too many"),
		strings.Contains(lower, "quota"),
		strings.Contains(lower, "credit"):
		return &Error{Kind: KindRateLimited, StatusCode: http.StatusTooManyRequests, Message: message}
	default:
		if message == "" {
			message = "magpie returned an error"
		}
		return &Error{Kind: KindUpstream, StatusCode: http.StatusBadGateway, Message: message}
	}
}

func errorMessage(data []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &payload) == nil && payload.Error.Message != "" {
		return redact(payload.Error.Message)
	}
	return redact(snippetBytes(data))
}

var secretPattern = regexp.MustCompile(`sk-magpie-key-[A-Za-z0-9_-]+`)

func redact(s string) string {
	return secretPattern.ReplaceAllString(s, "sk-magpie-key-***")
}

func protocolError(message string) *Error {
	return &Error{Kind: KindProtocol, StatusCode: http.StatusBadGateway, Message: message}
}

func snippetBytes(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > 300 {
		s = s[:300]
	}
	if s == "" {
		return "upstream request failed"
	}
	return s
}
