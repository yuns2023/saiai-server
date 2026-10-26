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
	pkgopenai "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
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
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		body := rec.Body.Bytes()
		require.Equal(t, "invalid_request_error", gjson.GetBytes(body, "error.type").String())
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
	require.Equal(t, int64(http.StatusBadRequest), gjson.GetBytes(frame, "status").Int())
	require.Equal(t, "invalid_request_error", gjson.GetBytes(frame, "error.type").String())
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
// session then ends through closeOpenAIWSRejectedSession.
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
		closed := closeOpenAIWSRejectedSession(c, conn, wrapped)
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
	require.Equal(t, http.StatusBadRequest, got.opsFailure.status)
	require.Equal(t, codexTimezoneGuardCode, got.opsFailure.code)
	require.Equal(t, int(coderws.StatusPolicyViolation), got.opsFailure.wsCloseCode)

	// Ordinary errors are not treated as guard rejections.
	require.False(t, closeOpenAIWSRejectedSession(nil, nil, errors.New("other")))
}

func TestCodexTimezoneGuardWSRebuiltPayloadIsScanned(t *testing.T) {
	group := &service.Group{CodexCNTimezoneGuard: true}
	// A continuation rebuild prepends replayed history; the new frame itself
	// carries no tag, so only scanning the rebuilt payload catches it.
	original := []byte(`{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"message","role":"user","content":"next"}]}`)
	rebuilt := []byte(`{"type":"response.create","input":[{"type":"message","role":"user","content":"<environment_context><timezone>Asia/Macau</timezone></environment_context>"},{"type":"message","role":"user","content":"next"}]}`)
	require.Nil(t, codexTimezoneGuardZone(group, original))
	rejection := codexTimezoneGuardZone(group, rebuilt)
	require.NotNil(t, rejection)
	require.Equal(t, codexTimezoneRejectZone, rejection.reason)
	require.Equal(t, "Asia/Macau", rejection.zone)
	require.True(t, rejection.located)
	require.Equal(t, "input[0].content", rejection.location.Path)
	require.Equal(t, "message", rejection.location.ItemType)
	require.Equal(t, "user", rejection.location.ItemRole)
	require.Nil(t, codexTimezoneGuardZone(&service.Group{}, rebuilt), "switch off")
	require.Nil(t, codexTimezoneGuardZone(nil, rebuilt))

	// Rewrite mode never rejects for the zone, only for a block that cannot
	// be rewritten safely.
	rewrite := &service.Group{CodexCNTimezoneGuard: true, CodexTimezoneAction: service.CodexTimezoneActionRewrite}
	require.Nil(t, codexTimezoneGuardZone(rewrite, rebuilt))
	rejection = codexTimezoneGuardZone(rewrite, []byte(`{"input":"<environment_context><timezone>PRC</environment_context>"}`))
	require.NotNil(t, rejection)
	require.Equal(t, codexTimezoneRejectMalformed, rejection.reason)
	require.True(t, rejection.located)
	require.Equal(t, "input", rejection.location.Path)
	require.True(t, rejection.location.StartsWithEnvOpen)
}

func TestRevalidateOpenAIWSTurnKeyReadsCurrentGroupSwitch(t *testing.T) {
	groupID := int64(9)
	repo := &openAIWSRevalidationAPIKeyRepoStub{apiKey: &service.APIKey{
		ID: 3, Key: "sk-test", Status: service.StatusAPIKeyActive, GroupID: &groupID,
		User:  &service.User{ID: 2, Status: service.StatusActive},
		Group: &service.Group{ID: 9, CodexCNTimezoneGuard: false},
	}}
	h := &OpenAIGatewayHandler{
		apiKeyService: service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{}),
	}
	connectTime := &service.APIKey{Key: "sk-test", GroupID: &groupID, Group: &service.Group{ID: 9}}

	key, err := h.revalidateOpenAIWSTurnKey(context.Background(), connectTime, 1)
	require.NoError(t, err)
	require.Same(t, connectTime, key)

	repo.apiKey.Group = &service.Group{ID: 9, CodexCNTimezoneGuard: true}
	key, err = h.revalidateOpenAIWSTurnKey(context.Background(), connectTime, 2)
	require.NoError(t, err)
	require.True(t, key.Group.CodexCNTimezoneGuard, "later turns read the group's current switch")
	require.True(t, sameOpenAIWSGroup(connectTime, key))

	// A key rebound to another group is detected (the session must reconnect).
	other := int64(10)
	repo.apiKey.GroupID = &other
	key, err = h.revalidateOpenAIWSTurnKey(context.Background(), connectTime, 3)
	require.NoError(t, err)
	require.False(t, sameOpenAIWSGroup(connectTime, key))
	require.False(t, sameOpenAIWSGroup(nil, key))
	require.True(t, sameOpenAIWSGroup(&service.APIKey{}, &service.APIKey{}))
}

