package repository

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestStandardHTTP2MultiplexesControlRequestBesideOpenStream(t *testing.T) {
	server, slowStarted, fastSeen, releaseSlow := newLongConnectionTLSServer(t, true)
	cfg := longConnectionTestConfig(true, 0)
	svc, transport := newLongConnectionTestUpstream(t, cfg, server, 1)
	require.True(t, transport.ForceAttemptHTTP2)
	require.Equal(t, 1, transport.MaxConnsPerHost)

	slowResp := startLongConnectionRequest(t, svc, server.URL+"/slow")
	require.Equal(t, "HTTP/2.0", waitProtocol(t, slowStarted))
	response := waitLongConnectionResponse(t, slowResp)

	fastResp := startLongConnectionRequest(t, svc, server.URL+"/fast")
	select {
	case protocol := <-fastSeen:
		require.Equal(t, "HTTP/2.0", protocol)
	case <-time.After(time.Second):
		close(releaseSlow)
		t.Fatal("HTTP/2 control request queued behind the open stream")
	}
	close(releaseSlow)
	closeLongConnectionResponse(t, response)
	closeLongConnectionResponse(t, waitLongConnectionResponse(t, fastResp))

	metrics := svc.SnapshotTransportMetrics()
	require.Equal(t, int64(2), metrics.Requests)
	require.Equal(t, int64(2), metrics.GotConn)
	require.Equal(t, int64(2), metrics.HTTP2Responses)
	require.Zero(t, metrics.HTTP1Responses)
	require.GreaterOrEqual(t, metrics.ReusedConnections, int64(1))
}

func TestHTTP1AuxReservePreventsControlRequestHeadOfLineBlocking(t *testing.T) {
	server, slowStarted, fastSeen, releaseSlow := newLongConnectionTLSServer(t, false)
	cfg := longConnectionTestConfig(true, 1)
	svc, transport := newLongConnectionTestUpstream(t, cfg, server, 1)
	require.True(t, transport.ForceAttemptHTTP2)
	require.Equal(t, 2, transport.MaxConnsPerHost)

	slowResp := startLongConnectionRequest(t, svc, server.URL+"/slow")
	require.Equal(t, "HTTP/1.1", waitProtocol(t, slowStarted))
	response := waitLongConnectionResponse(t, slowResp)

	fastResp := startLongConnectionRequest(t, svc, server.URL+"/fast")
	select {
	case protocol := <-fastSeen:
		require.Equal(t, "HTTP/1.1", protocol)
	case <-time.After(time.Second):
		close(releaseSlow)
		t.Fatal("HTTP/1.1 auxiliary reserve did not prevent queueing")
	}
	close(releaseSlow)
	closeLongConnectionResponse(t, response)
	closeLongConnectionResponse(t, waitLongConnectionResponse(t, fastResp))

	metrics := svc.SnapshotTransportMetrics()
	require.Equal(t, int64(2), metrics.Requests)
	require.Equal(t, int64(2), metrics.HTTP1Responses)
	require.Zero(t, metrics.HTTP2Responses)
	require.Equal(t, int64(2), metrics.NewConnections)
}

func TestAccountAuxReserveDoesNotChangeTLSFingerprintPoolSize(t *testing.T) {
	cfg := longConnectionTestConfig(true, 2)
	svc := NewHTTPUpstream(cfg).(*httpUpstreamService)
	standard := svc.resolvePoolSettings(config.ConnectionPoolIsolationAccountProxy, 1, true)
	fingerprinted := svc.resolvePoolSettings(config.ConnectionPoolIsolationAccountProxy, 1, false)
	require.Equal(t, 3, standard.maxConnsPerHost)
	require.Equal(t, 3, standard.maxIdleConnsPerHost)
	require.Equal(t, 1, fingerprinted.maxConnsPerHost)
	require.Equal(t, 1, fingerprinted.maxIdleConnsPerHost)
}

