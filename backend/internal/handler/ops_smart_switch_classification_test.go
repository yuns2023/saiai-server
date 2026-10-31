package handler

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Smart switching reports an exhausted policy with the non-switching path's
// business codes; only a mix with platform reasons stays in the SLA.
func TestOpsClassifiesSmartSwitchExhaustion(t *testing.T) {
	for _, tc := range []struct {
		status   int
		body     string
		business bool
	}{
		{http.StatusForbidden, `{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance; no other group in the smart switching policy is available (subscription_limit_or_expiry, insufficient_balance)"}`, true},
		{http.StatusTooManyRequests, `{"code":"USAGE_LIMIT_EXCEEDED","message":"Every group in the smart switching policy is blocked by a usage limit, the Key budget or the subscription status (subscription_limit_or_expiry)"}`, true},
		{http.StatusForbidden, `{"code":"USAGE_LIMIT_EXCEEDED","message":"Every group in the smart switching policy is blocked by a usage limit, the Key budget or the subscription status (subscription_unavailable, payg_budget_exhausted)"}`, true},
		{http.StatusServiceUnavailable, `{"code":"SMART_SWITCH_UNAVAILABLE","message":"No eligible group in the smart switching policy (group_unavailable, insufficient_balance)"}`, false},
	} {
		parsed := parseOpsErrorResponse([]byte(tc.body))
		errType := normalizeOpsErrorType(parsed.ErrorType, parsed.Code)
		phase := classifyOpsPhase(errType, parsed.Message, parsed.Code)
		require.Equal(t, tc.business, classifyOpsIsBusinessLimited(errType, phase, parsed.Code, tc.status, parsed.Message), tc.body)
		if tc.business {
			require.Equal(t, "request", phase)
			require.Equal(t, "client", classifyOpsErrorOwner(phase, parsed.Message))
		}
	}
}
