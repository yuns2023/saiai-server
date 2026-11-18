//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type proxyRecordingUpstream struct {
	scriptedGatewayHTTPUpstream
	proxyURLs []string
}

func (u *proxyRecordingUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, tls bool) (*http.Response, error) {
	u.proxyURLs = append(u.proxyURLs, proxyURL)
	return u.scriptedGatewayHTTPUpstream.DoWithTLS(req, proxyURL, accountID, concurrency, tls)
}

func (u *proxyRecordingUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, concurrency, false)
}

// A self-onboarded account is never forwarded without its proxy; the request
// never reaches the upstream transport.
func TestForwardRefusesSelfOnboardedAccountWithoutProxy(t *testing.T) {
	c, _, parsed := gatewayHTTPRetryRequest(t)
	upstream := &proxyRecordingUpstream{scriptedGatewayHTTPUpstream: scriptedGatewayHTTPUpstream{
		results: []scriptedGatewayHTTPResult{{response: gatewayHTTPSuccessResponse}},
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)
	account := gatewayHTTPRetryAccount()
	account.SelfOnboarded = true

	_, err := svc.Forward(c.Request.Context(), c, account, parsed)
	require.ErrorIs(t, err, ErrAccountProxyRequired)
	require.Zero(t, upstream.calls)

	proxyID := int64(4)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: 4, Protocol: "http", Host: "203.0.113.4", Port: 3128, Status: "disabled"}
	_, err = svc.Forward(c.Request.Context(), c, account, parsed)
	require.ErrorIs(t, err, ErrAccountProxyRequired)
	require.Zero(t, upstream.calls)

	c2, _, parsed2 := gatewayHTTPRetryRequest(t)
	account.Proxy.Status = StatusActive
	_, err = svc.Forward(c2.Request.Context(), c2, account, parsed2)
	require.NoError(t, err)
	require.Equal(t, []string{"http://203.0.113.4:3128"}, upstream.proxyURLs)
}

// Existing accounts without a proxy keep direct egress.
func TestForwardDirectAccountUnchanged(t *testing.T) {
	c, _, parsed := gatewayHTTPRetryRequest(t)
	upstream := &proxyRecordingUpstream{scriptedGatewayHTTPUpstream: scriptedGatewayHTTPUpstream{
		results: []scriptedGatewayHTTPResult{{response: gatewayHTTPSuccessResponse}},
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)
	_, err := svc.Forward(c.Request.Context(), c, gatewayHTTPRetryAccount(), parsed)
	require.NoError(t, err)
	require.Equal(t, []string{""}, upstream.proxyURLs)
}
