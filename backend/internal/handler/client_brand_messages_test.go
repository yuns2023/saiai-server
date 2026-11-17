package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/model"
	pkgerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Independent baselines copied from the texts before client brand settings.
const (
	baselineHandlerDeviceUnavailable     = "SAIAI 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"
	baselineLocalProxyIdentityIncomplete = "本地代理身份信息不完整，请重新运行 SAIAI Codex 初始化并重启代理及 Codex。"
	baselineLocalProxyRequired           = "此分组仅允许通过 SAIAI 本地代理使用官方 Codex 客户端。"
	baselineLocalProxyRequiredWSReason   = "SAIAI local proxy with official Codex required"
	baselineCodexReinitGeneric           = "请求内容或历史中包含中国地区时区信息。请重新运行 saiai cli 初始化（会关闭 Codex 的时区上报），重启 Codex 并新开会话后再使用。"
	baselineCodexReinitOld               = "当前 Codex 版本过旧，会上报本机时区。请将 Codex 升级到 0.163 或以上，重新运行 saiai cli 初始化，重启 Codex 并新开会话后再使用。"
	baselineCodexDesktop                 = "Codex 桌面版会在每轮对话中附带本机系统时区，无法通过配置关闭，当前分组不允许。请改用 Codex CLI 或 VS Code 插件，或联系管理员。"
)

func handlerTestBrand(t *testing.T, name, command string) service.ClientBrand {
	t.Helper()
	brand, err := service.NewClientBrand(name, command)
	require.NoError(t, err)
	return brand
}

func requireHandlerNoSAIAI(t *testing.T, text string) {
	t.Helper()
	require.NotContains(t, strings.ToLower(text), "saiai", text)
}

func brandedTestContext(t *testing.T, method, path string, brand service.ClientBrand) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, nil)
	c.Request = c.Request.WithContext(service.WithClientBrand(c.Request.Context(), brand))
	return c, recorder
}

func TestLocalProxyMessagesFollowBrand(t *testing.T) {
	unset := handlerTestBrand(t, " ", "  ")
	for _, brand := range []service.ClientBrand{{}, unset} {
		require.Equal(t, baselineLocalProxyIdentityIncomplete, codexLocalProxyIdentityIncompleteMessageFor(brand))
		require.Equal(t, baselineLocalProxyRequired, codexLocalProxyRequiredMessageFor(brand))
		require.Equal(t, baselineLocalProxyRequiredWSReason, codexLocalProxyRequiredWSReasonFor(brand))
	}
	for _, tc := range []struct {
		name, brand, command                   string
		identity, required, reason, apiKeyText string
	}{
		{
			name: "brand only", brand: "CCYou",
			identity: "本地代理身份信息不完整，请重新运行客户端初始化并重启代理及 Codex。",
			required: "此分组仅允许通过 CCYou 本地代理使用官方 Codex 客户端。",
			reason:   "CCYou local proxy with official Codex required",
		},
		{
			name: "command only", command: "aitp init",
			identity: "本地代理身份信息不完整，请重新运行 aitp init 并重启代理及 Codex。",
			required: "此分组仅允许通过本地代理使用官方 Codex 客户端。",
			reason:   "Local proxy with official Codex required",
		},
		{
			name: "both", brand: "CCYou", command: "aitp init",
			identity: "本地代理身份信息不完整，请重新运行 aitp init 并重启代理及 Codex。",
			required: "此分组仅允许通过 CCYou 本地代理使用官方 Codex 客户端。",
			reason:   "CCYou local proxy with official Codex required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brand := handlerTestBrand(t, tc.brand, tc.command)
			for want, got := range map[string]string{
				tc.identity: codexLocalProxyIdentityIncompleteMessageFor(brand),
				tc.required: codexLocalProxyRequiredMessageFor(brand),
				tc.reason:   codexLocalProxyRequiredWSReasonFor(brand),
			} {
				require.Equal(t, want, got)
				requireHandlerNoSAIAI(t, got)
			}
		})
	}
}

