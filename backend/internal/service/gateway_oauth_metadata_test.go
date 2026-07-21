package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func setRealClaudeCodeRequestHeaders(req *http.Request, userAgent string) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,claude-code-20250219")
}

func expectedClaudeCodeOAuthBodyForTest(t *testing.T, model, userID, userAgent, prompt string) []byte {
	t.Helper()

	version := ExtractCLIVersion(userAgent)
	entrypoint := extractClaudeCLIEntrypoint(userAgent)
	if entrypoint == "" {
		entrypoint = "cli"
	}
	body := []byte(`{"model":` + strconvQuote(model) + `,"metadata":{"user_id":` + strconvQuote(userID) + `},"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=` + version + `.000; cc_entrypoint=` + entrypoint + `; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":` + strconvQuote(prompt) + `}]}]}`)
	return expectedOAuthBillingBodyForTest(t, body, userAgent)
}

func TestPrepareOAuthRequestIdentity_RejectsMissingMetadataUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: "",
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, []byte(`{"model":"claude-sonnet-4-6"}`), false)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, http.StatusBadRequest, clientErr.StatusCode)
	require.Equal(t, "invalid_request_error", clientErr.ErrorType)
	require.Equal(t, claudeOAuthRealClaudeCodeRequiredMessage, clientErr.Message)
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_NonClaudeCodeRequestReturnsClientRequestError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.139 (external, cli)")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid": "forced-account-uuid",
		},
	}
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: FormatMetadataUserID(strings.Repeat("a", 64), "original-account-uuid", "11111111-2222-4333-8444-555555555555", "2.1.139"),
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, []byte(`{"model":"claude-sonnet-4-6"}`), false)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, http.StatusBadRequest, clientErr.StatusCode)
	require.Equal(t, "invalid_request_error", clientErr.ErrorType)
	require.Equal(t, claudeOAuthRealClaudeCodeRequiredMessage, clientErr.Message)
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_HaikuRejectsLegacyClaudeCodeFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.139 (external, cli)")
	c.Request = req

	svc := &GatewayService{}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid": "forced-account-uuid",
		},
	}
	parsed := &ParsedRequest{
		Model:          "claude-3-5-haiku-20241022",
		MetadataUserID: FormatMetadataUserID(strings.Repeat("a", 64), "original-account-uuid", "11111111-2222-4333-8444-555555555555", "2.1.139"),
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, []byte(`{"model":"claude-3-5-haiku-20241022"}`), false)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, claudeOAuthRealClaudeCodeRequiredMessage, clientErr.Message)
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_HaikuMaxTokensOneAuxRequestAllowedWithoutBillingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalUserID := FormatMetadataUserID(
		strings.Repeat("a", 64),
		"client-account-uuid",
		"11111111-2222-4333-8444-555555555555",
		"2.1.146",
	)
	body := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"metadata":{"user_id":` + strconvQuote(originalUserID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"count"}]}]}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.146 (external, cli)")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
	}
	parsed := &ParsedRequest{
		Body:           body,
		Model:          "claude-haiku-4-5-20251001",
		MaxTokens:      1,
		Stream:         false,
		MetadataUserID: originalUserID,
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, ClaudeOAuthModeCarpool, identity.Mode)
	require.True(t, identity.NativeBillingStyle)
	require.Empty(t, rec.Body.String())

	rewritten := ParseMetadataUserID(gjson.GetBytes(newBody, "metadata.user_id").String())
	require.NotNil(t, rewritten)
	require.NotEqual(t, strings.Repeat("a", 64), rewritten.DeviceID)
	require.NotEqual(t, "11111111-2222-4333-8444-555555555555", rewritten.SessionID)
	require.Empty(t, rewritten.AccountUUID)
}

func TestPrepareOAuthRequestIdentity_HaikuMaxTokensOneAuxRequestRejectsMissingMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.146 (external, cli)")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
	}
	body := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"count"}]}]}`)
	parsed := &ParsedRequest{
		Body:      body,
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 1,
		Stream:    false,
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, claudeOAuthRealClaudeCodeRequiredMessage, clientErr.Message)
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_CountTokensAuxRequestAllowedWithoutMetadataOrBillingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"tools":[]}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.143 (external, cli)")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,token-counting-2024-11-01")
	req.Header.Set("X-Claude-Code-Session-Id", "00000000-0000-4000-8000-000000000009")
	req.Header.Set("x-app", "cli")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
	}
	parsed := &ParsedRequest{
		Body:  body,
		Model: "claude-sonnet-4-6",
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, true)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, ClaudeOAuthModeCarpool, identity.Mode)
	require.Equal(t, int64(123), identity.TransportAccountID)
	require.Equal(t, body, newBody)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_CountTokensAuxRequestRequiresTokenCountingBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"tools":[]}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.143 (external, cli)")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	req.Header.Set("X-Claude-Code-Session-Id", "00000000-0000-4000-8000-000000000009")
	req.Header.Set("x-app", "cli")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
	}
	parsed := &ParsedRequest{
		Body:  body,
		Model: "claude-sonnet-4-6",
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, true)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, claudeOAuthRealClaudeCodeRequiredMessage, clientErr.Message)
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_SonnetMaxTokensOneStillRejectsWithoutBillingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalUserID := FormatMetadataUserID(
		strings.Repeat("b", 64),
		"client-account-uuid",
		"22222222-2222-4333-8444-555555555555",
		"2.1.146",
	)
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1,"metadata":{"user_id":` + strconvQuote(originalUserID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"count"}]}]}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.146 (external, cli)")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid": "forced-account-uuid",
		},
	}
	parsed := &ParsedRequest{
		Body:           body,
		Model:          "claude-sonnet-4-6",
		MaxTokens:      1,
		Stream:         false,
		MetadataUserID: originalUserID,
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, claudeOAuthRealClaudeCodeRequiredMessage, clientErr.Message)
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestGenerateStickySessionHashForRequest_CarpoolUsesOriginalSessionID(t *testing.T) {
	svc := &GatewayService{}
	userID := FormatMetadataUserID(strings.Repeat("a", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.81")
	parsed := &ParsedRequest{MetadataUserID: userID}
	groupID := int64(10)
	ctx := WithClaudeOAuthGroupMode(context.Background(), groupID, ClaudeOAuthModeCarpool)

	sessionHash, mode, err := svc.GenerateStickySessionHashForRequest(ctx, &groupID, PlatformAnthropic, parsed)
	require.NoError(t, err)
	require.Equal(t, ClaudeOAuthModeCarpool, mode)
	require.Equal(t, generateSessionUUID("group:10::11111111-2222-4333-8444-555555555555"), sessionHash)
}

func TestGenerateStickySessionHashForRequest_SharedUsesOriginalDeviceAndSessionID(t *testing.T) {
	svc := &GatewayService{}
	userID := FormatMetadataUserID(strings.Repeat("b", 64), "", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "2.1.81")
	parsed := &ParsedRequest{MetadataUserID: userID}
	groupID := int64(11)
	ctx := WithClaudeOAuthGroupMode(context.Background(), groupID, ClaudeOAuthModeShared)

	sessionHash, mode, err := svc.GenerateStickySessionHashForRequest(ctx, &groupID, PlatformAnthropic, parsed)
	require.NoError(t, err)
	require.Equal(t, ClaudeOAuthModeShared, mode)
	require.Equal(t, generateSessionUUID("group:11:"+strings.Repeat("b", 64)+"::aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), sessionHash)
}

func TestGenerateStickySessionHashForRequest_PinnedDisablesStickyBinding(t *testing.T) {
	svc := &GatewayService{}
	userID := FormatMetadataUserID(strings.Repeat("c", 64), "", "bbbbbbbb-2222-4333-8444-555555555555", "2.1.81")
	parsed := &ParsedRequest{MetadataUserID: userID}
	groupID := int64(12)
	ctx := WithClaudeOAuthGroupMode(context.Background(), groupID, ClaudeOAuthModePinned)

	sessionHash, mode, err := svc.GenerateStickySessionHashForRequest(ctx, &groupID, PlatformAnthropic, parsed)
	require.NoError(t, err)
	require.Equal(t, ClaudeOAuthModePinned, mode)
	require.Empty(t, sessionHash)
}

func TestResolveClaudeOAuthGroupMode_FailsOpenOnRepoError(t *testing.T) {
	repo := &accountRepoStubForOAuthMode{
		listByGroupFunc: func(_ context.Context, _ int64) ([]Account, error) {
			return nil, errors.New("db down")
		},
	}
	svc := &GatewayService{accountRepo: repo}
	groupID := int64(12)

	mode, err := svc.ResolveClaudeOAuthGroupMode(context.Background(), &groupID, PlatformAnthropic)
	require.NoError(t, err)
	require.Empty(t, mode)
	require.Equal(t, 1, repo.listByGroupCalls)
}

func TestResolveClaudeOAuthGroupMode_ContextCachesEmptyMode(t *testing.T) {
	repo := &accountRepoStubForOAuthMode{
		listByGroupFunc: func(_ context.Context, _ int64) ([]Account, error) {
			return nil, errors.New("should not be called")
		},
	}
	svc := &GatewayService{accountRepo: repo}
	groupID := int64(13)
	ctx := WithClaudeOAuthGroupMode(context.Background(), groupID, "")

	mode, err := svc.ResolveClaudeOAuthGroupMode(ctx, &groupID, PlatformAnthropic)
	require.NoError(t, err)
	require.Empty(t, mode)
	require.Zero(t, repo.listByGroupCalls)
}

func TestResolveClaudeOAuthGroupMode_AllowsMixedModes(t *testing.T) {
	repo := &accountRepoStubForOAuthMode{
		listByGroupFunc: func(_ context.Context, _ int64) ([]Account, error) {
			return []Account{
				{
					ID:       1,
					Platform: PlatformAnthropic,
					Type:     AccountTypeOAuth,
					Extra: map[string]any{
						"claude_oauth_mode": ClaudeOAuthModeCarpool,
					},
				},
				{
					ID:       2,
					Platform: PlatformAnthropic,
					Type:     AccountTypeSetupToken,
					Extra: map[string]any{
						"claude_oauth_mode": ClaudeOAuthModeSingleDevice,
					},
				},
				{
					ID:       3,
					Platform: PlatformAnthropic,
					Type:     AccountTypeAPIKey,
				},
			}, nil
		},
	}
	svc := &GatewayService{accountRepo: repo}
	groupID := int64(14)

	mode, err := svc.ResolveClaudeOAuthGroupMode(context.Background(), &groupID, PlatformAnthropic)
	require.NoError(t, err)
	require.Empty(t, mode)
	require.Equal(t, 1, repo.listByGroupCalls)
}

func TestValidateClaudeOAuthRequestShapeForGroup_RejectsMixedGroupBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.146 (external, cli)")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	req.Header.Set("Authorization", "Bearer should-be-redacted")
	c.Request = req

	groupID := int64(20)
	repo := &accountRepoStubForOAuthMode{
		listByGroupFunc: func(_ context.Context, gotGroupID int64) ([]Account, error) {
			require.Equal(t, groupID, gotGroupID)
			return []Account{
				{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
				{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			}, nil
		},
	}
	svc := &GatewayService{accountRepo: repo}
	body := []byte(`{"model":"claude-opus-4-7","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"bad side request"}]}],"max_tokens":64000}`)
	parsed := &ParsedRequest{Model: "claude-opus-4-7", Stream: true, MaxTokens: 64000, Body: body}

	err := svc.ValidateClaudeOAuthRequestShapeForGroup(c.Request.Context(), c, &groupID, PlatformAnthropic, parsed, body, false)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, claudeOAuthRealClaudeCodeRequiredMessage, clientErr.Message)
	require.True(t, ShouldCaptureFullOpsRequestHeaders(c))
	opts, ok := GetOpsRequestBodyCaptureOptions(c)
	require.True(t, ok)
	require.Equal(t, 64*1024, opts.MaxBytes)

	v, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := v.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "client_reject", events[0].Kind)
	require.Contains(t, events[0].Detail, `"reason":"invalid_metadata_user_id"`)
}

