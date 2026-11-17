//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Independent baselines copied from the texts before client brand settings
// existed. They are deliberately not references to the constants under test.
const (
	baselineClaudeCodeRequired = "Please use the official Claude Code client. If it is already installed, run SAIAI CLI initialization and try again. Contact your administrator if the issue persists."
	baselineDeviceUnavailable  = "SAIAI 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"
	baselineCodexCapacity      = "SAIAI upstream capacity is temporarily unavailable. Please retry later or contact your administrator. This is not your SAIAI balance or quota."
	baselineRechargeSubject    = "SAIAI Balance Recharge"
	baselineTOTPIssuer         = "SAiAi"
)

func testClientBrand(t *testing.T, name, command string) ClientBrand {
	t.Helper()
	brand, err := NewClientBrand(name, command)
	require.NoError(t, err)
	return brand
}

func requireNoSAIAI(t *testing.T, text string) {
	t.Helper()
	require.NotContains(t, strings.ToLower(text), "saiai", text)
}

type clientBrandTexts struct {
	claudeCode, device, capacity, subject, issuer string
}

func clientBrandTextsOf(brand ClientBrand) clientBrandTexts {
	return clientBrandTexts{
		claudeCode: brand.ClaudeCodeRequiredMessage(),
		device:     brand.DeviceAuthorizationUnavailableMessage(),
		capacity:   brand.CodexUpstreamCapacityMessage(),
		subject:    brand.BalanceRechargeSubject(),
		issuer:     brand.TOTPIssuer(),
	}
}

var baselineClientBrandTexts = clientBrandTexts{
	claudeCode: baselineClaudeCodeRequired,
	device:     baselineDeviceUnavailable,
	capacity:   baselineCodexCapacity,
	subject:    baselineRechargeSubject,
	issuer:     baselineTOTPIssuer,
}

func TestClientBrandUnsetKeepsOriginalTextsByteForByte(t *testing.T) {
	require.Equal(t, baselineClientBrandTexts, clientBrandTextsOf(ClientBrand{}), "missing settings")
	for _, values := range [][2]string{{"", ""}, {"   ", "\t "}, {"  ", ""}} {
		brand := testClientBrand(t, values[0], values[1])
		require.False(t, brand.Customized())
		require.Equal(t, baselineClientBrandTexts, clientBrandTextsOf(brand), "%q", values)
	}
}

func TestClientBrandCustomizedTexts(t *testing.T) {
	for _, tc := range []struct {
		name, brand, command string
		want                 clientBrandTexts
	}{
		{
			name: "brand only", brand: "CCYou",
			want: clientBrandTexts{
				claudeCode: "Please use the official Claude Code client. If it is already installed, re-run your client setup and try again. Contact your administrator if the issue persists.",
				device:     "CCYou 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。",
				capacity:   "CCYou upstream capacity is temporarily unavailable. Please retry later or contact your administrator. This is not your CCYou balance or quota.",
				subject:    "CCYou Balance Recharge",
				issuer:     "CCYou",
			},
		},
		{
			name: "command only", command: "aitp init",
			want: clientBrandTexts{
				claudeCode: "Please use the official Claude Code client. If it is already installed, run `aitp init` and try again. Contact your administrator if the issue persists.",
				device:     "服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。",
				capacity:   "Upstream capacity is temporarily unavailable. Please retry later or contact your administrator. This is not your account balance or quota.",
				subject:    "Balance Recharge",
				issuer:     "2FA",
			},
		},
		{
			name: "both", brand: " CCYou ", command: " aitp init ",
			want: clientBrandTexts{
				claudeCode: "Please use the official Claude Code client. If it is already installed, run `aitp init` and try again. Contact your administrator if the issue persists.",
				device:     "CCYou 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。",
				capacity:   "CCYou upstream capacity is temporarily unavailable. Please retry later or contact your administrator. This is not your CCYou balance or quota.",
				subject:    "CCYou Balance Recharge",
				issuer:     "CCYou",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brand := testClientBrand(t, tc.brand, tc.command)
			require.True(t, brand.Customized())
			got := clientBrandTextsOf(brand)
			require.Equal(t, tc.want, got)
			for _, text := range []string{got.claudeCode, got.device, got.capacity, got.subject, got.issuer} {
				requireNoSAIAI(t, text)
			}
		})
	}
}

