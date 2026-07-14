package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeClaudeOAuthSingleDeviceSlotKey(t *testing.T) {
	require.Equal(
		t,
		"claude-cli (external, sdk-cli)",
		NormalizeClaudeOAuthSingleDeviceSlotKey("claude-cli/2.1.100 (external, sdk-cli)"),
	)
	require.Equal(
		t,
		"claude-cli (external, claude-vscode, agent-sdk)",
		NormalizeClaudeOAuthSingleDeviceSlotKey("claude-cli/2.1.101 (external, claude-vscode, agent-sdk/0.2.101)"),
	)
}

func TestParseClaudeOAuthFixedHeadersText_AllowsArbitraryHeaders(t *testing.T) {
	headers, err := ParseClaudeOAuthFixedHeadersText("X-Stainless-Arch: x64\nx-app: cli\nUser-Agent: claude-cli/2.1.109 (external, cli)")
	require.NoError(t, err)
	require.Equal(t, "x64", headers["X-Stainless-Arch"])
	require.Equal(t, "cli", headers["X-App"])
	require.Equal(t, "claude-cli/2.1.109 (external, cli)", headers["User-Agent"])
}

func TestPrepareOAuthRequestIdentity_SingleDeviceRewritesFixedIdentityAndOverridesHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalSessionID := "00000000-0000-4000-8000-000000000004"
	originalUserID := FormatMetadataUserID(
		strings.Repeat("a", 64),
		"00000000-0000-4000-8000-000000000005",
		originalSessionID,
		"2.1.101",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	req.Header.Set("User-Agent", "claude-cli/2.1.101 (external, sdk-cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,interleaved-thinking-2025-05-14,claude-code-20250219")
	req.Header.Set("X-Stainless-Arch", "arm64")
	req.Header.Set("X-Stainless-Lang", "js")
	req.Header.Set("X-Client-Request-Id", "old-client-request-id")
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	req.Header.Set("X-Real-IP", "203.0.113.2")
	req.Header.Set("X-Request-ID", "local-request-id")
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                    "fixed-account-uuid",
			"claude_oauth_mode":               ClaudeOAuthModeSingleDevice,
			"claude_oauth_fixed_device_id":    "fixed-device-id",
			"claude_oauth_fixed_headers_text": "User-Agent: claude-cli/2.1.109 (external, cli)\nX-Stainless-Arch: x64\nX-Stainless-Lang: js",
		},
	}
	body := expectedOAuthBillingBodyForTest(t,
		[]byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":`+strconvQuote(originalUserID)+`},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.101.000; cc_entrypoint=sdk-cli; cch=00000;"}]}`),
		req.Header.Get("User-Agent"),
	)
	parsed := &ParsedRequest{Model: "claude-sonnet-4-6", MetadataUserID: originalUserID}

	rewrittenBody, oauthIdentity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, oauthIdentity)
	require.Equal(t, ClaudeOAuthModeSingleDevice, oauthIdentity.Mode)

	rewrittenUserID := ParseMetadataUserID(gjson.GetBytes(rewrittenBody, "metadata.user_id").String())
	require.NotNil(t, rewrittenUserID)
	require.Equal(t, "fixed-account-uuid", rewrittenUserID.AccountUUID)
	require.Equal(t, "fixed-device-id", rewrittenUserID.DeviceID)

	SetOpsRequestBodyCaptureOptions(c, OpsRequestBodyCaptureOptions{
		MaxBytes:        64 * 1024,
		CaptureSuccess:  true,
		CaptureUpstream: true,
	})
	upstreamReq, err := svc.buildUpstreamRequest(c.Request.Context(), c, account, rewrittenBody, "real-oauth-token", "oauth", "claude-sonnet-4-6", true, oauthIdentity)
	require.NoError(t, err)
	defer func() { _ = upstreamReq.Body.Close() }()

	gotBody, err := io.ReadAll(upstreamReq.Body)
	require.NoError(t, err)
	finalUserID := ParseMetadataUserID(gjson.GetBytes(gotBody, "metadata.user_id").String())
	require.NotNil(t, finalUserID)
	require.Equal(t, "fixed-device-id", finalUserID.DeviceID)
	require.Equal(t, finalUserID.SessionID, upstreamReq.Header.Get("x-claude-code-session-id"))
	require.Equal(t, "oauth-2025-04-20,interleaved-thinking-2025-05-14,claude-code-20250219", upstreamReq.Header.Get("anthropic-beta"))
	require.Equal(t, "claude-cli/2.1.109 (external, cli)", upstreamReq.Header.Get("user-agent"))
	require.Equal(t, "x64", upstreamReq.Header.Get("x-stainless-arch"))
	require.Equal(t, "js", upstreamReq.Header.Get("x-stainless-lang"))
	freshClientRequestID := upstreamReq.Header.Get("x-client-request-id")
	require.NotEqual(t, "old-client-request-id", freshClientRequestID)
	require.NoError(t, uuid.Validate(freshClientRequestID))
	require.Empty(t, upstreamReq.Header.Get("x-forwarded-for"))
	require.Empty(t, upstreamReq.Header.Get("x-real-ip"))
	require.Empty(t, upstreamReq.Header.Get("x-request-id"))

	snapshot, ok := GetOpsUpstreamForwardRequestSnapshot(c)
	require.True(t, ok)
	require.Equal(t, "POST", snapshot.Method)
	require.Contains(t, snapshot.URL, "/v1/messages?beta=true")
	require.Contains(t, string(snapshot.Body), "fixed-device-id")
	requireOrderedHeaderValue(t, snapshot.Headers, "Authorization", "[REDACTED]")
	requireOrderedHeaderValue(t, snapshot.Headers, "X-Client-Request-Id", freshClientRequestID)
	requireNoOrderedHeader(t, snapshot.Headers, "X-Forwarded-For")
	requireNoOrderedHeader(t, snapshot.Headers, "X-Real-Ip")
	requireNoOrderedHeader(t, snapshot.Headers, "X-Request-Id")
}

func TestPrepareOAuthRequestIdentity_SingleDeviceReusesMatchedCCHProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalUserID := FormatMetadataUserID(
		strings.Repeat("c", 64),
		"client-account-uuid",
		"00000000-0000-4000-8000-000000000004",
		"2.1.185",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	req.Header.Set("User-Agent", "claude-cli/2.1.185 (external, sdk-cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,interleaved-thinking-2025-05-14,claude-code-20250219")
	req.Header.Set("X-Stainless-Arch", "x64")
	req.Header.Set("X-Stainless-Lang", "js")
	c.Request = req

	account := &Account{
		ID:       124,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                 "fixed-account-uuid",
			"claude_oauth_mode":            ClaudeOAuthModeSingleDevice,
			"claude_oauth_fixed_device_id": "fixed-device-id",
		},
	}
	baseBody := []byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":` + strconvQuote(originalUserID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.000; cc_entrypoint=sdk-cli; cch=00000;"}],"stream":true}`)
	body := expectedOAuthBillingBodyForProfileTest(t, baseBody, req.Header.Get("User-Agent"), claudebilling.CCHSeedV2, claudebilling.CCHInputModeFullBody)
	parsed := &ParsedRequest{Model: "claude-sonnet-4-6", MetadataUserID: originalUserID}

	rewrittenBody, oauthIdentity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, oauthIdentity)
	require.Equal(t, claudebilling.CCHSeedV2, oauthIdentity.BillingCCHSeed)
	require.Equal(t, claudebilling.CCHInputModeFullBody, oauthIdentity.BillingCCHMode)

	upstreamReq, err := svc.buildUpstreamRequest(c.Request.Context(), c, account, rewrittenBody, "real-oauth-token", "oauth", "claude-sonnet-4-6", true, oauthIdentity)
	require.NoError(t, err)
	defer func() { _ = upstreamReq.Body.Close() }()

	gotBody, err := io.ReadAll(upstreamReq.Body)
	require.NoError(t, err)
	normalized, match, err := claudebilling.NormalizeBodyForCCH(gotBody)
	require.NoError(t, err)
	_, expectedFullBodyCCH := claudebilling.ComputeCCHWithProfile(normalized, claudebilling.CCHSeedV2, claudebilling.CCHInputModeFullBody)
	_, defaultFilteredCCH := claudebilling.ComputeCCHWithProfile(normalized, claudebilling.CCHSeedV2, claudebilling.CCHInputModeFilteredBodyV2)
	require.Equal(t, expectedFullBodyCCH, match.Value)
	require.NotEqual(t, defaultFilteredCCH, match.Value)
}