func TestClaudeOAuthAccountFailoverErrorRecordsStatusCode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{
		ID:       203,
		Name:     "carpool-full",
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
	}
	svc := &GatewayService{}

	err := svc.claudeOAuthAccountFailoverError(c, account, http.StatusTooManyRequests, "rate_limit_error", claudeOAuthCarpoolDevicesFullMessage, ErrClaudeOAuthCarpoolDevicesFull)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.ErrorIs(t, err, ErrClaudeOAuthCarpoolDevicesFull)

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "account_failover", events[0].Kind)
	require.Equal(t, int64(203), events[0].AccountID)
	require.Equal(t, http.StatusTooManyRequests, events[0].UpstreamStatusCode)
	require.Equal(t, claudeOAuthCarpoolDevicesFullMessage, events[0].Message)
}

func TestClaudeOAuthPermanentForbiddenSkipsSameAccountRetry(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{
		ID:       202,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
	}
	body := []byte(`{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."},"request_id":"req_123"}`)

	require.True(t, svc.shouldRetryUpstreamError(account, http.StatusForbidden))
	require.False(t, svc.shouldRetryUpstreamErrorBody(account, http.StatusForbidden, body))
	require.True(t, svc.shouldFailoverUpstreamError(http.StatusForbidden))
}

func TestClaudeOAuthOtherForbiddenStillRetries(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{
		ID:       203,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
	}
	body := []byte(`{"type":"error","error":{"type":"permission_error","message":"temporary upstream permission check failed"},"request_id":"req_123"}`)

	require.True(t, svc.shouldRetryUpstreamErrorBody(account, http.StatusForbidden, body))
}

func TestValidateClaudeOAuthRequestShapeForGroup_SkipsWhenGroupGateDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.146 (external, cli)")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	groupID := int64(20)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, &Group{
		ID:                             groupID,
		Platform:                       PlatformAnthropic,
		Status:                         StatusActive,
		Hydrated:                       true,
		ClaudeOAuthRequestGateDisabled: true,
	}))
	c.Request = req

	repo := &accountRepoStubForOAuthMode{
		listByGroupFunc: func(context.Context, int64) ([]Account, error) {
			t.Fatal("disabled Claude OAuth request gate should skip mixed-group OAuth account lookup")
			return nil, nil
		},
	}
	svc := &GatewayService{accountRepo: repo}
	body := []byte(`{"model":"claude-opus-4-7","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"bad side request"}]}],"max_tokens":64000}`)
	parsed := &ParsedRequest{Model: "claude-opus-4-7", Stream: true, MaxTokens: 64000, Body: body}

	require.NoError(t, svc.ValidateClaudeOAuthRequestShapeForGroup(c.Request.Context(), c, &groupID, PlatformAnthropic, parsed, body, false))
	require.False(t, ShouldCaptureFullOpsRequestHeaders(c))
}

func TestValidateClaudeOAuthRequestShapeForGroup_IgnoresPlainAPIRequestInMixedGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "custom-client/1.0")
	c.Request = req

	groupID := int64(20)
	repo := &accountRepoStubForOAuthMode{
		listByGroupFunc: func(context.Context, int64) ([]Account, error) {
			t.Fatal("plain API traffic should not query OAuth group shape")
			return nil, nil
		},
	}
	svc := &GatewayService{accountRepo: repo}
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`)
	parsed := &ParsedRequest{Model: "claude-opus-4-7", Body: body}

	require.NoError(t, svc.ValidateClaudeOAuthRequestShapeForGroup(c.Request.Context(), c, &groupID, PlatformAnthropic, parsed, body, false))
	require.False(t, ShouldCaptureFullOpsRequestHeaders(c))
}

func TestValidateClaudeOAuthRequestShapeForGroup_AllowsTokenModeMissingCCHFrom2181(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.185 (external, sdk-cli)")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	c.Request = req

	groupID := int64(20)
	repo := &accountRepoStubForOAuthMode{
		listByGroupFunc: func(context.Context, int64) ([]Account, error) {
			return []Account{{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}}, nil
		},
	}
	svc := &GatewayService{accountRepo: repo}
	userID := FormatMetadataUserID(strings.Repeat("1", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.185")
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"metadata":{"user_id":` + strconvQuote(userID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.ecf; cc_entrypoint=sdk-cli;"}]}`)
	parsed := &ParsedRequest{Model: "claude-opus-4-8", Stream: true, MetadataUserID: userID, Body: body}

	require.NoError(t, svc.ValidateClaudeOAuthRequestShapeForGroup(c.Request.Context(), c, &groupID, PlatformAnthropic, parsed, body, false))
	require.False(t, ShouldCaptureFullOpsRequestHeaders(c))
	diag := diagnoseClaudeOAuthRealClaudeCodeRequest(c, parsed, body)
	require.True(t, diag.Valid)
	require.True(t, diag.MissingCCHTokenModeAllowed)
	require.False(t, diag.CCHPresent)
}

func TestDiagnoseClaudeOAuthRealClaudeCodeRejectsMissingCCHBefore2181(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.179 (external, sdk-cli)")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	c.Request = req

	userID := FormatMetadataUserID(strings.Repeat("2", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.179")
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"metadata":{"user_id":` + strconvQuote(userID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.179.61a; cc_entrypoint=sdk-cli;"}]}`)
	parsed := &ParsedRequest{Model: "claude-opus-4-8", Stream: true, MetadataUserID: userID, Body: body}

	diag := diagnoseClaudeOAuthRealClaudeCodeRequest(c, parsed, body)
	require.False(t, diag.Valid)
	require.False(t, diag.MissingCCHTokenModeAllowed)
	require.Equal(t, "missing_cch_unsupported_mode", diag.Reason)
	require.Equal(t, "2.1.179", diag.BodyCCVersion)
	require.Equal(t, "sdk-cli", diag.BodyEntrypoint)
}

func TestDiagnoseClaudeOAuthRealClaudeCodeRejectsMissingCCHForLoginBackedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.185 (external, cli)")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	c.Request = req

	userID := FormatMetadataUserID(strings.Repeat("3", 64), "login-account-uuid", "11111111-2222-4333-8444-555555555555", "2.1.185")
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"metadata":{"user_id":` + strconvQuote(userID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.ecf; cc_entrypoint=cli;"}]}`)
	parsed := &ParsedRequest{Model: "claude-opus-4-8", Stream: true, MetadataUserID: userID, Body: body}

	diag := diagnoseClaudeOAuthRealClaudeCodeRequest(c, parsed, body)
	require.False(t, diag.Valid)
	require.False(t, diag.MetadataAccountUUIDEmpty)
	require.False(t, diag.MissingCCHTokenModeAllowed)
	require.Equal(t, "missing_cch_unsupported_mode", diag.Reason)
}

func TestDiagnoseClaudeOAuthRealClaudeCodeRejectsLoginStateUserEmailContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.185 (external, cli)")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	c.Request = req

	userID := FormatMetadataUserID(strings.Repeat("4", 64), "login-account-uuid", "11111111-2222-4333-8444-555555555555", "2.1.185")
	prompt := "<system-reminder>\n# userEmail\nThe user's email address is user@example.com.\n# currentDate\nToday's date is 2026-06-22.\n</system-reminder>\n\nhi"
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-opus-4-8", userID, req.Header.Get("User-Agent"), prompt)
	parsed := &ParsedRequest{Model: "claude-opus-4-8", Stream: true, MetadataUserID: userID, Body: body}

	diag := diagnoseClaudeOAuthRealClaudeCodeRequest(c, parsed, body)
	require.False(t, diag.Valid)
	require.True(t, diag.LoginStateUserEmailContextPresent)
	require.Equal(t, "login_state_user_email_context_present", diag.Reason)
	require.True(t, diag.CCHValid)
}