func TestNewClientBrandValidation(t *testing.T) {
	maxName := strings.Repeat("品", MaxClientBrandNameRunes)
	brand, err := NewClientBrand(" "+maxName+" ", strings.Repeat("x", MaxClientSetupCommandRunes))
	require.NoError(t, err)
	require.Equal(t, maxName, brand.Name())
	require.Len(t, brand.SetupCommand(), MaxClientSetupCommandRunes)

	for _, tc := range []struct{ name, command string }{
		{name: strings.Repeat("品", MaxClientBrandNameRunes+1)},
		{command: strings.Repeat("x", MaxClientSetupCommandRunes+1)},
		{name: "CC\nYou"},
		{name: "CC\tYou"},
		{name: "CC\x7fYou"},
		{name: "CC\u200bYou"}, // zero-width space
		{name: "CC\u00a0You"}, // no-break space
		{command: "aitp\rinit"},
		{name: "CC\xffYou"},
		{name: "ReClaude"},
		{command: "reclaude init"},
	} {
		_, err := NewClientBrand(tc.name, tc.command)
		require.Error(t, err, "%q / %q", tc.name, tc.command)
	}
}

func TestClientSafeUpstreamErrorMessageUsesBrand(t *testing.T) {
	restricted := "ReClaude 客户端状态异常，请重启"
	require.Equal(t, baselineDeviceUnavailable, ClientSafeUpstreamErrorMessage(ClientBrand{}, restricted))
	for _, brand := range []ClientBrand{
		testClientBrand(t, "CCYou", ""),
		testClientBrand(t, "", "aitp init"),
		testClientBrand(t, "CCYou", "aitp init"),
	} {
		got := ClientSafeUpstreamErrorMessage(brand, restricted)
		require.Equal(t, brand.DeviceAuthorizationUnavailableMessage(), got)
		require.NotContains(t, strings.ToLower(got), "reclaude", "custom brand still scrubs the upstream identity")
		requireNoSAIAI(t, got)
		require.Equal(t, "ordinary upstream error", ClientSafeUpstreamErrorMessage(brand, "ordinary upstream error"))
	}
}

func TestClientBrandContextHelpers(t *testing.T) {
	brand := testClientBrand(t, "CCYou", "aitp init")
	require.Equal(t, ClientBrand{}, ClientBrandFromContext(context.Background()))
	require.Equal(t, ClientBrand{}, ClientBrandFromContext(nil)) //nolint:staticcheck // nil context must be safe
	require.Equal(t, brand, ClientBrandFromContext(WithClientBrand(context.Background(), brand)))
	require.Equal(t, ClientBrand{}, ClientBrandFromGin(nil))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Equal(t, ClientBrand{}, ClientBrandFromGin(c), "no request")
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	require.Equal(t, ClientBrand{}, ClientBrandFromGin(c))
	c.Request = c.Request.WithContext(WithClientBrand(c.Request.Context(), brand))
	require.Equal(t, brand, ClientBrandFromGin(c))
}

// clientBrandRepoStub serves GetMultiple/SetMultiple from a map.
type clientBrandRepoStub struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
	err    error
	calls  atomic.Int32
}

func (s *clientBrandRepoStub) set(values map[string]string, err error) {
	s.mu.Lock()
	s.values, s.err = values, err
	s.mu.Unlock()
}

func (s *clientBrandRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (s *clientBrandRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]string{}
	}
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

func expireClientBrandCache(svc *SettingService) {
	next := *svc.clientBrandCache.Load()
	next.expiresAt = time.Now().Add(-time.Second).UnixNano()
	svc.clientBrandCache.Store(&next)
}

func waitClientBrandRefresh(t *testing.T, svc *SettingService) {
	t.Helper()
	require.Eventually(t, func() bool {
		cached := svc.clientBrandCache.Load()
		return !svc.clientBrandRefreshing.Load() && cached != nil && time.Now().UnixNano() < cached.expiresAt
	}, 5*time.Second, time.Millisecond)
}

