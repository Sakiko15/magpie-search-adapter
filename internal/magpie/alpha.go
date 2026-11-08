package magpie

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// alphaSearcher uses Magpie's Codex-compatible POST /v1/alpha/search. It
// never touches MCP, but unlike the MCP tool it cannot name a result count
// (Magpie's global Settings count is used) and has no fetch equivalent.
type alphaSearcher struct {
	endpoint   string
	apiKey     string
	model      string
	httpClient *http.Client
	log        *slog.Logger
}

func newAlphaSearcher(endpoint, apiKey, model string, client *http.Client, log *slog.Logger) *alphaSearcher {
	if log == nil {
		log = slog.Default()
	}
	return &alphaSearcher{endpoint: endpoint, apiKey: apiKey, model: model, httpClient: client, log: log}
}

type alphaQuery struct {
	Q       string   `json:"q"`
	Domains []string `json:"domains,omitempty"`
}

type alphaRequest struct {
	Model    string `json:"model"`
	Commands struct {
		SearchQuery []alphaQuery `json:"search_query"`
	} `json:"commands"`
	Settings struct {
		ExternalWebAccess bool `json:"external_web_access"`
	} `json:"settings"`
}

type alphaReply struct {
	EncryptedOutput *string `json:"encrypted_output"`
	Output          string  `json:"output"`
}

func (a *alphaSearcher) Search(ctx context.Context, query string, count int, domains []string) ([]Result, error) {
	reqBody := alphaRequest{Model: a.model}
	q := alphaQuery{Q: query}
	if len(domains) > 0 {
		q.Domains = domains
	}
	reqBody.Commands.SearchQuery = []alphaQuery{q}
	reqBody.Settings.ExternalWebAccess = true

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", mcpClientName+"/"+mcpClientVersion)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, &Error{Kind: KindTimeout, StatusCode: http.StatusGatewayTimeout, Message: "magpie did not answer in time", Err: err}
		}
		return nil, &Error{Kind: KindUpstream, StatusCode: http.StatusBadGateway, Message: "could not reach magpie", Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRPCBody+1))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, &Error{Kind: KindTimeout, StatusCode: http.StatusGatewayTimeout, Message: "magpie did not answer in time", Err: err}
		}
		return nil, &Error{Kind: KindUpstream, StatusCode: http.StatusBadGateway, Message: "could not read magpie's answer", Err: err}
	}
	if len(data) > maxRPCBody {
		return nil, protocolError("magpie's answer is too long")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, withRetryAfter(upstreamError(resp.StatusCode, errorMessage(data)), resp.Header)
	}

	var reply alphaReply
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, protocolError("magpie sent an unreadable alpha search answer")
	}
	output := strings.TrimSpace(sanitizeText(reply.Output))
	switch {
	case strings.Contains(output, "magpie couldn't search the web"):
		return nil, &Error{Kind: KindNoSearcher, StatusCode: http.StatusServiceUnavailable, Message: redact(output)}
	case strings.Contains(output, "magpie can only search the web for"):
		return nil, protocolError(redact(output))
	case output == "" && reply.EncryptedOutput != nil:
		return nil, protocolError("magpie's alpha search returned encrypted output; use MCP mode")
	case output == "":
		return nil, nil
	}
	output = stripChatGPTPrefix(output)
	results := ParsePages(output)
	if count > 0 && len(results) > count {
		results = results[:count]
	}
	return results, nil
}

// stripChatGPTPrefix removes the note Magpie prepends when a ChatGPT
// account's search failed and Magpie searched in its place.
func stripChatGPTPrefix(output string) string {
	const prefix = "ChatGPT's web search failed ("
	if !strings.HasPrefix(output, prefix) {
		return output
	}
	if i := strings.Index(output, "\n\n"); i >= 0 {
		return strings.TrimSpace(output[i+2:])
	}
	return output
}
