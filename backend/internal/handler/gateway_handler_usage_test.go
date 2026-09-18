//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayUsageReturnsSubscriptionAndKeyQuotaTogether(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now()
	dailyLimit := 10.0
	group := &service.Group{
		ID:               42,
		Name:             "Team subscription",
		SubscriptionType: service.SubscriptionTypeSubscription,
		DailyLimitUSD:    &dailyLimit,
	}
	user := &service.User{ID: 7, Balance: 99}
	apiKey := &service.APIKey{
		ID:        100,
		UserID:    user.ID,
		Status:    service.StatusAPIKeyActive,
		Quota:     20,
		QuotaUsed: 3,
		User:      user,
		Group:     group,
	}
	subscription := &service.UserSubscription{
		ID:               55,
		UserID:           user.ID,
		GroupID:          group.ID,
		Status:           service.SubscriptionStatusActive,
		ExpiresAt:        now.Add(48 * time.Hour),
		DailyWindowStart: ptrTime(now.Add(-time.Hour)),
		DailyUsageUSD:    4,
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID})
	c.Set(string(middleware2.ContextKeySubscription), subscription)

	(&GatewayHandler{}).Usage(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Contains(t, recorder.Header().Values("Vary"), "Authorization")
	require.Contains(t, recorder.Header().Values("Vary"), "X-API-Key")

	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "quota_limited", response["mode"])
	require.Equal(t, "subscription", response["billing_mode"])
	require.Equal(t, "Team subscription", response["planName"])
	require.Equal(t, float64(17), response["remaining"])

	quota := response["quota"].(map[string]any)
	require.Equal(t, float64(17), quota["remaining"])
	sub := response["subscription"].(map[string]any)
	require.Equal(t, true, sub["shared"])
	require.Equal(t, float64(4), sub["daily_usage_usd"])
	require.Equal(t, float64(6), sub["remaining"])
	require.NotEmpty(t, sub["daily_reset_at"])
}

func TestGatewayUsageReturnsWalletAndKeyQuotaTogether(t *testing.T) {
	gin.SetMode(gin.TestMode)
	user := &service.User{ID: 8, Balance: 9}
	apiKey := &service.APIKey{
		ID:        101,
		UserID:    user.ID,
		Status:    service.StatusAPIKeyActive,
		Quota:     25,
		QuotaUsed: 7,
		User:      user,
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID})

	(&GatewayHandler{}).Usage(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "quota_limited", response["mode"])
	require.Equal(t, "balance", response["billing_mode"])
	require.Equal(t, float64(18), response["remaining"])
	require.Equal(t, float64(9), response["balance"])
}

func TestBuildSubscriptionUsageDetailsTreatsExpiredWindowsAsReset(t *testing.T) {
	dailyLimit := 12.0
	weeklyLimit := 30.0
	group := &service.Group{
		DailyLimitUSD:  &dailyLimit,
		WeeklyLimitUSD: &weeklyLimit,
	}
	now := time.Now()
	subscription := &service.UserSubscription{
		Status:            service.SubscriptionStatusActive,
		ExpiresAt:         now.Add(24 * time.Hour),
		DailyWindowStart:  ptrTime(now.Add(-25 * time.Hour)),
		WeeklyWindowStart: ptrTime(now.Add(-time.Hour)),
		DailyUsageUSD:     11,
		WeeklyUsageUSD:    9,
	}

	details := buildSubscriptionUsageDetails(group, subscription)

	require.Equal(t, float64(0), details["daily_usage_usd"])
	require.NotContains(t, details, "daily_reset_at")
	require.Equal(t, float64(9), details["weekly_usage_usd"])
	require.Equal(t, float64(12), details["remaining"])
	require.Contains(t, details, "weekly_reset_at")
	// The read-only query must not mutate the cached subscription object.
	require.Equal(t, float64(11), subscription.DailyUsageUSD)
	require.NotNil(t, subscription.DailyWindowStart)
}

func TestUsageAPIKeyStatusUsesRuntimeExpiry(t *testing.T) {
	expiresAt := time.Now().Add(-time.Minute)
	apiKey := &service.APIKey{
		Status:    service.StatusAPIKeyActive,
		ExpiresAt: &expiresAt,
	}
	require.Equal(t, service.StatusAPIKeyExpired, usageAPIKeyStatus(apiKey))
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
