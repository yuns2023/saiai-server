package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
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
	opts    RPCOptions
	client  *http.Client
	nextID  atomic.Uint64
	secrets []string // substrings that must never appear in returned errors
}

// redactedError hides the endpoint (which often embeds an API key in its
// path or query) while keeping errors.Is/As on the underlying cause
// (context.DeadlineExceeded, net.Error, ...).
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// Timeout reports whether the underlying error is a timeout.
func (e *redactedError) Timeout() bool {
	var ne interface{ Timeout() bool }
	return errors.As(e.err, &ne) && ne.Timeout()
}

const redactedMarker = "[REDACTED]"

func endpointSecrets(raw string, headers map[string]string) []string {
	set := map[string]struct{}{raw: {}}
	if u, err := url.Parse(raw); err == nil {
		set[u.String()] = struct{}{}
		if p := strings.Trim(u.EscapedPath(), "/"); len(p) >= 6 {
			set[p] = struct{}{}
			set[u.Path] = struct{}{}
		}
		if p := strings.Trim(u.EscapedPath(), "/"); p != "" {
			for _, seg := range strings.Split(p, "/") {
				if len(seg) >= 6 {
					set[seg] = struct{}{}
				}
			}
		}
		if u.RawQuery != "" {
			set[u.RawQuery] = struct{}{}
			for _, vs := range u.Query() {
				for _, v := range vs {
					if len(v) >= 4 {
						set[v] = struct{}{}
						set[url.QueryEscape(v)] = struct{}{}
					}
				}
			}
		}
		if u.User != nil {
			set[u.User.String()] = struct{}{}
			if pw, ok := u.User.Password(); ok && pw != "" {
				set[pw] = struct{}{}
			}
		}
	}
	for _, v := range headers {
		if len(v) >= 4 {
			set[v] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		if len(s) >= 4 {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func (c *rpcClient) redact(s string) string {
	for _, sec := range c.secrets {
		s = strings.ReplaceAll(s, sec, redactedMarker)
	}
	return s
}

func (c *rpcClient) redactRPCError(e *RPCError) *RPCError {
	out := &RPCError{Code: e.Code, Message: c.redact(e.Message)}
	if len(e.Data) > 0 {
		out.Data = json.RawMessage(c.redact(string(e.Data)))
	}
	return out
}

func (c *rpcClient) transportError(method string, err error) error {
	cause := err
	op := "request"
	var ue *url.Error
	if errors.As(err, &ue) {
		cause, op = ue.Err, ue.Op
	}
	return &redactedError{msg: fmt.Sprintf("chain: %s: %s failed: %s", method, op, c.redact(cause.Error())), err: cause}
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
	return &rpcClient{opts: opts, client: c, secrets: endpointSecrets(opts.URL, opts.Headers)}, nil
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
	id := c.nextID.Add(1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.URL, bytes.NewReader(body))
	if err != nil {
		return nil, c.transportError(method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.opts.UserAgent)
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, c.transportError(method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.opts.MaxResponseBytes+1))
	if err != nil {
		return nil, c.transportError(method, err)
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
			return nil, fmt.Errorf("chain: %s: http %d: %w", method, resp.StatusCode, c.redactRPCError(r.Error))
		}
		return nil, fmt.Errorf("chain: %s: %w", method, &HTTPStatusError{StatusCode: resp.StatusCode, Body: c.redact(string(snippet))})
	}
	var r rpcResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("chain: %s: decode response: %w", method, err)
	}
	wantID := strconv.FormatUint(id, 10)
	if r.Error != nil {
		// A null id is legal for errors the server could not attribute; any
		// other id must be ours, or the error belongs to another request.
		if gotID := string(bytes.TrimSpace(r.ID)); gotID != wantID && gotID != "null" && gotID != "" {
			return nil, fmt.Errorf("chain: %s: error response id %s does not match request id %d", method, c.redact(gotID), id)
		}
		return nil, fmt.Errorf("chain: %s: %w", method, c.redactRPCError(r.Error))
	}
	if string(bytes.TrimSpace(r.ID)) != wantID {
		return nil, fmt.Errorf("chain: %s: response id %s does not match request id %d", method, c.redact(string(r.ID)), id)
	}
	if len(r.Result) == 0 {
		return nil, fmt.Errorf("chain: %s: response has neither result nor error", method)
	}
	return r.Result, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null"
}