func newTZRewriteFixture(t *testing.T) *tzGuardFixture {
	t.Helper()
	f := newTZGuardFixture(t, true)
	f.group.CodexTimezoneAction = service.CodexTimezoneActionRewrite
	return f
}

const tzGuardMalformedBody = `{"model":"TEST_ONLY_MODEL","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <timezone>Asia/Shanghai\n</environment_context>"}]}]}`

func TestCodexTimezoneRewriteHTTPNeverRejectsZoneButFailsClosed(t *testing.T) {
	f := newTZRewriteFixture(t)
	rec := f.post("/v1/responses", tzGuardTaggedBody)
	require.Equal(t, int32(1), f.slotCalls.Load(), "rewrite mode: the tagged request proceeds")
	require.NotContains(t, rec.Body.String(), codexTimezoneGuardCode)

	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		rec = f.post(path, tzGuardMalformedBody)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.Equal(t, codexTimezoneGuardCode, gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	}
	require.Equal(t, int32(1), f.slotCalls.Load(), "a malformed block never reaches scheduling")
}

// After user-slot wait heartbeats started the SSE stream, a rejection (e.g.
// after a smart switch) is delivered as an SSE error event, not a status.
func TestCodexTimezoneGuardHTTPRejectionAfterStreamStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.WriteHeaderNow()
	h := &OpenAIGatewayHandler{}
	group := &service.Group{ID: 3, CodexCNTimezoneGuard: true}
	body, ok := h.applyCodexTimezoneGuardHTTP(c, group, 1, []byte(tzGuardTaggedBody), &codexTimezoneFrozenDate{}, true)
	require.False(t, ok)
	require.Nil(t, body)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "event: error")
	require.Contains(t, rec.Body.String(), codexTimezoneGuardCode)
}

func TestCodexTimezoneGuardHTTPAppliesFromOriginal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	h := &OpenAIGatewayHandler{}
	original := []byte(tzGuardTaggedBody)
	date := &codexTimezoneFrozenDate{}
	rewrite := &service.Group{CodexCNTimezoneGuard: true, CodexTimezoneAction: service.CodexTimezoneActionRewrite}
	sent, ok := h.applyCodexTimezoneGuardHTTP(c, rewrite, 1, original, date, false)
	require.True(t, ok)
	require.Contains(t, string(sent), "<timezone>America/Los_Angeles</timezone>")
	require.Equal(t, tzGuardTaggedBody, string(original), "the original stays immutable")
	sent, ok = h.applyCodexTimezoneGuardHTTP(c, &service.Group{}, 1, original, date, false)
	require.True(t, ok)
	require.True(t, sameOpenAIBodyBytes(sent, original), "guard off sends the original")
}

func TestCodexTimezoneDatesAreFrozen(t *testing.T) {
	saved := codexTimezoneNow
	t.Cleanup(func() { codexTimezoneNow = saved })
	now := time.Date(2026, 10, 25, 6, 59, 0, 0, time.UTC) // 2026-10-24 in LA
	codexTimezoneNow = func() time.Time { return now }

	laDate := func(at time.Time) string {
		date, err := pkgopenai.CodexRewriteDate(at)
		require.NoError(t, err)
		return date
	}
	httpDate := &codexTimezoneFrozenDate{}
	first := httpDate.get()
	require.Equal(t, "2026-10-24", laDate(first))
	now = now.Add(2 * time.Minute) // 2026-10-25 in LA
	require.Equal(t, first, httpDate.get(), "one clock per HTTP request")

	dates := &codexTimezoneTurnDates{}
	now = time.Date(2026, 10, 25, 6, 59, 0, 0, time.UTC)
	turn1 := dates.get(1)
	now = now.Add(2 * time.Minute)
	require.Equal(t, turn1, dates.get(1), "retries and replays of a turn reuse its clock")
	require.Equal(t, "2026-10-24", laDate(turn1))
	require.Equal(t, "2026-10-25", laDate(dates.get(2)))
	for turn := 3; turn < 3+2*codexTimezoneTurnDatesKept; turn++ {
		_ = dates.get(turn)
	}
	require.LessOrEqual(t, len(dates.dates), codexTimezoneTurnDatesKept)
}