func TestSettingServiceClientBrandReadsAndCaches(t *testing.T) {
	for _, values := range []map[string]string{
		{},
		{SettingKeyClientBrandName: "", SettingKeyClientSetupCommand: ""},
		{SettingKeyClientBrandName: "  ", SettingKeyClientSetupCommand: "\t"},
	} {
		svc := NewSettingService(&clientBrandRepoStub{values: values}, &config.Config{})
		require.Equal(t, ClientBrand{}, svc.ClientBrand(context.Background()), "%v", values)
	}

	repo := &clientBrandRepoStub{values: map[string]string{SettingKeyClientBrandName: " CCYou ", SettingKeyClientSetupCommand: "aitp init"}}
	svc := NewSettingService(repo, &config.Config{})
	want := testClientBrand(t, "CCYou", "aitp init")
	require.Equal(t, want, svc.ClientBrand(context.Background()))
	require.Equal(t, want, svc.ClientBrand(context.Background()))
	require.Equal(t, int32(1), repo.calls.Load(), "served from the cache")

	// A stored value that fails validation is ignored, never spliced into texts.
	repo = &clientBrandRepoStub{values: map[string]string{SettingKeyClientBrandName: "CC\nYou", SettingKeyClientSetupCommand: "aitp init"}}
	require.Equal(t, testClientBrand(t, "", "aitp init"), NewSettingService(repo, &config.Config{}).ClientBrand(context.Background()))
	require.Equal(t, ClientBrand{}, (*SettingService)(nil).ClientBrand(context.Background()))
}

func TestSettingServiceClientBrandColdStartFailureUsesOriginalTexts(t *testing.T) {
	repo := &clientBrandRepoStub{err: errors.New("TEST_ONLY_DB_DOWN")}
	svc := NewSettingService(repo, &config.Config{})
	brand := svc.ClientBrand(context.Background())
	require.False(t, brand.Customized())
	require.Equal(t, baselineClaudeCodeRequired, brand.ClaudeCodeRequiredMessage())

	// The placeholder is not a known value: the next success replaces it.
	repo.set(map[string]string{SettingKeyClientBrandName: "CCYou"}, nil)
	expireClientBrandCache(svc)
	_ = svc.ClientBrand(context.Background())
	waitClientBrandRefresh(t, svc)
	require.Equal(t, testClientBrand(t, "CCYou", ""), svc.ClientBrand(context.Background()))
}

func TestSettingServiceClientBrandRefreshFailureKeepsLastValue(t *testing.T) {
	repo := &clientBrandRepoStub{values: map[string]string{SettingKeyClientBrandName: "CCYou", SettingKeyClientSetupCommand: "aitp init"}}
	svc := NewSettingService(repo, &config.Config{})
	want := testClientBrand(t, "CCYou", "aitp init")
	require.Equal(t, want, svc.ClientBrand(context.Background()))

	repo.set(nil, errors.New("TEST_ONLY_DB_DOWN"))
	expireClientBrandCache(svc)
	require.Equal(t, want, svc.ClientBrand(context.Background()), "stale value served while refreshing")
	waitClientBrandRefresh(t, svc)
	require.Equal(t, int32(2), repo.calls.Load())
	require.Equal(t, want, svc.ClientBrand(context.Background()), "failed refresh keeps the last known value")
}

func TestSettingServiceClientBrandSaveRefreshesAndInstancesAreIsolated(t *testing.T) {
	repoA := &clientBrandRepoStub{values: map[string]string{}}
	repoB := &clientBrandRepoStub{values: map[string]string{SettingKeyClientBrandName: "OtherSite"}}
	siteA := NewSettingService(repoA, &config.Config{})
	siteB := NewSettingService(repoB, &config.Config{})
	require.Equal(t, ClientBrand{}, siteA.ClientBrand(context.Background()))
	require.Equal(t, testClientBrand(t, "OtherSite", ""), siteB.ClientBrand(context.Background()))

	require.NoError(t, siteA.UpdateSettings(context.Background(), &SystemSettings{ClientBrandName: " CCYou ", ClientSetupCommand: "aitp init"}))
	require.Equal(t, "CCYou", repoA.values[SettingKeyClientBrandName])
	require.Equal(t, "aitp init", repoA.values[SettingKeyClientSetupCommand])
	callsBefore := repoA.calls.Load()
	require.Equal(t, testClientBrand(t, "CCYou", "aitp init"), siteA.ClientBrand(context.Background()), "save publishes immediately")
	require.Equal(t, callsBefore, repoA.calls.Load())
	require.Equal(t, testClientBrand(t, "OtherSite", ""), siteB.ClientBrand(context.Background()), "the other site is unaffected")

	// Clearing both restores the original texts.
	require.NoError(t, siteA.UpdateSettings(context.Background(), &SystemSettings{}))
	require.Equal(t, "", repoA.values[SettingKeyClientBrandName])
	require.Equal(t, ClientBrand{}, siteA.ClientBrand(context.Background()))
}