func TestPrepareOAuthRequestIdentity_SetupTokenSingleDeviceUsesFixedIdentityWhenGateDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalSessionID := "00000000-0000-4000-8000-000000000004"
	originalUserID := FormatMetadataUserID(
		strings.Repeat("b", 64),
		"client-account-uuid",
		originalSessionID,
		"2.1.101",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, &Group{
		ID:                             22,
		Platform:                       PlatformAnthropic,
		Status:                         StatusActive,
		Hydrated:                       true,
		ClaudeOAuthRequestGateDisabled: true,
	}))
	req.Header.Set("User-Agent", "claude-cli/2.1.101 (external, sdk-cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,interleaved-thinking-2025-05-14,claude-code-20250219")
	req.Header.Set("X-Stainless-Arch", "arm64")
	req.Header.Set("X-Stainless-Lang", "js")
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
		Extra: map[string]any{
			"account_uuid":                    "fixed-account-uuid",
			"claude_oauth_mode":               ClaudeOAuthModeSingleDevice,
			"claude_oauth_fixed_device_id":    "fixed-device-id",
			"claude_oauth_fixed_headers_text": "X-Stainless-Arch: x64\nX-Stainless-Lang: js",
		},
	}
	body := expectedOAuthBillingBodyForTest(t,
		[]byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":`+strconvQuote(originalUserID)+`},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.101.000; cc_entrypoint=sdk-cli; cch=00000;"}]}`),
		req.Header.Get("User-Agent"),
	)
	parsed := &ParsedRequest{Model: "claude-sonnet-4-6", MetadataUserID: originalUserID}

	rewrittenBody, oauthIdentity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, oauthIdentity)
	require.Equal(t, ClaudeOAuthModeSingleDevice, oauthIdentity.Mode)

	rewrittenUserID := ParseMetadataUserID(gjson.GetBytes(rewrittenBody, "metadata.user_id").String())
	require.NotNil(t, rewrittenUserID)
	require.Equal(t, "fixed-account-uuid", rewrittenUserID.AccountUUID)
	require.Equal(t, "fixed-device-id", rewrittenUserID.DeviceID)
	require.NotEqual(t, "client-account-uuid", rewrittenUserID.AccountUUID)
	require.NotEqual(t, originalSessionID, rewrittenUserID.SessionID)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_SetupTokenSingleDeviceRequiresFixedAccountUUID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}

	originalUserID := FormatMetadataUserID(
		strings.Repeat("b", 64),
		"client-account-uuid",
		"00000000-0000-4000-8000-000000000004",
		"2.1.101",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	req.Header.Set("User-Agent", "claude-cli/2.1.101 (external, sdk-cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,interleaved-thinking-2025-05-14,claude-code-20250219")
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
		Extra: map[string]any{
			"claude_oauth_mode":            ClaudeOAuthModeSingleDevice,
			"claude_oauth_fixed_device_id": "fixed-device-id",
		},
	}
	body := expectedOAuthBillingBodyForTest(t,
		[]byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":`+strconvQuote(originalUserID)+`},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.101.000; cc_entrypoint=sdk-cli; cch=00000;"}]}`),
		req.Header.Get("User-Agent"),
	)
	parsed := &ParsedRequest{Model: "claude-sonnet-4-6", MetadataUserID: originalUserID}

	_, oauthIdentity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.ErrorIs(t, err, ErrClaudeOAuthAccountUUIDRequired)
	require.Nil(t, oauthIdentity)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Contains(t, clientErr.Message, "single_device mode requires account.extra.account_uuid")
	require.Empty(t, rec.Body.String())
}