func TestLocalProxyIdentityWSReasonNamesCommandWhenItFits(t *testing.T) {
	const original = "本地代理身份信息不完整，请重新初始化并重启代理及 Codex。"
	for _, brand := range []service.ClientBrand{{}, handlerTestBrand(t, "CCYou", "")} {
		require.Equal(t, original, codexLocalProxyIdentityIncompleteWSReasonFor(brand))
	}
	require.Equal(t, "本地代理身份信息不完整，请重新运行 aitp init 并重启代理及 Codex。",
		codexLocalProxyIdentityIncompleteWSReasonFor(handlerTestBrand(t, "CCYou", "aitp init")))
	longest := codexLocalProxyIdentityIncompleteWSReasonFor(handlerTestBrand(t, "", strings.Repeat("x", service.MaxClientSetupCommandRunes)))
	require.Equal(t, original, longest, "a command that does not fit falls back to the neutral text")
	for _, command := range []string{strings.Repeat("x", 60), strings.Repeat("令", service.MaxClientSetupCommandRunes)} {
		reason := codexLocalProxyIdentityIncompleteWSReasonFor(handlerTestBrand(t, "", command))
		require.LessOrEqual(t, len(reason), openAIWSCloseReasonMaxBytes)
		require.True(t, utf8.ValidString(reason))
	}
}

func TestLocalProxyWSReasonFitsCloseFrameForLongestBrand(t *testing.T) {
	for _, tc := range []struct {
		brand   string
		branded bool
	}{
		{strings.Repeat("B", service.MaxClientBrandNameRunes), true},
		{strings.Repeat("品", service.MaxClientBrandNameRunes), false},
		{strings.Repeat("😀", service.MaxClientBrandNameRunes), false},
	} {
		brand := handlerTestBrand(t, tc.brand, "")
		reason := codexLocalProxyRequiredWSReasonFor(brand)
		require.LessOrEqual(t, len(reason), 123, "WebSocket close reason limit")
		require.LessOrEqual(t, len(reason), openAIWSCloseReasonMaxBytes, "never cut by closeOpenAIClientWS")
		require.True(t, utf8.ValidString(reason))
		requireHandlerNoSAIAI(t, reason)
		if tc.branded {
			require.Equal(t, tc.brand+" local proxy with official Codex required", reason)
		} else {
			require.Equal(t, "Local proxy with official Codex required", reason)
		}
	}
}

