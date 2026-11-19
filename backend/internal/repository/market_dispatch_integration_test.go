//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// admit runs the repository admission with the service's real evaluation.
func admit(t *testing.T, f *marketFixture, sel *service.MarketSelection, req *service.MarketAdmissionRequest) *service.MarketAdmissionVerdict {
	t.Helper()
	verdict, err := f.market.AdmitMarketCandidate(context.Background(), req, func(facts *service.MarketAuthoritativeFacts) string {
		return service.EvaluateMarketAdmission(sel, req, facts)
	})
	require.NoError(t, err)
	return verdict
}

func buyerSelection(t *testing.T, f *marketFixture, accountID, tier, owner int64) *service.MarketSelection {
	t.Helper()
	l, err := f.market.GetListing(context.Background(), accountID)
	require.NoError(t, err)
	return &service.MarketSelection{
		Class: service.MarketClassBuyer, AccountID: accountID, GroupID: tier, OwnerUserID: owner,
		ListingVersion: l.Version, Concurrency: 4, BuyerMaxConcurrency: 4 - l.Protection.ReservedConcurrency,
		ReservedDevices: l.Protection.ReservedDevices, ReservedAdmissions: l.Protection.ReservedAdmissions,
		DeviceLimit: 5, // fixture: claude_oauth_carpool_device_limit = 5, no cycle budget
	}
}

func buyerRequest(accountID, tier, buyer int64) *service.MarketAdmissionRequest {
	return &service.MarketAdmissionRequest{
		Class: service.MarketClassBuyer, AttemptID: uuid.NewString(), RequestRef: "req-" + uuid.NewString(),
		AccountID: accountID, GroupID: tier, AuthUserID: buyer, APIKeyID: 77,
	}
}

func admissionCount(t *testing.T, accountID int64) int {
	t.Helper()
	var n int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM market_admissions WHERE account_id=$1", accountID).Scan(&n))
	return n
}

func TestMarketAdmitBuyerWritesAdmission(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner, buyer := f.user(t), f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	_, err := f.list(acc, owner, tier)
	require.NoError(t, err)

	state, err := f.market.LoadMarketDispatchState(ctx)
	require.NoError(t, err)
	require.True(t, state.Enabled)
	require.Contains(t, state.ListedAccountIDs, acc)

	sel := buyerSelection(t, f, acc, tier, owner)
	req := buyerRequest(acc, tier, buyer)
	v := admit(t, f, sel, req)
	require.True(t, v.Admitted, v.Reason)
	require.NotNil(t, v.Admission)
	var lastEvent int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT MAX(id) FROM account_owner_events WHERE account_id=$1", acc).Scan(&lastEvent))
	require.Equal(t, lastEvent, v.Admission.OwnerEventID)
	require.Equal(t, sel.ListingVersion, v.Admission.ListingVersion)
	require.Equal(t, owner, v.Admission.OwnerUserID)
	require.Equal(t, buyer, v.Admission.BuyerUserID)
	require.Equal(t, req.RequestRef, v.Admission.RequestRef)
	require.False(t, v.Admission.AdmittedAt.IsZero())
	got, err := f.market.GetMarketAdmissionByAttempt(ctx, req.AttemptID)
	require.NoError(t, err)
	require.Equal(t, v.Admission.ID, got.ID)

	// Self purchase is still the buyer path.
	self := buyerRequest(acc, tier, owner)
	require.True(t, admit(t, f, sel, self).Admitted)
}

