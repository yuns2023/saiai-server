package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// DefaultUserAgent identifies the scanner. Public Solana RPC (2026-11-16)
// accepts ordinary non-browser agents and rejects browser agents that lack
// the rest of the browser headers, so an honest UA is the right default.
const DefaultUserAgent = "saiai-cryptopay/1"

const (
	defaultRPCTimeout       = 30 * time.Second
	defaultMaxResponseBytes = 16 << 20
)

// RPCOptions configures a JSON-RPC endpoint.
type RPCOptions struct {
	URL              string
	Timeout          time.Duration // per request; default 30s
	UserAgent        string        // default DefaultUserAgent
	MaxResponseBytes int64         // default 16 MiB; larger responses fail with ErrResponseTooLarge
	Headers          map[string]string
	HTTPClient       *http.Client // optional; Timeout above still applies via context
}

// RPCError is a JSON-RPC error object returned by the node.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// HTTPStatusError is a non-200 HTTP response from the RPC endpoint.
type HTTPStatusError struct {
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("rpc http status %d: %s", e.StatusCode, e.Body)
}

// ErrResponseTooLarge means the response exceeded MaxResponseBytes.
var ErrResponseTooLarge = errors.New("chain: rpc response exceeds size limit")

type rpcClient struct {
	opts   RPCOptions
	client *http.Client
	nextID atomic.Uint64
}

func newRPCClient(opts RPCOptions) (*rpcClient, error) {
	if opts.URL == "" {
		return nil, errors.New("chain: rpc URL is required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultRPCTimeout
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = defaultMaxResponseBytes
	}
	c := opts.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: opts.Timeout}
	}
	return &rpcClient{opts: opts, client: c}, nil
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// call performs one JSON-RPC request and returns the raw result ("null" is
// returned as-is; callers decide whether null is an error).
func (c *rpcClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: c.nextID.Add(1), Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.opts.UserAgent)
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chain: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.opts.MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("chain: %s: read body: %w", method, err)
	}
	if int64(len(data)) > c.opts.MaxResponseBytes {
		return nil, fmt.Errorf("chain: %s: %w", method, ErrResponseTooLarge)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := data
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		// Some providers return a JSON-RPC error body with a non-200 status.
		var r rpcResponse
		if json.Unmarshal(data, &r) == nil && r.Error != nil {
			return nil, fmt.Errorf("chain: %s: %w", method, r.Error)
		}
		return nil, fmt.Errorf("chain: %s: %w", method, &HTTPStatusError{StatusCode: resp.StatusCode, Body: string(snippet)})
	}
	var r rpcResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("chain: %s: decode response: %w", method, err)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("chain: %s: %w", method, r.Error)
	}
	if len(r.Result) == 0 {
		return nil, fmt.Errorf("chain: %s: response has neither result nor error", method)
	}
	return r.Result, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null"
}
