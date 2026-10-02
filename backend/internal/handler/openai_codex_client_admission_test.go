package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Exercise the actual HTTP/WS handlers with every proxy shape header present.
// Rejections must happen before acquiring a slot or selecting an upstream.
func TestOpenAICodexAdmissionRejectsBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct{ name, ua, originator string }{
		{"curl with official originator", "curl/7.76.1", "codex_cli_rs"},
		{"missing UA", "", "codex_cli_rs"},
		{"embedded product", "curl/7.76.1 codex_cli_rs/0.159.2", "codex_cli_rs"},
		{"unknown originator", "codex_cli_rs/0.159.2", "codex_fake"},
		{"missing product version", "codex_cli_rs/", "codex_cli_rs"},
	}
	for _, policy := range []string{"official_clients", "cli_only", "local_proxy_only"} {
		for _, tc := range cases {
			t.Run(policy+"/"+tc.name, func(t *testing.T) {
				var slotCalls atomic.Int32
				cache := &concurrencyCacheMock{
					acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
						slotCalls.Add(1)
						return false, errors.New("admission must reject before scheduling")
					},
				}
				h := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
				groupID := int64(2)
				apiKey := &service.APIKey{
					ID: 101, GroupID: &groupID,
					Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, CodexClientPolicy: policy},
					User:  &service.User{ID: 1},
				}
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Set(string(middleware.ContextKeyAPIKey), apiKey)
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
				})
				router.POST("/v1/responses", h.Responses)
				router.GET("/v1/responses", h.ResponsesWebSocket)
				router.GET("/v1/models", h.CodexModels)
				headers := http.Header{}
				headers.Set("User-Agent", tc.ua)
				headers.Set("originator", tc.originator)
				headers.Set("version", "0.159.2")
				headers.Set("chatgpt-account-id", "TEST_ONLY_ACCOUNT")

				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"TEST_ONLY_MODEL","stream":true,"input":"TEST_ONLY_INPUT"}`))
				req.Header = headers.Clone()
				router.ServeHTTP(rec, req)
				require.Equal(t, http.StatusForbidden, rec.Code)

				modelsRec := httptest.NewRecorder()
				modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
				modelsReq.Header = headers.Clone()
				router.ServeHTTP(modelsRec, modelsReq)
				require.Equal(t, http.StatusForbidden, modelsRec.Code)

				server := httptest.NewServer(router)
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
				require.NoError(t, err)
				defer func() { _ = conn.CloseNow() }()
				require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"TEST_ONLY_MODEL","input":"TEST_ONLY_INPUT"}`)))
				_, _, err = conn.Read(ctx)
				require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
				require.Zero(t, slotCalls.Load(), "no admitted request may reach scheduling or provider egress")
			})
		}
	}
}

func TestLogCodexClientPolicyRejectionOmitsSensitiveFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink, restore := captureHandlerStructuredLog(t)
	defer restore()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("TEST_ONLY_BODY"))
	c.Request.Header.Set("Authorization", "Bearer TEST_ONLY_KEY")
	c.Request.Header.Set("Cookie", "TEST_ONLY_COOKIE")
	c.Request.Header.Set("User-Agent", "curl/7.76.1")
	c.Request.Header.Set("originator", "codex_cli_rs")
	c.Request.Header.Set("chatgpt-account-id", "TEST_ONLY_ACCOUNT")
	logCodexClientPolicyRejection(c, &service.Group{ID: 2, CodexClientPolicy: "local_proxy_only"})
	require.True(t, sink.ContainsMessageAtLevel("openai.codex_client_policy_rejected", "warn"))
	require.True(t, sink.ContainsFieldValue("reason", "invalid_client_headers"))
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, event := range sink.events {
		for _, field := range []string{"authorization", "cookie", "request_headers", "request_body", "account_id", "user_agent", "originator"} {
			require.NotContains(t, event.Fields, field)
		}
		for _, value := range event.Fields {
			require.NotContains(t, fmt.Sprint(value), "TEST_ONLY_")
		}
	}
}
