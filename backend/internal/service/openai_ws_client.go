package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/extracerts"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const openAIWSMessageReadLimitBytes int64 = 16 * 1024 * 1024
const (
	openAIWSProxyTransportMaxIdleConns        = 128
	openAIWSProxyTransportMaxIdleConnsPerHost = 64
	openAIWSProxyTransportIdleConnTimeout     = 90 * time.Second
	openAIWSProxyClientCacheMaxEntries        = 256
	openAIWSProxyClientCacheIdleTTL           = 15 * time.Minute
)

type OpenAIWSTransportMetricsSnapshot struct {
	ProxyClientCacheHits   int64   `json:"proxy_client_cache_hits"`
	ProxyClientCacheMisses int64   `json:"proxy_client_cache_misses"`
	TransportReuseRatio    float64 `json:"transport_reuse_ratio"`
}

// openAIWSClientConn 抽象 WS 客户端连接，便于替换底层实现。
type openAIWSClientConn interface {
	WriteJSON(ctx context.Context, value any) error
	ReadMessage(ctx context.Context) ([]byte, error)
	Ping(ctx context.Context) error
	Close() error
}

// openAIWSClientDialer 抽象 WS 建连器。
type openAIWSClientDialer interface {
	Dial(ctx context.Context, wsURL string, headers http.Header, proxyURL string) (openAIWSClientConn, int, http.Header, error)
}

// openAIWSPolicyDialer dials with an explicit TLS transport policy. The pool,
// prewarm and v2 passthrough all go through dialOpenAIWS so the policy is
// passed explicitly and never inferred from the client's headers.
type openAIWSPolicyDialer interface {
	DialWithPolicy(ctx context.Context, wsURL string, headers http.Header, proxyURL string, policy tlsfingerprint.TransportPolicy) (openAIWSClientConn, int, http.Header, error)
}

// dialOpenAIWS uses d.Dial for the zero policy. A non-zero policy requires a
// policy-capable dialer; otherwise the dial fails (no std-TLS fallback).
func dialOpenAIWS(ctx context.Context, d openAIWSClientDialer, wsURL string, headers http.Header, proxyURL string, policy tlsfingerprint.TransportPolicy) (openAIWSClientConn, int, http.Header, error) {
	if !policy.Enabled() {
		return d.Dial(ctx, wsURL, headers, proxyURL)
	}
	pd, ok := d.(openAIWSPolicyDialer)
	if !ok {
		return nil, 0, nil, ErrTransportPolicyUnsupported
	}
	return pd.DialWithPolicy(ctx, wsURL, headers, proxyURL, policy)
}

type openAIWSTransportMetricsDialer interface {
	SnapshotTransportMetrics() OpenAIWSTransportMetricsSnapshot
}

func newDefaultOpenAIWSClientDialer() openAIWSClientDialer {
	return &coderOpenAIWSClientDialer{
		proxyClients:  make(map[string]*openAIWSProxyClientEntry),
		policyClients: make(map[string]*openAIWSProxyClientEntry),
		trace:         newOpenAIWSTraceFromEnv(),
	}
}

type coderOpenAIWSClientDialer struct {
	proxyMu      sync.Mutex
	proxyClients map[string]*openAIWSProxyClientEntry
	proxyHits    atomic.Int64
	proxyMisses  atomic.Int64
	// policyClients caches TLS-policy clients by policy key + proxy. They are
	// deliberately excluded from the proxy-client metrics above, which keep
	// meaning "standard proxy client cache hit ratio".
	policyClients map[string]*openAIWSProxyClientEntry
	trace         *openAIWSTrace
}

type openAIWSProxyClientEntry struct {
	client           *http.Client
	lastUsedUnixNano int64
}

func (d *coderOpenAIWSClientDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	return d.DialWithPolicy(ctx, wsURL, headers, proxyURL, tlsfingerprint.TransportPolicy{})
}

