//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Ops may reclassify an untyped error only when the client received the
// upstream body unchanged; rewritten capacity errors must not be marked.
func TestOpenAIErrorRelayMarksOnlyUnchangedUpstreamBodies(t *testing.T) {
	detail := `{"detail":"Input must be a list"}`
	capacity := `{"error":{"type":"usage_limit_reached","plan_type":"plus","resets_at":123}}`
	for _, tc := range []struct {
		name, payload string
		status        int
		native        bool
		wantMarked    bool
	}{
		{"oauth_detail", detail, 400, false, true},
		{"oauth_capacity_rewrite", capacity, 429, false, false},
		{"native_relay_detail", detail, 400, true, true},
		{"native_relay_capacity_rewrite", capacity, 429, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, recorder := auditOAuthPolicyContext("/v1/responses")
			svc := &OpenAIGatewayService{}
			resp := &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.payload))}
			var err error
			if tc.native {
				_, err = svc.handleNativeRelayErrorResponse(resp, ctx, newOpenAINativeRelayAccount("https://relay.example"), []byte(tc.payload))
			} else {
				_, err = svc.handleErrorResponse(context.Background(), resp, ctx, auditOAuthPolicyAccount(), nil)
			}
			require.Error(t, err)
			_, marked := ctx.Get(OpsUpstreamBodyRelayedKey)
			require.Equal(t, tc.wantMarked, marked)
			if tc.wantMarked {
				require.Equal(t, tc.status, ctx.GetInt(OpsUpstreamBodyRelayedKey))
				require.Equal(t, tc.status, recorder.Code)
				require.Equal(t, tc.payload, recorder.Body.String())
			}
		})
	}
}

func TestOpenAIImageErrorRelayMarksOnlyUnchangedUpstreamBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"access_token": "TEST_ONLY", "chatgpt_account_id": "TEST_ONLY_ACCOUNT",
	}}
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		wantMarked              bool
	}{
		{"http_detail", "application/json", `{"detail":"Input must be a list"}`, http.StatusBadRequest, true},
		// The gateway builds this client error from an SSE frame; it is not a relay.
		{"sse_user_error", "text/event-stream", "data: {\"type\":\"error\",\"error\":{\"type\":\"image_generation_user_error\",\"message\":\"request rejected\"}}\n\n", http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &openAIImageUpstreamRecorder{resp: &http.Response{StatusCode: tc.status,
				Header: http.Header{"Content-Type": []string{tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body))}}
			svc := &OpenAIGatewayService{httpUpstream: upstream}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			c.Request.Header.Set("Content-Type", "application/json")
			_, err := svc.ForwardImage(c.Request.Context(), c, account, OpenAIImageEndpointGenerations,
				[]byte(`{"model":"gpt-image-2","prompt":"draw it"}`), "gpt-image-2", "")
			require.Error(t, err)
			_, marked := c.Get(OpsUpstreamBodyRelayedKey)
			require.Equal(t, tc.wantMarked, marked)
			if tc.wantMarked {
				require.Equal(t, tc.status, c.GetInt(OpsUpstreamBodyRelayedKey))
				require.Equal(t, tc.body, recorder.Body.String())
			}
		})
	}
}
