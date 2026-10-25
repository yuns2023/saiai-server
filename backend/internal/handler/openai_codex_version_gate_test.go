package handler

import (
	"context"
	"errors"
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
	"github.com/tidwall/gjson"
)

type codexVersionGateStub struct {
	gate  service.CodexVersionGate
	reads *atomic.Int32
}

func (s codexVersionGateStub) GetCodexVersionGate(context.Context) service.CodexVersionGate {
	if s.reads != nil {
		s.reads.Add(1)
	}
	return s.gate
}

func newCodexVersionGateRouter(t *testing.T, gate service.CodexVersionGate) (*gin.Engine, *atomic.Int32) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	slotCalls := &atomic.Int32{}
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
			slotCalls.Add(1)
			return false, errors.New("TEST_ONLY_STOP_BEFORE_UPSTREAM")
		},
	})
	h.codexVersionGate = codexVersionGateStub{gate: gate}
	groupID := int64(2)
	apiKey := &service.APIKey{ID: 101, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI}, User: &service.User{ID: 1}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
	})
	router.POST("/v1/responses", h.Responses)
	router.POST("/v1/responses/*subpath", h.Responses)
	router.GET("/v1/responses", h.ResponsesWebSocket)
	return router, slotCalls
}

func postCodexVersionGate(router *gin.Engine, path, userAgent, version string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"TEST_ONLY_MODEL","stream":true,"input":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if version != "" {
		req.Header.Set("version", version)
	}
	router.ServeHTTP(rec, req)
	return rec
}

func TestCodexVersionGateHTTP(t *testing.T) {
	gate := service.CodexVersionGate{Enabled: true, Min: "0.164.0", Max: "0.170.0"}
	router, slotCalls := newCodexVersionGateRouter(t, gate)
	cases := []struct {
		name, path, ua, version, target string
		rejected                        bool
	}{
		{name: "below_min", path: "/v1/responses", ua: "codex_cli_rs/0.163.0 (Mac OS 15.5.0; arm64)", version: "0.163.0", target: "0.164.0", rejected: true},
		{name: "prerelease_of_min_is_below", path: "/v1/responses/compact", ua: "codex-tui/0.164.0-alpha.3 (Ubuntu)", version: "0.164.0-alpha.3", target: "0.164.0", rejected: true},
		{name: "above_max", path: "/v1/responses/compact", ua: "Codex Desktop/0.171.0 (Windows)", version: "0.171.0", target: "0.170.0", rejected: true},
		{name: "unknown_header_mismatch", path: "/v1/responses", ua: "codex_cli_rs/0.165.0", version: "0.166.0", target: "0.164.0", rejected: true},
		{name: "unknown_header_missing", path: "/v1/responses", ua: "codex_exec/0.165.0", target: "0.164.0", rejected: true},
		{name: "inside_range", path: "/v1/responses", ua: "codex_cli_rs/0.165.0 (Mac OS 15.5.0; arm64)", version: "0.165.0"},
		{name: "max_inclusive", path: "/v1/responses", ua: "codex_vscode/0.170.0", version: "0.170.0"},
		{name: "non_codex_unaffected", path: "/v1/responses", ua: "curl/8.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := slotCalls.Load()
			rec := postCodexVersionGate(router, tc.path, tc.ua, tc.version)
			body := rec.Body.Bytes()
			if !tc.rejected {
				require.NotEqual(t, codexVersionUnsupportedCode, gjson.GetBytes(body, "error.code").String())
				require.Equal(t, before+1, slotCalls.Load(), "admitted requests continue to scheduling")
				return
			}
			require.Equal(t, http.StatusBadRequest, rec.Code, string(body))
			require.Equal(t, "invalid_request_error", gjson.GetBytes(body, "error.type").String())
			require.Equal(t, codexVersionUnsupportedCode, gjson.GetBytes(body, "error.code").String())
			message := gjson.GetBytes(body, "error.message").String()
			require.Contains(t, message, "[0.164.0, 0.170.0]")
			require.Contains(t, message, "npm i -g @openai/codex@"+tc.target)
			require.Contains(t, message, "codex app-server daemon update --from-cli -y")
			require.Equal(t, before, slotCalls.Load(), "rejected before scheduling")
		})
	}
}