func TestCodexTimezoneRewriteWSFirstFrame(t *testing.T) {
	f := newTZRewriteFixture(t)
	conn, ctx := dialTZGuardWS(t, f.router)
	blockFrame := `{"type":"response.create","model":"TEST_ONLY_MODEL","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context><timezone>Asia/Hong_Kong</timezone></environment_context>"}]}]}`
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(blockFrame)))
	_, _, err := conn.Read(ctx)
	require.Equal(t, coderws.StatusInternalError, coderws.CloseStatus(err), "rewrite mode: proceeds to scheduling")
	require.Equal(t, int32(1), f.slotCalls.Load())

	conn, ctx = dialTZGuardWS(t, f.router)
	malformed := `{"type":"response.create","model":"TEST_ONLY_MODEL","input":"<environment_context><timezone>PRC</environment_context>"}`
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(malformed)))
	_, frame, err := conn.Read(ctx)
	require.NoError(t, err)
	requireTZGuardErrorFrame(t, frame, codexTimezoneGuardMessageOld)
	require.Equal(t, int32(1), f.slotCalls.Load(), "a malformed first frame is rejected before scheduling")
}

func TestRewriteCodexTimezoneWS(t *testing.T) {
	dates := &codexTimezoneTurnDates{}
	payload := []byte(tzGuardTaggedFrame)
	// Reject mode and guard off never modify sent bytes.
	for _, group := range []*service.Group{nil, {}, {CodexCNTimezoneGuard: true}} {
		out, err := rewriteCodexTimezoneWS(context.Background(), nil, nil, group, 1, 1, payload, dates)
		require.NoError(t, err)
		require.True(t, sameOpenAIBodyBytes(out, payload))
	}
	group := &service.Group{CodexCNTimezoneGuard: true, CodexTimezoneAction: service.CodexTimezoneActionRewrite}
	tagged := []byte(`{"type":"response.create","input":"<environment_context><timezone>Asia/Hong_Kong</timezone></environment_context>"}`)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	out, err := rewriteCodexTimezoneWS(context.Background(), c, nil, group, 1, 2, tagged, dates)
	require.NoError(t, err)
	require.Equal(t, `{"type":"response.create","input":"<environment_context><timezone>America/Los_Angeles</timezone></environment_context>"}`, string(out))
}

// Acceptance false positive: a user prompt that mentions the tag (closed or
// not, mid-sentence) is user content in rewrite mode: forwarded, not rejected.
func TestCodexTimezoneRewriteUserTextMentioningTagIsNotRejected(t *testing.T) {
	f := newTZRewriteFixture(t)
	for i, text := range []string{
		`what does <environment_context> mean?`,
		`see <environment_context><timezone>Europe/Berlin</timezone></environment_context> above`,
	} {
		body := `{"model":"TEST_ONLY_MODEL","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}]}`
		rec := f.post("/v1/responses", body)
		require.NotContains(t, rec.Body.String(), codexTimezoneGuardCode)
		require.Equal(t, int32(i+1), f.slotCalls.Load(), "proceeds to scheduling")

		conn, ctx := dialTZGuardWS(t, f.router)
		frame := `{"type":"response.create","model":"TEST_ONLY_MODEL","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}]}`
		require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(frame)))
		_, _, err := conn.Read(ctx)
		require.Equal(t, coderws.StatusInternalError, coderws.CloseStatus(err), "WS proceeds to scheduling, no rejection frame")
		f.slotCalls.Add(-1)
	}
}