func TestSettingServiceClientBrandSaveWinsOverInFlightLoad(t *testing.T) {
	repo := &clientBrandRepoStub{values: map[string]string{SettingKeyClientBrandName: "Old"}}
	svc := NewSettingService(repo, &config.Config{})
	require.Equal(t, "Old", svc.ClientBrand(context.Background()).Name())
	expireClientBrandCache(svc)
	previous := svc.clientBrandCache.Load()
	svc.storeClientBrand("New", "")
	// A load that started from the expired value cannot overwrite the save.
	require.False(t, svc.clientBrandCache.CompareAndSwap(previous, &cachedClientBrand{}))
	require.Equal(t, "New", svc.loadClientBrand(context.Background(), time.Second).Name())
}

// --- Exit paths -----------------------------------------------------------

func withClientBrandRequest(c *gin.Context, brand ClientBrand) {
	c.Request = c.Request.WithContext(WithClientBrand(c.Request.Context(), brand))
}

func TestClaudeCodeRequiredMessageFollowsRequestBrand(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name  string
		brand ClientBrand
		want  string
	}{
		{"unbranded", ClientBrand{}, baselineClaudeCodeRequired},
		{"branded", testClientBrand(t, "CCYou", "aitp init"), "Please use the official Claude Code client. If it is already installed, run `aitp init` and try again. Contact your administrator if the issue persists."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Carpool group gate before scheduling (ValidateClaudeOAuthRequestShapeForGroup).
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("User-Agent", "claude-cli/2.1.146 (external, cli)")
			c.Request.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
			withClientBrandRequest(c, tc.brand)
			groupID := int64(20)
			repo := &accountRepoStubForOAuthMode{listByGroupFunc: func(context.Context, int64) ([]Account, error) {
				return []Account{{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}}, nil
			}}
			svc := &GatewayService{accountRepo: repo}
			body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}],"max_tokens":64}`)
			parsed := &ParsedRequest{Model: "claude-opus-4-7", MaxTokens: 64, Body: body}
			err := svc.ValidateClaudeOAuthRequestShapeForGroup(c.Request.Context(), c, &groupID, PlatformAnthropic, parsed, body, false)
			var clientErr *ClientRequestError
			require.ErrorAs(t, err, &clientErr)
			require.ErrorIs(t, err, ErrClaudeCodeOnly)
			require.Equal(t, http.StatusBadRequest, clientErr.StatusCode)
			require.Equal(t, "invalid_request_error", clientErr.ErrorType)
			require.Equal(t, tc.want, clientErr.Message)

			// Carpool billing integrity on an OAuth account (account failover body).
			c2, _ := gin.CreateTestContext(httptest.NewRecorder())
			c2.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c2.Request.Header.Set("User-Agent", "claude-cli/2.1.185 (external, sdk-cli)")
			c2.Request.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
			withClientBrandRequest(c2, tc.brand)
			userID := FormatMetadataUserID(strings.Repeat("1", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.185")
			missingCCH := []byte(`{"model":"claude-opus-4-8","metadata":{"user_id":` + strconvQuote(userID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.ecf; cc_entrypoint=sdk-cli;"}]}`)
			_, _, err = svc.validateCarpoolBillingIntegrityWithResult(c2, missingCCH, c2.Request.Header, &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth}, false)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
			require.Equal(t, "error", gjson.GetBytes(failoverErr.ResponseBody, "type").String())
			require.Equal(t, "invalid_request_error", gjson.GetBytes(failoverErr.ResponseBody, "error.type").String())
			require.Equal(t, tc.want, gjson.GetBytes(failoverErr.ResponseBody, "error.message").String())

			// Request identity preparation (non Claude Code request on an OAuth account).
			c3, _ := gin.CreateTestContext(httptest.NewRecorder())
			c3.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c3.Request.Header.Set("User-Agent", "claude-cli/2.1.139 (external, cli)")
			withClientBrandRequest(c3, tc.brand)
			identitySvc := &GatewayService{identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32))}
			parsed3 := &ParsedRequest{Model: "claude-sonnet-4-6", MetadataUserID: FormatMetadataUserID(strings.Repeat("a", 64), "original-account-uuid", "11111111-2222-4333-8444-555555555555", "2.1.139")}
			_, _, err = identitySvc.prepareOAuthRequestIdentity(c3.Request.Context(), c3, &Account{ID: 123, Platform: PlatformAnthropic, Type: AccountTypeOAuth}, parsed3, []byte(`{"model":"claude-sonnet-4-6"}`), false)
			require.ErrorAs(t, err, &clientErr)
			require.Equal(t, tc.want, clientErr.Message)

			// Pinned mode: a request without login state.
			c4, _ := gin.CreateTestContext(httptest.NewRecorder())
			pinnedReq := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			pinnedCtx := WithClaudeOAuthGroupMode(SetClaudeCodeClient(WithClientBrand(pinnedReq.Context(), tc.brand), true), 22, ClaudeOAuthModePinned)
			pinnedReq = pinnedReq.WithContext(pinnedCtx)
			pinnedReq.Header.Set("User-Agent", "claude-cli/2.1.101 (external, sdk-cli)")
			pinnedReq.Header.Set("anthropic-beta", "oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,claude-code-20250219")
			c4.Request = pinnedReq
			pinnedUserID := FormatMetadataUserID(strings.Repeat("f", 64), "", "00000000-0000-4000-8000-000000000006", "2.1.101")
			pinnedAccount := &Account{ID: 123, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{
				"account_uuid": "forced-account-uuid", "claude_oauth_mode": ClaudeOAuthModePinned,
			}}
			pinnedBody := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", pinnedUserID, pinnedReq.Header.Get("User-Agent"), "hello")
			_, _, err = identitySvc.prepareOAuthRequestIdentity(pinnedCtx, c4, pinnedAccount, &ParsedRequest{Model: "claude-sonnet-4-6", MetadataUserID: pinnedUserID}, pinnedBody, false)
			require.ErrorIs(t, err, ErrClaudeOAuthLoginStateRequired)
			require.ErrorAs(t, err, &clientErr)
			require.Equal(t, tc.want, clientErr.Message)
		})
	}
}

