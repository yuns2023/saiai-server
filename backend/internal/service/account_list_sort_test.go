//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAccountListSort(t *testing.T) {
	cases := []struct {
		by, order string
		want      AccountListSort
	}{
		{"", "", AccountListSort{}},
		{"", "asc", AccountListSort{}},
		{"unknown", "asc", AccountListSort{}},
		{"name; DROP TABLE accounts", "asc", AccountListSort{}},
		{"created_at", "asc", AccountListSort{}},
		{"name", "asc", AccountListSort{By: AccountSortByName}},
		{" Name ", "ASC", AccountListSort{By: AccountSortByName}},
		{"name", "desc", AccountListSort{By: AccountSortByName, Desc: true}},
		{"name", "", AccountListSort{By: AccountSortByName, Desc: true}},
		{"name", "sideways", AccountListSort{By: AccountSortByName, Desc: true}},
		{"id", "asc", AccountListSort{By: AccountSortByID}},
		{"status", "asc", AccountListSort{By: AccountSortByStatus}},
		{"schedulable", "desc", AccountListSort{By: AccountSortBySchedulable, Desc: true}},
		{"priority", "asc", AccountListSort{By: AccountSortByPriority}},
		{"rate_multiplier", "asc", AccountListSort{By: AccountSortByRateMultiplier}},
		{"last_used_at", "desc", AccountListSort{By: AccountSortByLastUsedAt, Desc: true}},
		{"expires_at", "asc", AccountListSort{By: AccountSortByExpiresAt}},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, NormalizeAccountListSort(tc.by, tc.order), "by=%q order=%q", tc.by, tc.order)
	}
	require.True(t, AccountListSort{}.IsDefault())
	require.True(t, AccountListSort{By: AccountSortByID, Desc: true}.IsDefault())
	require.False(t, AccountListSort{By: AccountSortByID}.IsDefault())
	require.False(t, AccountListSort{By: AccountSortByName, Desc: true}.IsDefault())
}
