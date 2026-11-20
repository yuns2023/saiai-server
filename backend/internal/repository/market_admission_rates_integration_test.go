//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The buyer admission reads the tier's rate and model rate factors in its
// own snapshot: the final price check sees an admin change made while the
// request waited, not the group captured at request start.
func TestMarketAdmissionReadsGroupRates(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner, buyer := f.user(t), f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	_, err := f.list(acc, owner, tier)
	require.NoError(t, err)

	_, err = integrationDB.ExecContext(ctx,
		`UPDATE groups SET rate_multiplier = 1.3, model_rate_multipliers = '{"claude-fable-5": 0, "claude-*": 0.5}'::jsonb WHERE id = $1`, tier)
	require.NoError(t, err)

	sel := buyerSelection(t, f, acc, tier, owner)
	req := buyerRequest(acc, tier, buyer)
	var seen *service.MarketAuthoritativeFacts
	verdict, err := f.market.AdmitMarketCandidate(ctx, req, func(facts *service.MarketAuthoritativeFacts) string {
		seen = facts
		return service.EvaluateMarketAdmission(sel, req, facts)
	})
	require.NoError(t, err)
	require.True(t, verdict.Admitted, verdict.Reason)
	require.NotNil(t, seen)
	require.InDelta(t, 1.3, seen.GroupRate, 1e-9)
	require.Equal(t, map[string]float64{"claude-fable-5": 0, "claude-*": 0.5}, seen.GroupModelRates)

	// A factor map the ent field could not load either fails the admission
	// (nothing inserted, no dispatch).
	_, err = integrationDB.ExecContext(ctx,
		`UPDATE groups SET model_rate_multipliers = '{"claude-*": "x"}'::jsonb WHERE id = $1`, tier)
	require.NoError(t, err)
	bad := buyerRequest(acc, tier, buyer)
	before := admissionCount(t, acc)
	_, err = f.market.AdmitMarketCandidate(ctx, bad, func(facts *service.MarketAuthoritativeFacts) string {
		return service.EvaluateMarketAdmission(sel, bad, facts)
	})
	require.ErrorContains(t, err, "model_rate_multipliers")
	require.Equal(t, before, admissionCount(t, acc))
}

// The buyer admission also reads the buyer's own user rate override on the
// tier in its snapshot (a zero override is allowed since migration 136 and
// makes the sale unpriced; another user's override is never used).
func TestMarketAdmissionReadsBuyerRateOverride(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner, buyer, other := f.user(t), f.user(t), f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	_, err := f.list(acc, owner, tier)
	require.NoError(t, err)
	rates := &userGroupRateRepository{sql: integrationDB}
	sel := buyerSelection(t, f, acc, tier, owner)

	seen := func() *service.MarketAuthoritativeFacts {
		t.Helper()
		req := buyerRequest(acc, tier, buyer)
		var facts *service.MarketAuthoritativeFacts
		_, err := f.market.AdmitMarketCandidate(ctx, req, func(fa *service.MarketAuthoritativeFacts) string {
			facts = fa
			return "probe" // refuse: nothing inserted
		})
		require.NoError(t, err)
		require.NotNil(t, facts)
		return facts
	}
	require.Nil(t, seen().BuyerRateOverride, "no override row")

	zero := 0.0
	require.NoError(t, rates.SyncUserGroupRates(ctx, other, map[int64]*float64{tier: &zero}))
	require.Nil(t, seen().BuyerRateOverride, "another user's override is not the buyer's")

	require.NoError(t, rates.SyncUserGroupRates(ctx, buyer, map[int64]*float64{tier: &zero}))
	got := seen().BuyerRateOverride
	require.NotNil(t, got)
	require.Zero(t, *got)

	positive := 1.25
	require.NoError(t, rates.SyncUserGroupRates(ctx, buyer, map[int64]*float64{tier: &positive}))
	got = seen().BuyerRateOverride
	require.NotNil(t, got)
	require.InDelta(t, 1.25, *got, 1e-9)
	req := buyerRequest(acc, tier, buyer)
	verdict, err := f.market.AdmitMarketCandidate(ctx, req, func(facts *service.MarketAuthoritativeFacts) string {
		return service.EvaluateMarketAdmission(sel, req, facts)
	})
	require.NoError(t, err)
	require.True(t, verdict.Admitted, verdict.Reason)
}

// The production final-admission judgement on the real SQL snapshot: the
// request started with a positive (cached) rate, but the buyer's override
// on the tier is 0 in the database. The callback runs, refuses, and no
// market_admissions row is written (so there is no settlement credential
// and nothing an owner accrue could be written for). The positive control
// admits the same candidate once the override is positive again.
func TestMarketFinalAdmissionRefusesZeroBuyerOverrideInSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner, buyer := f.user(t), f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	_, err := f.list(acc, owner, tier)
	require.NoError(t, err)
	rates := &userGroupRateRepository{sql: integrationDB}
	sel := buyerSelection(t, f, acc, tier, owner)

	admit := func() (*service.MarketAdmissionVerdict, bool) {
		t.Helper()
		req := buyerRequest(acc, tier, buyer)
		entered := false
		verdict, err := f.market.AdmitMarketCandidate(ctx, req, func(facts *service.MarketAuthoritativeFacts) string {
			entered = true
			return service.MarketFinalAdmissionReason(ctx, sel, req, facts, "claude-fable-5")
		})
		require.NoError(t, err)
		require.NotNil(t, verdict)
		return verdict, entered
	}

	zero := 0.0
	require.NoError(t, rates.SyncUserGroupRates(ctx, buyer, map[int64]*float64{tier: &zero}))
	before := admissionCount(t, acc)
	verdict, entered := admit()
	require.True(t, entered, "the final-admission judgement ran on the snapshot")
	require.False(t, verdict.Admitted)
	require.Equal(t, "not_priced", verdict.Reason)
	require.Nil(t, verdict.Admission, "no settlement credential")
	require.Equal(t, before, admissionCount(t, acc), "no market_admissions row")

	positive := 1.5
	require.NoError(t, rates.SyncUserGroupRates(ctx, buyer, map[int64]*float64{tier: &positive}))
	verdict, entered = admit()
	require.True(t, entered)
	require.True(t, verdict.Admitted, verdict.Reason)
	require.NotNil(t, verdict.Admission)
	require.Equal(t, before+1, admissionCount(t, acc))
}
