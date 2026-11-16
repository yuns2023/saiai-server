package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// rpcClient is a minimal JSON-RPC client for the online tool. The endpoint
// URL (which may embed an API key) is never printed: every error message is
// scrubbed of the URL, its host-less path and its query values.
type rpcClient struct {
	url     string
	secrets []string
	http    *http.Client
	id      atomic.Uint64
}

func newRPC(rawURL string) (*rpcClient, error) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("invalid RPC URL (not shown)")
	}
	c := &rpcClient{url: rawURL, http: &http.Client{Timeout: 30 * time.Second}}
	add := func(v string, min int) {
		if len(v) >= min {
			c.secrets = append(c.secrets, v)
		}
	}
	// Mask the whole URL, its path and query, and every path segment / query
	// value long enough to be a credential (API keys are 20+ chars; short
	// values like "v2", "rpc", "true" are not masked to avoid mangling output).
	add(rawURL, 1)
	add(u.RawQuery, 4)
	add(u.Path, 12)
	for _, seg := range strings.Split(u.Path, "/") {
		add(seg, 12)
		if d, err := url.PathUnescape(seg); err == nil {
			add(d, 12)
		}
	}
	for _, vs := range u.Query() {
		for _, v := range vs {
			add(v, 8)
			add(url.QueryEscape(v), 8)
		}
	}
	if u.User != nil {
		add(u.User.String(), 1)
		if p, ok := u.User.Password(); ok {
			add(p, 1)
		}
	}
	// Longest first so a secret contained in another is still masked.
	sort.Slice(c.secrets, func(i, j int) bool { return len(c.secrets[i]) > len(c.secrets[j]) })
	registerSecrets(c.secrets)
	return c, nil
}

// Every secret of every RPC client is also masked at the process output
// boundary (stdout, stderr, error message, result file), because RPC-derived
// text reaches those through many paths.
var (
	secretsMu  sync.Mutex
	allSecrets []string
)

func registerSecrets(s []string) {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	allSecrets = append(allSecrets, s...)
	sort.Slice(allSecrets, func(i, j int) bool { return len(allSecrets[i]) > len(allSecrets[j]) })
}

func scrubAll(s string) string {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for _, sec := range allSecrets {
		s = strings.ReplaceAll(s, sec, "<redacted>")
	}
	return s
}

// scrubWriter masks secrets in everything written through it.
type scrubWriter struct{ w io.Writer }

func (s scrubWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(s.w, scrubAll(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *rpcClient) scrub(s string) string {
	for _, sec := range c.secrets {
		s = strings.ReplaceAll(s, sec, "<redacted>")
	}
	return s
}

// rpcError is a JSON-RPC error object (message scrubbed).
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

func (c *rpcClient) call(ctx context.Context, method string, params any, out any) error {
	id := c.id.Add(1)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return errors.New("rpc: bad request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "saiai-cryptopay-sweep/1")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rpc %s: %s", method, c.scrub(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("rpc %s: read: %s", method, c.scrub(err.Error()))
	}
	var r struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("rpc %s: HTTP %d, undecodable response", method, resp.StatusCode)
	}
	if r.Error != nil {
		r.Error.Message = c.scrub(r.Error.Message)
		if len(r.Error.Data) > 0 {
			r.Error.Message += " | " + c.scrub(string(r.Error.Data))
		}
		return fmt.Errorf("rpc %s: %w", method, r.Error)
	}
	if string(bytes.TrimSpace(r.ID)) != fmt.Sprint(id) {
		return fmt.Errorf("rpc %s: response id mismatch", method)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(r.Result, out); err != nil {
		return fmt.Errorf("rpc %s: result does not have the expected shape", method)
	}
	return nil
}

// resolveRPC picks the endpoint: --rpc, then --rpc-file, then
// --helius-key-file (Solana), then the environment variable, then the
// public default. The chosen URL is never printed.
func resolveRPC(flagURL, flagFile, heliusKeyFile, envName, def string) (string, string, error) {
	switch {
	case flagURL != "":
		return flagURL, "--rpc", nil
	case flagFile != "":
		b, err := os.ReadFile(flagFile)
		if err != nil {
			return "", "", fmt.Errorf("read --rpc-file: %w", err)
		}
		return strings.TrimSpace(string(b)), "--rpc-file", nil
	case heliusKeyFile != "":
		b, err := os.ReadFile(heliusKeyFile)
		if err != nil {
			return "", "", fmt.Errorf("read --helius-key-file: %w", err)
		}
		k := strings.TrimSpace(string(b))
		if k == "" || strings.ContainsAny(k, "/?&# \n") {
			return "", "", errors.New("--helius-key-file must contain only the API key")
		}
		return "https://mainnet.helius-rpc.com/?api-key=" + url.QueryEscape(k), "helius (key file)", nil
	case os.Getenv(envName) != "":
		return os.Getenv(envName), "$" + envName, nil
	}
	return def, "public default", nil
}