func TestCodexCapacityErrorUsesBrandOnEveryBranch(t *testing.T) {
	brand := testClientBrand(t, "CCYou", "aitp init")
	for _, tc := range []struct{ payload, path string }{
		{`{"error":{"type":"usage_limit_reached","plan_type":"plus"}}`, "error"},
		{`{"type":"error","sequence_number":9,"error":{"type":"usage_limit_reached"}}`, "error"},
		{`{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"usage_limit_reached"},"usage":{"output_tokens":2}}}`, "response.error"},
		{`{"type":"response.done","response":{"id":"resp_2","status":"failed","error":{"type":"insufficient_quota"}}}`, "response.error"},
	} {
		original := normalizeOpenAICodexCapacityError(ClientBrand{}, auditOAuthPolicyAccount(), []byte(tc.payload))
		branded := normalizeOpenAICodexCapacityError(brand, auditOAuthPolicyAccount(), []byte(tc.payload))
		require.Equal(t, baselineCodexCapacity, gjson.GetBytes(original, tc.path+".message").String())
		require.Equal(t, brand.CodexUpstreamCapacityMessage(), gjson.GetBytes(branded, tc.path+".message").String())
		require.Equal(t, "saiai_upstream_capacity_unavailable", gjson.GetBytes(branded, tc.path+".code").String(), "code unchanged")
		requireNoSAIAI(t, gjson.GetBytes(branded, tc.path+".message").String())
		// Everything but the message is identical to the unbranded output.
		strippedOriginal, err := sjson.DeleteBytes(original, tc.path+".message")
		require.NoError(t, err)
		strippedBranded, err := sjson.DeleteBytes(branded, tc.path+".message")
		require.NoError(t, err)
		require.Equal(t, string(strippedOriginal), string(strippedBranded))
	}
}