// DialWithPolicy dials like Dial; a non-zero policy replaces Go TLS (and Go's
// proxy handling) with tlsfingerprint.PolicyDialer for the upgrade request.
func (d *coderOpenAIWSClientDialer) DialWithPolicy(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	policy tlsfingerprint.TransportPolicy,
) (openAIWSClientConn, int, http.Header, error) {
	targetURL := strings.TrimSpace(wsURL)
	if targetURL == "" {
		return nil, 0, nil, errors.New("ws url is empty")
	}
	if d.trace != nil {
		d.trace.write(map[string]any{
			"event":     "handshake",
			"direction": "to_openai",
			"url":       wsURL,
			"proxy":     proxyURL,
			"headers":   traceHeaders(headers),
		})
	}

	opts := &coderws.DialOptions{
		HTTPHeader:      cloneHeader(headers),
		CompressionMode: coderws.CompressionContextTakeover,
	}
	if policy.Enabled() {
		policyClient, err := d.policyHTTPClient(proxyURL, policy)
		if err != nil {
			return nil, 0, nil, err
		}
		opts.HTTPClient = policyClient
	} else if proxy := strings.TrimSpace(proxyURL); proxy != "" {
		proxyClient, err := d.proxyHTTPClient(proxy)
		if err != nil {
			return nil, 0, nil, err
		}
		opts.HTTPClient = proxyClient
	} else {
		// Keep direct WebSocket dialing aligned with the HTTP upstream path:
		// explicitly load SAIAI_EXTRA_CA_FILE/SAIAI_EXTRA_CA_DIR so isolated
		// staging upstreams and controlled test roots work for WSS as well.
		opts.HTTPClient = &http.Client{Transport: &http.Transport{
			TLSClientConfig: extracerts.TLSClientConfig(),
		}}
	}

	if NativeCodexRejectsRedirects(ctx) {
		nativeClient := *opts.HTTPClient
		nativeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		opts.HTTPClient = &nativeClient
	}
	// coder/websocket reads part of a failed handshake's body before Dial
	// returns, so record when the response headers arrived.
	timing := &openAIWSHandshakeTimingTransport{base: opts.HTTPClient.Transport}
	timedClient := *opts.HTTPClient
	timedClient.Transport = timing
	opts.HTTPClient = &timedClient
	conn, resp, err := coderws.Dial(ctx, targetURL, opts)
	if err != nil {
		status := 0
		respHeaders := http.Header(nil)
		if resp != nil {
			status = resp.StatusCode
			respHeaders = cloneHeader(resp.Header)
		}
		if receivedAt, ok := timing.receivedAt(); ok {
			err = &openAIWSHandshakeResponseError{err: err, receivedAt: receivedAt}
		}
		if d.trace != nil {
			d.trace.write(map[string]any{
				"event":     "handshake_response",
				"direction": "from_openai",
				"status":    status,
				"headers":   traceHeaders(respHeaders),
				"error":     errorString(err),
			})
		}
		return nil, status, respHeaders, err
	}
	// coder/websocket 默认单消息读取上限为 32KB，Codex WS 事件（如 rate_limits/大 delta）
	// 可能超过该阈值，需显式提高上限，避免本地 read_fail(message too big)。
	conn.SetReadLimit(openAIWSMessageReadLimitBytes)
	respHeaders := http.Header(nil)
	status := http.StatusSwitchingProtocols
	if resp != nil {
		status = resp.StatusCode
		respHeaders = cloneHeader(resp.Header)
	}
	if d.trace != nil {
		d.trace.write(map[string]any{
			"event":     "handshake_response",
			"direction": "from_openai",
			"status":    status,
			"headers":   traceHeaders(respHeaders),
			"error":     "",
		})
		return &openAIWSTracedConn{inner: &coderOpenAIWSClientConn{conn: conn}, trace: d.trace}, status, respHeaders, nil
	}
	return &coderOpenAIWSClientConn{conn: conn}, status, respHeaders, nil
}

func (d *coderOpenAIWSClientDialer) proxyHTTPClient(proxy string) (*http.Client, error) {
	if d == nil {
		return nil, errors.New("openai ws dialer is nil")
	}
	normalizedProxy := strings.TrimSpace(proxy)
	if normalizedProxy == "" {
		return nil, errors.New("proxy url is empty")
	}
	parsedProxyURL, err := url.Parse(normalizedProxy)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy url: %w", err)
	}
	now := time.Now().UnixNano()

	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()
	if entry, ok := d.proxyClients[normalizedProxy]; ok && entry != nil && entry.client != nil {
		entry.lastUsedUnixNano = now
		d.proxyHits.Add(1)
		return entry.client, nil
	}
	d.cleanupProxyClientsLocked(now)
	transport := &http.Transport{
		Proxy:               http.ProxyURL(parsedProxyURL),
		MaxIdleConns:        openAIWSProxyTransportMaxIdleConns,
		MaxIdleConnsPerHost: openAIWSProxyTransportMaxIdleConnsPerHost,
		IdleConnTimeout:     openAIWSProxyTransportIdleConnTimeout,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     extracerts.TLSClientConfig(),
		ForceAttemptHTTP2:   true,
	}
	client := &http.Client{Transport: transport}
	d.proxyClients[normalizedProxy] = &openAIWSProxyClientEntry{
		client:           client,
		lastUsedUnixNano: now,
	}
	d.ensureProxyClientCapacityLocked()
	d.proxyMisses.Add(1)
	return client, nil
}