func TestStandardHTTP2RollbackSwitchAppliesToDirectAndProxyTransports(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := longConnectionTestConfig(enabled, 2)
		svc := NewHTTPUpstream(cfg).(*httpUpstreamService)
		for _, proxyURL := range []string{"", "http://proxy.example:8080", "socks5h://proxy.example:1080"} {
			entry, err := svc.getOrCreateClient(proxyURL, 1, 1)
			require.NoError(t, err)
			transport := entry.client.Transport.(*http.Transport)
			require.Equal(t, enabled, transport.ForceAttemptHTTP2)
			require.Equal(t, 3, transport.MaxConnsPerHost)
		}
	}
}

func TestStandardHTTP2NegotiatesThroughHTTPConnectProxy(t *testing.T) {
	protocols := make(chan string, 1)
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocols <- r.Proto
		_, _ = io.WriteString(w, "ok")
	}))
	target.EnableHTTP2 = true
	target.StartTLS()
	t.Cleanup(target.Close)
	proxyServer := newHTTPConnectProxy(t)

	cfg := longConnectionTestConfig(true, 2)
	svc := NewHTTPUpstream(cfg).(*httpUpstreamService)
	entry, err := svc.getOrCreateClient(proxyServer.URL, 1, 1)
	require.NoError(t, err)
	transport := entry.client.Transport.(*http.Transport)
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // local fixture only
	t.Cleanup(transport.CloseIdleConnections)

	req, err := http.NewRequest(http.MethodGet, target.URL, nil)
	require.NoError(t, err)
	resp, err := svc.Do(req, proxyServer.URL, 1, 1)
	require.NoError(t, err)
	closeLongConnectionResponse(t, resp)
	require.Equal(t, "HTTP/2.0", waitProtocol(t, protocols))
	require.Equal(t, int64(1), svc.SnapshotTransportMetrics().HTTP2Responses)
}

func TestStandardHTTP2NegotiatesThroughSOCKS5HProxy(t *testing.T) {
	protocols := make(chan string, 1)
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocols <- r.Proto
		_, _ = io.WriteString(w, "ok")
	}))
	target.EnableHTTP2 = true
	target.StartTLS()
	t.Cleanup(target.Close)
	proxyURL := newSOCKS5TestProxy(t)

	cfg := longConnectionTestConfig(true, 2)
	svc := NewHTTPUpstream(cfg).(*httpUpstreamService)
	entry, err := svc.getOrCreateClient(proxyURL, 1, 1)
	require.NoError(t, err)
	transport := entry.client.Transport.(*http.Transport)
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // local fixture only
	t.Cleanup(transport.CloseIdleConnections)

	req, err := http.NewRequest(http.MethodGet, target.URL, nil)
	require.NoError(t, err)
	resp, err := svc.Do(req, proxyURL, 1, 1)
	require.NoError(t, err)
	closeLongConnectionResponse(t, resp)
	require.Equal(t, "HTTP/2.0", waitProtocol(t, protocols))
	require.Equal(t, int64(1), svc.SnapshotTransportMetrics().HTTP2Responses)
}

func longConnectionTestConfig(http2Enabled bool, reserve int) *config.Config {
	return &config.Config{
		Gateway: config.GatewayConfig{
			ConnectionPoolIsolation:      config.ConnectionPoolIsolationAccountProxy,
			StandardUpstreamHTTP2Enabled: http2Enabled,
			AccountAuxConnectionReserve:  reserve,
		},
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{AllowPrivateHosts: true},
		},
	}
}

func newLongConnectionTestUpstream(
	t *testing.T,
	cfg *config.Config,
	server *httptest.Server,
	accountConcurrency int,
) (*httpUpstreamService, *http.Transport) {
	t.Helper()
	svc := NewHTTPUpstream(cfg).(*httpUpstreamService)
	entry, err := svc.getOrCreateClient("", 1, accountConcurrency)
	require.NoError(t, err)
	transport := entry.client.Transport.(*http.Transport)
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // local fixture only
	t.Cleanup(transport.CloseIdleConnections)
	return svc, transport
}