func TestCodexCapacitySSEUsesRequestBrand(t *testing.T) {
	brand := testClientBrand(t, "CCYou", "")
	for _, payload := range []string{
		`{"type":"response.failed","response":{"id":"resp_capacity","status":"failed","error":{"type":"usage_limit_reached","plan_type":"plus"}}}`,
		`{"type":"response.done","response":{"id":"resp_capacity","status":"failed","error":{"type":"insufficient_quota"}}}`,
	} {
		for _, branded := range []bool{false, true} {
			ctx, recorder := auditOAuthPolicyContext("/v1/responses")
			if branded {
				withClientBrandRequest(ctx, brand)
			}
			response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}
			// The terminal-event error result is not under test; the wire text is.
			_, _ = (&OpenAIGatewayService{}).handleStreamingResponse(context.Background(), response, ctx, auditOAuthPolicyAccount(), time.Now(), "model", "model")
			body := recorder.Body.String()
			require.Contains(t, body, "saiai_upstream_capacity_unavailable")
			if branded {
				require.Contains(t, body, brand.CodexUpstreamCapacityMessage())
				require.NotContains(t, body, baselineCodexCapacity)
			} else {
				require.Contains(t, body, baselineCodexCapacity)
			}
		}
	}
}

func TestCodexCapacityHTTPUsesRequestBrand(t *testing.T) {
	brand := testClientBrand(t, "", "aitp init")
	payload := `{"error":{"type":"usage_limit_reached","plan_type":"plus","resets_at":123}}`
	ctx, recorder := auditOAuthPolicyContext("/v1/responses")
	withClientBrandRequest(ctx, brand)
	_, err := (&OpenAIGatewayService{}).handleErrorResponse(context.Background(), &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, ctx, auditOAuthPolicyAccount(), nil)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, "server_error", gjson.Get(recorder.Body.String(), "error.type").String())
	require.Equal(t, "saiai_upstream_capacity_unavailable", gjson.Get(recorder.Body.String(), "error.code").String())
	require.Equal(t, brand.CodexUpstreamCapacityMessage(), gjson.Get(recorder.Body.String(), "error.message").String())
}

