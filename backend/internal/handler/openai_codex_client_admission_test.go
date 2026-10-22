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

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
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
		{"non-Codex originator", "codex_cli_rs/0.159.2", "cherry_studio"},
		{"missing product version", "codex_cli_rs/", "codex_cli_rs"},
		{"embedded TUI product", "curl/8.0 codex-tui/0.160.0", "codex-tui"},
		{"versioned TUI originator", "codex-tui/0.160.0", "codex-tui/0.160.0"},
		{"missing TUI version", "codex-tui/", "codex-tui"},
		{"curl with work desktop originator", "curl/8.0", "codex_work_desktop"},
		{"embedded work desktop", "curl/8.0 codex_work_desktop/0.159.0-alpha.12.1", "codex_work_desktop"},
		{"embedded work desktop originator", "codex_work_desktop/0.159.0-alpha.12.1", "my_client codex_work_desktop"},
		{"missing work desktop version", "codex_work_desktop/", "codex_work_desktop"},
		{"browser with sidepanel originator", "Mozilla/5.0", "codex-chrome-extension-sidepanel"},
		{"embedded sidepanel", "Mozilla/5.0 codex-chrome-extension-sidepanel/0.159.0", "codex-chrome-extension-sidepanel"},
		{"versioned sidepanel originator", "codex-chrome-extension-sidepanel/0.159.0", "codex-chrome-extension-sidepanel/0.159.0"},
		{"missing sidepanel version", "codex-chrome-extension-sidepanel/", "codex-chrome-extension-sidepanel"},
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

func TestOpenAICodexTUIAdmissionReachesScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, policy := range []string{"official_clients", "cli_only", "local_proxy_only"} {
		for _, version := range []string{"0.154.0", "0.160.0"} {
			t.Run(policy+"/"+version, func(t *testing.T) {
				var slotCalls atomic.Int32
				cache := &concurrencyCacheMock{
					acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
						slotCalls.Add(1)
						return false, errors.New("TEST_ONLY_STOP_BEFORE_UPSTREAM")
					},
				}
				handler := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
				groupID := int64(2)
				apiKey := &service.APIKey{
					ID: 101, GroupID: &groupID,
					Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, CodexClientPolicy: policy},
					User:  &service.User{ID: 1},
				}
				router := gin.New()
				router.Use(func(requestContext *gin.Context) {
					requestContext.Set(string(middleware.ContextKeyAPIKey), apiKey)
					requestContext.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
				})
				router.POST("/v1/responses", handler.Responses)
				router.GET("/v1/responses", handler.ResponsesWebSocket)
				headers := http.Header{}
				headers.Set("User-Agent", "codex-tui/"+version+" (Ubuntu 24.4.0; x86_64) xterm-256color (codex-tui; "+version+")")
				headers.Set("originator", "codex-tui")
				headers.Set("version", version)
				headers.Set("chatgpt-account-id", "TEST_ONLY_ACCOUNT")
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"TEST_ONLY_MODEL","stream":true,"input":"TEST_ONLY_INPUT"}`))
				request.Header = headers.Clone()
				router.ServeHTTP(recorder, request)
				require.Equal(t, int32(1), slotCalls.Load())
				require.Equal(t, http.StatusTooManyRequests, recorder.Code)

				server := httptest.NewServer(router)
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				connection, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
				require.NoError(t, err)
				defer func() { _ = connection.CloseNow() }()
				require.NoError(t, connection.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"TEST_ONLY_MODEL","input":"TEST_ONLY_INPUT"}`)))
				_, _, err = connection.Read(ctx)
				require.Equal(t, coderws.StatusInternalError, coderws.CloseStatus(err))
				require.Equal(t, int32(2), slotCalls.Load())
			})
		}
	}
}

func TestOpenAICodexDesktopSurfaceAdmissionReachesScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, policy := range []string{"official_clients", "local_proxy_only"} {
		for _, client := range []struct{ product, version string }{
			{"codex_work_desktop", "0.155.0-alpha.9.2"},
			{"codex_work_desktop", "0.159.0-alpha.12.1"},
			{"codex-chrome-extension-sidepanel", "0.159.0"},
			{"codex-browser-use", "0.163.0-alpha.10.2"},
		} {
			t.Run(policy+"/"+client.product+"/"+client.version, func(t *testing.T) {
				var slotCalls atomic.Int32
				cache := &concurrencyCacheMock{
					acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
						slotCalls.Add(1)
						return false, errors.New("TEST_ONLY_STOP_BEFORE_UPSTREAM")
					},
				}
				handler := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
				groupID := int64(2)
				apiKey := &service.APIKey{
					ID: 101, GroupID: &groupID,
					Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, CodexClientPolicy: policy},
					User:  &service.User{ID: 1},
				}
				router := gin.New()
				router.Use(func(requestContext *gin.Context) {
					requestContext.Set(string(middleware.ContextKeyAPIKey), apiKey)
					requestContext.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
				})
				router.POST("/v1/responses", handler.Responses)
				router.GET("/v1/responses", handler.ResponsesWebSocket)
				headers := http.Header{}
				headers.Set("User-Agent", client.product+"/"+client.version+" (Windows 10.0.26200; x86_64)")
				headers.Set("originator", client.product)
				headers.Set("version", client.version)
				headers.Set("chatgpt-account-id", "TEST_ONLY_ACCOUNT")
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"TEST_ONLY_MODEL","stream":true,"input":"TEST_ONLY_INPUT"}`))
				request.Header = headers.Clone()
				router.ServeHTTP(recorder, request)
				require.Equal(t, int32(1), slotCalls.Load())
				require.Equal(t, http.StatusTooManyRequests, recorder.Code)
				server := httptest.NewServer(router)
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				connection, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
				require.NoError(t, err)
				defer func() { _ = connection.CloseNow() }()
				require.NoError(t, connection.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"TEST_ONLY_MODEL","input":"TEST_ONLY_INPUT"}`)))
				_, _, err = connection.Read(ctx)
				require.Error(t, err)
				require.Equal(t, int32(2), slotCalls.Load())
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

