//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyUserBillingFactors(t *testing.T) {
	group := &Group{ModelRateMultipliers: map[string]float64{"claude-fable-*": 0.8}}
	discount := 0.75
	user := &User{PaygDiscountMultiplier: &discount}
	cost := &CostBreakdown{TotalCost: 10, ActualCost: 11}
	modelRate, userRate := applyUserBillingFactors(cost, group, user, "CLAUDE-FABLE-5-1", false)
	require.Equal(t, 0.8, modelRate)
	require.Equal(t, 0.75, userRate)
	require.InDelta(t, 6.6, cost.ActualCost, 1e-12)
	require.Equal(t, 10.0, cost.TotalCost)

	cost = &CostBreakdown{TotalCost: 10, ActualCost: 11}
	modelRate, userRate = applyUserBillingFactors(cost, group, user, "claude-fable-5-1", true)
	require.Equal(t, 0.8, modelRate)
	require.Equal(t, 1.0, userRate)
	require.InDelta(t, 8.8, cost.ActualCost, 1e-12)
	require.Equal(t, 10.0, cost.TotalCost)
}

func TestApplyUserBillingFactors_IgnoresLegacyAccountDiscount(t *testing.T) {
	userDiscount := 0.8
	legacyAccountDiscount := 0.5
	user := &User{PaygDiscountMultiplier: &userDiscount}
	account := &Account{PaygDiscountMultiplier: &legacyAccountDiscount}
	keys := []*APIKey{{ID: 1, User: user}, {ID: 2, User: user}}

	for _, key := range keys {
		cost := &CostBreakdown{TotalCost: 10, ActualCost: 10}
		_, discount := applyUserBillingFactors(cost, &Group{}, key.User, "gpt-5.1", false)
		require.Equal(t, userDiscount, discount)
		require.Equal(t, 8.0, cost.ActualCost)
		require.Equal(t, legacyAccountDiscount, account.PaygDiscountRate())
	}
}

func TestNormalizeModelRateMultipliers(t *testing.T) {
	rates, err := NormalizeModelRateMultipliers(map[string]float64{" CLAUDE-FABLE-* ": 0, "gpt-5.6": 1.2})
	require.NoError(t, err)
	require.Equal(t, 0.0, rates["claude-fable-*"])
	require.Equal(t, 1.2, rates["gpt-5.6"])
	for _, pattern := range []string{"*", "claude-*-5", "claude-**", "claude-?"} {
		_, err = NormalizeModelRateMultipliers(map[string]float64{pattern: 2})
		require.Error(t, err, pattern)
	}
	_, err = NormalizeModelRateMultipliers(map[string]float64{"gpt-5.6": 0.12345})
	require.Error(t, err)
}

func TestModelRateForWildcardPrecedence(t *testing.T) {
	group := &Group{ModelRateMultipliers: map[string]float64{
		"claude-*":         0.9,
		"claude-fable-*":   0.6,
		"claude-fable-5-*": 0.5,
		"claude-fable-5-1": 0.4,
	}}
	require.Equal(t, 0.4, group.ModelRateFor("CLAUDE-FABLE-5-1"))
	require.Equal(t, 0.6, group.ModelRateFor("claude-fable-5"))
	require.Equal(t, 0.5, group.ModelRateFor("claude-fable-5-2"))
	require.Equal(t, 0.9, group.ModelRateFor("claude-sonnet-5"))
	require.Equal(t, 1.0, group.ModelRateFor("gpt-5.6"))
}