func TestPrepareOAuthRequestIdentity_SetupTokenAllowsMissingCCHFrom2181(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ctx := WithClaudeOAuthGroupMode(SetClaudeCodeClient(req.Context(), true), 22, ClaudeOAuthModeCarpool)
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", "claude-cli/2.1.185 (external, sdk-cli)")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	c.Request = req

	originalUserID := FormatMetadataUserID(
		strings.Repeat("5", 64),
		"",
		"11111111-2222-4333-8444-555555555555",
		"2.1.185",
	)
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"metadata":{"user_id":` + strconvQuote(originalUserID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.ecf; cc_entrypoint=sdk-cli;"}]}`)
	parsed := &ParsedRequest{Model: "claude-opus-4-8", Stream: true, MetadataUserID: originalUserID, Body: body}
	account := &Account{ID: 123, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(ctx, c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, ClaudeOAuthModeCarpool, identity.Mode)
	require.True(t, identity.NativeBillingStyle)
	require.Zero(t, identity.BillingCCHSeed)
	require.Empty(t, identity.BillingCCHMode)
	require.Contains(t, string(newBody), "cc_entrypoint=sdk-cli;")
	require.NotContains(t, string(newBody), "cch=")
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_SkipsRealClaudeCodeGateWhenGroupDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.146 (external, cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,claude-code-20250219")
	groupID := int64(22)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, &Group{
		ID:                             groupID,
		Platform:                       PlatformAnthropic,
		Status:                         StatusActive,
		Hydrated:                       true,
		ClaudeOAuthRequestGateDisabled: true,
	}))
	c.Request = req

	originalUserID := FormatMetadataUserID(
		strings.Repeat("a", 64),
		"original-account-uuid",
		"11111111-2222-4333-8444-555555555555",
		"2.1.146",
	)
	body := []byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":` + strconvQuote(originalUserID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"no active billing system"}]}]}`)
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
		Body:           body,
	}
	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid": "forced-account-uuid",
		},
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, ClaudeOAuthModeCarpool, identity.Mode)
	rewritten := ParseMetadataUserID(gjson.GetBytes(newBody, "metadata.user_id").String())
	require.NotNil(t, rewritten)
	require.Equal(t, "forced-account-uuid", rewritten.AccountUUID)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_GroupDisabledSkipsCarpoolBillingIntegrity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.193 (external, cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,claude-code-20250219")
	groupID := int64(22)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, &Group{
		ID:                             groupID,
		Platform:                       PlatformAnthropic,
		Status:                         StatusActive,
		Hydrated:                       true,
		ClaudeOAuthRequestGateDisabled: true,
	}))
	c.Request = req

	originalUserID := FormatMetadataUserID(
		strings.Repeat("a", 64),
		"original-account-uuid",
		"11111111-2222-4333-8444-555555555555",
		"2.1.193",
	)
	body := []byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":` + strconvQuote(originalUserID) + `},"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.193.45e; cc_entrypoint=cli;"}],"messages":[{"role":"user","content":[{"type":"text","text":"missing cch but group disabled"}]}]}`)
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
		Body:           body,
	}
	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid": "forced-account-uuid",
		},
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, ClaudeOAuthModeCarpool, identity.Mode)
	require.Zero(t, identity.BillingCCHSeed)
	require.Empty(t, identity.BillingCCHMode)
	require.Contains(t, string(newBody), "cc_entrypoint=cli;")
	require.NotContains(t, string(newBody), "cch=")
	require.Empty(t, rec.Body.String())
}

func TestGatewayService_OAuthAccountSessionKey_SharedUsesBucketAwareMapping(t *testing.T) {
	cache := &identityCacheStub{}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                     "forced-account-uuid",
			"claude_oauth_mode":                ClaudeOAuthModeShared,
			"claude_oauth_shared_bucket_count": 2,
		},
	}
	userID := FormatMetadataUserID(strings.Repeat("c", 64), "", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "2.1.81")

	bucket, err := svc.identityService.ResolveSharedBucket(context.Background(), account, userID)
	require.NoError(t, err)

	key := svc.oauthAccountSessionKey(context.Background(), account, userID, "fallback")
	require.Equal(t, generateSessionUUID("123:"+strconv.Itoa(bucket)+"::aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), key)
}

func TestGatewayService_OAuthAccountSessionKey_PinnedUsesPerAccountSession(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode": ClaudeOAuthModePinned,
		},
	}
	userID := FormatMetadataUserID(strings.Repeat("d", 64), "", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "2.1.81")
	key := svc.oauthAccountSessionKey(context.Background(), account, userID, "fallback")
	require.Equal(t, generateSessionUUID("123::aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), key)
}

func TestPrepareOAuthRequestIdentity_RewritesUserIDToSlotDevice(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.80",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
	req.Header.Set("X-Stainless-Lang", "js")
	req.Header.Set("X-Stainless-Package-Version", "0.74.0")
	req.Header.Set("X-Stainless-OS", "Linux")
	req.Header.Set("X-Stainless-Arch", "x64")
	req.Header.Set("X-Stainless-Runtime", "node")
	req.Header.Set("X-Stainless-Runtime-Version", "v24.3.0")
	req.Header.Set("X-App", "cli")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid": "forced-account-uuid",
		},
	}
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", originalUserID, req.Header.Get("User-Agent"), "hello")
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)

	rewritten := gjson.GetBytes(newBody, "metadata.user_id").String()
	require.NotEqual(t, originalUserID, rewritten)
	parsedRewritten := ParseMetadataUserID(rewritten)
	require.NotNil(t, parsedRewritten)
	require.Equal(t, "forced-account-uuid", parsedRewritten.AccountUUID)
	require.Len(t, parsedRewritten.DeviceID, 64)
}

func TestPrepareOAuthRequestIdentity_PinnedRewritesUserIDWithSelectedAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalUserID := FormatMetadataUserID(
		strings.Repeat("e", 64),
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.80",
	)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := WithClaudeOAuthGroupMode(SetClaudeCodeClient(req.Context(), true), 22, ClaudeOAuthModePinned)
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", "claude-cli/2.1.80 (external, cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,claude-code-20250219")
	c.Request = req

	account := &Account{
		ID:       123,
		Name:     "pinned-a",
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":      "forced-account-uuid",
			"claude_oauth_mode": ClaudeOAuthModePinned,
		},
	}
	body := expectedOAuthBillingBodyForTest(t,
		[]byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":`+strconvQuote(originalUserID)+`},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.000; cc_entrypoint=cli; cch=00000;"}]}`),
		req.Header.Get("User-Agent"),
	)
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, ClaudeOAuthModePinned, identity.Mode)
	require.Equal(t, pinnedSlotKey(account.ID), identity.PinnedSlotKey)

	rewritten := ParseMetadataUserID(gjson.GetBytes(newBody, "metadata.user_id").String())
	require.NotNil(t, rewritten)
	require.Equal(t, "forced-account-uuid", rewritten.AccountUUID)
	require.NotEqual(t, strings.Repeat("e", 64), rewritten.DeviceID)
	require.NotEqual(t, "00000000-0000-4000-8000-000000000006", rewritten.SessionID)
}

func TestPrepareOAuthRequestIdentity_PinnedRejectsAuthTokenLikeRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalUserID := FormatMetadataUserID(
		strings.Repeat("f", 64),
		"",
		"00000000-0000-4000-8000-000000000006",
		"2.1.101",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := WithClaudeOAuthGroupMode(SetClaudeCodeClient(req.Context(), true), 22, ClaudeOAuthModePinned)
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", "claude-cli/2.1.101 (external, sdk-cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,claude-code-20250219")
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":      "forced-account-uuid",
			"claude_oauth_mode": ClaudeOAuthModePinned,
		},
	}
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", originalUserID, req.Header.Get("User-Agent"), "hello")
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(ctx, c, account, parsed, body, false)
	require.ErrorIs(t, err, ErrClaudeOAuthLoginStateRequired)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, http.StatusBadRequest, clientErr.StatusCode)
	require.Equal(t, "invalid_request_error", clientErr.ErrorType)
	require.Equal(t, claudeOAuthRealClaudeCodeRequiredMessage, clientErr.Message)
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_PinnedRejectsPlaceholderCCH(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalUserID := FormatMetadataUserID(
		strings.Repeat("a", 64),
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.101",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := WithClaudeOAuthGroupMode(SetClaudeCodeClient(req.Context(), true), 22, ClaudeOAuthModePinned)
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", "claude-cli/2.1.101 (external, cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,claude-code-20250219")
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":      "forced-account-uuid",
			"claude_oauth_mode": ClaudeOAuthModePinned,
		},
	}
	body := []byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":` + strconvQuote(originalUserID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.101.a46; cc_entrypoint=cli; cch=00000;"}]}`)
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(ctx, c, account, parsed, body, false)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, claudeOAuthRealClaudeCodeRequiredMessage, clientErr.Message)
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_PinnedRejectsBillingMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalUserID := FormatMetadataUserID(
		strings.Repeat("b", 64),
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.109",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := WithClaudeOAuthGroupMode(SetClaudeCodeClient(req.Context(), true), 22, ClaudeOAuthModePinned)
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", "claude-cli/2.1.109 (external, cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,claude-code-20250219")
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":      "forced-account-uuid",
			"claude_oauth_mode": ClaudeOAuthModePinned,
		},
	}
	validBody := expectedOAuthBillingBodyForTest(t,
		[]byte(`{"model":"claude-haiku-4-5-20251001","metadata":{"user_id":`+strconvQuote(originalUserID)+`},"messages":[{"role":"user","content":[{"type":"text","text":"做个本地代理"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.109.000; cc_entrypoint=cli; cch=00000;"}]}`),
		req.Header.Get("User-Agent"),
	)
	_, observedSuffix := claudebilling.ExtractCCVersionFromBody(validBody)
	_, cchMatch, err := claudebilling.NormalizeBodyForCCH(validBody)
	require.NoError(t, err)
	observedCCH := cchMatch.Value
	badSuffix := "fff"
	if observedSuffix == badSuffix {
		badSuffix = "ffe"
	}
	badCCH := "fffff"
	if observedCCH == badCCH {
		badCCH = "ffffe"
	}
	body := []byte(strings.Replace(
		strings.Replace(string(validBody), "."+observedSuffix+";", "."+badSuffix+";", 1),
		"cch="+observedCCH+";",
		"cch="+badCCH+";",
		1,
	))
	parsed := &ParsedRequest{
		Model:          "claude-haiku-4-5-20251001",
		MetadataUserID: originalUserID,
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(ctx, c, account, parsed, body, false)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, claudeOAuthRealClaudeCodeRequiredMessage, clientErr.Message)
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestRewriteClaudeOAuthBillingHeader_PinnedKeepsPlaceholderCCHAndCCVersion(t *testing.T) {
	svc := &GatewayService{}
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.100.f22; cc_entrypoint=sdk-cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	out := svc.rewriteClaudeOAuthBillingHeader(body, &oauthRequestIdentity{
		Mode:               ClaudeOAuthModePinned,
		NativeBillingStyle: true,
		BillingUserAgent:   "claude-cli/2.1.100 (external, sdk-cli)",
	})
	require.Equal(t, string(body), string(out))
}

