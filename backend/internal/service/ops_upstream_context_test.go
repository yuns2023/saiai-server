package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAppendOpsUpstreamError_UsesRequestBodyBytesFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	setOpsUpstreamRequestBody(c, []byte(`{"model":"gpt-5"}`))
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Kind:    "http_error",
		Message: "upstream failed",
	})

	v, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := v.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, `{"model":"gpt-5"}`, events[0].UpstreamRequestBody)
}

func TestAppendOpsUpstreamError_AttachesSafeOAuthAttribution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	account := &Account{
		ID:       42,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
		Extra: map[string]any{
			"claude_oauth_mode": ClaudeOAuthModeSingleDevice,
		},
	}

	SetOpsClaudeOAuthSelectionAttribution(c, account, "failover", "count_tokens")
	beforeHash := sha256.Sum256([]byte(`{"metadata":{"user_id":"before"}}`))
	updateOpsClaudeOAuthIdentityAttribution(c, account, beforeHash, []byte(`{"metadata":{"user_id":"after"}}`), &oauthRequestIdentity{
		TransportAccountID: 99,
	})
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		AccountID: account.ID,
		Kind:      "http_error",
		Message:   "upstream failed",
	})

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, &OpsOAuthAttribution{
		AccountType:       AccountTypeSetupToken,
		TrafficMode:       ClaudeOAuthModeSingleDevice,
		SelectionSource:   "failover",
		RequestKind:       "count_tokens",
		IdentityPrepared:  true,
		IdentityRewritten: true,
		TransportIsolated: true,
	}, events[0].OAuth)

	SetOpsClaudeOAuthSelectionAttribution(c, account, "scheduler", "messages")
	next, ok := GetOpsOAuthAttribution(c)
	require.True(t, ok)
	require.False(t, next.IdentityPrepared)
	require.False(t, next.IdentityRewritten)
	require.True(t, events[0].OAuth.IdentityPrepared, "prior error attempt must keep its attribution snapshot")

	encoded, err := json.Marshal(events[0])
	require.NoError(t, err)
	for _, forbidden := range []string{"before", "after", "device_id", "session_id", "access_token", "oauth_token"} {
		require.NotContains(t, strings.ToLower(string(encoded)), forbidden)
	}
}

func TestSetOpsClaudeOAuthSelectionAttribution_IgnoresNonOAuthAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	SetOpsClaudeOAuthSelectionAttribution(c, &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
	}, "scheduler", "messages")

	_, ok := GetOpsOAuthAttribution(c)
	require.False(t, ok)
}

func TestSetOpsClaudeOAuthSelectionAttribution_PreservesStickyBindingSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode": ClaudeOAuthModeShared,
		},
	}

	for _, source := range []string{"sticky_confirmed", "sticky_pending", "sticky"} {
		SetOpsClaudeOAuthSelectionAttribution(c, account, source, "messages")
		got, ok := GetOpsOAuthAttribution(c)
		require.True(t, ok)
		require.Equal(t, source, got.SelectionSource)
	}
}

func TestLogClaudeOAuthAttribution_WritesOnceToOpsSinkAboveRuntimeLogLevel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, releaseLogCapture := captureStructuredLog(t)
	defer releaseLogCapture()
	require.NoError(t, logger.SetLevel("error"))
	defer func() { require.NoError(t, logger.SetLevel("debug")) }()

	captured := make([]*OpsInsertSystemLogInput, 0, 1)
	flushed := make(chan struct{}, 1)
	repo := &opsRepoMock{
		BatchInsertSystemLogsFn: func(_ context.Context, inputs []*OpsInsertSystemLogInput) (int64, error) {
			captured = append(captured, inputs...)
			select {
			case flushed <- struct{}{}:
			default:
			}
			return int64(len(inputs)), nil
		},
	}
	sink := NewOpsSystemLogSink(repo)
	sink.batchSize = 1
	sink.flushInterval = time.Hour
	sink.Start()
	logger.SetSink(sink)
	defer func() {
		logger.SetSink(nil)
		sink.Stop()
	}()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	requestCtx := context.WithValue(context.Background(), ctxkey.RequestID, "req-oauth-audit")
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(requestCtx)
	account := &Account{
		ID:       42,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode": ClaudeOAuthModeShared,
		},
	}
	SetOpsClaudeOAuthSelectionAttribution(c, account, "sticky_pending", "messages")
	beforeHash := sha256.Sum256([]byte(`{"metadata":{"user_id":"private-before"}}`))
	updateOpsClaudeOAuthIdentityAttribution(c, account, beforeHash, []byte(`{"metadata":{"user_id":"private-after"}}`), &oauthRequestIdentity{
		NativeBillingStyle: true,
		TransportAccountID: 99,
	})

	logClaudeOAuthAttribution(c, account, true)
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OAuth attribution sink flush")
	}
	require.Len(t, captured, 1, "one attribution attempt must create one indexed record")
	item := captured[0]
	require.Equal(t, "info", item.Level)
	require.Equal(t, opsClaudeOAuthAuditComponent, item.Component)
	require.Equal(t, "claude_oauth_request_attribution", item.Message)
	require.Equal(t, "req-oauth-audit", item.RequestID)
	require.NotNil(t, item.AccountID)
	require.Equal(t, account.ID, *item.AccountID)
	require.Equal(t, PlatformAnthropic, item.Platform)

	var extra map[string]any
	require.NoError(t, json.Unmarshal([]byte(item.ExtraJSON), &extra))
	require.Equal(t, AccountTypeOAuth, extra["account_type"])
	require.Equal(t, ClaudeOAuthModeShared, extra["traffic_mode"])
	require.Equal(t, "sticky_pending", extra["selection_source"])
	require.Equal(t, "messages", extra["request_kind"])
	require.Equal(t, "prepared", extra["stage"])
	require.Equal(t, true, extra["identity_prepared"])
	require.Equal(t, true, extra["identity_rewritten"])
	require.Equal(t, true, extra["native_billing"])
	require.Equal(t, true, extra["transport_isolated"])

	encoded := strings.ToLower(item.ExtraJSON)
	for _, forbidden := range []string{"private-before", "private-after", "device_id", "session_id", "account_uuid", "access_token", "oauth_token"} {
		require.NotContains(t, encoded, forbidden)
	}
}

func TestSanitizeOpsUpstreamErrors_DropsUnrecognizedOAuthAttribution(t *testing.T) {
	entry := &OpsInsertErrorLogInput{UpstreamErrors: []*OpsUpstreamErrorEvent{{
		Kind:    "http_error",
		Message: "upstream failed",
		OAuth: &OpsOAuthAttribution{
			AccountType: "oauth-secret-value",
			TrafficMode: "carpool-secret-value",
		},
	}}}

	require.NoError(t, sanitizeOpsUpstreamErrors(entry))
	require.NotNil(t, entry.UpstreamErrorsJSON)
	events, err := ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Nil(t, events[0].OAuth)
}

func TestAppendOpsUpstreamError_UsesRequestBodyStringFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	c.Set(OpsUpstreamRequestBodyKey, `{"model":"gpt-4"}`)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Kind:    "request_error",
		Message: "dial timeout",
	})

	v, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := v.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, `{"model":"gpt-4"}`, events[0].UpstreamRequestBody)
}
