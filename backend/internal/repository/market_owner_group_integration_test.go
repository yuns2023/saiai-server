//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The owner-path group predicate (marketOwnerGroupQuery): admin-built and
// P2c-mapped exclusive hosted groups qualify; anything shared, mapped to
// another user or holding another owner's (or a platform) account does not.
func TestMarketOwnerGroupPredicate(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner, other := f.user(t), f.user(t)
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := integrationDB.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM hosted_owner_groups WHERE user_id = ANY(ARRAY[$1, $2]::bigint[])", owner, other)
	})

	// Admin-built: an exclusive hosted group, the owner as its only allowed
	// user, the owner's account as an admin member; no hosted_owner_groups row.
	hosted := f.group(t, false)
	exec("UPDATE groups SET hosted_usage_enabled = TRUE, is_exclusive = TRUE WHERE id = $1", hosted)
	exec("INSERT INTO user_allowed_groups (user_id, group_id) VALUES ($1, $2)", owner, hosted)
	acc := f.account(t, owner)
	require.NoError(t, f.accounts.BindGroups(ctx, acc, []int64{hosted}))

	sel := &service.MarketSelection{Class: service.MarketClassOwner, AccountID: acc, GroupID: hosted, OwnerUserID: owner, Concurrency: 4}
	req := &service.MarketAdmissionRequest{Class: service.MarketClassOwner, AccountID: acc, GroupID: hosted, AuthUserID: owner}
	admitted := func(msg string) {
		t.Helper()
		v := admit(t, f, sel, req)
		require.True(t, v.Admitted, "%s: %s", msg, v.Reason)
		require.True(t, v.Facts.OwnerGroupQualified, msg)
		require.Nil(t, v.Admission, "the owner path writes no admission row")
	}
	refused := func(msg string) {
		t.Helper()
		v := admit(t, f, sel, req)
		require.False(t, v.Admitted, msg)
		require.False(t, v.Facts.OwnerGroupQualified, msg)
		require.NotEmpty(t, v.Reason, msg)
	}
	admitted("admin-built group without a mapping")

	// Allowed users: the owner must have a row, nobody else may.
	exec("DELETE FROM user_allowed_groups WHERE user_id = $1 AND group_id = $2", owner, hosted)
	refused("owner not an allowed user")
	require.Equal(t, "not_owner_group", admit(t, f, sel, req).Reason)
	exec("INSERT INTO user_allowed_groups (user_id, group_id) VALUES ($1, $2)", owner, hosted)
	exec("INSERT INTO user_allowed_groups (user_id, group_id) VALUES ($1, $2)", other, hosted)
	refused("another user is allowed")
	require.Equal(t, "not_owner_group", admit(t, f, sel, req).Reason)
	exec("DELETE FROM user_allowed_groups WHERE user_id = $1 AND group_id = $2", other, hosted)
	admitted("shared row removed")

	// hosted_owner_groups: mapped to another user = not this owner's group;
	// mapped to the owner (P2c) still qualifies.
	exec("INSERT INTO hosted_owner_groups (user_id, group_id) VALUES ($1, $2)", other, hosted)
	refused("mapped to another user")
	exec("DELETE FROM hosted_owner_groups WHERE user_id = $1", other)
	exec("INSERT INTO hosted_owner_groups (user_id, group_id) VALUES ($1, $2)", owner, hosted)
	admitted("P2c-mapped group of the owner")
	exec("DELETE FROM hosted_owner_groups WHERE user_id = $1", owner)

	// Members: every live member account must be the owner's.
	platformAcc := f.account(t, 0)
	require.NoError(t, f.accounts.BindGroups(ctx, platformAcc, []int64{hosted}))
	refused("platform account (no owner) in the group")
	require.NoError(t, f.accounts.BindGroups(ctx, platformAcc, nil))
	otherAcc := f.account(t, other)
	require.NoError(t, f.accounts.BindGroups(ctx, otherAcc, []int64{hosted}))
	refused("another owner's account in the group")
	exec("UPDATE accounts SET deleted_at = NOW() WHERE id = $1", otherAcc)
	admitted("a deleted member does not count")
	ownAcc := f.account(t, owner)
	require.NoError(t, f.accounts.BindGroups(ctx, ownAcc, []int64{hosted}))
	admitted("a second account of the same owner")

	// Group flags (each restored afterwards). A hosted group is exclusive,
	// standard, rate >= 0 and never a tier by the groups CHECK constraints
	// (groups_hosted_usage_policy, groups_market_tier_check), so those
	// clauses are only reachable with hosted usage off; NaN passes ">= 0".
	for _, c := range []struct {
		name, set, reset string
	}{
		{"not hosted", "hosted_usage_enabled = FALSE", "hosted_usage_enabled = TRUE"},
		{"not hosted, not exclusive", "hosted_usage_enabled = FALSE, is_exclusive = FALSE", "hosted_usage_enabled = TRUE, is_exclusive = TRUE"},
		{"market tier", "hosted_usage_enabled = FALSE, market_tier = TRUE", "market_tier = FALSE, hosted_usage_enabled = TRUE"},
		{"inactive", "status = 'disabled'", "status = 'active'"},
		{"NaN fee rate", "rate_multiplier = 'NaN'", "rate_multiplier = 1.2"},
		{"other platform", "platform = 'openai'", "platform = 'anthropic'"},
		{"deleted", "deleted_at = NOW()", "deleted_at = NULL"},
	} {
		exec("UPDATE groups SET "+c.set+" WHERE id = $1", hosted)
		refused(c.name)
		exec("UPDATE groups SET "+c.reset+" WHERE id = $1", hosted)
		admitted(c.name + " restored")
	}

	// Another authenticated user never takes the owner path on this account,
	// even if allowed into the group.
	exec("INSERT INTO user_allowed_groups (user_id, group_id) VALUES ($1, $2)", other, hosted)
	otherReq := &service.MarketAdmissionRequest{Class: service.MarketClassOwner, AccountID: acc, GroupID: hosted, AuthUserID: other}
	require.Equal(t, "not_owner", admit(t, f, sel, otherReq).Reason)
	exec("DELETE FROM user_allowed_groups WHERE user_id = $1 AND group_id = $2", other, hosted)

	// The predicate is not evaluated on the platform path.
	tier := f.group(t, true)
	pAcc := f.account(t, 0)
	require.NoError(t, f.accounts.BindGroups(ctx, pAcc, []int64{tier}))
	pSel := &service.MarketSelection{Class: service.MarketClassPlatform, AccountID: pAcc, GroupID: tier, Concurrency: 4}
	pReq := &service.MarketAdmissionRequest{Class: service.MarketClassPlatform, AccountID: pAcc, GroupID: tier, AuthUserID: owner}
	pv := admit(t, f, pSel, pReq)
	require.True(t, pv.Admitted, pv.Reason)
	require.False(t, pv.Facts.OwnerGroupQualified)
}
