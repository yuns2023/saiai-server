//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// ownerForwardEnv: the listed account reached through its owner's exclusive
// hosted group; facts() answers the owner admission (qualified unless edited).
func ownerForwardEnv(t *testing.T, qualified bool) (*marketEnv, *queuedClaudeCodeUpstream, *Account, *marketDeviceStub) {
	t.Helper()
	env, upstream, acc := marketForwardEnv(t)
	dev, ok := env.svc.identityService.cache.(*marketDeviceStub)
	require.True(t, ok)
	acc.GroupIDs = []int64{mktHosted}
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		a := *acc
		a.Extra = map[string]any{"claude_oauth_mode": ClaudeOAuthModeCarpool, "claude_oauth_carpool_device_limit": 5}
		return &MarketAuthoritativeFacts{AccountFound: true, Account: &a, MembershipSource: AccountGroupSourceAdmin,
			GroupFound: true, GroupActive: true, GroupPlatform: PlatformAnthropic, GroupHosted: true, GroupExclusive: true,
			OwnerUserStatus: StatusActive, OwnerGroupQualified: qualified}
	}
	return env, upstream, acc, dev
}

func ownerForwardCtx(base context.Context, key *APIKey) (context.Context, *MarketAttempt) {
	ctx := WithAccountUserID(base, mktOwner)
	ctx = WithMarketDispatch(ctx, key)
	return WithMarketAttempt(ctx, &MarketSelection{Class: MarketClassOwner, AccountID: 1, GroupID: mktHosted, OwnerUserID: mktOwner, Concurrency: 4})
}

func ownerKey(userID, groupID int64) *APIKey {
	return &APIKey{ID: 6, UserID: userID, GroupID: &groupID, User: &User{ID: userID}}
}

func requireMarketFailover(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.True(t, errors.As(err, &failover), "fails over (lease released, candidate dropped): %v", err)
}

func TestMarketOwnerPathAdmittedRegistersOwnerDevice(t *testing.T) {
	env, upstream, acc, dev := ownerForwardEnv(t, true)
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, attempt := ownerForwardCtx(c.Request.Context(), ownerKey(mktOwner, mktHosted))
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	require.NoError(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, []string{MarketDeviceClassOwner}, dev.classes)
	require.Len(t, env.store.requests, 2, "the owner check runs before the device registration and again before the send")
	for _, r := range env.store.requests {
		require.Equal(t, MarketClassOwner, r.Class)
		require.Equal(t, mktHosted, r.GroupID)
		require.Equal(t, mktOwner, r.AuthUserID)
	}
	marketPath, proof := attempt.SettlementFields()
	require.False(t, marketPath)
	require.Nil(t, proof)
	require.Empty(t, env.store.inserted)
}

func TestMarketOwnerPathRefusedBeforeDeviceRegistration(t *testing.T) {
	env, upstream, acc, dev := ownerForwardEnv(t, false)
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, _ := ownerForwardCtx(c.Request.Context(), ownerKey(mktOwner, mktHosted))
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	requireMarketFailover(t, err)
	require.Zero(t, upstream.calls)
	require.Empty(t, dev.classes, "no device registered (no owner-class record, no reclassification, no cycle admission)")
	require.Len(t, env.store.requests, 1, "refused at the pre-registration check")
}

func TestMarketOwnerPathDBErrorFailsOverBeforeDeviceRegistration(t *testing.T) {
	env, upstream, acc, dev := ownerForwardEnv(t, true)
	env.store.admitErr = errors.New("db down")
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, attempt := ownerForwardCtx(c.Request.Context(), ownerKey(mktOwner, mktHosted))
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	requireMarketFailover(t, err)
	require.Zero(t, upstream.calls)
	require.Empty(t, dev.classes)
	require.Equal(t, MarketClassOwner, attempt.Selection.Class, "never downgraded")
	require.Empty(t, env.store.inserted)
}

func TestMarketOwnerPathRevokedBetweenChecksNeverSends(t *testing.T) {
	env, upstream, acc, dev := ownerForwardEnv(t, true)
	qualified := env.store.facts
	calls := 0
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		calls++
		f := qualified(req)
		f.OwnerGroupQualified = calls == 1 // shared with another user after the pre-check
		return f
	}
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, _ := ownerForwardCtx(c.Request.Context(), ownerKey(mktOwner, mktHosted))
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	requireMarketFailover(t, err)
	require.Equal(t, []string{MarketDeviceClassOwner}, dev.classes, "registered once after the passing pre-check")
	require.Len(t, env.store.requests, 2)
	require.Zero(t, upstream.calls, "the pre-send check refuses")
}

func TestMarketOwnerPathKeyMustBeOwnersKeyOfTheGroup(t *testing.T) {
	for name, key := range map[string]*APIKey{
		"key of another group": ownerKey(mktOwner, mktPlain),
		"key of another user":  ownerKey(mktBuyer, mktHosted),
		"key without a group":  {ID: 6, UserID: mktOwner, User: &User{ID: mktOwner}},
	} {
		t.Run(name, func(t *testing.T) {
			env, upstream, acc, dev := ownerForwardEnv(t, true)
			c, _, parsed := claudeCodeCacheSafeRetryContext(t)
			ctx, _ := ownerForwardCtx(c.Request.Context(), key)
			_, err := env.svc.Forward(ctx, c, acc, parsed)
			requireMarketFailover(t, err)
			require.Zero(t, upstream.calls)
			require.Empty(t, dev.classes)
			require.Empty(t, env.store.requests, "refused before any database read")
		})
	}
}

func TestEvaluateMarketAdmissionOwnerGroupPredicate(t *testing.T) {
	acc := mktListedAccount(1)
	sel := &MarketSelection{Class: MarketClassOwner, AccountID: 1, GroupID: mktHosted, OwnerUserID: mktOwner}
	req := &MarketAdmissionRequest{Class: MarketClassOwner, AccountID: 1, GroupID: mktHosted, AuthUserID: mktOwner}
	facts := func(qualified bool) *MarketAuthoritativeFacts {
		a := acc
		return &MarketAuthoritativeFacts{AccountFound: true, Account: &a, MembershipSource: AccountGroupSourceAdmin,
			GroupFound: true, GroupActive: true, GroupPlatform: PlatformAnthropic, GroupHosted: true, GroupExclusive: true,
			OwnerUserStatus: StatusActive, OwnerGroupQualified: qualified}
	}
	require.Equal(t, "", EvaluateMarketAdmission(sel, req, facts(true)))
	require.Equal(t, "not_owner_group", EvaluateMarketAdmission(sel, req, facts(false)))
	other := &MarketAdmissionRequest{Class: MarketClassOwner, AccountID: 1, GroupID: mktHosted, AuthUserID: mktBuyer}
	require.Equal(t, "not_owner", EvaluateMarketAdmission(sel, other, facts(true)))
}