func TestCodexMissingProxyIdentityReportsAccurateReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, version, accountID string
	}{
		{"missing version", "", "TEST_ONLY_ACCOUNT"},
		{"missing account", "0.162.0-alpha.17.2", ""},
		{"missing both", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queue := opsFailureTestQueue(t)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			var slotCalls atomic.Int32
			cache := &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
				slotCalls.Add(1)
				return false, errors.New("TEST_ONLY_MUST_NOT_SCHEDULE")
			}}
			h := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
			group := &service.Group{ID: 42, Platform: service.PlatformOpenAI, CodexClientPolicy: "local_proxy_only"}
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "TEST_ONLY_GATEWAY_REQUEST_ID"))
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &group.ID, Group: group, User: &service.User{ID: 7}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7, Concurrency: 1})
			})
			router.POST("/responses", h.Responses)
			router.GET("/responses", h.ResponsesWebSocket)
			headers := http.Header{}
			headers.Set("User-Agent", "Codex Desktop/0.162.0-alpha.17.2 (Windows 10.0.26200; x86_64)")
			headers.Set("version", tc.version)
			headers.Set("chatgpt-account-id", tc.accountID)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"TEST_ONLY_MODEL","input":"TEST_ONLY_INPUT"}`))
			req.Header = headers.Clone()
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusForbidden, w.Code)
			require.Contains(t, w.Body.String(), "saiai_local_proxy_required")
			require.Contains(t, w.Body.String(), "本地代理身份信息不完整")
			require.NotContains(t, w.Body.String(), "This group requires SAIAI local proxy mode")
			assertSnapshot := func(job opsErrorLogJob, transport string, status int) {
				require.Equal(t, "TEST_ONLY_GATEWAY_REQUEST_ID", job.entry.RequestID)
				require.NotNil(t, job.entry.Diagnostics)
				require.Equal(t, transport, job.entry.Diagnostics.Transport)
				require.Equal(t, status, job.entry.Diagnostics.TransportStatusCode)
				checks := job.entry.Diagnostics.Checks
				require.Len(t, checks, 4)
				require.Equal(t, "version", checks[2].Field)
				require.Equal(t, tc.version != "", checks[2].Passed)
				require.Equal(t, "chatgpt-account-id", checks[3].Field)
				require.Equal(t, tc.accountID != "", checks[3].Passed)
			}
			select {
			case job := <-queue:
				assertSnapshot(job, "http", 403)
			default:
				t.Fatal("HTTP rejection diagnostics were not queued")
			}
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/responses", &coderws.DialOptions{HTTPHeader: headers})
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"TEST_ONLY_MODEL","input":"TEST_ONLY_INPUT"}`)))
			_, _, err = conn.Read(ctx)
			require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
			require.Contains(t, err.Error(), "本地代理身份信息不完整")
			require.Equal(t, int32(0), slotCalls.Load())
			select {
			case job := <-queue:
				assertSnapshot(job, "websocket", 101)
			case <-ctx.Done():
				t.Fatal("WS rejection diagnostics were not queued")
			}
		})
	}
}

func TestCodexMissingProxyIdentityLogUsesOnlyPresenceFlags(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink, restore := captureHandlerStructuredLog(t)
	defer restore()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/0.162.0-alpha.17.2")
	c.Request.Header.Set("chatgpt-account-id", "TEST_ONLY_SECRET_ACCOUNT")
	logCodexClientPolicyRejection(c, &service.Group{ID: 42, CodexClientPolicy: "local_proxy_only"})
	require.True(t, sink.ContainsFieldValue("reason", "required_proxy_headers_missing"))
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, event := range sink.events {
		if event.Message == "openai.codex_client_policy_rejected" {
			require.Equal(t, true, event.Fields["missing_version"])
			require.Equal(t, false, event.Fields["missing_account_id"])
		}
		for _, value := range event.Fields {
			require.NotContains(t, fmt.Sprint(value), "TEST_ONLY_SECRET_ACCOUNT")
		}
	}
}
