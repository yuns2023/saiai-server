package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	MaxResponseBytes int64         // default 16 MiB; larger responses fail with ErrTooMuchData
	Headers          map[string]string
	HTTPClient       *http.Client // optional; Timeout above still applies via context
}

// RPCError is the JSON-RPC error object on the wire. It is never returned
// as an error (its Message/Data are provider-controlled text).
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// RPCErrorKind is the fixed classification of an RPC failure.
type RPCErrorKind string

const (
	RPCKindTimeout     RPCErrorKind = "timeout"
	RPCKindCanceled    RPCErrorKind = "canceled"
	RPCKindTransport   RPCErrorKind = "transport"
	RPCKindHTTPStatus  RPCErrorKind = "http_status"
	RPCKindRPCError    RPCErrorKind = "rpc_error"
	RPCKindTooMuchData RPCErrorKind = "too_much_data"
	RPCKindDecode      RPCErrorKind = "decode"
)

// ErrTooMuchData is the sentinel behind RPCKindTooMuchData: the provider
// refused or could not deliver the result size (narrow the query).
var ErrTooMuchData = errors.New("chain: rpc result too large for provider; narrow the query")

// RPCCallError is the only error type returned by the RPC client. It
// deliberately carries NO provider-supplied text, URL, header or body: only
// the method name, the HTTP status, the JSON-RPC numeric code and a fixed
// classification. Unwrap exposes only sentinels (context.DeadlineExceeded,
// context.Canceled, ErrTooMuchData), never the raw transport error.
type RPCCallError struct {
	Method     string
	Kind       RPCErrorKind
	HTTPStatus int // 0 if no HTTP response
	RPCCode    int // valid when HasRPCCode
	HasRPCCode bool
}

func (e *RPCCallError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "chain: rpc %s: %s", e.Method, e.Kind)
	if e.HTTPStatus != 0 {
		fmt.Fprintf(&b, " http=%d", e.HTTPStatus)
	}
	if e.HasRPCCode {
		fmt.Fprintf(&b, " code=%d", e.RPCCode)
	}
	return b.String()
}

// Unwrap exposes only sentinel errors.
func (e *RPCCallError) Unwrap() error {
	switch e.Kind {
	case RPCKindTimeout:
		return context.DeadlineExceeded
	case RPCKindCanceled:
		return context.Canceled
	case RPCKindTooMuchData:
		return ErrTooMuchData
	}
	return nil
}

// Timeout reports whether the call timed out.
func (e *RPCCallError) Timeout() bool { return e.Kind == RPCKindTimeout }

type rpcClient struct {
	opts   RPCOptions
	client *http.Client
	nextID atomic.Uint64
}

// Provider phrases for "narrow your query" (Geth/Erigon/Alchemy/Infura/
// QuickNode/Ankr/Arbitrum Nitro). Only used for classification; the text is
// never kept. A false positive only causes a bisection, which ends in an
// error at a single block/address; it can never hide data.
var tooMuchDataPhrases = []string{
	"query returned more than",
	"too many results",
	"range too large",
	"range is too large",
	"block range",
	"blocks range", // QuickNode: "eth_getLogs is limited to a 10000 blocks range"
	"limited to a",
	"exceed maximum block range",
	"response size",
	"response is too big",
	"too large",
	"query timeout",
}

func rpcErrorIsTooMuchData(e *RPCError) bool {
	msg := strings.ToLower(e.Message)
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many requests") {
		return false
	}
	if e.Code == -32005 { // Infura "limit exceeded"
		return true
	}
	for _, p := range tooMuchDataPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
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

func (c *rpcClient) fail(method string, kind RPCErrorKind, status int, rpcErr *RPCError) error {
	e := &RPCCallError{Method: method, Kind: kind, HTTPStatus: status}
	if rpcErr != nil {
		e.RPCCode, e.HasRPCCode = rpcErr.Code, true
	}
	return e
}

func classifyTransport(ctx context.Context, err error) RPCErrorKind {
	var te interface{ Timeout() bool }
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return RPCKindCanceled
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		(errors.As(err, &te) && te.Timeout()):
		return RPCKindTimeout
	}
	return RPCKindTransport
}

// idMatches allows a null/absent id only on error responses.
func idMatches(raw json.RawMessage, want uint64, allowNull bool) bool {
	got := string(bytes.TrimSpace(raw))
	if got == strconv.FormatUint(want, 10) {
		return true
	}
	return allowNull && (got == "" || got == "null")
}

// call performs one JSON-RPC request and returns the raw result ("null" is
// returned as-is; callers decide whether null is an error). Every error is a
// *RPCCallError without provider-supplied text.
func (c *rpcClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	id := c.nextID.Add(1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, c.fail(method, RPCKindDecode, 0, nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.URL, bytes.NewReader(body))
	if err != nil {
		return nil, c.fail(method, RPCKindTransport, 0, nil)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.opts.UserAgent)
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, c.fail(method, classifyTransport(ctx, err), 0, nil)
	}
	defer func() { _ = resp.Body.Close() }()
	status := resp.StatusCode
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.opts.MaxResponseBytes+1))
	if err != nil {
		return nil, c.fail(method, classifyTransport(ctx, err), status, nil)
	}
	if int64(len(data)) > c.opts.MaxResponseBytes {
		return nil, c.fail(method, RPCKindTooMuchData, status, nil)
	}

	var r rpcResponse
	decodeErr := json.Unmarshal(data, &r)
	if status != http.StatusOK {
		var rpcErr *RPCError
		if decodeErr == nil && r.Error != nil && idMatches(r.ID, id, true) {
			rpcErr = r.Error
		}
		switch {
		case status == http.StatusTooManyRequests:
			return nil, c.fail(method, RPCKindHTTPStatus, status, rpcErr) // rate limited: never split
		case status == http.StatusRequestEntityTooLarge || status == http.StatusGatewayTimeout:
			return nil, c.fail(method, RPCKindTooMuchData, status, rpcErr)
		case rpcErr != nil && rpcErrorIsTooMuchData(rpcErr):
			return nil, c.fail(method, RPCKindTooMuchData, status, rpcErr)
		case rpcErr != nil:
			return nil, c.fail(method, RPCKindRPCError, status, rpcErr)
		}
		return nil, c.fail(method, RPCKindHTTPStatus, status, nil)
	}
	if decodeErr != nil {
		return nil, c.fail(method, RPCKindDecode, status, nil)
	}
	if r.Error != nil {
		if !idMatches(r.ID, id, true) {
			return nil, c.fail(method, RPCKindDecode, status, nil) // someone else's error
		}
		if rpcErrorIsTooMuchData(r.Error) {
			return nil, c.fail(method, RPCKindTooMuchData, status, r.Error)
		}
		return nil, c.fail(method, RPCKindRPCError, status, r.Error)
	}
	if !idMatches(r.ID, id, false) || len(r.Result) == 0 {
		return nil, c.fail(method, RPCKindDecode, status, nil)
	}
	return r.Result, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null"
}