func TestLocalProxyRejectionHTTPAndWSUseRequestBrand(t *testing.T) {
	gin.SetMode(gin.TestMode)
	brand := handlerTestBrand(t, "CCYou", "aitp init")
	var slotCalls atomic.Int32
	cache := &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
		slotCalls.Add(1)
		return false, errors.New("TEST_ONLY_MUST_NOT_SCHEDULE")
	}}
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
	group := &service.Group{ID: 42, Platform: service.PlatformOpenAI, CodexClientPolicy: "local_proxy_only"}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithClientBrand(c.Request.Context(), brand))
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &group.ID, Group: group, User: &service.User{ID: 7}})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7, Concurrency: 1})
	})
	router.POST("/responses", h.Responses)
	router.GET("/responses", h.ResponsesWebSocket)

	for _, tc := range []struct{ ua, want string }{
		{"curl/8.5.0", "此分组仅允许通过 CCYou 本地代理使用官方 Codex 客户端。"},
		{"Codex Desktop/0.162.0-alpha.17.2 (Windows 10.0.26200; x86_64)", "本地代理身份信息不完整，请重新运行 aitp init 并重启代理及 Codex。"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"TEST_ONLY_MODEL","input":"TEST_ONLY_INPUT"}`))
		request.Header.Set("User-Agent", tc.ua)
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusForbidden, recorder.Code)
		require.Equal(t, "saiai_local_proxy_required", gjson.Get(recorder.Body.String(), "error.type").String(), "type unchanged")
		require.Equal(t, tc.want, gjson.Get(recorder.Body.String(), "error.message").String())
	}

	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	headers := http.Header{}
	headers.Set("User-Agent", "curl/8.5.0")
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/responses", &coderws.DialOptions{HTTPHeader: headers})
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"TEST_ONLY_MODEL","input":"TEST_ONLY_INPUT"}`)))
	_, _, err = conn.Read(ctx)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, "CCYou local proxy with official Codex required", closeErr.Reason)

	headers.Set("User-Agent", "Codex Desktop/0.162.0-alpha.17.2 (Windows 10.0.26200; x86_64)")
	official, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/responses", &coderws.DialOptions{HTTPHeader: headers})
	require.NoError(t, err)
	defer func() { _ = official.CloseNow() }()
	require.NoError(t, official.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"TEST_ONLY_MODEL","input":"TEST_ONLY_INPUT"}`)))
	_, _, err = official.Read(ctx)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, "本地代理身份信息不完整，请重新运行 aitp init 并重启代理及 Codex。", closeErr.Reason)
	require.Zero(t, slotCalls.Load())
}

func TestCodexReinitMessageFollowsBrand(t *testing.T) {
	const (
		generic = "codex_cli_rs/0.170.0 (Ubuntu 24.4.0; x86_64) xterm-256color"
		old     = "codex_cli_rs/0.150.0 (Ubuntu 24.4.0; x86_64) xterm-256color"
		desktop = "Codex Desktop/0.170.0 (Windows 10.0.26200; x86_64)"
	)
	for _, brand := range []service.ClientBrand{{}, handlerTestBrand(t, "", " ")} {
		require.Equal(t, baselineCodexReinitGeneric, codexTimezoneGuardMessageFor(brand, generic))
		require.Equal(t, baselineCodexReinitOld, codexTimezoneGuardMessageFor(brand, old))
		require.Equal(t, baselineCodexDesktop, codexTimezoneGuardMessageFor(brand, desktop))
	}
	for _, tc := range []struct {
		brand, command, generic, old string
	}{
		{"CCYou", "",
			"请求内容或历史中包含中国地区时区信息。请重新运行客户端初始化（会关闭 Codex 的时区上报），重启 Codex 并新开会话后再使用。",
			"当前 Codex 版本过旧，会上报本机时区。请将 Codex 升级到 0.163 或以上，重新运行客户端初始化，重启 Codex 并新开会话后再使用。"},
		{"", "aitp init",
			"请求内容或历史中包含中国地区时区信息。请重新运行 aitp init（会关闭 Codex 的时区上报），重启 Codex 并新开会话后再使用。",
			"当前 Codex 版本过旧，会上报本机时区。请将 Codex 升级到 0.163 或以上，重新运行 aitp init，重启 Codex 并新开会话后再使用。"},
		{"CCYou", "aitp init",
			"请求内容或历史中包含中国地区时区信息。请重新运行 aitp init（会关闭 Codex 的时区上报），重启 Codex 并新开会话后再使用。",
			"当前 Codex 版本过旧，会上报本机时区。请将 Codex 升级到 0.163 或以上，重新运行 aitp init，重启 Codex 并新开会话后再使用。"},
	} {
		brand := handlerTestBrand(t, tc.brand, tc.command)
		require.Equal(t, tc.generic, codexTimezoneGuardMessageFor(brand, generic))
		require.Equal(t, tc.old, codexTimezoneGuardMessageFor(brand, old))
		require.Equal(t, baselineCodexDesktop, codexTimezoneGuardMessageFor(brand, desktop))
		requireHandlerNoSAIAI(t, codexTimezoneGuardMessageFor(brand, generic))
		requireHandlerNoSAIAI(t, codexTimezoneGuardMessageFor(brand, old))

		// WS error event: only the message changes.
		frame := codexTimezoneGuardRejection(codexTimezoneGuardMessageFor(brand, generic)).frame()
		require.Equal(t, "error", gjson.GetBytes(frame, "type").String())
		require.Equal(t, int64(http.StatusBadRequest), gjson.GetBytes(frame, "status").Int())
		require.Equal(t, "invalid_request_error", gjson.GetBytes(frame, "error.type").String())
		require.Equal(t, "codex_timezone_reinit_required", gjson.GetBytes(frame, "error.code").String())
		require.Equal(t, tc.generic, gjson.GetBytes(frame, "error.message").String())
	}
}

func TestOpenAIWriteErrorScrubsWithRequestBrand(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	for _, tc := range []struct {
		brand service.ClientBrand
		want  string
	}{
		{service.ClientBrand{}, baselineHandlerDeviceUnavailable},
		{handlerTestBrand(t, "CCYou", ""), "CCYou 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"},
		{handlerTestBrand(t, "", "aitp init"), "服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"},
	} {
		envelope := gatewayErrorEnvelope{Status: http.StatusBadGateway, Type: "upstream_error", Code: "upstream_failed", Message: "ReClaude device revoked"}
		c, recorder := brandedTestContext(t, http.MethodPost, "/v1/responses", tc.brand)
		h.writeError(c, envelope, false)
		require.Equal(t, http.StatusBadGateway, recorder.Code)
		require.Equal(t, "upstream_error", gjson.Get(recorder.Body.String(), "error.type").String())
		require.Equal(t, "upstream_failed", gjson.Get(recorder.Body.String(), "error.code").String())
		require.Equal(t, tc.want, gjson.Get(recorder.Body.String(), "error.message").String())

		c, recorder = brandedTestContext(t, http.MethodPost, "/v1/responses", tc.brand)
		h.writeError(c, envelope, true)
		body := recorder.Body.String()
		require.True(t, strings.HasPrefix(body, "event: error\ndata: "), body)
		data := strings.TrimSpace(strings.TrimPrefix(body, "event: error\ndata: "))
		require.True(t, json.Valid([]byte(data)), data)
		require.Equal(t, "upstream_failed", gjson.Get(data, "error.code").String())
		require.Equal(t, tc.want, gjson.Get(data, "error.message").String())
		require.NotContains(t, strings.ToLower(body), "reclaude")
	}
}

func TestOpenAIWSBillingRejectionScrubsWithRequestBrand(t *testing.T) {
	brand := handlerTestBrand(t, "CCYou", "aitp init")
	c, _ := brandedTestContext(t, http.MethodGet, "/v1/responses", brand)
	rejection := openAIWSBillingRejection(c, pkgerrors.Forbidden("TEST_ONLY", "reclaude quota failure"))
	require.Equal(t, "billing_error", rejection.code)
	require.Equal(t, "CCYou 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。", rejection.message)
	require.Equal(t, rejection.message, rejection.reason)
}

func TestAnthropicStreamingAwareErrorUsesRequestBrand(t *testing.T) {
	h := &GatewayHandler{}
	for _, tc := range []struct {
		brand service.ClientBrand
		want  string
	}{
		{service.ClientBrand{}, baselineHandlerDeviceUnavailable},
		{handlerTestBrand(t, "CCYou", "aitp init"), "CCYou 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"},
	} {
		// JSON response.
		c, recorder := brandedTestContext(t, http.MethodPost, "/v1/messages", tc.brand)
		h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "reclaude: client state unavailable", false)
		require.Equal(t, http.StatusBadGateway, recorder.Code)
		require.Equal(t, "error", gjson.Get(recorder.Body.String(), "type").String())
		require.Equal(t, "upstream_error", gjson.Get(recorder.Body.String(), "error.type").String())
		require.Equal(t, tc.want, gjson.Get(recorder.Body.String(), "error.message").String())

		// SSE error event after the stream started.
		c, recorder = brandedTestContext(t, http.MethodPost, "/v1/messages", tc.brand)
		h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "reclaude: client state unavailable", true)
		body := recorder.Body.String()
		require.True(t, strings.HasPrefix(body, "data: "), body)
		data := strings.TrimSpace(strings.TrimPrefix(body, "data: "))
		require.True(t, json.Valid([]byte(data)), data)
		require.Equal(t, "upstream_error", gjson.Get(data, "error.type").String())
		require.Equal(t, tc.want, gjson.Get(data, "error.message").String())

		// Device authorization failover exhausted.
		c, recorder = brandedTestContext(t, http.MethodPost, "/v1/messages", tc.brand)
		h.handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:   http.StatusBadRequest,
			ResponseBody: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"device has been unbound, login again"}}`),
			Kind:         service.UpstreamFailureDeviceAuthorizationRevoked,
		}, service.PlatformAnthropic, false)
		require.Equal(t, http.StatusBadGateway, recorder.Code)
		require.Equal(t, tc.want, gjson.Get(recorder.Body.String(), "error.message").String())
	}
}

