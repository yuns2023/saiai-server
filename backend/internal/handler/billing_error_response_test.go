package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestOpsClassifiesUserAndKeyLimitsAsBusiness(t *testing.T) {
	reset := time.Now().Add(time.Hour).Format("2006-01-02 15:04 -07:00")
	for _, tc := range []struct {
		errType, code, message string
		business               bool
	}{
		{"rate_limit_error", "", "Concurrency limit exceeded for user, please retry later", true},
		{"rate_limit_error", "", "Concurrency limit exceeded for api key, please retry later", true},
		{"rate_limit_error", "user_concurrency", "too many concurrent requests, please retry later", true},
		{"rate_limit_error", "", "too many concurrent requests for this API key, please retry later", true},
		{"rate_limit_exceeded", "", "api key 5小时限额已用完，将于 " + reset + " 重置", true},
		{"api_error", "API_KEY_RATE_LIMIT_EXCEEDED", "api key 7天限额已用完", true},
		// Platform capacity and upstream limits stay in the SLA.
		{"rate_limit_error", "", "Concurrency limit exceeded for account, please retry later", false},
		{"rate_limit_error", "", "Upstream rate limit exceeded, please retry later", false},
	} {
		errType := normalizeOpsErrorType(tc.errType, tc.code)
		phase := classifyOpsPhase(errType, tc.message, tc.code)
		business := classifyOpsIsBusinessLimited(errType, phase, tc.code, http.StatusTooManyRequests, tc.message)
		require.Equal(t, tc.business, business, tc.message)
		if tc.business {
			require.Equal(t, "request", phase, tc.message)
			require.Equal(t, "client", classifyOpsErrorOwner(phase, tc.message), tc.message)
			require.Equal(t, "P3", classifyOpsBusinessAwareSeverity(errType, http.StatusTooManyRequests, business), tc.message)
		} else {
			require.Equal(t, "P1", classifyOpsBusinessAwareSeverity(errType, http.StatusTooManyRequests, business), tc.message)
		}
	}
}
