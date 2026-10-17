package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPUpstreamTLSRequiresGlobalAndAccountSwitch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	for _, global := range []bool{false, true} {
		for _, account := range []bool{false, true} {
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.AllowPrivateHosts = true
			cfg.Gateway.TLSFingerprint.Enabled = global
			svc, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
			if !ok {
				t.Fatal("unexpected upstream implementation")
			}
			req, _ := http.NewRequest("GET", server.URL, nil)
			resp, err := svc.DoWithTLS(req, "", 1, 1, account)
			if err != nil {
				t.Fatal(err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if len(svc.clients) != 1 {
				t.Fatal("expected exactly one transport")
			}
			for key, entry := range svc.clients {
				if strings.HasPrefix(key, "tls:") != (global && account) {
					t.Fatalf("global=%v account=%v key=%s", global, account, key)
				}
				entry.client.CloseIdleConnections()
			}
		}
	}
}

// Exercise the public send path so version selection and pooling cannot diverge.
func TestHTTPUpstreamTLSVersionPools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	for _, isolation := range []string{"account", "proxy", "account_proxy"} {
		t.Run(isolation, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.ConnectionPoolIsolation = isolation
			cfg.Security.URLAllowlist.AllowPrivateHosts = true
			cfg.Gateway.TLSFingerprint.Enabled = true
			svc, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
			if !ok {
				t.Fatal("unexpected upstream implementation")
			}
			defer func() {
				for _, e := range svc.clients {
					e.client.CloseIdleConnections()
				}
			}()
			var old *upstreamClientEntry
			for i, version := range []string{"2.1.301", "2.1.302", "2.1.301", "2.1.302"} {
				req, _ := http.NewRequest("GET", server.URL, nil)
				req.Header.Set("User-Agent", "claude-cli/"+version+" (external, cli)")
				resp, err := svc.DoWithTLS(req, "", 1, 1, true)
				if err != nil {
					t.Fatal(err)
				}
				if err := resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					for _, e := range svc.clients {
						old = e
					}
				}
				if i > 0 && len(svc.clients) != 2 {
					t.Fatalf("expected two version pools, got %d", len(svc.clients))
				}
			}
			found := false
			for _, e := range svc.clients {
				if e == old {
					found = true
				}
			}
			if !found {
				t.Fatal("legacy pool was replaced")
			}
		})
	}
}