// Leak safety net: after rewriting, the bytes to send are scanned like reject
// mode. A CN zone the rewrite did not handle (a malformed embedded block or a
// pasted tag outside any block) is rejected; plain mentions of the tag and
// well-formed embedded blocks pass. (Before embedded blocks were rewritten,
// `note: <block>` was in the leaking list.)
func TestCodexTimezoneRewriteLeakSafetyNet(t *testing.T) {
	group := &service.Group{CodexCNTimezoneGuard: true, CodexTimezoneAction: service.CodexTimezoneActionRewrite}
	leaking := []string{
		// Malformed embedded block (truncated tag): left untouched.
		`note: <environment_context><timezone>Asia/Shanghai</timezone><current_date</environment_context>`,
		`<timezone>Asia/Hong_Kong</timezone>`,
		// A properly nested pair: ambiguous, left untouched.
		`x <environment_context><environment_context><timezone>PRC</timezone></environment_context></environment_context>`,
		`x\n<environment_context><environment_context><timezone>PRC</timezone></environment_context></environment_context>`,
		// A tag outside the block next to a rewritten block.
		`x <environment_context><timezone>PRC</timezone></environment_context> <timezone>Asia/Urumqi</timezone>`,
	}
	for _, text := range leaking {
		body := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}]}`)
		_, _, rejection := rewriteCodexTimezonePayload(group, body, time.Now())
		require.NotNil(t, rejection, text)
		require.Equal(t, codexTimezoneRejectAfterRewrite, rejection.reason, text)
		require.NotEmpty(t, rejection.zone)
		require.True(t, rejection.located)
		require.Equal(t, "input[0].content[0].text", rejection.location.Path)
		rejection = codexTimezoneGuardZone(group, body)
		require.NotNil(t, rejection, "checkpoints reject it too")
		require.Equal(t, codexTimezoneRejectAfterRewrite, rejection.reason)
	}
	for _, text := range []string{
		`what does <environment_context> mean?`,
		`<environment_context><timezone>Asia/Shanghai</timezone></environment_context>`, // a real block: rewritten
		`see <environment_context><timezone>America/New_York</timezone></environment_context>`,
		`note: <environment_context><timezone>Asia/Shanghai</timezone></environment_context>`, // embedded: rewritten
		// CN only in an independent good block; the nested segment is CN-free.
		`x <environment_context><timezone>PRC</timezone></environment_context>\ny <environment_context><environment_context></environment_context></environment_context>`,
		`x <environment_context><timezone>PRC</timezone></environment_context> y <environment_context><environment_context></environment_context></environment_context>`,
		// A lone leading close-tag mention does not stop the scan.
		`grep: </environment_context>\n<environment_context><timezone>Asia/Shanghai</timezone></environment_context>`,
		`grep: </environment_context> <environment_context><timezone>Asia/Shanghai</timezone></environment_context>`,
	} {
		body := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}]}`)
		out, _, rejection := rewriteCodexTimezonePayload(group, body, time.Now())
		require.Nil(t, rejection, text)
		require.NotContains(t, string(out), "Asia/Shanghai")
	}

	// End to end over HTTP and WS.
	f := newTZRewriteFixture(t)
	leak := `{"model":"TEST_ONLY_MODEL","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"x <environment_context><timezone>Asia/Shanghai</timezone><current_date</environment_context>"}]}]}`
	rec := f.post("/v1/responses", leak)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, codexTimezoneGuardCode, gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	conn, ctx := dialTZGuardWS(t, f.router)
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"TEST_ONLY_MODEL","input":"x <timezone>PRC</timezone>"}`)))
	_, frame, err := conn.Read(ctx)
	require.NoError(t, err)
	requireTZGuardErrorFrame(t, frame, codexTimezoneGuardMessageOld)
	require.Zero(t, f.slotCalls.Load(), "rejected before scheduling")
}

// The send-time WS rewrite rejects too, and a block that is rewritten does
// not shield a CN zone left in another string of the same payload.
func TestRewriteCodexTimezoneWSLeakSafetyNet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{CodexCNTimezoneGuard: true, CodexTimezoneAction: service.CodexTimezoneActionRewrite}
	mixed := []byte(`{"type":"response.create","input":[{"type":"message","content":[{"type":"input_text","text":"<environment_context><timezone>Asia/Shanghai</timezone></environment_context>"},{"type":"input_text","text":"pasted <timezone>Asia/Macau</timezone>"}]}]}`)
	_, _, rejection := rewriteCodexTimezonePayload(group, mixed, time.Now())
	require.NotNil(t, rejection)
	require.Equal(t, codexTimezoneRejectAfterRewrite, rejection.reason)
	require.Equal(t, "Asia/Macau", rejection.zone)
	require.Equal(t, "input[0].content[1].text", rejection.location.Path)

	results := make(chan error, 1)
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		conn, err := coderws.Accept(c.Writer, c.Request, nil)
		if err != nil {
			results <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		out, rewriteErr := rewriteCodexTimezoneWS(c.Request.Context(), c, conn, group, 1, 2, mixed, &codexTimezoneTurnDates{})
		require.Nil(t, out)
		results <- rewriteErr
	})
	conn, ctx := dialTZGuardWS(t, router)
	_, frame, err := conn.Read(ctx)
	require.NoError(t, err)
	requireTZGuardErrorFrame(t, frame, codexTimezoneGuardMessageOld)
	var closeErr *service.OpenAIWSClientCloseError
	require.ErrorAs(t, <-results, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
}
