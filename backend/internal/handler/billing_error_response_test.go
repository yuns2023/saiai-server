package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBillingErrorResponseStopsRetriesOnKeyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	status, code, message := billingErrorResponse(c, service.ErrAPIKeyRateLimit1dExceeded)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limit_exceeded", code)
	require.Equal(t, "api key 日限额已用完", message)
	require.Equal(t, "false", c.Writer.Header().Get("x-should-retry"))

	// Billing outages stay retryable.
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	status, _, _ = billingErrorResponse(c, service.ErrBillingServiceUnavailable)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Empty(t, c.Writer.Header().Get("x-should-retry"))
}

// Same wording without the gateway's marker (an upstream error relayed with
// it, or a slot store failure) stays in the SLA.
func TestOpsUserLimitNeedsMarkerNotText(t *testing.T) {
	for _, message := range []string{
		"Concurrency limit exceeded for user, please retry later",
		"too many concurrent requests, please retry later",
		"api key 5小时限额已用完",
	} {
		errType := normalizeOpsErrorType("rate_limit_error", "")
		phase := classifyOpsPhase(errType, message, "")
		require.False(t, classifyOpsIsBusinessLimited(errType, phase, "", http.StatusTooManyRequests, message), message)
	}
}

func TestMarkOpsUserConcurrencyOnlyForRealUserOrKeySlots(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		err      error
		slotType string
		marked   bool
	}{
		{&ConcurrencyError{SlotType: "user"}, "user", true},
		{&ConcurrencyError{SlotType: slotTypeAPIKey, IsTimeout: true}, "user", true},
		{&ConcurrencyError{SlotType: "account"}, "account", false},
		{errors.New("redis: connection refused"), "user", false},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		markOpsUserConcurrency(c, tc.err, tc.slotType)
		require.Equal(t, tc.marked, service.IsOpsUserLimit(c), tc.err.Error())
	}
}

func TestBillingErrorResponseMarksKeyLimitOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	billingErrorResponse(c, service.ErrAPIKeyRateLimit7dExceeded)
	require.True(t, service.IsOpsUserLimit(c))

	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	status, _, _ := billingErrorResponse(c, service.ErrInsufficientBalance)
	require.Equal(t, http.StatusForbidden, status)
	require.False(t, service.IsOpsUserLimit(c))
	require.Equal(t, "false", c.Writer.Header().Get("x-should-retry"))
}

// End to end through the logger: the same 429 text is P3/business only when
// the gateway marked it as the user's own limit.
func TestOpsLoggerUsesUserLimitMarker(t *testing.T) {
	for _, marked := range []bool{true, false} {
		prepareOpsLogFilterTestQueue(t)
		ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		r := gin.New()
		r.POST("/v1/messages", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
			if marked {
				service.MarkOpsUserLimit(c)
			}
			c.JSON(http.StatusTooManyRequests, gin.H{"type": "error", "error": gin.H{"type": "rate_limit_error", "message": "Concurrency limit exceeded for user, please retry later"}})
		})
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		require.EqualValues(t, 1, OpsErrorLogEnqueuedTotal())
		entry := (<-opsErrorLogQueue).entry
		require.Equal(t, marked, entry.IsBusinessLimited)
		if marked {
			require.Equal(t, "P3", entry.Severity)
			require.Equal(t, "client", entry.ErrorOwner)
		} else {
			require.Equal(t, "P1", entry.Severity)
		}
	}
}
