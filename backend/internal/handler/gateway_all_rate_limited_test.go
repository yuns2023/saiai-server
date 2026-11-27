//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ListWithFilters serves AllAccountsRateLimitedUntil: every member of the
// group, whatever its state.
func (r entrypointAccountRepo) ListWithFilters(ctx context.Context, _ pagination.PaginationParams, _, _, _, _ string, groupID int64, _ service.AccountListSort) ([]service.Account, *pagination.PaginationResult, error) {
	out, _ := r.ListSchedulableByGroupIDAndPlatform(ctx, groupID, "")
	return out, &pagination.PaginationResult{Total: int64(len(out))}, nil
}

// serveAllLimited runs POST /v1/messages through the Ops logger and returns
// the response and the Ops row.
func serveAllLimited(t *testing.T, h *entrypointHarness, apiKey *service.APIKey) (*httptest.ResponseRecorder, *service.OpsInsertErrorLogInput) {
	t.Helper()
	return serveAllLimitedWith(t, h, apiKey, nil)
}

func serveAllLimitedWith(t *testing.T, h *entrypointHarness, apiKey *service.APIKey, setup func(*gin.Context)) (*httptest.ResponseRecorder, *service.OpsInsertErrorLogInput) {
	t.Helper()
	prepareOpsLogFilterTestQueue(t)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/v1/messages", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: h.user.ID, Concurrency: h.user.Concurrency})
		if setup != nil {
			setup(c)
		}
		h.handler.Messages(c)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(entrypointBody("cli")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", entrypointT3CodeUA)
	r.ServeHTTP(rec, req)
	require.EqualValues(t, 1, OpsErrorLogEnqueuedTotal())
	return rec, (<-opsErrorLogQueue).entry
}

func TestMessagesAllAccountsRateLimitedAnswers429(t *testing.T) {
	cases := []struct {
		name        string
		wait        time.Duration
		noRetry     bool
		extraMember func(*service.Account)
		want503     bool
	}{
		{name: "weekly limit stops retries", wait: 22 * time.Hour, noRetry: true},
		{name: "short limit keeps retries", wait: 2 * time.Minute, noRetry: false},
		{name: "member in error keeps 503", wait: 22 * time.Hour, extraMember: func(a *service.Account) { a.Status = service.StatusError }, want503: true},
		{name: "paused member keeps 503", wait: 22 * time.Hour, extraMember: func(a *service.Account) { a.Schedulable = false }, want503: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groups := map[int64]*service.Group{entrypointGroupA: anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyOff)}
			h := newEntrypointHarness(t, groups, entrypointGroupA)
			resetAt := time.Now().Add(tc.wait)
			h.lab.accounts[0].RateLimitResetAt = &resetAt
			if tc.extraMember != nil {
				extra := h.lab.accounts[0]
				extra.ID++
				extra.RateLimitResetAt = nil
				tc.extraMember(&extra)
				h.lab.accounts = append(h.lab.accounts, extra)
			}

			rec, entry := serveAllLimited(t, h, h.key(entrypointGroupA))
			require.Zero(t, h.lab.upstreamCalls())
			var body struct {
				Type  string `json:"type"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
			require.Equal(t, "error", body.Type)

			if tc.want503 {
				require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
				require.Equal(t, "api_error", body.Error.Type)
				require.Empty(t, rec.Header().Get("Retry-After"))
				require.Empty(t, rec.Header().Get("x-should-retry"))
				require.Equal(t, "routing", entry.ErrorPhase)
				return
			}

			require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
			require.Equal(t, "rate_limit_error", body.Error.Type)
			require.Equal(t, service.AllAccountsRateLimitedMessage(resetAt), body.Error.Message)
			retryAfter, err := strconv.ParseInt(rec.Header().Get("Retry-After"), 10, 64)
			require.NoError(t, err)
			require.InDelta(t, tc.wait.Seconds(), float64(retryAfter), 2)
			if tc.noRetry {
				require.Equal(t, "false", rec.Header().Get("x-should-retry"))
			} else {
				require.Empty(t, rec.Header().Get("x-should-retry"))
			}

			// Still a visible routing failure owned by the platform.
			require.Equal(t, http.StatusTooManyRequests, entry.StatusCode)
			require.Equal(t, "routing", entry.ErrorPhase)
			require.Equal(t, "platform", entry.ErrorOwner)
			require.Equal(t, "gateway", entry.ErrorSource)
			require.Equal(t, "P1", entry.Severity)
			require.False(t, entry.IsBusinessLimited)
		})
	}
}

// The same text without the marker (e.g. relayed from an upstream) is not
// classified as the gateway's routing failure.
func TestOpsRoutingRateLimitedNeedsMarker(t *testing.T) {
	msg := service.AllAccountsRateLimitedMessage(time.Now().Add(time.Hour))
	require.Equal(t, "upstream", classifyOpsPhase("rate_limit_error", msg, ""))
}

func TestAllAccountsRateLimitedRetryHeaders(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		wait        time.Duration
		retryAfter  int64
		shouldRetry bool
	}{
		{wait: 1500 * time.Millisecond, retryAfter: 2, shouldRetry: true},
		{wait: 0, retryAfter: 1, shouldRetry: true},
		{wait: -time.Second, retryAfter: 1, shouldRetry: true},
		{wait: 5 * time.Minute, retryAfter: 300, shouldRetry: true},
		{wait: 5*time.Minute + time.Second, retryAfter: 301, shouldRetry: false},
		{wait: 22 * time.Hour, retryAfter: 79200, shouldRetry: false},
	} {
		retryAfter, shouldRetry := allAccountsRateLimitedRetryHeaders(now.Add(tc.wait), now)
		require.Equal(t, tc.retryAfter, retryAfter, tc.wait.String())
		require.Equal(t, tc.shouldRetry, shouldRetry, tc.wait.String())
	}
}

func TestSmartSwitchSpansGroups(t *testing.T) {
	a, b := int64(55), int64(56)
	policy := func(enabled bool, ids ...int64) *service.APIKey {
		return &service.APIKey{GroupID: &a, SmartSwitch: &domain.KeySmartSwitchPolicy{Enabled: enabled, GroupIDs: ids}}
	}
	require.False(t, smartSwitchSpansGroups(nil))
	require.False(t, smartSwitchSpansGroups(&service.APIKey{GroupID: &a}))
	require.False(t, smartSwitchSpansGroups(policy(false, a, b)), "disabled policy")
	require.False(t, smartSwitchSpansGroups(policy(true, a)), "policy of the bound group only")
	require.True(t, smartSwitchSpansGroups(policy(true, a, b)))
	require.True(t, smartSwitchSpansGroups(policy(true, b)), "policy group differs from the bound group")
	switched := policy(true, b)
	switched.GroupID, switched.SmartSwitchOriginalGroupID = &b, &a
	require.True(t, smartSwitchSpansGroups(switched))
}

// A multi-group smart switch policy keeps the 503 even when the switch to
// group B did not happen: B's read failed, or B was skipped because it
// recovers sooner. A reset for A alone would tell the client to wait for 22 h.
func TestMessagesAllAccountsRateLimitedKeeps503WithMultiGroupSmartSwitch(t *testing.T) {
	for _, tc := range []struct {
		name string
		next middleware2.SmartSwitchNext
	}{
		{name: "group B read fails", next: func(map[int64]bool) (*service.SmartSwitchSelection, error) {
			return nil, errors.New("TEST_ONLY group read failure")
		}},
		{name: "group B recovers sooner and is skipped", next: func(map[int64]bool) (*service.SmartSwitchSelection, error) {
			return nil, service.ErrSmartSwitchUnavailable
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups := map[int64]*service.Group{
				entrypointGroupA: anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyOff),
				entrypointGroupB: anthropicGroup(entrypointGroupB, service.ClaudeEntrypointPolicyOff),
			}
			h := newEntrypointHarness(t, groups, entrypointGroupA)
			resetAt := time.Now().Add(22 * time.Hour)
			h.lab.accounts[0].RateLimitResetAt = &resetAt
			key := h.key(entrypointGroupA)
			key.SmartSwitch = &domain.KeySmartSwitchPolicy{Enabled: true, GroupIDs: []int64{entrypointGroupA, entrypointGroupB}}

			rec, entry := serveAllLimitedWith(t, h, key, func(c *gin.Context) { c.Set("smart_switch_next", tc.next) })
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			require.Empty(t, rec.Header().Get("Retry-After"))
			require.Empty(t, rec.Header().Get("x-should-retry"))
			require.Equal(t, "routing", entry.ErrorPhase)
			require.Zero(t, h.lab.upstreamCalls())
		})
	}
}

// After an upstream 503 the failover loop clears the exclusion set and
// reselects; that reselection failing is not a routing-only failure, so it
// must not become the "all accounts rate limited" 429.
func TestMessagesAllAccountsRateLimitedNotAfterUpstreamAttempt(t *testing.T) {
	groups := map[int64]*service.Group{entrypointGroupA: anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyOff)}
	h := newEntrypointHarness(t, groups, entrypointGroupA)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			// The account hits its limit while the request is in flight.
			resetAt := time.Now().Add(22 * time.Hour)
			h.lab.mu.Lock()
			h.lab.accounts[0].RateLimitResetAt = &resetAt
			h.lab.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"TEST_ONLY unavailable"}}`))
	}))
	t.Cleanup(upstream.Close)
	h.lab.accounts[0].Credentials = map[string]any{"api_key": "TEST_ONLY_UPSTREAM", "base_url": upstream.URL}

	rec, _ := serveAllLimited(t, h, h.key(entrypointGroupA))
	require.GreaterOrEqual(t, calls.Load(), int32(1))
	require.NotEqual(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	require.Empty(t, rec.Header().Get("Retry-After"))
	require.NotContains(t, rec.Body.String(), "all accounts in this group are rate limited")
}

func TestWriteAllAccountsRateLimitedAfterStreamStartWritesNothing(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	h := &GatewayHandler{gatewayService: &service.GatewayService{}}
	id := int64(55)
	require.False(t, h.writeAllAccountsRateLimited(c, &service.APIKey{ID: 1, GroupID: &id}, entrypointModel, true))
	require.False(t, c.Writer.Written())
}
