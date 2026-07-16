//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFilterByMinPriority(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		require.Empty(t, filterByMinPriority(nil))
	})

	t.Run("single account", func(t *testing.T) {
		accounts := []accountWithLoad{
			{account: &Account{ID: 1, Priority: 5}, loadInfo: &AccountLoadInfo{}},
		}
		result := filterByMinPriority(accounts)
		require.Len(t, result, 1)
		require.Equal(t, int64(1), result[0].account.ID)
	})

	t.Run("filters to global minimum priority", func(t *testing.T) {
		accounts := []accountWithLoad{
			{account: &Account{ID: 1, Priority: 5}, loadInfo: &AccountLoadInfo{}},
			{account: &Account{ID: 2, Priority: 1}, loadInfo: &AccountLoadInfo{}},
			{account: &Account{ID: 3, Priority: 3}, loadInfo: &AccountLoadInfo{}},
			{account: &Account{ID: 4, Priority: 1}, loadInfo: &AccountLoadInfo{}},
		}
		result := filterByMinPriority(accounts)
		require.Len(t, result, 2)
		require.Equal(t, int64(2), result[0].account.ID)
		require.Equal(t, int64(4), result[1].account.ID)
	})
}

func TestFilterByMinLoadRate(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		require.Empty(t, filterByMinLoadRate(nil))
	})

	t.Run("single account", func(t *testing.T) {
		accounts := []accountWithLoad{
			{account: &Account{ID: 1}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
		}
		result := filterByMinLoadRate(accounts)
		require.Len(t, result, 1)
		require.Equal(t, int64(1), result[0].account.ID)
	})

	t.Run("filters to minimum realtime load", func(t *testing.T) {
		accounts := []accountWithLoad{
			{account: &Account{ID: 1}, loadInfo: &AccountLoadInfo{LoadRate: 80}},
			{account: &Account{ID: 2}, loadInfo: &AccountLoadInfo{LoadRate: 10}},
			{account: &Account{ID: 3}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
			{account: &Account{ID: 4}, loadInfo: &AccountLoadInfo{LoadRate: 10}},
		}
		result := filterByMinLoadRate(accounts)
		require.Len(t, result, 2)
		require.Equal(t, int64(2), result[0].account.ID)
		require.Equal(t, int64(4), result[1].account.ID)
	})
}

func TestLayeredSelection_RandomPeersIgnoreLastUsedAndAccountType(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Hour)
	muchEarlier := now.Add(-2 * time.Hour)
	accounts := []accountWithLoad{
		{account: &Account{ID: 1, Priority: 1, LastUsedAt: &now, Type: AccountTypeAPIKey}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
		{account: &Account{ID: 2, Priority: 1, LastUsedAt: &earlier, Type: AccountTypeAPIKey}, loadInfo: &AccountLoadInfo{LoadRate: 20}},
		{account: &Account{ID: 3, Priority: 1, LastUsedAt: &muchEarlier, Type: AccountTypeOAuth}, loadInfo: &AccountLoadInfo{LoadRate: 20}},
		{account: &Account{ID: 4, Priority: 2, LastUsedAt: nil, Type: AccountTypeOAuth}, loadInfo: &AccountLoadInfo{LoadRate: 0}},
	}

	priorityLayer := filterByMinPriority(accounts)
	require.Len(t, priorityLayer, 3)
	loadLayer := filterByMinLoadRate(priorityLayer)
	require.Len(t, loadLayer, 2)

	seen := map[int64]bool{}
	for range 100 {
		selected := selectRandomAccountWithLoad(loadLayer, true)
		require.NotNil(t, selected)
		require.Contains(t, []int64{2, 3}, selected.account.ID)
		seen[selected.account.ID] = true
	}
	require.Equal(t, map[int64]bool{2: true, 3: true}, seen)
}
