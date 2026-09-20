package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserAccessLevelAndDiscount(t *testing.T) {
	base := &AccessLevel{ID: 1, Name: "新手村", Rank: 0, BalanceThreshold: 0, PaygDiscountMultiplier: 1}
	vip := &AccessLevel{ID: 2, Name: "星舰会员", Rank: 2, BalanceThreshold: 100, PaygDiscountMultiplier: 0.8}
	required := &AccessLevel{ID: 3, Name: "稳定服务", Rank: 1, BalanceThreshold: 20, PaygDiscountMultiplier: 0.9}

	user := &User{AutoLevel: base}
	require.False(t, user.MeetsRequiredLevel(required))
	require.Equal(t, 1.0, user.PaygDiscountRate())

	user.AutoLevel = vip
	require.True(t, user.MeetsRequiredLevel(required))
	require.Equal(t, 0.8, user.PaygDiscountRate())

	legacyDiscount := 0.75
	legacyUser := &User{AutoLevel: vip, PaygDiscountMultiplier: &legacyDiscount}
	require.Equal(t, legacyDiscount, legacyUser.PaygDiscountRate(), "legacy explicit discounts must survive level rollout")

	manualDiscount := 0.7
	user.PaygDiscountMultiplier = &manualDiscount
	user.PaygDiscountOverrideEnabled = true
	require.Equal(t, 0.7, user.PaygDiscountRate(), "user override must replace, not stack with, the level discount")

	user.ManualLevel = base
	require.Equal(t, base, user.EffectiveAccessLevel(), "manual level must override automatic level")
}

func TestCanAccessGroupRequiresBothLevelAndLegacyEntitlement(t *testing.T) {
	required := &AccessLevel{Rank: 2}
	user := &User{AutoLevel: &AccessLevel{Rank: 2}}
	public := &Group{ID: 10, RequiredLevel: required}
	require.True(t, user.CanAccessGroup(public))

	exclusive := &Group{ID: 11, IsExclusive: true, RequiredLevel: required}
	require.False(t, user.CanAccessGroup(exclusive))
	user.AllowedGroups = []int64{exclusive.ID}
	require.True(t, user.CanAccessGroup(exclusive))

	user.AutoLevel = &AccessLevel{Rank: 1}
	require.False(t, user.CanAccessGroup(exclusive), "explicit group entitlement must not bypass the level prerequisite")
}

func TestValidateAccessLevelOrdering(t *testing.T) {
	require.NoError(t, validateAccessLevelOrdering([]AccessLevel{
		{Rank: 0, BalanceThreshold: 0},
		{Rank: 1, BalanceThreshold: 10},
	}))
	require.Error(t, validateAccessLevelOrdering([]AccessLevel{
		{Rank: 0, BalanceThreshold: 20},
		{Rank: 1, BalanceThreshold: 10},
	}))
	require.Error(t, validateAccessLevelOrdering([]AccessLevel{
		{Rank: 1, BalanceThreshold: 10},
		{Rank: 1, BalanceThreshold: 20},
	}))
}