func TestMarketAdmitBuyerRefusesChangedState(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner, buyer := f.user(t), f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	_, err := f.list(acc, owner, tier)
	require.NoError(t, err)
	sel := buyerSelection(t, f, acc, tier, owner)

	// Protection changed while waiting (reserved concurrency 1 -> 2): the
	// listing version moved on.
	_, err = f.market.AdminConfigure(ctx, service.MarketAdminConfigureParams{
		AccountID: acc, AdminUserID: 1, AllowedGroupIDs: []int64{tier}, Defaults: marketTestDefaults(),
		Merge: func(p service.MarketProtectionParams) service.MarketProtectionParams {
			p.ReservedConcurrency = 2
			return p
		},
	})
	require.NoError(t, err)
	v := admit(t, f, sel, buyerRequest(acc, tier, buyer))
	require.False(t, v.Admitted)
	require.Equal(t, "listing_changed", v.Reason)
	sel = buyerSelection(t, f, acc, tier, owner)
	require.True(t, admit(t, f, sel, buyerRequest(acc, tier, buyer)).Admitted)

	// Seller disabled.
	_, err = integrationDB.ExecContext(ctx, "UPDATE users SET status='disabled' WHERE id=$1", owner)
	require.NoError(t, err)
	v = admit(t, f, sel, buyerRequest(acc, tier, buyer))
	require.Equal(t, "owner_inactive", v.Reason)
	_, err = integrationDB.ExecContext(ctx, "UPDATE users SET status='active' WHERE id=$1", owner)
	require.NoError(t, err)

	// Account turned off by an admin.
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET schedulable=false WHERE id=$1", acc)
	require.NoError(t, err)
	require.Equal(t, "account_inactive", admit(t, f, sel, buyerRequest(acc, tier, buyer)).Reason)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET schedulable=true WHERE id=$1", acc)
	require.NoError(t, err)

	// Temporarily unschedulable while the request waited (same predicate as
	// scheduling: Account.IsSchedulable).
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET temp_unschedulable_until = NOW() + interval '1 hour' WHERE id=$1", acc)
	require.NoError(t, err)
	require.Equal(t, "account_inactive", admit(t, f, sel, buyerRequest(acc, tier, buyer)).Reason)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET temp_unschedulable_until = NULL WHERE id=$1", acc)
	require.NoError(t, err)
	// The carpool allowlists are part of the facts (re-checked against the
	// request identity by the gateway).
	_, err = integrationDB.ExecContext(ctx, "INSERT INTO claude_oauth_account_users (account_id, user_id) VALUES ($1, $2)", acc, owner)
	require.NoError(t, err)
	v = admit(t, f, sel, buyerRequest(acc, tier, buyer))
	require.True(t, v.Admitted, v.Reason)
	require.Equal(t, []int64{owner}, v.Facts.Account.CarpoolUserIDs)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM claude_oauth_account_users WHERE account_id = $1", acc)
	require.NoError(t, err)

	// Later-ineligible configuration (max_sessions set by an admin).
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = extra || '{"max_sessions": 3}'::jsonb WHERE id=$1`, acc)
	require.NoError(t, err)
	require.Equal(t, "ineligible_"+service.MarketReasonMaxSessions, admit(t, f, sel, buyerRequest(acc, tier, buyer)).Reason)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = extra - 'max_sessions' WHERE id=$1`, acc)
	require.NoError(t, err)

	// Device limit raised by an admin while the request waited.
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = extra || '{"claude_oauth_carpool_device_limit": 8}'::jsonb WHERE id=$1`, acc)
	require.NoError(t, err)
	require.Equal(t, "device_policy_changed", admit(t, f, sel, buyerRequest(acc, tier, buyer)).Reason)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra = extra || '{"claude_oauth_carpool_device_limit": 5}'::jsonb WHERE id=$1`, acc)
	require.NoError(t, err)

	// Concurrency changed (buyer cap C-R would differ).
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET concurrency=5 WHERE id=$1", acc)
	require.NoError(t, err)
	require.Equal(t, "capacity_changed", admit(t, f, sel, buyerRequest(acc, tier, buyer)).Reason)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET concurrency=4 WHERE id=$1", acc)
	require.NoError(t, err)

	// Switch off: no new market admissions.
	f.setMarketEnabled(t, false)
	require.Equal(t, "market_disabled", admit(t, f, sel, buyerRequest(acc, tier, buyer)).Reason)
	f.setMarketEnabled(t, true)

	before := admissionCount(t, acc)
	// Owner unlists while a request waited.
	svc := service.NewMarketListingService(f.market, NewSettingRepository(integrationEntClient))
	_, err = svc.OwnerSet(ctx, owner, acc, 0, false)
	require.NoError(t, err)
	require.Equal(t, "not_market_member", admit(t, f, sel, buyerRequest(acc, tier, buyer)).Reason)
	require.Equal(t, before, admissionCount(t, acc), "refusals write nothing")

	state, err := f.market.LoadMarketDispatchState(ctx)
	require.NoError(t, err)
	require.NotContains(t, state.ListedAccountIDs, acc)
}

