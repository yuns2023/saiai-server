package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexModelsAdmissionTransport func(*http.Request) (*http.Response, error)

func (f codexModelsAdmissionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// Exercise the handler through account selection and credential substitution.
// All provider I/O is intercepted; no live token or provider request is used.
func TestCodexModelsAdmissionWithoutAccountHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client, err := httpclient.GetClient(httpclient.Options{Timeout: 15 * time.Second, ResponseHeaderTimeout: 10 * time.Second})
	require.NoError(t, err)
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	var outgoing *http.Request
	const manifest = `{"models":[{"slug":"TEST_ONLY_MODEL","future":true}]}`
	client.Transport = codexModelsAdmissionTransport(func(r *http.Request) (*http.Response, error) {
		outgoing = r.Clone(r.Context())
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(manifest))}, nil
	})
	cases := []struct{ name, ua, originator string }{
		{"CLI", "codex_cli_rs/0.162.1", "codex_cli_rs"},
		{"TUI", "codex-tui/0.160.0", "codex-tui"},
		{"VSCode", "codex_vscode/0.159.2", ""},
		{"Desktop", "codex_chatgpt_desktop/0.153.4", "codex_chatgpt_desktop"},
		{"Work Desktop", "codex_work_desktop/0.159.0-alpha.12.1", "codex_work_desktop"},
		{"Chrome sidepanel", "codex-chrome-extension-sidepanel/0.159.0", "codex-chrome-extension-sidepanel"},
		{"Browser use", "codex-browser-use/0.163.0-alpha.10.2 (Windows 10.0.26200; x86_64) unknown (codex-browser-use; 0.1.0)", "codex-browser-use"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outgoing = nil
			account := service.Account{ID: 19, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"access_token": "TEST_ONLY_UPSTREAM_TOKEN", "chatgpt_account_id": "TEST_ONLY_UPSTREAM_ACCOUNT"}}
			cfg := &config.Config{}
			svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: account}, nil, nil, nil, nil, nil, nil,
				cfg, nil, nil, nil, nil, nil, nil, nil, nil)
			h := NewOpenAIGatewayHandler(svc, nil, nil, nil, nil, nil, nil, cfg, nil)
			for _, path := range []string{"/v1/models", "/backend-api/codex/models"} {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodGet, path+"?client_version=0.162.1&future=a%2Fb&future=a+b", nil)
				c.Request.Header.Set("User-Agent", tc.ua)
				c.Request.Header.Set("originator", tc.originator)
				c.Request.Header.Set("Authorization", "Bearer TEST_ONLY_GATEWAY_KEY")
				groupID := int64(9)
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 3, GroupID: &groupID,
					Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, CodexClientPolicy: "local_proxy_only"}})
				h.CodexModels(c)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, manifest, rec.Body.String())
				require.NotNil(t, outgoing)
				require.Equal(t, "TEST_ONLY_UPSTREAM_ACCOUNT", outgoing.Header.Get("ChatGPT-Account-Id"))
				require.Equal(t, "Bearer TEST_ONLY_UPSTREAM_TOKEN", outgoing.Header.Get("Authorization"))
				require.Equal(t, tc.ua, outgoing.Header.Get("User-Agent"))
				require.Equal(t, c.Request.URL.RawQuery, outgoing.URL.RawQuery)
				require.Empty(t, c.GetHeader("chatgpt-account-id"), "ingress must not be patched")
				require.Empty(t, c.GetHeader("version"))
				require.False(t, codexClientPolicyMatched(c, "local_proxy_only"), "model-turn admission must remain strict")
			}
		})
	}
}

