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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	tzGuardTUIUserAgent = "codex-tui/0.163.0 (Ubuntu 24.4.0; x86_64) xterm-256color (codex-tui; 0.163.0)"
	tzGuardTaggedBody   = `{"model":"TEST_ONLY_MODEL","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>"}]}]}`
	tzGuardTaggedFrame  = `{"type":"response.create","model":"TEST_ONLY_MODEL","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<timezone>Asia/Hong_Kong</timezone>"}]}]}`
)

func TestCodexTimezoneGuardMessageSelection(t *testing.T) {
	cases := map[string]string{
		"codex_cli_rs/0.162.9 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1":             codexTimezoneGuardMessageOld,
		"codex-tui/0.150.0 (Windows 10.0.26200; x86_64) unknown (codex-tui; 0.150.0)":  codexTimezoneGuardMessageOld,
		"codex_exec/0.147.0 (Ubuntu 24.4.0; x86_64) unknown":                           codexTimezoneGuardMessageOld,
		"codex_vscode/0.156.0 (Mac OS 15.5.0; arm64) vscode/1.100":                     codexTimezoneGuardMessageOld,
		"Codex Desktop/0.159.0-alpha.12.1 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1": codexTimezoneGuardMessageOld,
		tzGuardTUIUserAgent: codexTimezoneGuardMessageGeneric,
		"codex_cli_rs/0.163.0-alpha.1 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1": codexTimezoneGuardMessageGeneric,
		"codex_cli_rs/1.0.0 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1":           codexTimezoneGuardMessageGeneric,
		"codex_cli_rs/0.104.0":                                codexTimezoneGuardMessageGeneric,
		"codex_work_desktop/0.150.0 (Mac OS 15.5.0; arm64) x": codexTimezoneGuardMessageGeneric,
		"curl/8.0": codexTimezoneGuardMessageGeneric,
		"":         codexTimezoneGuardMessageGeneric,
	}
	for ua, want := range cases {
		require.Equal(t, want, codexTimezoneGuardMessage(ua), ua)
	}
}

type tzGuardFixture struct {
	router    *gin.Engine
	slotCalls *atomic.Int32
	group     *service.Group
}

func newTZGuardFixture(t *testing.T, guard bool) *tzGuardFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	slotCalls := &atomic.Int32{}
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
			slotCalls.Add(1)
			return false, errors.New("TEST_ONLY_STOP_BEFORE_UPSTREAM")
		},
	}
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
	groupID := int64(2)
	group := &service.Group{ID: groupID, Platform: service.PlatformOpenAI, CodexClientPolicy: "off", CodexCNTimezoneGuard: guard}
	apiKey := &service.APIKey{ID: 101, GroupID: &groupID, Group: group, User: &service.User{ID: 1}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
	})
	router.POST("/v1/responses", h.Responses)
	router.POST("/v1/responses/*subpath", h.Responses)
	router.GET("/v1/responses", h.ResponsesWebSocket)
	return &tzGuardFixture{router: router, slotCalls: slotCalls, group: group}
}