func TestRewriteClaudeOAuthBillingHeader_PinnedRecomputesOnlyNonPlaceholderCCH(t *testing.T) {
	svc := &GatewayService{}
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.100.f22; cc_entrypoint=sdk-cli; cch=abcde;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	out := svc.rewriteClaudeOAuthBillingHeader(body, &oauthRequestIdentity{
		Mode:               ClaudeOAuthModePinned,
		NativeBillingStyle: true,
		BillingUserAgent:   "claude-cli/2.1.100 (external, sdk-cli)",
	})
	require.Contains(t, string(out), "cc_version=2.1.100.f22;")
	require.NotContains(t, string(out), "cch=abcde;")
	normalized, _, err := claudebilling.NormalizeBodyForCCH(out)
	require.NoError(t, err)
	_, expectedCCH := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeed)
	require.Contains(t, string(out), "cch="+expectedCCH+";")
}

func TestRewriteClaudeOAuthBillingHeader_PinnedRecomputesWithMatchedV2Seed(t *testing.T) {
	svc := &GatewayService{}
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.110.f22; cc_entrypoint=sdk-cli; cch=abcde;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	out := svc.rewriteClaudeOAuthBillingHeader(body, &oauthRequestIdentity{
		Mode:               ClaudeOAuthModePinned,
		NativeBillingStyle: true,
		BillingUserAgent:   "claude-cli/2.1.110 (external, sdk-cli)",
		BillingCCHSeed:     claudebilling.CCHSeedV2,
	})
	normalized, match, err := claudebilling.NormalizeBodyForCCH(out)
	require.NoError(t, err)
	_, expectedV2 := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeedV2)
	_, unexpectedV1 := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeed)
	require.Equal(t, expectedV2, match.Value)
	require.NotEqual(t, unexpectedV1, match.Value)
}

func TestRewriteClaudeOAuthBillingHeader_SharedChoosesV2SeedFromVersion(t *testing.T) {
	svc := &GatewayService{}
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.110.000; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	out := svc.rewriteClaudeOAuthBillingHeader(body, &oauthRequestIdentity{
		Mode:             ClaudeOAuthModeShared,
		BillingUserAgent: "claude-cli/2.1.110 (external, cli)",
	})
	normalized, match, err := claudebilling.NormalizeBodyForCCH(out)
	require.NoError(t, err)
	_, expectedV2 := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeedV2)
	_, unexpectedV1 := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeed)
	require.Equal(t, expectedV2, match.Value)
	require.NotEqual(t, unexpectedV1, match.Value)
}

func newCarpoolBillingTestCtx(t *testing.T, ua string) (*GatewayService, *gin.Context, *httptest.ResponseRecorder, http.Header) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := &GatewayService{}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	c.Request = req
	return svc, c, rec, req.Header
}

func TestCarpoolBillingValidate_NoBillingHeader_Pass(t *testing.T) {
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, "claude-cli/2.1.100 (external, cli)")
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"ping"}]}`)

	err := svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{ID: 1}, false)
	require.NoError(t, err)
	require.Equal(t, 200, rec.Code)
}

func TestCarpoolBillingValidate_UAMissing_Reject(t *testing.T) {
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, "python-requests/2.31")
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.100.abc; cc_entrypoint=cli; cch=abcde;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)

	err := svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{ID: 1}, false)
	require.ErrorIs(t, err, ErrClaudeOAuthCarpoolBillingMismatch)
	require.Equal(t, 400, rec.Code)
	require.Contains(t, rec.Body.String(), "integrity check failed")
}

func TestCarpoolBillingValidate_UAVersionMismatch_Reject(t *testing.T) {
	ua := "claude-cli/2.1.100 (external, cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	// body 声称 2.1.80，但 UA 是 2.1.100
	input := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.000; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	body := expectedOAuthBillingBodyForTest(t, input, "claude-cli/2.1.80 (external, cli)")

	err := svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{ID: 1}, false)
	require.ErrorIs(t, err, ErrClaudeOAuthCarpoolBillingMismatch)
	require.Equal(t, 400, rec.Code)
}

func TestCarpoolBillingValidate_EntrypointMismatch_Reject(t *testing.T) {
	ua := "claude-cli/2.1.100 (external, sdk-cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	// UA 是 sdk-cli，body 是 cli
	input := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.100.000; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	body := expectedOAuthBillingBodyForTest(t, input, ua)

	err := svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{ID: 1}, false)
	require.ErrorIs(t, err, ErrClaudeOAuthCarpoolBillingMismatch)
	require.Equal(t, 400, rec.Code)
}

func TestCarpoolBillingValidate_EntrypointAllowsAgentSDKUASuffix(t *testing.T) {
	ua := "claude-cli/2.1.128 (external, claude-vscode, agent-sdk/0.2.128)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	input := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.128.000; cc_entrypoint=claude-vscode; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	body := expectedOAuthBillingBodyForTest(t, input, "claude-cli/2.1.128 (external, claude-vscode)")

	err := svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{ID: 1}, false)
	require.NoError(t, err)
	require.Equal(t, 200, rec.Code)
}

func TestCarpoolBillingValidate_CCVersionMismatch_Allows(t *testing.T) {
	ua := "claude-cli/2.1.100 (external, cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	input := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.100.000; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	valid := expectedOAuthBillingBodyForTest(t, input, ua)

	_, observedSuffix := claudebilling.ExtractCCVersionFromBody(valid)
	bad := "fff"
	if observedSuffix == bad {
		bad = "ffe"
	}
	bodyWithBadSuffix := []byte(strings.Replace(string(valid), "."+observedSuffix+";", "."+bad+";", 1))
	normalized, match, err := claudebilling.NormalizeBodyForCCH(bodyWithBadSuffix)
	require.NoError(t, err)
	_, cch := claudebilling.ComputeCCH(normalized)
	body := claudebilling.ReplaceCCH(bodyWithBadSuffix, match, cch)

	err = svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{ID: 1}, false)
	require.NoError(t, err)
	require.Equal(t, 200, rec.Code)
}

func TestCarpoolBillingValidate_CCVersionCanMatchLaterUserMessage(t *testing.T) {
	ua := "claude-cli/2.1.128 (external, cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	laterPrompt := "This session is being continued from a previous conversation that ran out of context."
	suffix := claudebilling.ComputeCCVersionSuffix(laterPrompt, "2.1.128")
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.128.` + suffix + `; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>Called the Read tool with the following input</system-reminder>"}]},{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"text","text":"` + laterPrompt + `"}]}]}`)

	err := svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{ID: 1}, false)
	require.NoError(t, err)
	require.Equal(t, 200, rec.Code)
}

