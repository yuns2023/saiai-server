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