func (f *tzGuardFixture) post(path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("User-Agent", tzGuardTUIUserAgent)
	req.Header.Set("Content-Type", "application/json")
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestCodexTimezoneGuardHTTPRejectsBeforeScheduling(t *testing.T) {
	f := newTZGuardFixture(t, true)
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		rec := f.post(path, tzGuardTaggedBody)
		require.Equal(t, http.StatusForbidden, rec.Code, path)
		body := rec.Body.Bytes()
		require.Equal(t, "permission_error", gjson.GetBytes(body, "error.type").String())
		require.Equal(t, codexTimezoneGuardCode, gjson.GetBytes(body, "error.code").String())
		require.Equal(t, codexTimezoneGuardMessageGeneric, gjson.GetBytes(body, "error.message").String())
	}
	require.Zero(t, f.slotCalls.Load(), "a rejected request must not reach scheduling")

	// Without the tag the enabled guard is transparent.
	rec := f.post("/v1/responses", `{"model":"TEST_ONLY_MODEL","stream":true,"input":"TEST_ONLY_INPUT"}`)
	require.Equal(t, int32(1), f.slotCalls.Load())
	require.NotEqual(t, codexTimezoneGuardCode, gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
}

func TestCodexTimezoneGuardHTTPSwitchOffIsUnchanged(t *testing.T) {
	f := newTZGuardFixture(t, false)
	rec := f.post("/v1/responses", tzGuardTaggedBody)
	require.Equal(t, int32(1), f.slotCalls.Load(), "switch off: the tagged request proceeds to scheduling")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotContains(t, rec.Body.String(), codexTimezoneGuardCode)
}

func dialTZGuardWS(t *testing.T, router *gin.Engine) (*coderws.Conn, context.Context) {
	t.Helper()
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	headers := http.Header{}
	headers.Set("User-Agent", "codex_cli_rs/0.160.0 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1")
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn, ctx
}

func requireTZGuardErrorFrame(t *testing.T, frame []byte, message string) {
	t.Helper()
	require.Equal(t, "error", gjson.GetBytes(frame, "type").String(), string(frame))
	require.Equal(t, int64(http.StatusForbidden), gjson.GetBytes(frame, "status").Int())
	require.Equal(t, "permission_error", gjson.GetBytes(frame, "error.type").String())
	require.Equal(t, codexTimezoneGuardCode, gjson.GetBytes(frame, "error.code").String())
	require.Equal(t, message, gjson.GetBytes(frame, "error.message").String())
}

func TestCodexTimezoneGuardWSFirstFrameRejectsBeforeScheduling(t *testing.T) {
	f := newTZGuardFixture(t, true)
	conn, ctx := dialTZGuardWS(t, f.router)
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(tzGuardTaggedFrame)))

	msgType, frame, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, msgType)
	// UA 0.160.0 is a recognised Codex backend below 0.163.
	requireTZGuardErrorFrame(t, frame, codexTimezoneGuardMessageOld)

	_, _, err = conn.Read(ctx)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Equal(t, codexTimezoneGuardWSReason, closeErr.Reason)
	require.Zero(t, f.slotCalls.Load())
}

func TestCodexTimezoneGuardWSSwitchOffIsUnchanged(t *testing.T) {
	f := newTZGuardFixture(t, false)
	conn, ctx := dialTZGuardWS(t, f.router)
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(tzGuardTaggedFrame)))
	_, _, err := conn.Read(ctx)
	require.Equal(t, coderws.StatusInternalError, coderws.CloseStatus(err), "switch off: proceeds to scheduling")
	require.Equal(t, int32(1), f.slotCalls.Load())
}

// Later turns, rebuilt continuation payloads and failover replays all go
// through checkCodexTimezoneGuardWS while the relay owns the connection; the
// session then ends through closeCodexTimezoneGuardWSFromError.
func TestCodexTimezoneGuardWSLaterTurnFrameAndClose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{ID: 7, CodexCNTimezoneGuard: true}
	type result struct {
		checkErr   error
		closed     bool
		opsFailure opsLocalFailure
	}
	results := make(chan result, 1)
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		conn, err := coderws.Accept(c.Writer, c.Request, nil)
		if err != nil {
			results <- result{checkErr: err}
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx := c.Request.Context()
		// An untagged later turn passes.
		if err := checkCodexTimezoneGuardWS(ctx, c, conn, group, 1, 2, []byte(`{"type":"response.create","input":"ok"}`)); err != nil {
			results <- result{checkErr: err}
			return
		}
		checkErr := checkCodexTimezoneGuardWS(ctx, c, conn, group, 1, 3, []byte(tzGuardTaggedFrame))
		// The relay wraps hook errors; the marker must survive wrapping.
		wrapped := fmt.Errorf("relay: %w", checkErr)
		closed := closeCodexTimezoneGuardWSFromError(c, conn, wrapped)
		raw, _ := c.Get(opsLocalFailureKey)
		failure, _ := raw.(opsLocalFailure)
		results <- result{checkErr: checkErr, closed: closed, opsFailure: failure}
	})
	conn, ctx := dialTZGuardWS(t, router)

	_, frame, err := conn.Read(ctx)
	require.NoError(t, err)
	requireTZGuardErrorFrame(t, frame, codexTimezoneGuardMessageOld)
	_, _, err = conn.Read(ctx)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))

	got := <-results
	var closeErr *service.OpenAIWSClientCloseError
	require.ErrorAs(t, got.checkErr, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	require.True(t, got.closed)
	require.Equal(t, http.StatusForbidden, got.opsFailure.status)
	require.Equal(t, codexTimezoneGuardCode, got.opsFailure.code)
	require.Equal(t, int(coderws.StatusPolicyViolation), got.opsFailure.wsCloseCode)

	// Ordinary errors are not treated as guard rejections.
	require.False(t, closeCodexTimezoneGuardWSFromError(nil, nil, errors.New("other")))
}