func TestMarketAdmitPlatformAndOwnerPaths(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner, other := f.user(t), f.user(t)
	tier := f.group(t, true)
	platformAcc := f.account(t, 0)
	require.NoError(t, f.accounts.BindGroups(ctx, platformAcc, []int64{tier}))

	platformSel := &service.MarketSelection{Class: service.MarketClassPlatform, AccountID: platformAcc, GroupID: tier, Concurrency: 4}
	platformReq := &service.MarketAdmissionRequest{Class: service.MarketClassPlatform, AccountID: platformAcc, GroupID: tier, AuthUserID: other}
	v := admit(t, f, platformSel, platformReq)
	require.True(t, v.Admitted, v.Reason)
	require.Nil(t, v.Admission, "platform admissions write no row")
	require.Zero(t, admissionCount(t, platformAcc))

	// A cached "no owner" must not dispatch an account that just got one.
	_, err := f.owners.SetAccountOwner(ctx, platformAcc, &owner, 1)
	require.NoError(t, err)
	require.Equal(t, "has_owner", admit(t, f, platformSel, platformReq).Reason)
	_, err = f.owners.SetAccountOwner(ctx, platformAcc, nil, 1)
	require.NoError(t, err)

	// A revoked admin membership still present in an old cache.
	require.NoError(t, f.accounts.BindGroups(ctx, platformAcc, nil))
	require.Equal(t, "not_admin_member", admit(t, f, platformSel, platformReq).Reason)

	// Owner path: the authenticated user's own exclusive hosted group.
	hosted := f.group(t, false)
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET hosted_usage_enabled=TRUE, is_exclusive=TRUE WHERE id=$1", hosted)
	require.NoError(t, err)
	listed := f.account(t, owner)
	require.NoError(t, f.accounts.BindGroups(ctx, listed, []int64{hosted}))
	f.configure(t, listed, tier)
	_, err = f.list(listed, owner, tier)
	require.NoError(t, err)
	ownerSel := &service.MarketSelection{Class: service.MarketClassOwner, AccountID: listed, GroupID: hosted, OwnerUserID: owner, Concurrency: 4}
	ownerReq := &service.MarketAdmissionRequest{Class: service.MarketClassOwner, AccountID: listed, GroupID: hosted, AuthUserID: owner}
	require.Equal(t, "not_owner_group", admit(t, f, ownerSel, ownerReq).Reason, "the owner is not an allowed user of the group yet")
	_, err = integrationDB.ExecContext(ctx, "INSERT INTO user_allowed_groups (user_id, group_id) VALUES ($1, $2)", owner, hosted)
	require.NoError(t, err)
	v = admit(t, f, ownerSel, ownerReq)
	require.True(t, v.Admitted, v.Reason)
	require.Nil(t, v.Admission)
	// Another user of the same group is not the owner.
	require.Equal(t, "not_owner", admit(t, f, ownerSel, &service.MarketAdmissionRequest{Class: service.MarketClassOwner, AccountID: listed, GroupID: hosted, AuthUserID: other}).Reason)
	// The tier itself is never an owner group.
	require.Equal(t, "not_owner_group", admit(t, f, &service.MarketSelection{Class: service.MarketClassOwner, AccountID: listed, GroupID: tier, OwnerUserID: owner},
		&service.MarketAdmissionRequest{Class: service.MarketClassOwner, AccountID: listed, GroupID: tier, AuthUserID: owner}).Reason)

	n, err := f.market.CountMarketTierPlatformMembers(ctx, tier)
	require.NoError(t, err)
	require.Zero(t, n, "the platform account left the tier; the listed one is not a platform member")
	require.NoError(t, f.accounts.BindGroups(ctx, platformAcc, []int64{tier}))
	n, err = f.market.CountMarketTierPlatformMembers(ctx, tier)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
