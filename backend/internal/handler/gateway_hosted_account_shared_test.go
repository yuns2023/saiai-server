//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// sharedMarketStore answers the market dispatch reads of an owner whose
// only hosted account is (or was) shared.
type sharedMarketStore struct {
	mu       sync.Mutex
	account  service.Account
	groups   []int64 // admin groups of the listed account in the registry poll
	listed   bool
	total    int
	shared   []int64
	err      error
	admitErr error
	checks   int
}

func (s *sharedMarketStore) LoadMarketDispatchState(context.Context) (*service.MarketDispatchState, error) {
	return &service.MarketDispatchState{ListedAccountIDs: []int64{s.account.ID}, ListedAdminGroupIDs: s.groups}, nil
}

func (s *sharedMarketStore) AdmitMarketCandidate(_ context.Context, req *service.MarketAdmissionRequest, evaluate func(*service.MarketAuthoritativeFacts) string) (*service.MarketAdmissionVerdict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks++
	if s.admitErr != nil {
		return nil, s.admitErr
	}
	acc := s.account
	status := service.MarketListingStatusUnlisted
	if s.listed {
		status = service.MarketListingStatusListed
	}
	f := &service.MarketAuthoritativeFacts{AccountFound: true, Account: &acc, MembershipSource: service.AccountGroupSourceAdmin,
		GroupFound: true, GroupActive: true, GroupPlatform: service.PlatformAnthropic, GroupHosted: true, GroupExclusive: true,
		OwnerGroupQualified: true, OwnerUserStatus: service.StatusActive,
		Listing: &service.MarketListing{AccountID: acc.ID, OwnerUserID: *acc.OwnerUserID, Status: status, Version: 2}}
	reason := evaluate(f)
	return &service.MarketAdmissionVerdict{Admitted: reason == "", Reason: reason, Facts: f}, nil
}

func (s *sharedMarketStore) GetMarketAdmissionByAttempt(context.Context, string) (*service.MarketAdmission, error) {
	return nil, service.ErrMarketAdmissionNotFound
}

func (s *sharedMarketStore) CountMarketTierPlatformMembers(context.Context, int64) (int, error) {
	return 0, nil
}

func (s *sharedMarketStore) HostedGroupSharedAccounts(context.Context, int64, int64) (int, []int64, error) {
	return s.total, s.shared, s.err
}

func newHostedSharedHarness(t *testing.T) (*entrypointHarness, *sharedMarketStore, *service.APIKey) {
	return newHostedSharedHarnessWith(t, nil)
}

// newHostedSharedHarnessWith: registryGroups = the admin groups the registry
// poll reports for the listed account (nil: the registry does not know the
// group yet, so only the selection-time checks can see the listing).
func newHostedSharedHarnessWith(t *testing.T, registryGroups []int64) (*entrypointHarness, *sharedMarketStore, *service.APIKey) {
	t.Helper()
	group := anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyOff)
	group.HostedUsageEnabled, group.IsExclusive, group.RateMultiplier = true, true, 0.1
	h := newEntrypointHarness(t, map[int64]*service.Group{entrypointGroupA: group}, entrypointGroupA)
	owner := h.user.ID
	h.lab.accounts[0].OwnerUserID = &owner
	h.lab.accounts[0].AccountGroups = []service.AccountGroup{{AccountID: entrypointAccountID, GroupID: entrypointGroupA, Group: group}}
	store := &sharedMarketStore{account: h.lab.accounts[0], groups: registryGroups, listed: true, total: 1, shared: []int64{entrypointAccountID}}
	rt := service.NewMarketRuntime(store)
	require.True(t, rt.Refresh(context.Background()))
	h.handler.gatewayService.SetMarketRuntime(rt)
	return h, store, h.key(entrypointGroupA)
}

// serveOwner runs the request as the API key middleware would (the
// authenticated user is the account's owner).
func serveOwner(t *testing.T, h *entrypointHarness, key *service.APIKey, capture **gin.Context) *httptest.ResponseRecorder {
	t.Helper()
	return h.serve(t, key, entrypointT3CodeUA, entrypointBody("cli"), func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithAccountUserID(c.Request.Context(), h.user.ID))
		if capture != nil {
			*capture = c
		}
	})
}

func TestMessagesHostedAccountShared403(t *testing.T) {
	h, store, key := newHostedSharedHarness(t)
	var captured *gin.Context
	rec := serveOwner(t, h, key, &captured)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Equal(t, "false", rec.Header().Get("x-should-retry"))
	require.Empty(t, rec.Header().Get("Retry-After"))
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "HOSTED_ACCOUNT_SHARED", body.Error.Type)
	require.Equal(t, service.HostedAccountSharedMessage, body.Error.Message)
	require.Zero(t, h.lab.upstreamCalls(), "nothing sent upstream")
	require.GreaterOrEqual(t, store.checks, 1, "the owner candidate was checked in the database")
	require.True(t, service.IsOpsUserLimit(captured), "Ops: the user's own state, not an SLA failure")
}

func TestMessagesHostedAccountSharedOnlyWhenConfirmed(t *testing.T) {
	// The database read of the group fails: never reported as shared.
	h, store, key := newHostedSharedHarness(t)
	store.err = errors.New("db down")
	rec := serveOwner(t, h, key, nil)
	require.NotEqual(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "HOSTED_ACCOUNT_SHARED")

	// Another account of the group is not shared (e.g. rate limited or its
	// devices are full): the existing error.
	h, store, key = newHostedSharedHarness(t)
	store.total = 2
	rec = serveOwner(t, h, key, nil)
	require.NotEqual(t, http.StatusForbidden, rec.Code, rec.Body.String())

	// The owner check itself fails (database error): temporarily refused.
	h, store, key = newHostedSharedHarness(t)
	store.admitErr = errors.New("db down")
	rec = serveOwner(t, h, key, nil)
	require.NotContains(t, rec.Body.String(), "HOSTED_ACCOUNT_SHARED")
	require.Zero(t, h.lab.upstreamCalls())

	// Stale listed view, unlisted in the database: served (self-use).
	h, store, key = newHostedSharedHarness(t)
	store.listed = false
	rec = serveOwner(t, h, key, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, h.lab.upstreamCalls())
}

// The registry already knows the shared account: the 403 is answered before
// the request waits for anything, so even a streaming request gets the HTTP
// status and header first (nothing flushed before).
func TestMessagesHostedAccountSharedEarlyForStreaming(t *testing.T) {
	h, store, key := newHostedSharedHarnessWith(t, []int64{entrypointGroupA})
	body := strings.Replace(entrypointBody("cli"), `"max_tokens":16`, `"max_tokens":16,"stream":true`, 1)
	rec := h.serve(t, key, entrypointT3CodeUA, body, func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithAccountUserID(c.Request.Context(), h.user.ID))
	})
	res := rec.Result()
	require.Equal(t, http.StatusForbidden, res.StatusCode, rec.Body.String())
	require.Equal(t, "false", res.Header.Get("x-should-retry"))
	require.Empty(t, res.Header.Get("Retry-After"))
	require.NotContains(t, res.Header.Get("Content-Type"), "event-stream", "no SSE started")
	require.Contains(t, rec.Body.String(), `"type":"HOSTED_ACCOUNT_SHARED"`)
	require.Zero(t, store.checks, "answered before any selection or admission")
	require.Zero(t, h.lab.upstreamCalls())

	// Not all shared: the early check lets the request through.
	h, store, key = newHostedSharedHarnessWith(t, []int64{entrypointGroupA})
	store.total, store.listed = 2, false
	rec = serveOwner(t, h, key, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
