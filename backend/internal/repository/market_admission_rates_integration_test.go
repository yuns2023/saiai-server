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
