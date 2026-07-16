//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestShuffleWithinPriorityAndLoadGroups_EmptyOrSingle(t *testing.T) {
	shuffleWithinPriorityAndLoadGroups(nil)
	shuffleWithinPriorityAndLoadGroups([]accountWithLoad{})

	accounts := []accountWithLoad{
		{account: &Account{ID: 1, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 10}},
	}
	shuffleWithinPriorityAndLoadGroups(accounts)
	require.Equal(t, int64(1), accounts[0].account.ID)
}

func TestShuffleWithinPriorityAndLoadGroups_PreservesLayerOrder(t *testing.T) {
	accounts := []accountWithLoad{
		{account: &Account{ID: 1, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 10}},
		{account: &Account{ID: 2, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 20}},
		{account: &Account{ID: 3, Priority: 2}, loadInfo: &AccountLoadInfo{LoadRate: 10}},
	}

	for range 20 {
		cpy := append([]accountWithLoad(nil), accounts...)
		shuffleWithinPriorityAndLoadGroups(cpy)
		require.Equal(t, int64(1), cpy[0].account.ID)
		require.Equal(t, int64(2), cpy[1].account.ID)
		require.Equal(t, int64(3), cpy[2].account.ID)
	}
}

func TestShuffleWithinPriorityAndLoadGroups_IgnoresLastUsedAndAccountType(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Hour)
	accounts := []accountWithLoad{
		{account: &Account{ID: 1, Priority: 1, LastUsedAt: nil, Type: AccountTypeAPIKey}, loadInfo: &AccountLoadInfo{LoadRate: 10}},
		{account: &Account{ID: 2, Priority: 1, LastUsedAt: &earlier, Type: AccountTypeOAuth}, loadInfo: &AccountLoadInfo{LoadRate: 10}},
		{account: &Account{ID: 3, Priority: 1, LastUsedAt: &now, Type: AccountTypeAPIKey}, loadInfo: &AccountLoadInfo{LoadRate: 10}},
	}

	seenFirst := map[int64]bool{}
	for range 100 {
		cpy := append([]accountWithLoad(nil), accounts...)
		shuffleWithinPriorityAndLoadGroups(cpy)
		seenFirst[cpy[0].account.ID] = true
		ids := map[int64]bool{}
		for _, item := range cpy {
			ids[item.account.ID] = true
		}
		require.Equal(t, map[int64]bool{1: true, 2: true, 3: true}, ids)
	}
	require.GreaterOrEqual(t, len(seenFirst), 2)
}

func TestSamePriorityAndLoadGroup(t *testing.T) {
	base := accountWithLoad{
		account:  &Account{Priority: 1},
		loadInfo: &AccountLoadInfo{LoadRate: 10},
	}
	require.True(t, samePriorityAndLoadGroup(base, base))
	require.False(t, samePriorityAndLoadGroup(base, accountWithLoad{
		account:  &Account{Priority: 2},
		loadInfo: &AccountLoadInfo{LoadRate: 10},
	}))
	require.False(t, samePriorityAndLoadGroup(base, accountWithLoad{
		account:  &Account{Priority: 1},
		loadInfo: &AccountLoadInfo{LoadRate: 20},
	}))
}