func TestBrandedSSEErrorStaysValidJSONForUnicodeBrand(t *testing.T) {
	brand := handlerTestBrand(t, "品牌😀\"'<>&", "")
	c, recorder := brandedTestContext(t, http.MethodPost, "/v1/messages", brand)
	(&GatewayHandler{}).handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "reclaude", true)
	data := strings.TrimSpace(strings.TrimPrefix(recorder.Body.String(), "data: "))
	require.True(t, json.Valid([]byte(data)), data)
	require.Equal(t, brand.DeviceAuthorizationUnavailableMessage(), gjson.Get(data, "error.message").String())
}

// With no brand settings the wire bytes are exactly what they were before.
func TestUnbrandedErrorBodiesAreByteIdentical(t *testing.T) {
	h := &GatewayHandler{}
	c, recorder := brandedTestContext(t, http.MethodPost, "/v1/messages", service.ClientBrand{})
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "reclaude: client state unavailable", false)
	require.Equal(t, `{"error":{"message":"SAIAI 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。","type":"upstream_error"},"type":"error"}`, recorder.Body.String())

	c, recorder = brandedTestContext(t, http.MethodPost, "/v1/messages", service.ClientBrand{})
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "reclaude: client state unavailable", true)
	require.Equal(t, `data: {"type":"error","error":{"type":"upstream_error","message":"SAIAI 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"}}`+"\n\n", recorder.Body.String())

	frame := codexTimezoneGuardRejection(codexTimezoneGuardMessageFor(service.ClientBrand{}, "codex_cli_rs/0.170.0 (Ubuntu 24.4.0; x86_64) xterm-256color")).frame()
	require.Equal(t, `{"error":{"code":"codex_timezone_reinit_required","message":"`+baselineCodexReinitGeneric+`","type":"invalid_request_error"},"status":400,"type":"error"}`, string(frame))
}