func TestCarpoolBillingValidate_CCHPlaceholder_Pass(t *testing.T) {
	ua := "claude-cli/2.1.100 (external, cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	// 先生成合法 fixture，再把 cch 改回 00000 并保持合法 cc_version suffix（模拟 npm 客户端形态）
	input := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.100.000; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	valid := expectedOAuthBillingBodyForTest(t, input, ua)
	_, match, err := claudebilling.NormalizeBodyForCCH(valid)
	require.NoError(t, err)
	body := []byte(strings.Replace(string(valid), "cch="+match.Value+";", "cch=00000;", 1))

	err = svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{ID: 1}, false)
	require.NoError(t, err)
	require.Equal(t, 200, rec.Code)
}

func TestCarpoolBillingValidate_CCHV2PassesAndReturnsSeed(t *testing.T) {
	ua := "claude-cli/2.1.110 (external, cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	input := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.110.000; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	body := expectedOAuthBillingBodyForTest(t, input, ua)

	seed, mode, err := svc.validateCarpoolBillingIntegrityWithResult(c, body, headers, &Account{ID: 1}, false)
	require.NoError(t, err)
	require.Equal(t, claudebilling.CCHSeedV2, seed)
	require.Equal(t, claudebilling.CCHInputModeFullBody, mode)
	require.Equal(t, 200, rec.Code)
}

func TestCarpoolBillingValidate_CCHFilteredV2PassesAndReturnsProfile(t *testing.T) {
	ua := "claude-cli/2.1.185 (external, sdk-cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	input := []byte(`{"model":"claude-haiku-4-5-20251001","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.000; cc_entrypoint=sdk-cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}],"max_tokens":32000,"thinking":{"budget_tokens":31999,"type":"enabled"}}`)
	body := expectedOAuthBillingBodyForTest(t, input, ua)

	seed, mode, err := svc.validateCarpoolBillingIntegrityWithResult(c, body, headers, &Account{ID: 1}, false)
	require.NoError(t, err)
	require.Equal(t, claudebilling.CCHSeedV2, seed)
	require.Equal(t, claudebilling.CCHInputModeFilteredBodyV2, mode)
	require.Equal(t, 200, rec.Code)
}

func TestCarpoolBillingValidate_SetupTokenMissingCCHFrom2181Passes(t *testing.T) {
	ua := "claude-cli/2.1.185 (external, sdk-cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	headers.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	userID := FormatMetadataUserID(strings.Repeat("6", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.185")
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"metadata":{"user_id":` + strconvQuote(userID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.ecf; cc_entrypoint=sdk-cli;"}]}`)

	seed, mode, err := svc.validateCarpoolBillingIntegrityWithResult(c, body, headers, &Account{
		ID:       1,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
	}, false)
	require.NoError(t, err)
	require.Zero(t, seed)
	require.Empty(t, mode)
	require.Equal(t, 200, rec.Code)
}

func TestCarpoolBillingValidate_OAuthMissingCCHFrom2181Rejects(t *testing.T) {
	ua := "claude-cli/2.1.185 (external, sdk-cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	headers.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	userID := FormatMetadataUserID(strings.Repeat("7", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.185")
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"metadata":{"user_id":` + strconvQuote(userID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.ecf; cc_entrypoint=sdk-cli;"}]}`)

	err := svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{
		ID:       1,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}, false)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
	require.False(t, c.Writer.Written())
	require.Equal(t, 200, rec.Code)
}

func TestCarpoolBillingValidate_RejectRecordsOpsDetail(t *testing.T) {
	ua := "claude-cli/2.1.185 (external, sdk-cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	userID := FormatMetadataUserID(strings.Repeat("8", 64), "original-account-uuid", "11111111-2222-4333-8444-555555555555", "2.1.185")
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"metadata":{"user_id":` + strconvQuote(userID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.ecf; cc_entrypoint=sdk-cli;"}]}`)

	err := svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{
		ID:       1,
		Name:     "oauth account",
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}, false)
	require.ErrorIs(t, err, ErrClaudeOAuthCarpoolBillingMismatch)
	require.Equal(t, 400, rec.Code)

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "client_reject", events[0].Kind)
	require.Equal(t, int64(1), events[0].AccountID)
	require.Equal(t, carpoolBillingMismatchMessage, events[0].Message)
	require.Equal(t, "billing_cch_malformed", gjson.Get(events[0].Detail, "reason").String())
	require.Equal(t, "cch field missing or malformed", gjson.Get(events[0].Detail, "detail").String())
	require.True(t, gjson.Get(events[0].Detail, "has_billing_header").Bool())
	require.False(t, gjson.Get(events[0].Detail, "cch_present").Bool())
	require.Equal(t, "2.1.185", gjson.Get(events[0].Detail, "body_cc_version").String())
	require.Equal(t, "sdk-cli", gjson.Get(events[0].Detail, "body_entrypoint").String())
}

func TestCarpoolBillingValidate_CCHMismatch_Reject(t *testing.T) {
	ua := "claude-cli/2.1.100 (external, cli)"
	svc, c, rec, headers := newCarpoolBillingTestCtx(t, ua)
	input := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.100.000; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	valid := expectedOAuthBillingBodyForTest(t, input, ua)

	_, match, err := claudebilling.NormalizeBodyForCCH(valid)
	require.NoError(t, err)
	bad := "fffff"
	if match.Value == bad {
		bad = "ffffe"
	}
	body := []byte(strings.Replace(string(valid), "cch="+match.Value+";", "cch="+bad+";", 1))

	err = svc.validateCarpoolBillingIntegrity(c, body, headers, &Account{ID: 1}, false)
	require.ErrorIs(t, err, ErrClaudeOAuthCarpoolBillingMismatch)
	require.Equal(t, 400, rec.Code)
}

func TestPrepareOAuthRequestIdentity_CarpoolRewritesDeterministicIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.80",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid": "forced-account-uuid",
		},
	}
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", originalUserID, req.Header.Get("User-Agent"), "hello")
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, ClaudeOAuthModeCarpool, identity.Mode)
	require.Nil(t, identity.Fingerprint)
	require.Equal(t, int64(0), identity.TransportAccountID)

	rewritten := ParseMetadataUserID(gjson.GetBytes(newBody, "metadata.user_id").String())
	require.NotNil(t, rewritten)
	require.Equal(t, "forced-account-uuid", rewritten.AccountUUID)
	require.NotEqual(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rewritten.DeviceID)
	require.NotEqual(t, "00000000-0000-4000-8000-000000000006", rewritten.SessionID)
}

func TestPrepareOAuthRequestIdentity_CarpoolCarriesMatchedCCHV2Seed(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.110",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.110 (external, cli)")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid": "forced-account-uuid",
		},
	}
	input := []byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":` + strconvQuote(originalUserID) + `},"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.110.000; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)
	body := expectedOAuthBillingBodyForTest(t, input, "claude-cli/2.1.110 (external, cli)")
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, claudebilling.CCHSeedV2, identity.BillingCCHSeed)
	require.Equal(t, claudebilling.CCHInputModeFullBody, identity.BillingCCHMode)

	out := svc.rewriteClaudeOAuthBillingHeader(newBody, identity)
	normalized, match, err := claudebilling.NormalizeBodyForCCH(out)
	require.NoError(t, err)
	_, expectedV2 := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeedV2)
	_, unexpectedV1 := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeed)
	require.Equal(t, expectedV2, match.Value)
	require.NotEqual(t, unexpectedV1, match.Value)
}

func TestPrepareOAuthRequestIdentity_CarpoolCarriesMatchedCCHFilteredMode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.185",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.185 (external, sdk-cli)")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid": "forced-account-uuid",
		},
	}
	input := []byte(`{"model":"claude-haiku-4-5-20251001","metadata":{"user_id":` + strconvQuote(originalUserID) + `},"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.000; cc_entrypoint=sdk-cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}],"max_tokens":32000,"thinking":{"budget_tokens":31999,"type":"enabled"}}`)
	body := expectedOAuthBillingBodyForTest(t, input, "claude-cli/2.1.185 (external, sdk-cli)")
	parsed := &ParsedRequest{
		Model:          "claude-haiku-4-5-20251001",
		MetadataUserID: originalUserID,
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, claudebilling.CCHSeedV2, identity.BillingCCHSeed)
	require.Equal(t, claudebilling.CCHInputModeFilteredBodyV2, identity.BillingCCHMode)

	out := svc.rewriteClaudeOAuthBillingHeader(newBody, identity)
	normalized, match, err := claudebilling.NormalizeBodyForCCH(out)
	require.NoError(t, err)
	_, expectedFiltered := claudebilling.ComputeCCHWithProfile(normalized, claudebilling.CCHSeedV2, claudebilling.CCHInputModeFilteredBodyV2)
	_, unexpectedFull := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeedV2)
	require.Equal(t, expectedFiltered, match.Value)
	require.NotEqual(t, unexpectedFull, match.Value)
}

