package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSetupTokenCachedHeadersPreserveCurrentRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.300 (external, cli)")
	c.Request.Header.Set("anthropic-beta", "oauth-2025-04-20,context-management-2025-06-27")
	body := []byte(`{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.300.abc; cc_entrypoint=cli;"}],"messages":[{"role":"user","content":"ping"}]}`)
	for _, mode := range []string{ClaudeOAuthModeShared, ClaudeOAuthModeSingleDevice} {
		identity := &oauthRequestIdentity{Mode: mode, BillingUserAgent: "claude-cli/2.1.257 (external, cli)", Fingerprint: &Fingerprint{UserAgent: "claude-cli/2.1.257 (external, cli)", AnthropicBeta: "oauth-2025-04-20"}}
		req, err := (&GatewayService{}).buildUpstreamRequest(context.Background(), c, &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}, body, "synthetic-audit-token", "oauth", "claude-sonnet-4-6", false, identity)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(out), "cc_version=2.1.300.abc") || !strings.Contains(req.Header.Get("User-Agent"), "2.1.300") {
			t.Fatal("current client version was not preserved")
		}
		kept := strings.Contains(req.Header.Get("anthropic-beta"), "context-management-2025-06-27")
		if !kept {
			t.Fatal("unexpected beta preservation result")
		}

	}
}