func TestCodexCapacityWSPassthroughUsesSessionBrand(t *testing.T) {
	brand := testClientBrand(t, "CCYou", "aitp init")
	payload := []byte(`{"type":"error","error":{"type":"usage_limit_reached","plan_type":"plus","resets_at":123}}`)
	conn := &openAIWSCaptureConn{events: [][]byte{payload}, eventTypes: []coderws.MessageType{coderws.MessageText}}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModePassthrough
	account := auditOAuthPolicyAccount()
	account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
	svc := &OpenAIGatewayService{cfg: cfg, openaiWSPassthroughDialer: &openAIWSCaptureDialer{conn: conn}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg)}
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		client, err := coderws.Accept(writer, request, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = client.CloseNow() }()
		ginCtx, _ := auditOAuthPolicyContext("/v1/responses")
		withClientBrandRequest(ginCtx, brand)
		kind, first, err := client.Read(request.Context())
		if err != nil {
			serverErr <- err
			return
		}
		serverErr <- svc.ProxyResponsesWebSocketFromClient(request.Context(), ginCtx, client, account, "MOCK_ONLY", kind, first, nil)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"fixture","input":[]}`)))
	_, received, err := client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "error", gjson.GetBytes(received, "type").String())
	require.Equal(t, "saiai_upstream_capacity_unavailable", gjson.GetBytes(received, "error.code").String())
	require.Equal(t, brand.CodexUpstreamCapacityMessage(), gjson.GetBytes(received, "error.message").String())
	require.NoError(t, <-serverErr)
}

func TestAnthropicRestrictedIdentity400UsesRequestBrand(t *testing.T) {
	brand := testClientBrand(t, "CCYou", "")
	for _, tc := range []struct {
		brand ClientBrand
		want  string
	}{{ClientBrand{}, baselineDeviceUnavailable}, {brand, "CCYou 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"}} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		withClientBrandRequest(c, tc.brand)
		body := `{"type":"error","error":{"type":"invalid_request_error","message":"reclaude says no"}}`
		resp := &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
		_, err := (&GatewayService{}).handleErrorResponse(context.Background(), resp, c, &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey})
		require.Error(t, err)
		require.Equal(t, http.StatusBadGateway, recorder.Code)
		require.Equal(t, "error", gjson.Get(recorder.Body.String(), "type").String())
		require.Equal(t, "upstream_error", gjson.Get(recorder.Body.String(), "error.type").String())
		require.Equal(t, tc.want, gjson.Get(recorder.Body.String(), "error.message").String())
		require.NotContains(t, strings.ToLower(recorder.Body.String()), "reclaude")
	}
}

func TestTOTPIssuerFollowsRequestBrand(t *testing.T) {
	for _, tc := range []struct {
		brand ClientBrand
		want  string
	}{
		{ClientBrand{}, baselineTOTPIssuer},
		{testClientBrand(t, "CCYou", "aitp init"), "CCYou"},
		{testClientBrand(t, "", "aitp init"), "2FA"},
	} {
		user := &User{ID: 5, Email: "user@example.test"}
		require.NoError(t, user.SetPassword("TEST_ONLY_PASSWORD"))
		settings := NewSettingService(&clientBrandRepoStub{values: map[string]string{}}, &config.Config{})
		settings.settingRepo = &totpBrandSettingRepo{SettingRepository: settings.settingRepo}
		cache := &totpBrandCache{}
		svc := NewTotpService(&totpBrandUserRepo{user: user}, nil, cache, settings, nil, nil)
		ctx := WithClientBrand(context.Background(), tc.brand)
		result, err := svc.InitiateSetup(ctx, user.ID, "", "TEST_ONLY_PASSWORD")
		require.NoError(t, err)
		parsed, err := url.Parse(result.QRCodeURL)
		require.NoError(t, err)
		require.Equal(t, tc.want, parsed.Query().Get("issuer"))
		require.Equal(t, "/"+tc.want+":user@example.test", parsed.Path)
	}
}

type totpBrandSettingRepo struct{ SettingRepository }

func (totpBrandSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if key == SettingKeyTotpEnabled {
		return "true", nil
	}
	return "false", nil
}

type totpBrandUserRepo struct {
	UserRepository
	user *User
}

func (r *totpBrandUserRepo) GetByID(context.Context, int64) (*User, error) { return r.user, nil }

type totpBrandCache struct{ TotpCache }

func (*totpBrandCache) SetSetupSession(context.Context, int64, *TotpSetupSession, time.Duration) error {
	return nil
}

type subjectCapturingPaymentProvider struct {
	*paymentTestProvider
	subjects []string
}

func (p *subjectCapturingPaymentProvider) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	p.subjects = append(p.subjects, req.Subject)
	return p.paymentTestProvider.CreatePayment(ctx, req)
}

func TestBalanceRechargeSubjectFollowsRequestBrand(t *testing.T) {
	svc, _, fakeProvider, _ := newPaymentServiceFixture(t)
	capturing := &subjectCapturingPaymentProvider{paymentTestProvider: fakeProvider}
	svc.providerFactory = func(string, string, map[string]string) (payment.Provider, error) { return capturing, nil }
	for _, brand := range []ClientBrand{{}, testClientBrand(t, "CCYou", ""), testClientBrand(t, "", "aitp init")} {
		_, err := svc.CreateOrder(WithClientBrand(context.Background(), brand), CreatePaymentOrderRequest{UserID: 42, Amount: 10, ProviderInstanceID: 1, PaymentType: payment.TypeAlipay})
		require.NoError(t, err)
	}
	require.Equal(t, []string{baselineRechargeSubject, "CCYou Balance Recharge", "Balance Recharge"}, capturing.subjects)
}
