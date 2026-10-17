package tlsfingerprint

import (
	"bufio"
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHTTPSProxyVerifiedOuterAndInnerTLS(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer target.Close()
	targetURL, _ := url.Parse(target.URL)
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.TLS == nil {
			http.Error(w, "TLS CONNECT required", 400)
			return
		}
		upstream, err := net.Dial("tcp", targetURL.Host)
		if err != nil {
			http.Error(w, "mock dial", http.StatusBadGateway)
			return
		}
		defer func() { _ = upstream.Close() }()
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { _, _ = io.Copy(upstream, conn); _ = upstream.Close() }()
		_, _ = io.Copy(conn, upstream)
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	roots.AddCert(proxy.Certificate())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := NewHTTPProxyDialer(nil, proxyURL).WithRootCAs(roots).DialTLSContext(ctx, "tcp", targetURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	req, _ := http.NewRequest("GET", target.URL, nil)
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatal("target response missing")
	}
	_, err = NewHTTPProxyDialer(nil, proxyURL).WithRootCAs(x509.NewCertPool()).DialTLSContext(ctx, "tcp", targetURL.Host)
	if err == nil || !strings.Contains(err.Error(), "proxy TLS handshake") {
		t.Fatalf("untrusted proxy must fail outer TLS: %v", err)
	}
}

func TestHTTPProxyCancellationDuringCONNECT(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		conn, e := listener.Accept()
		if e == nil {
			defer func() { _ = conn.Close() }()
			<-done
		}
	}()
	u, _ := url.Parse("http://" + listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		_, e := NewHTTPProxyDialer(nil, u).DialTLSContext(ctx, "tcp", "example.com:443")
		returned <- e
	}()
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("expected cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CONNECT ignored cancellation")
	}
}