func TestPrepareOAuthRequestIdentity_CarpoolDeviceLimitTriggersFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{
		carpoolDevices: map[string]*CarpoolDeviceRecord{
			carpoolDeviceCacheKey(123, "existing-device"): {
				DeviceKey:        carpoolDeviceKey("existing-device"),
				OriginalDeviceID: "existing-device",
				CreatedAt:        1,
				LastSeenAt:       1,
			},
		},
	}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.80",
	)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                      "forced-account-uuid",
			"claude_oauth_mode":                 ClaudeOAuthModeCarpool,
			"claude_oauth_carpool_device_limit": 1,
		},
	}
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", originalUserID, req.Header.Get("User-Agent"), "hello")
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.ErrorIs(t, err, ErrClaudeOAuthCarpoolDevicesFull)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "carpool devices are full")
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_CarpoolUnlimitedDevicesBypassesLocalRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{
		carpoolDevices: map[string]*CarpoolDeviceRecord{
			carpoolDeviceCacheKey(123, "existing-device"): {
				DeviceKey:        carpoolDeviceKey("existing-device"),
				OriginalDeviceID: "existing-device",
				CreatedAt:        1,
				LastSeenAt:       1,
			},
		},
	}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalDeviceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	originalUserID := FormatMetadataUserID(
		originalDeviceID,
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.80",
	)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                           "forced-account-uuid",
			"claude_oauth_mode":                      ClaudeOAuthModeCarpool,
			"claude_oauth_carpool_device_limit":      1,
			"claude_oauth_carpool_unlimited_devices": true,
		},
	}
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", originalUserID, req.Header.Get("User-Agent"), "hello")
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, ClaudeOAuthModeCarpool, identity.Mode)
	require.Len(t, cache.carpoolDevices, 1, "unlimited mode must not grow the non-expiring device registry")
	require.NotContains(t, cache.carpoolDevices, carpoolDeviceCacheKey(account.ID, originalDeviceID))
	overview, err := svc.identityService.ListCarpoolDevices(c.Request.Context(), account)
	require.NoError(t, err)
	require.True(t, overview.UnlimitedDevices)
	require.Equal(t, 1, overview.RecordedCount, "bounded-mode history should be preserved")

	rewritten := ParseMetadataUserID(gjson.GetBytes(newBody, "metadata.user_id").String())
	require.NotNil(t, rewritten)
	require.NotEqual(t, originalDeviceID, rewritten.DeviceID)
	require.Equal(t, "forced-account-uuid", rewritten.AccountUUID)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_CarpoolDeviceLimitCountTokensReturnsClientError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{
		carpoolDevices: map[string]*CarpoolDeviceRecord{
			carpoolDeviceCacheKey(123, "existing-device"): {
				DeviceKey:        carpoolDeviceKey("existing-device"),
				OriginalDeviceID: "existing-device",
				CreatedAt:        1,
				LastSeenAt:       1,
			},
		},
	}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.80",
	)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                      "forced-account-uuid",
			"claude_oauth_mode":                 ClaudeOAuthModeCarpool,
			"claude_oauth_carpool_device_limit": 1,
		},
	}
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", originalUserID, req.Header.Get("User-Agent"), "hello")
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, true)
	require.ErrorIs(t, err, ErrClaudeOAuthCarpoolDevicesFull)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, http.StatusBadRequest, clientErr.StatusCode)
	require.Equal(t, "invalid_request_error", clientErr.ErrorType)
	require.Equal(t, claudeOAuthCarpoolDevicesFullMessage, clientErr.Message)
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_CarpoolDeviceLimitClearsBoundStickySession(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{
		carpoolDevices: map[string]*CarpoolDeviceRecord{
			carpoolDeviceCacheKey(123, "existing-device"): {
				DeviceKey:        carpoolDeviceKey("existing-device"),
				OriginalDeviceID: "existing-device",
				CreatedAt:        1,
				LastSeenAt:       1,
			},
		},
	}
	sessionID := "00000000-0000-4000-8000-000000000006"
	groupID := int64(16)
	sessionHash := generateSessionUUID(fmt.Sprintf("group:%d::%s", groupID, sessionID))
	gatewayCache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			sessionHash: 123,
			sessionID:   999,
		},
	}
	svc := &GatewayService{
		cache:           gatewayCache,
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"original-account-uuid",
		sessionID,
		"2.1.80",
	)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := WithClaudeOAuthGroupMode(SetClaudeCodeClient(req.Context(), true), groupID, ClaudeOAuthModeCarpool)
	req = req.WithContext(ctx)
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                      "forced-account-uuid",
			"claude_oauth_mode":                 ClaudeOAuthModeCarpool,
			"claude_oauth_carpool_device_limit": 1,
		},
	}
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", originalUserID, req.Header.Get("User-Agent"), "hello")
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(ctx, c, account, parsed, body, false)
	require.ErrorIs(t, err, ErrClaudeOAuthCarpoolDevicesFull)
	require.Nil(t, identity)
	require.NotContains(t, gatewayCache.sessionBindings, sessionHash)
	require.Equal(t, int64(999), gatewayCache.sessionBindings[sessionID])
	require.Equal(t, 1, gatewayCache.deletedSessions[sessionHash])
}

func TestPrepareOAuthRequestIdentity_SharedFoldsDevicesIntoBuckets(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{}
	svc := &GatewayService{
		identityService: NewIdentityService(cache, strings.Repeat("x", 32)),
	}

	makeContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		req := httptest.NewRequest("POST", "/v1/messages", nil)
		req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
		setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
		c.Request = req
		return c, rec
	}

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                     "forced-account-uuid",
			"claude_oauth_mode":                ClaudeOAuthModeShared,
			"claude_oauth_shared_bucket_count": 1,
		},
	}
	userIDA := FormatMetadataUserID(strings.Repeat("a", 64), "original-account-uuid", "00000000-0000-4000-8000-000000000006", "2.1.80")
	userIDB := FormatMetadataUserID(strings.Repeat("b", 64), "original-account-uuid", "11111111-2222-4333-8444-555555555555", "2.1.80")
	bodyA := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", userIDA, "claude-cli/2.1.80 (external, cli)", "hello a")
	bodyB := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", userIDB, "claude-cli/2.1.80 (external, cli)", "hello b")

	c1, _ := makeContext()
	rewrittenA, identityA, err := svc.prepareOAuthRequestIdentity(c1.Request.Context(), c1, account, &ParsedRequest{Model: "claude-sonnet-4-6", MetadataUserID: userIDA}, bodyA, false)
	require.NoError(t, err)
	require.NotNil(t, identityA)
	require.Equal(t, ClaudeOAuthModeShared, identityA.Mode)

	c2, _ := makeContext()
	rewrittenB, identityB, err := svc.prepareOAuthRequestIdentity(c2.Request.Context(), c2, account, &ParsedRequest{Model: "claude-sonnet-4-6", MetadataUserID: userIDB}, bodyB, false)
	require.NoError(t, err)
	require.NotNil(t, identityB)
	require.Equal(t, identityA.Slot, identityB.Slot)

	parsedA := ParseMetadataUserID(gjson.GetBytes(rewrittenA, "metadata.user_id").String())
	parsedB := ParseMetadataUserID(gjson.GetBytes(rewrittenB, "metadata.user_id").String())
	require.NotNil(t, parsedA)
	require.NotNil(t, parsedB)
	require.Equal(t, parsedA.DeviceID, parsedB.DeviceID)
}

func TestPrepareOAuthRequestIdentity_RejectsMissingAccountUUID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"",
		"00000000-0000-4000-8000-000000000006",
		"2.1.80",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-sonnet-4-6", originalUserID, req.Header.Get("User-Agent"), "hello")
	parsed := &ParsedRequest{
		Model:          "claude-sonnet-4-6",
		MetadataUserID: originalUserID,
	}

	_, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.ErrorIs(t, err, ErrClaudeOAuthAccountUUIDRequired)
	var clientErr *ClientRequestError
	require.ErrorAs(t, err, &clientErr)
	require.Equal(t, http.StatusBadRequest, clientErr.StatusCode)
	require.Equal(t, "invalid_request_error", clientErr.ErrorType)
	require.Contains(t, clientErr.Message, "account.extra.account_uuid")
	require.Nil(t, identity)
	require.Empty(t, rec.Body.String())
}

func TestPrepareOAuthRequestIdentity_SetupTokenForcesEmptyAccountUUID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"client-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.80",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
		Extra: map[string]any{
			"account_uuid":      "configured-account-uuid",
			"claude_oauth_mode": ClaudeOAuthModeCarpool,
		},
	}
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-haiku-4-5-20251001", originalUserID, req.Header.Get("User-Agent"), "hello")
	parsed := &ParsedRequest{
		Model:          "claude-haiku-4-5-20251001",
		MetadataUserID: originalUserID,
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)

	rewritten := ParseMetadataUserID(gjson.GetBytes(newBody, "metadata.user_id").String())
	require.NotNil(t, rewritten)
	require.Empty(t, rewritten.AccountUUID)
	require.NotEqual(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rewritten.DeviceID)
	require.NotEqual(t, "00000000-0000-4000-8000-000000000006", rewritten.SessionID)
}

func TestPrepareOAuthRequestIdentity_SetupTokenPreservesAccountUUIDWhenGroupGateDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"client-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.80",
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, &Group{
		ID:                             22,
		Platform:                       PlatformAnthropic,
		Status:                         StatusActive,
		Hydrated:                       true,
		ClaudeOAuthRequestGateDisabled: true,
	}))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
	c.Request = req

	svc := &GatewayService{
		identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
	}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
		Extra: map[string]any{
			"account_uuid":      "configured-account-uuid",
			"claude_oauth_mode": ClaudeOAuthModeCarpool,
		},
	}
	body := expectedClaudeCodeOAuthBodyForTest(t, "claude-haiku-4-5-20251001", originalUserID, req.Header.Get("User-Agent"), "hello")
	parsed := &ParsedRequest{
		Model:          "claude-haiku-4-5-20251001",
		MetadataUserID: originalUserID,
	}

	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, identity)

	rewritten := ParseMetadataUserID(gjson.GetBytes(newBody, "metadata.user_id").String())
	require.NotNil(t, rewritten)
	require.Equal(t, "client-account-uuid", rewritten.AccountUUID)
	require.NotEqual(t, "configured-account-uuid", rewritten.AccountUUID)
	require.NotEqual(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rewritten.DeviceID)
	require.NotEqual(t, "00000000-0000-4000-8000-000000000006", rewritten.SessionID)
}