func newLongConnectionTLSServer(
	t *testing.T,
	enableHTTP2 bool,
) (*httptest.Server, <-chan string, <-chan string, chan struct{}) {
	t.Helper()
	slowStarted := make(chan string, 1)
	fastSeen := make(chan string, 1)
	releaseSlow := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			slowStarted <- r.Proto
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-releaseSlow
			_, _ = io.WriteString(w, "data: done\n\n")
		case "/fast":
			fastSeen <- r.Proto
			_, _ = io.WriteString(w, "ok")
		default:
			http.NotFound(w, r)
		}
	}))
	server.EnableHTTP2 = enableHTTP2
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, slowStarted, fastSeen, releaseSlow
}

func newHTTPConnectProxy(t *testing.T) *httptest.Server {
	t.Helper()
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		upstream, err := net.DialTimeout("tcp", r.Host, time.Second)
		if err != nil {
			http.Error(w, "dial failed", http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			_ = upstream.Close()
			http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		client, buffered, err := hijacker.Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		go func() {
			_, _ = io.Copy(upstream, client)
			_ = upstream.Close()
		}()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
	}))
	t.Cleanup(proxyServer.Close)
	return proxyServer
}

func newSOCKS5TestProxy(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go handleSOCKS5TestConnection(client)
		}
	}()
	return "socks5h://" + listener.Addr().String()
}

func handleSOCKS5TestConnection(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(client)
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return
	}
	if _, err := client.Write([]byte{5, 0}); err != nil {
		return
	}
	requestHeader := make([]byte, 4)
	if _, err := io.ReadFull(reader, requestHeader); err != nil || requestHeader[0] != 5 || requestHeader[1] != 1 {
		return
	}
	var host string
	switch requestHeader[3] {
	case 1:
		address := make([]byte, 4)
		if _, err := io.ReadFull(reader, address); err != nil {
			return
		}
		host = net.IP(address).String()
	case 3:
		length, err := reader.ReadByte()
		if err != nil {
			return
		}
		name := make([]byte, int(length))
		if _, err := io.ReadFull(reader, name); err != nil {
			return
		}
		host = string(name)
	case 4:
		address := make([]byte, 16)
		if _, err := io.ReadFull(reader, address); err != nil {
			return
		}
		host = net.IP(address).String()
	default:
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return
	}
	target := net.JoinHostPort(host, fmt.Sprintf("%d", binary.BigEndian.Uint16(portBytes)))
	upstream, err := net.DialTimeout("tcp", target, time.Second)
	if err != nil {
		_, _ = client.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err := client.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	_ = upstream.SetDeadline(time.Time{})
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, reader)
		_ = upstream.Close()
		close(done)
	}()
	_, _ = io.Copy(client, upstream)
	<-done
}

type longConnectionResult struct {
	response *http.Response
	err      error
}

func startLongConnectionRequest(t *testing.T, svc *httpUpstreamService, target string) <-chan longConnectionResult {
	t.Helper()
	results := make(chan longConnectionResult, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			results <- longConnectionResult{err: fmt.Errorf("create request: %w", err)}
			return
		}
		resp, err := svc.Do(req, "", 1, 1)
		if err != nil {
			results <- longConnectionResult{err: fmt.Errorf("send request: %w", err)}
			return
		}
		results <- longConnectionResult{response: resp}
	}()
	return results
}

func waitLongConnectionResponse(t *testing.T, results <-chan longConnectionResult) *http.Response {
	t.Helper()
	select {
	case result := <-results:
		require.NoError(t, result.err)
		require.NotNil(t, result.response)
		return result.response
	case <-time.After(time.Second):
		t.Fatal("upstream response headers were not received")
		return nil
	}
}

func waitProtocol(t *testing.T, protocols <-chan string) string {
	t.Helper()
	select {
	case protocol := <-protocols:
		return protocol
	case <-time.After(time.Second):
		t.Fatal("upstream request was not observed")
		return ""
	}
}

func closeLongConnectionResponse(t *testing.T, resp *http.Response) {
	t.Helper()
	require.NotNil(t, resp)
	_, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}