func newCodexModelsAdmissionHandler(t *testing.T, manifest string, header http.Header) (*OpenAIGatewayHandler, *[]*http.Request) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	client, err := httpclient.GetClient(httpclient.Options{Timeout: 15 * time.Second, ResponseHeaderTimeout: 10 * time.Second})
	require.NoError(t, err)
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	var outgoing []*http.Request
	client.Transport = codexModelsAdmissionTransport(func(r *http.Request) (*http.Response, error) {
		outgoing = append(outgoing, r.Clone(r.Context()))
		h := header.Clone()
		h.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(manifest))}, nil
	})
	account := service.Account{ID: 19, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "TEST_ONLY_UPSTREAM_TOKEN", "chatgpt_account_id": "TEST_ONLY_UPSTREAM_ACCOUNT"}}
	cfg := &config.Config{}
	svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: account}, nil, nil, nil, nil, nil, nil,
		cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	return NewOpenAIGatewayHandler(svc, nil, nil, nil, nil, nil, nil, cfg, nil), &outgoing
}

func serveCodexModels(h *OpenAIGatewayHandler, group *service.Group, header http.Header) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.164.0&future=a%2Fb", nil)
	c.Request.Header = header
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 3, GroupID: &group.ID, Group: group})
	h.CodexModels(c)
	return rec
}

// Model discovery is not gated by client policy. The SAIAI daemon check reaches
// it through the official app-server with its own clientInfo name; that name
// must not be forwarded to the upstream account.
func TestCodexModelsDiscoveryOpenToNonOfficialClients(t *testing.T) {
	const manifest = `{"models":[{"slug":"TEST_ONLY_MODEL"}]}`
	for _, policy := range []string{"local_proxy_only", "official_clients", "cli_only", "unknown_policy"} {
		t.Run(policy, func(t *testing.T) {
			h, outgoing := newCodexModelsAdmissionHandler(t, manifest, http.Header{})
			header := http.Header{
				"User-Agent":            {"saiai_daemon_check/0.164.0 (Debian 13.0.0; x86_64) xterm-256color (saiai_daemon_check; 1.1.53)"},
				"Originator":            {"saiai_daemon_check"},
				"X-Test-Only-Extension": {"kept"},
			}
			rec := serveCodexModels(h, &service.Group{ID: 9, Platform: service.PlatformOpenAI, CodexClientPolicy: policy}, header)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, manifest, rec.Body.String())
			require.Len(t, *outgoing, 1)
			out := (*outgoing)[0]
			require.Equal(t, "codex_cli_rs/0.164.0", out.Header.Get("User-Agent"))
			require.Equal(t, "codex_cli_rs", out.Header.Get("Originator"))
			require.Equal(t, "kept", out.Header.Get("X-Test-Only-Extension"))
			require.Equal(t, "client_version=0.164.0&future=a%2Fb", out.URL.RawQuery)
			require.Equal(t, "saiai_daemon_check", header.Get("Originator"), "ingress headers must not be patched")
		})
	}
}

func TestCodexModelsDiscoveryFiltersGroupBlockedModels(t *testing.T) {
	const manifest = `{"etag_hint":1,"models":[{"slug":"test-only-allowed","future":true},{"slug":"test-only-blocked"}]}`
	h, outgoing := newCodexModelsAdmissionHandler(t, manifest, http.Header{"Etag": {`"TEST_ONLY_UPSTREAM"`}, "Cache-Control": {"max-age=300"}})
	header := http.Header{
		"User-Agent":    {"codex-tui/0.164.0"},
		"Originator":    {"codex-tui"},
		"If-None-Match": {`"TEST_ONLY_UPSTREAM"`},
	}
	group := &service.Group{ID: 9, Platform: service.PlatformOpenAI, CodexClientPolicy: "local_proxy_only",
		BlockedModelPatterns: []string{"test-only-blocked"}}
	rec := serveCodexModels(h, group, header)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"etag_hint":1,"models":[{"slug":"test-only-allowed","future":true}]}`, rec.Body.String())
	require.Empty(t, rec.Header().Get("ETag"))
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Len(t, *outgoing, 1)
	require.Empty(t, (*outgoing)[0].Header.Get("If-None-Match"))
	require.Equal(t, "codex-tui/0.164.0", (*outgoing)[0].Header.Get("User-Agent"))
}
