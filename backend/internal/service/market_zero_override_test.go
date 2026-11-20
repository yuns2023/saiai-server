//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// zeroOverrideRateRepo returns the buyer's user rate override on the tier
// (nil = no override row).
type zeroOverrideRateRepo struct {
	userGroupRateRepoStubForGroupRate
	rates map[[2]int64]float64
}

func (r *zeroOverrideRateRepo) GetByUserAndGroup(_ context.Context, userID, groupID int64) (*float64, error) {
	if v, ok := r.rates[[2]int64{userID, groupID}]; ok {
		return &v, nil
	}
	return nil, nil
}

func buyerZeroOverrideRepo() *zeroOverrideRateRepo {
	return &zeroOverrideRateRepo{rates: map[[2]int64]float64{{mktBuyer, mktTier}: 0}}
}

// A user rate override of 0 on a tier (free self-use, allowed since
// migration 136) makes the buyer price zero: the listed account is never
// selected; a platform account of the tier serves the request, and with only
// listed accounts there is no available account.
func TestMarketZeroUserOverrideNeverSelectsListedAccount(t *testing.T) {
	// A fresh request context per request (the dispatch caches its price).
	ctx := buyerCtx(mktTier)

	// Control: without the override the listed account is sold.
	env := newMarketEnv(t, []Account{mktListedAccount(1)})
	res, err := env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktTier), "", "claude-fable-5", nil, "")
	require.NoError(t, err)
	require.True(t, res.Market.IsBuyer())
	res.ReleaseFunc()

	// Only listed accounts + zero override: nothing to dispatch to.
	env = newMarketEnv(t, []Account{mktListedAccount(1)})
	env.svc.userGroupRateRepo = buyerZeroOverrideRepo()
	ctx = buyerCtx(mktTier)
	_, err = env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktTier), "", "claude-fable-5", nil, "")
	require.ErrorIs(t, err, ErrNoAvailableAccounts, "a zero price never selects a listed account")
	require.Empty(t, env.store.requests, "no admission attempted")

	// Listed + platform account: the platform account serves, every time.
	env = newMarketEnv(t, []Account{mktListedAccount(1), mktPlatformAccount(2, mktTier)})
	env.svc.userGroupRateRepo = buyerZeroOverrideRepo()
	for i := 0; i < 5; i++ {
		ctx = buyerCtx(mktTier)
		res, err := env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktTier), "", "claude-fable-5", nil, "")
		require.NoError(t, err)
		require.NotNil(t, res.Account)
		require.Equal(t, int64(2), res.Account.ID, "attempt %d", i)
		require.False(t, res.Market.IsBuyer())
		res.ReleaseFunc()
	}
	listed := mktListedAccount(1)
	class, _ := env.svc.classifyMarketCandidate(withMarketRequestedModel(withMarketSelectionGroup(ctx, ptr64(mktTier)), "claude-fable-5"), &listed)
	require.Equal(t, MarketClassExcluded, class)
	_, buyers := env.slots.counts(1)
	require.Zero(t, buyers, "no buyer lease on the listed account")

	// Another user's zero override does not affect this buyer.
	env = newMarketEnv(t, []Account{mktListedAccount(1)})
	env.svc.userGroupRateRepo = &zeroOverrideRateRepo{rates: map[[2]int64]float64{{mktOwner, mktTier}: 0}}
	ctx = buyerCtx(mktTier)
	res, err = env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktTier), "", "claude-fable-5", nil, "")
	require.NoError(t, err)
	require.True(t, res.Market.IsBuyer())
	res.ReleaseFunc()
}

// The pre-send price check reads the buyer's override again: an attempt
// selected before the override became 0 never reaches upstream and carries
// no settlement proof, so RecordUsage can never write a shared_sale or an
// owner earning for it.
func TestMarketZeroUserOverrideRefusedAtFinalAdmission(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	env.svc.userGroupRateRepo = buyerZeroOverrideRepo()
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, attempt := buyerForwardCtx(t, c.Request.Context())
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Zero(t, upstream.calls)
	require.Empty(t, env.store.requests, "refused before the admission row")
	marketPath, proof := attempt.SettlementFields()
	require.False(t, marketPath)
	require.Nil(t, proof)

	// What RecordUsage builds for such a request: no market branch.
	gid := mktTier
	p := &postUsageBillingParams{Cost: &CostBreakdown{TotalCost: 1, ActualCost: 0}, APIKey: &APIKey{ID: mktKey, GroupID: &gid, Group: mktGroups()[mktTier]},
		User: &User{ID: mktBuyer}, Account: acc}
	p.MarketPath, p.Market = attempt.SettlementFields()
	cmd := buildUsageBillingCommand("req-zero", nil, p)
	require.False(t, cmd.MarketPath)
	require.Nil(t, cmd.Settlement.Market)
}

// The request-start price used a positive rate (no override seen, e.g. the
// rate cache), but the admission snapshot has the buyer's override at 0:
// the final-admission judgement is entered and refuses; no admission row,
// no upstream send, no settlement credential.
func TestMarketZeroUserOverrideInSnapshotRefusedByFinalJudgement(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	inner := env.store.facts
	entered := 0
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		entered++
		f := inner(req)
		zero := 0.0
		f.BuyerRateOverride = &zero
		return f
	}
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, attempt := buyerForwardCtx(t, c.Request.Context())
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, 1, entered, "the final-admission judgement ran")
	require.Len(t, env.store.requests, 1)
	require.Empty(t, env.store.inserted, "no admission row")
	require.Zero(t, upstream.calls, "no upstream send")
	marketPath, proof := attempt.SettlementFields()
	require.False(t, marketPath)
	require.Nil(t, proof, "no settlement credential")
}