func expectedOAuthBillingBodyForProfileTest(t *testing.T, body []byte, userAgent string, seed uint64, mode claudebilling.CCHInputMode) []byte {
	t.Helper()

	version := ExtractCLIVersion(userAgent)
	prompt, err := claudebilling.ExtractFirstUserText(body)
	require.NoError(t, err)
	suffix := claudebilling.ComputeCCVersionSuffix(prompt, version)
	bodyWithVersion, err := claudebilling.ReplaceCCVersion(body, version, suffix)
	require.NoError(t, err)
	normalized, match, err := claudebilling.NormalizeBodyForCCH(bodyWithVersion)
	require.NoError(t, err)
	_, cch := claudebilling.ComputeCCHWithProfile(normalized, seed, mode)
	return claudebilling.ReplaceCCH(normalized, match, cch)
}

func requireOrderedHeaderValue(t *testing.T, headers []OpsCapturedHeaderLine, name string, value string) {
	t.Helper()
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			require.Equal(t, value, header.Value)
			require.Greater(t, header.Index, 0)
			return
		}
	}
	require.Failf(t, "missing header", "header %q not found in %#v", name, headers)
}

func requireNoOrderedHeader(t *testing.T, headers []OpsCapturedHeaderLine, name string) {
	t.Helper()
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			require.Failf(t, "unexpected header", "header %q found in %#v", name, headers)
		}
	}
}

