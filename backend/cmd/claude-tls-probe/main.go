// claude-tls-probe exercises the production default TLS dialer against a local
// CONNECT mock. It has no provider credentials and refuses non-loopback proxies.
package main

import (
	"bufio"
	"context"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

func run() error {
	proxy := flag.String("proxy", "", "loopback HTTP CONNECT mock URL")
	ca := flag.String("ca", "", "ephemeral mock certificate")
	userAgent := flag.String("user-agent", "", "Claude User-Agent selecting the model TLS profile")
	flag.Parse()
	u, err := url.Parse(*proxy)
	if err != nil {
		return err
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Scheme != "http" || u.User != nil {
		return fmt.Errorf("only numeric loopback HTTP mock proxies are allowed")
	}
	pem, err := os.ReadFile(*ca)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return fmt.Errorf("invalid mock CA")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := tlsfingerprint.NewHTTPProxyDialer(tlsfingerprint.ProfileForUserAgent(tlsfingerprint.DefaultProfile(), *userAgent), u).WithRootCAs(roots)
	conn, err := d.DialTLSContext(ctx, "tcp", "api.anthropic.com:443")
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages?beta=true", strings.NewReader(`{"model":"claude-sonnet-4-6","stream":false,"messages":[{"role":"user","content":"ping"}]}`))
	req.Header.Set("Content-Type", "application/json")
	if err := req.Write(conn); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("mock status %d", resp.StatusCode)
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