func TestBuildUpstreamRequest_OAuthRewritesBillingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	svc := &GatewayService{}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}
	body := []byte(`{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.79.abc; cc_entrypoint=sdk-cli; cch=fffff;"}],"metadata":{"user_id":"user_deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbe_account__session_11111111-1111-4111-8111-111111111111"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	oauthIdentity := &oauthRequestIdentity{
		Fingerprint: &Fingerprint{
			UserAgent:               "claude-cli/2.1.80 (external, cli)",
			StainlessLang:           "js",
			StainlessPackageVersion: "0.74.0",
			StainlessOS:             "Linux",
			StainlessArch:           "x64",
			StainlessRuntime:        "node",
			StainlessRuntimeVersion: "v24.3.0",
			XApp:                    "cli",
			AnthropicVersion:        "2023-06-01",
		},
	}

	req, err := svc.buildUpstreamRequest(context.Background(), nil, account, body, "fake-token", "oauth", "claude-sonnet-4-6", true, oauthIdentity)
	require.NoError(t, err)
	defer func() {
		_ = req.Body.Close()
	}()

	gotBody, err := io.ReadAll(req.Body)
	require.NoError(t, err)

	wantBody := expectedOAuthBillingBodyForTest(t, body, oauthIdentity.Fingerprint.UserAgent)
	require.JSONEq(t, string(wantBody), string(gotBody))
	require.Equal(t, gjson.GetBytes(wantBody, "system.0.text").String(), gjson.GetBytes(gotBody, "system.0.text").String())
}

func TestBuildCountTokensRequest_OAuthRewritesBillingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	svc := &GatewayService{}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}
	body := []byte(`{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.79.abc; cc_entrypoint=sdk-cli; cch=fffff;"}],"metadata":{"user_id":"user_deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbe_account__session_11111111-1111-4111-8111-111111111111"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	oauthIdentity := &oauthRequestIdentity{
		Fingerprint: &Fingerprint{
			UserAgent:               "claude-cli/2.1.80 (external, cli)",
			StainlessLang:           "js",
			StainlessPackageVersion: "0.74.0",
			StainlessOS:             "Linux",
			StainlessArch:           "x64",
			StainlessRuntime:        "node",
			StainlessRuntimeVersion: "v24.3.0",
			XApp:                    "cli",
			AnthropicVersion:        "2023-06-01",
		},
	}

	req, err := svc.buildCountTokensRequest(context.Background(), nil, account, body, "fake-token", "oauth", "claude-sonnet-4-6", oauthIdentity)
	require.NoError(t, err)
	defer func() {
		_ = req.Body.Close()
	}()

	gotBody, err := io.ReadAll(req.Body)
	require.NoError(t, err)

	wantBody := expectedOAuthBillingBodyForTest(t, body, oauthIdentity.Fingerprint.UserAgent)
	require.JSONEq(t, string(wantBody), string(gotBody))
	require.Equal(t, gjson.GetBytes(wantBody, "system.0.text").String(), gjson.GetBytes(gotBody, "system.0.text").String())
}

func TestBuildUpstreamRequest_OAuthUsesSlotHeadersAndKeepsDynamicHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.0.0 (external, cli)")
	c.Request.Header.Set("X-Stainless-Retry-Count", "7")
	c.Request.Header.Set("X-Stainless-Timeout", "777")
	c.Request.Header.Set("anthropic-beta", "context-1m-2025-08-07")

	svc := &GatewayService{}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}
	userID := FormatMetadataUserID(strings.Repeat("d", 64), "", "11111111-1111-4111-8111-111111111111", "2.1.77")
	body := []byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":` + strconvQuote(userID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	oauthIdentity := &oauthRequestIdentity{
		Fingerprint: &Fingerprint{
			UserAgent:               "claude-cli/2.1.81 (external, cli)",
			StainlessLang:           "js",
			StainlessPackageVersion: "0.74.0",
			StainlessOS:             "Linux",
			StainlessArch:           "x64",
			StainlessRuntime:        "node",
			StainlessRuntimeVersion: "v24.3.0",
			XApp:                    "cli",
			AnthropicBeta:           "claude-code-20250219,effort-2025-11-24,afk-mode-2026-01-31",
			AnthropicVersion:        "2023-06-01",
			DangerousDirectAccess:   "true",
		},
	}

	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID:                       1,
		Platform:                 PlatformAnthropic,
		Status:                   StatusActive,
		Hydrated:                 true,
		AllowClaudeContext1MBeta: false,
	})
	req, err := svc.buildUpstreamRequest(ctx, c, account, body, "fake-token", "oauth", "claude-sonnet-4-6", true, oauthIdentity)
	require.NoError(t, err)

	require.Equal(t, "Bearer fake-token", req.Header.Get("authorization"))
	require.Equal(t, "claude-cli/2.1.81 (external, cli)", req.Header.Get("user-agent"))
	require.Equal(t, "js", req.Header.Get("x-stainless-lang"))
	require.Equal(t, "0.74.0", req.Header.Get("x-stainless-package-version"))
	require.Equal(t, "7", req.Header.Get("x-stainless-retry-count"))
	require.Equal(t, "777", req.Header.Get("x-stainless-timeout"))
	require.Equal(t, "claude-code-20250219,oauth-2025-04-20,effort-2025-11-24", req.Header.Get("anthropic-beta"))
	require.Equal(t, "cli", req.Header.Get("x-app"))
	require.Equal(t, "true", req.Header.Get("anthropic-dangerous-direct-browser-access"))
	require.Equal(t, "application/json", req.Header.Get("accept"))
	require.Empty(t, req.Header.Get("x-stainless-helper-method"))
}

func TestBuildCountTokensRequest_OAuthKeepsSlotHeadersAndAddsTokenCountingOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("X-Stainless-Retry-Count", "3")
	c.Request.Header.Set("anthropic-beta", "context-1m-2025-08-07")

	svc := &GatewayService{}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}
	userID := FormatMetadataUserID(strings.Repeat("d", 64), "", "11111111-1111-4111-8111-111111111111", "2.1.77")
	body := []byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":` + strconvQuote(userID) + `},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	oauthIdentity := &oauthRequestIdentity{
		Fingerprint: &Fingerprint{
			UserAgent:               "claude-cli/2.1.81 (external, cli)",
			StainlessLang:           "js",
			StainlessPackageVersion: "0.74.0",
			StainlessOS:             "Linux",
			StainlessArch:           "x64",
			StainlessRuntime:        "node",
			StainlessRuntimeVersion: "v24.3.0",
			XApp:                    "cli",
			AnthropicBeta:           "claude-code-20250219,effort-2025-11-24",
			AnthropicVersion:        "2023-06-01",
			DangerousDirectAccess:   "true",
		},
	}

	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID:                       1,
		Platform:                 PlatformAnthropic,
		Status:                   StatusActive,
		Hydrated:                 true,
		AllowClaudeContext1MBeta: false,
	})
	req, err := svc.buildCountTokensRequest(ctx, c, account, body, "fake-token", "oauth", "claude-sonnet-4-6", oauthIdentity)
	require.NoError(t, err)

	require.Equal(t, "3", req.Header.Get("x-stainless-retry-count"))
	require.Equal(t, "claude-code-20250219,oauth-2025-04-20,effort-2025-11-24,token-counting-2024-11-01", req.Header.Get("anthropic-beta"))
	require.Equal(t, "11111111-1111-4111-8111-111111111111", req.Header.Get("x-claude-code-session-id"))
	require.Empty(t, req.Header.Get("x-stainless-helper-method"))
}

func TestBuildUpstreamRequest_OAuthPreservesContext1MWhenGroupAllowsIt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("anthropic-beta", "context-1m-2025-08-07")

	svc := &GatewayService{}
	account := &Account{ID: 123, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	body := []byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":"user_deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbe_account__session_11111111-1111-4111-8111-111111111111"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID:                       1,
		Platform:                 PlatformAnthropic,
		Status:                   StatusActive,
		Hydrated:                 true,
		AllowClaudeContext1MBeta: true,
	})

	req, err := svc.buildUpstreamRequest(ctx, c, account, body, "fake-token", "oauth", "claude-sonnet-4-6", true, nil)
	require.NoError(t, err)
	require.Contains(t, req.Header.Get("anthropic-beta"), claude.BetaContext1M)
}