func TestApplyRequiredClaudeOAuthHeaders_ClientRequestIDConditional(t *testing.T) {
	withInbound := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	withInbound.Header.Set("x-client-request-id", "old-client-request-id")
	applyRequiredClaudeOAuthHeaders(withInbound, true)
	generated := withInbound.Header.Get("x-client-request-id")
	require.NotEqual(t, "old-client-request-id", generated)
	require.NoError(t, uuid.Validate(generated))

	withoutInbound := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	withoutInbound.Header.Set("x-client-request-id", "fixed-or-stale-value")
	applyRequiredClaudeOAuthHeaders(withoutInbound, false)
	require.Empty(t, withoutInbound.Header.Get("x-client-request-id"))
}

func TestIdentityService_GetOrCreateSingleDeviceSlotFingerprint_FixedUserAgentSkipsDynamicUpdate(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                    "fixed-account-uuid",
			"claude_oauth_mode":               ClaudeOAuthModeSingleDevice,
			"claude_oauth_fixed_device_id":    "fixed-device-id",
			"claude_oauth_fixed_headers_text": "User-Agent: claude-cli/2.1.109 (external, cli)\nX-Stainless-Lang: js",
		},
	}

	fixedHeaders, err := ParseClaudeOAuthFixedHeadersText(account.GetClaudeOAuthFixedHeadersText())
	require.NoError(t, err)

	firstHeaders := CloneHeadersWithOverrides(http.Header{
		"User-Agent":       []string{"claude-cli/2.1.101 (external, cli)"},
		"X-Stainless-Lang": []string{"js"},
	}, fixedHeaders)
	_, _, fp1, err := svc.GetOrCreateSingleDeviceSlotFingerprint(context.Background(), account, firstHeaders, true)
	require.NoError(t, err)
	require.NotNil(t, fp1)
	require.Equal(t, "claude-cli/2.1.109 (external, cli)", fp1.UserAgent)

	secondHeaders := CloneHeadersWithOverrides(http.Header{
		"User-Agent":       []string{"claude-cli/2.1.200 (external, cli)"},
		"X-Stainless-Lang": []string{"js"},
	}, fixedHeaders)
	_, _, fp2, err := svc.GetOrCreateSingleDeviceSlotFingerprint(context.Background(), account, secondHeaders, true)
	require.NoError(t, err)
	require.NotNil(t, fp2)
	require.Equal(t, "claude-cli/2.1.109 (external, cli)", fp2.UserAgent)
}