func TestCodexVersionGateDisabledOrUnbounded(t *testing.T) {
	router, slotCalls := newCodexVersionGateRouter(t, service.CodexVersionGate{Enabled: false, Min: "9.0.0"})
	rec := postCodexVersionGate(router, "/v1/responses", "codex_cli_rs/0.100.0", "")
	require.NotEqual(t, codexVersionUnsupportedCode, gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	require.Equal(t, int32(1), slotCalls.Load())

	// Enabled without bounds: any known version passes, unknown is rejected.
	router, slotCalls = newCodexVersionGateRouter(t, service.CodexVersionGate{Enabled: true})
	rec = postCodexVersionGate(router, "/v1/responses", "codex_cli_rs/0.100.0", "0.100.0")
	require.NotEqual(t, codexVersionUnsupportedCode, gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	rec = postCodexVersionGate(router, "/v1/responses", "codex_cli_rs/0.100.0", "")
	require.Equal(t, codexVersionUnsupportedCode, gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	require.Contains(t, gjson.GetBytes(rec.Body.Bytes(), "error.message").String(), "[不限, 不限]")
	require.Contains(t, gjson.GetBytes(rec.Body.Bytes(), "error.message").String(), "@openai/codex@latest")
	require.Equal(t, int32(1), slotCalls.Load())
}

func TestCodexVersionGateMessage(t *testing.T) {
	require.Equal(t,
		"当前 Codex 引擎版本 0.171.0 不在允许范围 [不限, 0.170.0]。请安装 0.170.0：npm i -g @openai/codex@0.170.0；若使用后台进程再执行 codex app-server daemon update --from-cli -y，然后重启 Codex。Desktop/VS Code 请等待管理员放行。",
		codexVersionGateMessage("0.171.0", "", "0.170.0", "0.170.0"))
}

// The WebSocket gate upgrades, then sends the 400 as an error frame and
// closes 1008 (a local proxy would turn a non-101 handshake into a retryable
// 502). Nothing is scheduled.
func TestCodexVersionGateWSRejectsAfterUpgradeWithErrorFrame(t *testing.T) {
	router, slotCalls := newCodexVersionGateRouter(t, service.CodexVersionGate{Enabled: true, Min: "0.164.0"})
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	headers := http.Header{}
	headers.Set("User-Agent", "codex_cli_rs/0.160.0 (Mac OS 15.5.0; arm64)")
	headers.Set("version", "0.160.0")
	conn, resp, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	defer func() { _ = conn.CloseNow() }()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"TEST_ONLY_MODEL","input":[]}`)))
	msgType, frame, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, msgType)
	require.Equal(t, "error", gjson.GetBytes(frame, "type").String(), string(frame))
	require.Equal(t, int64(http.StatusBadRequest), gjson.GetBytes(frame, "status").Int())
	require.Equal(t, "invalid_request_error", gjson.GetBytes(frame, "error.type").String())
	require.Equal(t, codexVersionUnsupportedCode, gjson.GetBytes(frame, "error.code").String())
	require.Contains(t, gjson.GetBytes(frame, "error.message").String(), "npm i -g @openai/codex@0.164.0")
	_, _, err = conn.Read(ctx)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
	require.Zero(t, slotCalls.Load(), "no account selection or upstream connection")

	// An admitted version proceeds to scheduling.
	headers.Set("User-Agent", "codex_cli_rs/0.164.0 (Mac OS 15.5.0; arm64)")
	headers.Set("version", "0.164.0")
	conn2, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
	require.NoError(t, err)
	defer func() { _ = conn2.CloseNow() }()
	require.NoError(t, conn2.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"TEST_ONLY_MODEL","input":[]}`)))
	_, _, err = conn2.Read(ctx)
	require.Error(t, err)
	require.Equal(t, int32(1), slotCalls.Load())
}

// Non-Codex clients never read the gate settings.
func TestCodexVersionGateSkipsSettingsForNonCodexClients(t *testing.T) {
	reads := &atomic.Int32{}
	h := &OpenAIGatewayHandler{codexVersionGate: codexVersionGateStub{gate: service.CodexVersionGate{Enabled: true, Min: "9.0.0"}, reads: reads}}
	gin.SetMode(gin.TestMode)
	for _, ua := range []string{"curl/8.0", "", "OpenAI/Python 1.0"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("User-Agent", ua)
		require.Nil(t, h.codexVersionGateRejection(c, "http"))
	}
	require.Zero(t, reads.Load())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.164.0")
	c.Request.Header.Set("version", "0.164.0")
	require.NotNil(t, h.codexVersionGateRejection(c, "http"))
	require.Equal(t, int32(1), reads.Load())
}

// A rejected version still gets the error frame when the first frame is not
// valid JSON; nothing is scheduled.
func TestCodexVersionGateWSRejectsBeforeFrameValidation(t *testing.T) {
	router, slotCalls := newCodexVersionGateRouter(t, service.CodexVersionGate{Enabled: true, Min: "0.164.0"})
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	headers := http.Header{}
	headers.Set("User-Agent", "codex_cli_rs/0.160.0")
	headers.Set("version", "0.160.0")
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`not json`)))
	_, frame, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, codexVersionUnsupportedCode, gjson.GetBytes(frame, "error.code").String())
	_, _, err = conn.Read(ctx)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
	require.Zero(t, slotCalls.Load())
}