func TestBuildUpstreamRequest_OAuthPinnedRewritesSessionHeaderToMatchMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &identityCacheStub{}
	svc := &GatewayService{identityService: NewIdentityService(cache, strings.Repeat("x", 32))}

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
	ctx := WithClaudeOAuthGroupMode(SetClaudeCodeClient(req.Context(), true), 22, ClaudeOAuthModePinned)
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", "claude-cli/2.1.101 (external, sdk-cli)")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,interleaved-thinking-2025-05-14,claude-code-20250219")
	req.Header.Set("X-Claude-Code-Session-Id", originalSessionID)
	c.Request = req

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":      "forced-account-uuid",
			"claude_oauth_mode": ClaudeOAuthModePinned,
		},
	}
	body := expectedOAuthBillingBodyForTest(t,
		[]byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":`+strconvQuote(originalUserID)+`},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.101.000; cc_entrypoint=sdk-cli; cch=00000;"}]}`),
		req.Header.Get("User-Agent"),
	)
	parsed := &ParsedRequest{Model: "claude-sonnet-4-6", MetadataUserID: originalUserID}

	rewrittenBody, oauthIdentity, err := svc.prepareOAuthRequestIdentity(ctx, c, account, parsed, body, false)
	require.NoError(t, err)
	require.NotNil(t, oauthIdentity)
	require.Equal(t, ClaudeOAuthModePinned, oauthIdentity.Mode)

	upstreamReq, err := svc.buildUpstreamRequest(ctx, c, account, rewrittenBody, "real-oauth-token", "oauth", "claude-sonnet-4-6", true, oauthIdentity)
	require.NoError(t, err)
	defer func() { _ = upstreamReq.Body.Close() }()

	gotBody, err := io.ReadAll(upstreamReq.Body)
	require.NoError(t, err)
	rewrittenUserID := ParseMetadataUserID(gjson.GetBytes(gotBody, "metadata.user_id").String())
	require.NotNil(t, rewrittenUserID)
	require.NotEqual(t, originalSessionID, rewrittenUserID.SessionID)
	require.Equal(t, rewrittenUserID.SessionID, upstreamReq.Header.Get("x-claude-code-session-id"))
}

func TestBuildUpstreamRequest_OAuthTraceLikeHeadersStayAligned(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Accept", "application/json")
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Authorization", "Bearer client-should-not-pass")
	c.Request.Header.Set("Connection", "keep-alive")
	c.Request.Header.Set("Cookie", "session=client-cookie")
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.81 (external, cli)")
	c.Request.Header.Set("X-Stainless-Arch", "x64")
	c.Request.Header.Set("X-Claude-Code-Experimental", "future-proof")
	c.Request.Header.Set("X-Stainless-Lang", "js")
	c.Request.Header.Set("X-Stainless-OS", "Linux")
	c.Request.Header.Set("X-Stainless-Package-Version", "0.74.0")
	c.Request.Header.Set("X-Stainless-Retry-Count", "0")
	c.Request.Header.Set("X-Stainless-Runtime", "node")
	c.Request.Header.Set("X-Stainless-Runtime-Version", "v24.3.0")
	c.Request.Header.Set("X-Stainless-Timeout", "600")
	c.Request.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20,effort-2025-11-24")
	c.Request.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	c.Request.Header.Set("anthropic-version", "2023-06-01")
	c.Request.Header.Set("x-app", "cli")
	c.Request.Header.Set("X-Claude-Code-Session-Id", "00000000-0000-4000-8000-000000000002")
	c.Request.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")

	svc := &GatewayService{}
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}
	body := []byte(`{"model":"claude-sonnet-4-6","stream":true,"metadata":{"user_id":"{\"device_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"account_uuid\":\"00000000-0000-4000-8000-000000000001\",\"session_id\":\"00000000-0000-4000-8000-000000000002\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.000; cc_entrypoint=cli; cch=00000;"}]}`)
	oauthIdentity := &oauthRequestIdentity{
		Fingerprint: &Fingerprint{
			UserAgent:               "claude-cli/2.1.81 (external, cli)",
			StainlessLang:           "js",
			StainlessPackageVersion: "0.74.0",
			StainlessOS:             "Linux",
			StainlessArch:           "x64",
			StainlessRuntime:        "node",
			StainlessRuntimeVersion: "v24.3.0",
			XApp:                    "cli",
			AnthropicBeta:           "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20,effort-2025-11-24",
			AnthropicVersion:        "2023-06-01",
			DangerousDirectAccess:   "true",
		},
	}

	req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "real-oauth-token", "oauth", "claude-sonnet-4-6", true, oauthIdentity)
	require.NoError(t, err)

	require.Equal(t, "Bearer real-oauth-token", req.Header.Get("authorization"))
	require.Equal(t, "application/json", req.Header.Get("accept"))
	require.Equal(t, "application/json", req.Header.Get("content-type"))
	require.Equal(t, "claude-cli/2.1.81 (external, cli)", req.Header.Get("user-agent"))
	require.Equal(t, "future-proof", req.Header.Get("x-claude-code-experimental"))
	require.Equal(t, "x64", req.Header.Get("x-stainless-arch"))
	require.Equal(t, "js", req.Header.Get("x-stainless-lang"))
	require.Equal(t, "Linux", req.Header.Get("x-stainless-os"))
	require.Equal(t, "0.74.0", req.Header.Get("x-stainless-package-version"))
	require.Equal(t, "0", req.Header.Get("x-stainless-retry-count"))
	require.Equal(t, "node", req.Header.Get("x-stainless-runtime"))
	require.Equal(t, "v24.3.0", req.Header.Get("x-stainless-runtime-version"))
	require.Equal(t, "600", req.Header.Get("x-stainless-timeout"))
	require.Equal(t, "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20,effort-2025-11-24", req.Header.Get("anthropic-beta"))
	require.Equal(t, "true", req.Header.Get("anthropic-dangerous-direct-browser-access"))
	require.Equal(t, "2023-06-01", req.Header.Get("anthropic-version"))
	require.Equal(t, "cli", req.Header.Get("x-app"))
	require.Equal(t, "00000000-0000-4000-8000-000000000002", req.Header.Get("x-claude-code-session-id"))
	require.Equal(t, "Bearer real-oauth-token", req.Header.Get("authorization"))
	require.Empty(t, req.Header.Get("connection"))
	require.Empty(t, req.Header.Get("cookie"))
	require.Equal(t, "gzip, deflate, br, zstd", req.Header.Get("accept-encoding"))
	require.Empty(t, req.Header.Get("x-stainless-helper-method"))
}

func expectedOAuthBillingBodyForTest(t *testing.T, body []byte, userAgent string) []byte {
	t.Helper()

	version := ExtractCLIVersion(userAgent)
	prompt, err := claudebilling.ExtractFirstUserText(body)
	require.NoError(t, err)
	suffix := claudebilling.ComputeCCVersionSuffix(prompt, version)
	bodyWithVersion, err := claudebilling.ReplaceCCVersion(body, version, suffix)
	require.NoError(t, err)
	normalized, match, err := claudebilling.NormalizeBodyForCCH(bodyWithVersion)
	require.NoError(t, err)
	seed, mode := claudebilling.CCHProfileForCCVersion(version)
	_, cch := claudebilling.ComputeCCHWithProfile(normalized, seed, mode)
	return claudebilling.ReplaceCCH(normalized, match, cch)
}

type accountRepoStubForOAuthMode struct {
	listByGroupFunc  func(ctx context.Context, groupID int64) ([]Account, error)
	listByGroupCalls int
}

func (s *accountRepoStubForOAuthMode) Create(context.Context, *Account) error { return nil }
func (s *accountRepoStubForOAuthMode) GetByID(context.Context, int64) (*Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) GetByIDs(context.Context, []int64) ([]*Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ExistsByID(context.Context, int64) (bool, error) {
	return false, nil
}
func (s *accountRepoStubForOAuthMode) GetByCRSAccountID(context.Context, string) (*Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) FindByExtraField(context.Context, string, any) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ListCRSAccountIDs(context.Context) (map[string]int64, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) Update(context.Context, *Account) error { return nil }
func (s *accountRepoStubForOAuthMode) Delete(context.Context, int64) error    { return nil }
func (s *accountRepoStubForOAuthMode) List(context.Context, pagination.PaginationParams) ([]Account, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (s *accountRepoStubForOAuthMode) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string, int64) ([]Account, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (s *accountRepoStubForOAuthMode) ListByGroup(ctx context.Context, groupID int64) ([]Account, error) {
	s.listByGroupCalls++
	if s.listByGroupFunc != nil {
		return s.listByGroupFunc(ctx, groupID)
	}
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ListActive(context.Context) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ListByPlatform(context.Context, string) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) UpdateLastUsed(context.Context, int64) error { return nil }
func (s *accountRepoStubForOAuthMode) BatchUpdateLastUsed(context.Context, map[int64]time.Time) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) SetError(context.Context, int64, string) error     { return nil }
func (s *accountRepoStubForOAuthMode) ClearError(context.Context, int64) error           { return nil }
func (s *accountRepoStubForOAuthMode) SetSchedulable(context.Context, int64, bool) error { return nil }
func (s *accountRepoStubForOAuthMode) AutoPauseExpiredAccounts(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (s *accountRepoStubForOAuthMode) BindGroups(context.Context, int64, []int64) error { return nil }
func (s *accountRepoStubForOAuthMode) ListSchedulable(context.Context) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ListSchedulableByPlatforms(context.Context, []string) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]Account, error) {
	return nil, nil
}
func (s *accountRepoStubForOAuthMode) SetRateLimited(context.Context, int64, time.Time) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) SetModelRateLimit(context.Context, int64, string, time.Time) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) SetOverloaded(context.Context, int64, time.Time) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) ClearTempUnschedulable(context.Context, int64) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) ClearRateLimit(context.Context, int64) error { return nil }
func (s *accountRepoStubForOAuthMode) ClearAntigravityQuotaScopes(context.Context, int64) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) ClearModelRateLimits(context.Context, int64) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) BulkUpdate(context.Context, []int64, AccountBulkUpdate) (int64, error) {
	return 0, nil
}
func (s *accountRepoStubForOAuthMode) IncrementQuotaUsed(context.Context, int64, float64) error {
	return nil
}
func (s *accountRepoStubForOAuthMode) ResetQuotaUsed(context.Context, int64) error { return nil }