var errOpenAIWSPolicyPlainConnection = errors.New("openai ws TLS policy transport refuses plain connections")

// openAIWSPolicyRootCAs is the trust store for policy WS dials: system roots
// plus SAIAI extra CAs (nil = system roots). Tests replace it.
var openAIWSPolicyRootCAs = func() *x509.CertPool {
	pool, _ := extracerts.RootCAs()
	return pool
}

// policyHTTPClient returns a cached client whose transport dials TLS through
// the policy dialer (direct, HTTP/HTTPS CONNECT or SOCKS5 inside the dialer,
// Transport.Proxy=nil). HTTP/2 is disabled; the upgraded connection's 101
// body remains the raw io.ReadWriteCloser that coder/websocket requires.
func (d *coderOpenAIWSClientDialer) policyHTTPClient(proxy string, policy tlsfingerprint.TransportPolicy) (*http.Client, error) {
	if d == nil {
		return nil, errors.New("openai ws dialer is nil")
	}
	normalizedProxy, parsedProxyURL, err := proxyurl.Parse(proxy)
	if err != nil {
		return nil, err
	}
	key := policy.Key() + "|" + normalizedProxy
	now := time.Now().UnixNano()

	d.proxyMu.Lock()
	defer d.proxyMu.Unlock()
	if d.policyClients == nil {
		d.policyClients = make(map[string]*openAIWSProxyClientEntry)
	}
	if entry, ok := d.policyClients[key]; ok && entry != nil && entry.client != nil {
		entry.lastUsedUnixNano = now
		return entry.client, nil
	}
	cleanupOpenAIWSClientCacheLocked(d.policyClients, now)
	dialer, err := tlsfingerprint.NewPolicyDialer(policy, tlsfingerprint.PolicyDialerOptions{
		ProxyURL:         parsedProxyURL,
		RootCAs:          openAIWSPolicyRootCAs(),
		HandshakeTimeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		// One TLS connection per upgrade, like the official client. A failed
		// (non-101) handshake closes its connection instead of leaving it idle
		// for a later upgrade of a different account. Go does not add
		// "Connection: close" to protocol-switch requests.
		DisableKeepAlives: true,
		DialTLSContext:    dialer.DialTLSContext,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errOpenAIWSPolicyPlainConnection
		},
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	client := &http.Client{Transport: transport}
	d.policyClients[key] = &openAIWSProxyClientEntry{client: client, lastUsedUnixNano: now}
	ensureOpenAIWSClientCacheCapacityLocked(d.policyClients)
	return client, nil
}

func (d *coderOpenAIWSClientDialer) cleanupProxyClientsLocked(nowUnixNano int64) {
	if d == nil {
		return
	}
	cleanupOpenAIWSClientCacheLocked(d.proxyClients, nowUnixNano)
}

func cleanupOpenAIWSClientCacheLocked(clients map[string]*openAIWSProxyClientEntry, nowUnixNano int64) {
	if len(clients) == 0 {
		return
	}
	idleTTL := openAIWSProxyClientCacheIdleTTL
	if idleTTL <= 0 {
		return
	}
	now := time.Unix(0, nowUnixNano)
	for key, entry := range clients {
		if entry == nil || entry.client == nil {
			delete(clients, key)
			continue
		}
		lastUsed := time.Unix(0, entry.lastUsedUnixNano)
		if now.Sub(lastUsed) > idleTTL {
			closeOpenAIWSProxyClient(entry.client)
			delete(clients, key)
		}
	}
}

func (d *coderOpenAIWSClientDialer) ensureProxyClientCapacityLocked() {
	if d == nil {
		return
	}
	ensureOpenAIWSClientCacheCapacityLocked(d.proxyClients)
}

func ensureOpenAIWSClientCacheCapacityLocked(clients map[string]*openAIWSProxyClientEntry) {
	maxEntries := openAIWSProxyClientCacheMaxEntries
	if maxEntries <= 0 {
		return
	}
	for len(clients) > maxEntries {
		var oldestKey string
		var oldestLastUsed int64
		hasOldest := false
		for key, entry := range clients {
			lastUsed := int64(0)
			if entry != nil {
				lastUsed = entry.lastUsedUnixNano
			}
			if !hasOldest || lastUsed < oldestLastUsed {
				hasOldest = true
				oldestKey = key
				oldestLastUsed = lastUsed
			}
		}
		if !hasOldest {
			return
		}
		if entry := clients[oldestKey]; entry != nil {
			closeOpenAIWSProxyClient(entry.client)
		}
		delete(clients, oldestKey)
	}
}

func closeOpenAIWSProxyClient(client *http.Client) {
	if client == nil || client.Transport == nil {
		return
	}
	if transport, ok := client.Transport.(*http.Transport); ok && transport != nil {
		transport.CloseIdleConnections()
	}
}

func (d *coderOpenAIWSClientDialer) SnapshotTransportMetrics() OpenAIWSTransportMetricsSnapshot {
	if d == nil {
		return OpenAIWSTransportMetricsSnapshot{}
	}
	hits := d.proxyHits.Load()
	misses := d.proxyMisses.Load()
	total := hits + misses
	reuseRatio := 0.0
	if total > 0 {
		reuseRatio = float64(hits) / float64(total)
	}
	return OpenAIWSTransportMetricsSnapshot{
		ProxyClientCacheHits:   hits,
		ProxyClientCacheMisses: misses,
		TransportReuseRatio:    reuseRatio,
	}
}

type coderOpenAIWSClientConn struct {
	conn *coderws.Conn
}

var _ openaiwsv2.FrameConn = (*coderOpenAIWSClientConn)(nil)

func (c *coderOpenAIWSClientConn) WriteJSON(ctx context.Context, value any) error {
	if c == nil || c.conn == nil {
		return errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return wsjson.Write(ctx, c.conn, value)
}

func (c *coderOpenAIWSClientConn) ReadMessage(ctx context.Context) ([]byte, error) {
	if c == nil || c.conn == nil {
		return nil, errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	msgType, payload, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	switch msgType {
	case coderws.MessageText, coderws.MessageBinary:
		return payload, nil
	default:
		return nil, errOpenAIWSConnClosed
	}
}

func (c *coderOpenAIWSClientConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.conn == nil {
		return coderws.MessageText, nil, errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	msgType, payload, err := c.conn.Read(ctx)
	if err != nil {
		return coderws.MessageText, nil, err
	}
	return msgType, payload, nil
}

func (c *coderOpenAIWSClientConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	if c == nil || c.conn == nil {
		return errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.conn.Write(ctx, msgType, payload)
}

func (c *coderOpenAIWSClientConn) Ping(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.conn.Ping(ctx)
}

func (c *coderOpenAIWSClientConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	// Close 为幂等，忽略重复关闭错误。
	_ = c.conn.Close(coderws.StatusNormalClosure, "")
	_ = c.conn.CloseNow()
	return nil
}

// openAIWSHandshakeTimingTransport records when a handshake response's headers
// arrived: RoundTrip returns once the headers have been read.
type openAIWSHandshakeTimingTransport struct {
	base http.RoundTripper
	at   atomic.Int64
}

func (t *openAIWSHandshakeTimingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if resp != nil {
		t.at.Store(time.Now().UnixNano())
		if hook := openAIWSHandshakeHeadersHook; hook != nil {
			hook()
		}
	}
	return resp, err
}

// openAIWSHandshakeHeadersHook lets tests order events after header receipt.
var openAIWSHandshakeHeadersHook func()

func (t *openAIWSHandshakeTimingTransport) receivedAt() (time.Time, bool) {
	if nanos := t.at.Load(); nanos != 0 {
		return time.Unix(0, nanos), true
	}
	return time.Time{}, false
}

// openAIWSHandshakeResponseError carries a failed handshake's header receipt
// time; it is transparent to error matching and messages.
type openAIWSHandshakeResponseError struct {
	err        error
	receivedAt time.Time
}

func (e *openAIWSHandshakeResponseError) Error() string { return e.err.Error() }
func (e *openAIWSHandshakeResponseError) Unwrap() error { return e.err }

// withOpenAIWSHandshakeObservedAt makes 429 handling for a failed handshake
// use the time its response headers arrived.
func withOpenAIWSHandshakeObservedAt(ctx context.Context, err error) context.Context {
	var handshakeErr *openAIWSHandshakeResponseError
	if errors.As(err, &handshakeErr) && handshakeErr != nil {
		return withCodexObservedAt(ctx, handshakeErr.receivedAt)
	}
	return ctx
}