func TestCodexTimezoneGuardWSRebuiltPayloadIsScanned(t *testing.T) {
	group := &service.Group{CodexCNTimezoneGuard: true}
	// A continuation rebuild prepends replayed history; the new frame itself
	// carries no tag, so only scanning the rebuilt payload catches it.
	original := []byte(`{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"message","role":"user","content":"next"}]}`)
	rebuilt := []byte(`{"type":"response.create","input":[{"type":"message","role":"user","content":"<environment_context><timezone>Asia/Macau</timezone></environment_context>"},{"type":"message","role":"user","content":"next"}]}`)
	_, found := codexTimezoneGuardZone(group, original)
	require.False(t, found)
	zone, found := codexTimezoneGuardZone(group, rebuilt)
	require.True(t, found)
	require.Equal(t, "Asia/Macau", zone)
	_, found = codexTimezoneGuardZone(&service.Group{}, rebuilt)
	require.False(t, found, "switch off")
	_, found = codexTimezoneGuardZone(nil, rebuilt)
	require.False(t, found)
}

func TestRevalidateOpenAIWSTurnKeyReadsCurrentGroupSwitch(t *testing.T) {
	repo := &openAIWSRevalidationAPIKeyRepoStub{apiKey: &service.APIKey{
		ID: 3, Key: "sk-test", Status: service.StatusAPIKeyActive,
		User:  &service.User{ID: 2, Status: service.StatusActive},
		Group: &service.Group{ID: 9, CodexCNTimezoneGuard: false},
	}}
	h := &OpenAIGatewayHandler{
		apiKeyService: service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{}),
	}
	groupID := int64(9)
	connectTime := &service.APIKey{Key: "sk-test", GroupID: &groupID, Group: &service.Group{ID: 9}}

	key, err := h.revalidateOpenAIWSTurnKey(context.Background(), connectTime, 1)
	require.NoError(t, err)
	require.Same(t, connectTime, key)

	repo.apiKey.Group = &service.Group{ID: 9, CodexCNTimezoneGuard: true}
	key, err = h.revalidateOpenAIWSTurnKey(context.Background(), connectTime, 2)
	require.NoError(t, err)
	require.True(t, key.Group.CodexCNTimezoneGuard, "later turns read the group's current switch")
	require.True(t, h.openAIWSCurrentGroup(context.Background(), connectTime).CodexCNTimezoneGuard)
	require.True(t, openAIWSSessionGroup(connectTime, key).CodexCNTimezoneGuard)

	// A key rebound to another group mid-session keeps being checked against
	// the group whose accounts still serve the connection.
	connectTime.Group = &service.Group{ID: 9, CodexCNTimezoneGuard: true}
	repo.apiKey.Group = &service.Group{ID: 10, CodexCNTimezoneGuard: false}
	require.True(t, h.openAIWSCurrentGroup(context.Background(), connectTime).CodexCNTimezoneGuard)
	require.Nil(t, openAIWSSessionGroup(nil, key))
}
