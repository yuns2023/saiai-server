package tlsfingerprint

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

const (
	defaultPolicyConnectTimeout   = 30 * time.Second
	defaultPolicyHandshakeTimeout = 15 * time.Second
	policyDialKeepAlive           = 30 * time.Second
)

// PolicyDialerOptions configures how the TCP/tunnel path is established.
type PolicyDialerOptions struct {
	// ProxyURL is nil for direct connections; http, https, socks5 and socks5h
	// are supported. Any other scheme is rejected (never silently bypassed).
	ProxyURL *url.URL
	// RootCAs verifies the upstream (and an https proxy). nil = system roots.
	RootCAs *x509.CertPool
	// ConnectTimeout bounds the TCP connect to the target or proxy.
	ConnectTimeout time.Duration
	// HandshakeTimeout bounds proxy negotiation plus the TLS handshake.
	HandshakeTimeout time.Duration
}

// PolicyDialer dials TLS connections whose initial ClientHello follows a
// pinned TransportPolicy baseline. Each connection gets a fresh spec, a fresh
// rustls extension-order seed (when applicable) and no resumption state.
// It never falls back to standard Go TLS: any failure is returned.
type PolicyDialer struct {
	policy   TransportPolicy
	baseline *clientHelloBaseline
	opts     PolicyDialerOptions
	seed     func() (uint16, error) // test hook; nil = crypto/rand
	// dialDirect overrides the direct TCP dial in tests (routing only; the
	// TLS ServerName still comes from the requested address).
	dialDirect func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewPolicyDialer validates the policy and proxy scheme up front.
func NewPolicyDialer(policy TransportPolicy, opts PolicyDialerOptions) (*PolicyDialer, error) {
	b, err := lookupBaseline(policy)
	if err != nil {
		return nil, err
	}
	if opts.ProxyURL != nil {
		switch strings.ToLower(opts.ProxyURL.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf("tlsfingerprint: unsupported proxy scheme %q", opts.ProxyURL.Scheme)
		}
		if opts.ProxyURL.Hostname() == "" {
			return nil, errors.New("tlsfingerprint: proxy URL missing host")
		}
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = defaultPolicyConnectTimeout
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = defaultPolicyHandshakeTimeout
	}
	return &PolicyDialer{policy: policy, baseline: b, opts: opts}, nil
}

// Policy returns the immutable policy this dialer was built for.
func (d *PolicyDialer) Policy() TransportPolicy { return d.policy }

// DialTLSContext is suitable for http.Transport.DialTLSContext. The returned
// connection has no read/write deadline set.
func (d *PolicyDialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("tlsfingerprint: unsupported network %q", network)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("tlsfingerprint: invalid address: %w", err)
	}
	raw, err := d.dialTCP(ctx, addr)
	if err != nil {
		return nil, err
	}
	// Proxy CONNECT and the TLS handshake share one deadline, and context
	// cancellation closes the socket so no step can block past it.
	deadline := time.Now().Add(d.opts.HandshakeTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = raw.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	conn, err := d.setup(ctx, raw, addr, host)
	if !stop() {
		_ = raw.Close()
		if err == nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("tlsfingerprint: dial canceled: %w", err)
	}
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}

func (d *PolicyDialer) setup(ctx context.Context, raw net.Conn, addr, host string) (net.Conn, error) {
	stream := raw
	if p := d.opts.ProxyURL; p != nil && (strings.EqualFold(p.Scheme, "http") || strings.EqualFold(p.Scheme, "https")) {
		var err error
		if stream, err = d.connectTunnel(ctx, raw, addr); err != nil {
			return nil, err
		}
	}
	uconn, err := newBaselineUConn(d.baseline, stream, &utls.Config{
		ServerName: host,
		RootCAs:    d.opts.RootCAs,
		MinVersion: utls.VersionTLS12,
		MaxVersion: utls.VersionTLS13,
	}, d.seed)
	if err != nil {
		return nil, err
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("tlsfingerprint: %s handshake: %w", d.baseline.name, err)
	}
	if proto := uconn.ConnectionState().NegotiatedProtocol; proto != "" {
		return nil, fmt.Errorf("tlsfingerprint: server negotiated unexpected ALPN %q", proto)
	}
	return uconn, nil
}

// dialTCP returns the socket to the target (direct), to the HTTP(S) proxy
// (CONNECT pending), or an established SOCKS5 tunnel.
func (d *PolicyDialer) dialTCP(ctx context.Context, addr string) (net.Conn, error) {
	netDialer := &net.Dialer{Timeout: d.opts.ConnectTimeout, KeepAlive: policyDialKeepAlive}
	p := d.opts.ProxyURL
	if p == nil {
		dial := netDialer.DialContext
		if d.dialDirect != nil {
			dial = d.dialDirect
		}
		conn, err := dial(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("tlsfingerprint: connect: %w", err)
		}
		return conn, nil
	}
	switch strings.ToLower(p.Scheme) {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if p.User != nil {
			password, _ := p.User.Password()
			auth = &proxy.Auth{User: p.User.Username(), Password: password}
		}
		proxyAddr := p.Host
		if p.Port() == "" {
			proxyAddr = net.JoinHostPort(p.Hostname(), "1080")
		}
		socks, err := proxy.SOCKS5("tcp", proxyAddr, auth, netDialer)
		if err != nil {
			return nil, fmt.Errorf("tlsfingerprint: socks5 dialer: %w", err)
		}
		cd, ok := socks.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("tlsfingerprint: socks5 dialer is not context-aware")
		}
		conn, err := cd.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("tlsfingerprint: socks5 connect: %w", err)
		}
		return conn, nil
	default: // http, https (validated in NewPolicyDialer)
		proxyAddr := p.Host
		if p.Port() == "" {
			port := "80"
			if strings.EqualFold(p.Scheme, "https") {
				port = "443"
			}
			proxyAddr = net.JoinHostPort(p.Hostname(), port)
		}
		conn, err := netDialer.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("tlsfingerprint: connect to proxy: %w", err)
		}
		return conn, nil
	}
}

// connectTunnel issues CONNECT (inside a verified TLS session for https
// proxies) and returns the stream on which the target handshake runs.
func (d *PolicyDialer) connectTunnel(ctx context.Context, raw net.Conn, target string) (net.Conn, error) {
	p := d.opts.ProxyURL
	stream := raw
	if strings.EqualFold(p.Scheme, "https") {
		outer := tls.Client(raw, &tls.Config{
			ServerName: p.Hostname(),
			RootCAs:    d.opts.RootCAs,
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
		})
		if err := outer.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("tlsfingerprint: proxy TLS handshake: %w", err)
		}
		stream = outer
	}
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: make(http.Header),
	}
	if p.User != nil {
		password, _ := p.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.User.Username()+":"+password)))
	}
	if err := req.Write(stream); err != nil {
		return nil, fmt.Errorf("tlsfingerprint: write CONNECT: %w", err)
	}
	br := bufio.NewReader(stream)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, fmt.Errorf("tlsfingerprint: read CONNECT response: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tlsfingerprint: proxy CONNECT failed: %s", resp.Status)
	}
	if br.Buffered() > 0 {
		return nil, errors.New("tlsfingerprint: proxy sent data before the TLS handshake")
	}
	return stream, nil
}
