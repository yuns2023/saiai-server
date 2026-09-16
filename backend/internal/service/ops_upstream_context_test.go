package service

import (
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

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
