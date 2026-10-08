package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestChatGPTUpstreamHTMLControlsPreserveWireResponseAndAttributeProvider(t *testing.T) {
	groupID := int64(9)
	account := service.Account{ID: 19, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 10,
		Credentials: map[string]any{"access_token": "TEST_ONLY", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
	cache := &chatGPTSlotCache{active: make(map[string]int64), attempts: make(map[int64]int)}
	concurrency := service.NewConcurrencyService(cache)
	upstream := &chatGPTReplayUpstream{statusCode: 403, contentType: "text/html",
		responseBody: `<html><script src="/cdn-cgi/challenge-platform/TEST_ONLY_PRIVATE_TOKEN"></script></html>`}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIChatEnabled = true
	svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: account}, nil, nil, nil, nil, nil, nil,
		cfg, nil, concurrency, nil, nil, nil, upstream, nil, nil)
	h := NewOpenAIGatewayHandler(svc, concurrency, nil, nil, nil, nil, nil, cfg, nil)
	for _, path := range []string{"/chatgpt/backend-api/conversation/init", "/chatgpt/backend-api/f/conversation/prepare", "/chatgpt/backend-api/models"} {
		t.Run(path, func(t *testing.T) {
			method := http.MethodPost
			if path == "/chatgpt/backend-api/models" {
				method = http.MethodGet
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(`{"model":"gpt-test-pro","client_extension":[1,2]}`))
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 3, GroupID: &groupID,
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI}})
			if method == http.MethodGet {
				h.ChatGPTModels(c)
			} else {
				h.ChatGPTConversation(c)
			}
			require.Equal(t, 403, w.Code)
			require.Equal(t, upstream.responseBody, w.Body.String())
			require.Empty(t, w.Header().Get("Set-Cookie"))
			require.Empty(t, cache.active)
			require.True(t, service.NativeCodexRejectsRedirects(upstream.req.Context()))
			require.Equal(t, 403, c.GetInt(service.OpsUpstreamStatusCodeKey))
			require.Len(t, c.MustGet(service.OpsUpstreamErrorsKey).([]*service.OpsUpstreamErrorEvent), 1)

			parsed := parseOpsErrorResponse(w.Body.Bytes())
			upstreamStatus := c.GetInt(service.OpsUpstreamStatusCodeKey)
			message := c.GetString(service.OpsUpstreamErrorMessageKey)
			entry := &service.OpsInsertErrorLogInput{
				RequestPath: path, StatusCode: w.Code, ErrorPhase: "internal", ErrorType: "api_error",
				ErrorMessage: parsed.Message, ErrorBody: w.Body.String(),
				UpstreamStatusCode: &upstreamStatus, UpstreamErrorMessage: &message,
			}
			applyOpsNativeChatUpstreamClassification(entry)
			require.Equal(t, "upstream_http", entry.ErrorSource)
			require.Equal(t, "provider", entry.ErrorOwner)
			require.Equal(t, "upstream", entry.ErrorPhase)
			require.Equal(t, "upstream_error", entry.ErrorType)
			require.Contains(t, entry.ErrorMessage, "browser verification page")
			require.Equal(t, upstream.responseBody, entry.ErrorBody)
			require.False(t, entry.IsRetryable)
		})
	}
	// Local admission failures have no matching upstream response and retain
	// their classification. An unrelated route must not inherit native policy.
	status := 403
	for _, entry := range []*service.OpsInsertErrorLogInput{
		{RequestPath: "/chatgpt/backend-api/models", StatusCode: 403},
		{RequestPath: "/chatgpt/backend-api/models", StatusCode: 502, UpstreamStatusCode: &status},
		{RequestPath: "/v1/responses", StatusCode: 403, UpstreamStatusCode: &status},
	} {
		applyOpsNativeChatUpstreamClassification(entry)
		require.Empty(t, entry.ErrorSource)
	}
}