// passthroughRuleRepoStub feeds rules to a real ErrorPassthroughService.
type passthroughRuleRepoStub struct {
	service.ErrorPassthroughRepository
	rules []*model.ErrorPassthroughRule
}

func (r *passthroughRuleRepoStub) List(context.Context) ([]*model.ErrorPassthroughRule, error) {
	return r.rules, nil
}

// Failover exhausted after the stream started, through the real passthrough
// rule path (handleFailoverExhausted → rule → SSE error event). Admin-authored
// CustomMessage text is out of scope and is passed through unchanged.
func TestErrorPassthroughRuleFailoverSSEUsesRequestBrand(t *testing.T) {
	const adminText = "SAIAI 维护中，请稍后再试"
	branded := handlerTestBrand(t, "CCYou", "aitp init")
	sse := func(message string) string {
		return `data: {"type":"error","error":{"type":"upstream_error","message":"` + message + `"}}` + "\n\n"
	}
	for _, tc := range []struct {
		name            string
		brand           service.ClientBrand
		passthroughBody bool
		customMessage   string
		want            string
	}{
		{"restricted identity, empty settings", service.ClientBrand{}, true, "", sse(baselineHandlerDeviceUnavailable)},
		{"restricted identity, branded", branded, true, "", sse("CCYou 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。")},
		{"admin custom message, branded", branded, false, adminText, sse(adminText)},
		{"admin custom message, empty settings", service.ClientBrand{}, false, adminText, sse(adminText)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responseCode := http.StatusServiceUnavailable
			rule := &model.ErrorPassthroughRule{
				ID: 1, Name: "TEST_ONLY_RULE", Enabled: true, Priority: 1,
				ErrorCodes: []int{http.StatusBadRequest}, Keywords: []string{"upstream failure"}, MatchMode: model.MatchModeAll,
				Platforms: []string{service.PlatformAnthropic}, ResponseCode: &responseCode, PassthroughBody: tc.passthroughBody,
			}
			if tc.customMessage != "" {
				custom := tc.customMessage
				rule.CustomMessage = &custom
			}
			h := &GatewayHandler{errorPassthroughService: service.NewErrorPassthroughService(&passthroughRuleRepoStub{rules: []*model.ErrorPassthroughRule{rule}}, nil)}
			c, recorder := brandedTestContext(t, http.MethodPost, "/v1/messages", tc.brand)
			h.handleFailoverExhausted(c, &service.UpstreamFailoverError{
				StatusCode:   http.StatusBadRequest,
				ResponseBody: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"ReClaude upstream failure"}}`),
			}, service.PlatformAnthropic, true)
			require.Equal(t, tc.want, recorder.Body.String())
		})
	}
}
