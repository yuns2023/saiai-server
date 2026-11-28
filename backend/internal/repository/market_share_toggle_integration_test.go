//go:build integration

package repository

import (
	"context"
	"database/sql"
	"io/fs"
	"strconv"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// 共享/自用 toggle (migration 138): plan-derived tiers, owner switch
// cooldown, approval-created listings.

// backdateOwnerSwitches moves the account's owner switch events into the
// past (database time). History rows are insert-only; the test bypasses the
// trigger as the superuser.
func backdateOwnerSwitches(t *testing.T, accountID int64, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "SET LOCAL session_replication_role = replica")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE account_market_listing_events SET created_at = clock_timestamp() - make_interval(secs => $2)
		WHERE account_id = $1`, accountID, age.Seconds())
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func ownerSwitchCount(t *testing.T, accountID int64) int {
	t.Helper()
	return hostedCountRows(t, `SELECT COUNT(*) FROM account_market_listing_events
		WHERE account_id = $1 AND action IN ('owner_list', 'owner_unlist', 'owner_switch')`, accountID)
}

func requireCooldown(t *testing.T, err error, around time.Duration) time.Time {
	t.Helper()
	requireReason(t, err, "MARKET_SWITCH_COOLDOWN")
	app := infraerrors.FromError(err)
	require.EqualValues(t, 429, app.Code)
	next, parseErr := time.Parse(time.RFC3339, app.Metadata["next_switch_at"])
	require.NoError(t, parseErr)
	seconds, convErr := strconv.ParseInt(app.Metadata["retry_after_seconds"], 10, 64)
	require.NoError(t, convErr)
	require.InDelta(t, around.Seconds(), float64(seconds), 120, "retry after")
	return next
}

func TestMarketOwnerSwitchCooldown(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	f.setCooldownHours(t, 24)
	owner := f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	svc := service.NewMarketListingService(f.market, NewSettingRepository(integrationEntClient))

	// First switch: no history, allowed; the plan's only tier is implied.
	_, err := svc.OwnerSet(ctx, owner, acc, 0, true)
	require.NoError(t, err)
	require.Equal(t, service.MarketListingStatusListed, marketListingState(t, f, acc).Status)
	require.Equal(t, tier, *marketListingState(t, f, acc).SelectedGroupID)
	view, err := svc.OwnerGet(ctx, owner, acc)
	require.NoError(t, err)
	require.Equal(t, service.MarketModeShared, view.Mode)
	require.Equal(t, 24, view.CooldownHours)
	require.NotNil(t, view.NextSwitchAt)
	require.WithinDuration(t, time.Now().Add(24*time.Hour), *view.NextSwitchAt, 2*time.Minute)
	require.False(t, view.CanUnlist)
	require.Contains(t, view.UnavailableReasons, service.MarketReasonSwitchCooldown)

	// Same target again: idempotent, no event, no cooldown use.
	version := marketListingState(t, f, acc).Version
	_, err = svc.OwnerSet(ctx, owner, acc, tier, true)
	require.NoError(t, err)
	_, err = svc.OwnerSet(ctx, owner, acc, 0, true)
	require.NoError(t, err)
	require.Equal(t, version, marketListingState(t, f, acc).Version)
	require.Equal(t, 1, ownerSwitchCount(t, acc))

	// Switching back inside the cooldown: 429 with the database time.
	_, err = svc.OwnerSet(ctx, owner, acc, 0, false)
	next := requireCooldown(t, err, 24*time.Hour)
	require.WithinDuration(t, *view.NextSwitchAt, next, time.Second)
	require.Equal(t, service.MarketListingStatusListed, marketListingState(t, f, acc).Status)

	// Boundary: one minute short is refused, past the boundary is allowed.
	backdateOwnerSwitches(t, acc, 24*time.Hour-time.Minute)
	_, err = svc.OwnerSet(ctx, owner, acc, 0, false)
	requireCooldown(t, err, time.Minute)
	backdateOwnerSwitches(t, acc, 24*time.Hour)
	_, err = svc.OwnerSet(ctx, owner, acc, 0, false)
	require.NoError(t, err, "an elapsed cooldown (boundary equal) allows the switch")
	require.Equal(t, service.MarketListingStatusUnlisted, marketListingState(t, f, acc).Status)
	// Self-use again: a same-target PUT is idempotent even inside the cooldown.
	_, err = svc.OwnerSet(ctx, owner, acc, 0, false)
	require.NoError(t, err)
	require.Equal(t, 2, ownerSwitchCount(t, acc))

	// A smaller setting applies at once; 0 = no cooldown.
	f.setCooldownHours(t, 0)
	_, err = svc.OwnerSet(ctx, owner, acc, 0, true)
	require.NoError(t, err)
	view, err = svc.OwnerGet(ctx, owner, acc)
	require.NoError(t, err)
	require.Nil(t, view.NextSwitchAt)
	require.True(t, view.CanUnlist)
	f.setCooldownHours(t, 24)

	// Same target while the switch is off or the tier inactive: still 200.
	f.setMarketEnabled(t, false)
	_, err = svc.OwnerSet(ctx, owner, acc, tier, true)
	require.NoError(t, err, "already shared: idempotent with the market off")
	f.setMarketEnabled(t, true)
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET status = 'inactive' WHERE id = $1", tier)
	require.NoError(t, err)
	_, err = svc.OwnerSet(ctx, owner, acc, 0, true)
	require.NoError(t, err, "already shared: idempotent with the tier inactive")
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET status = 'active' WHERE id = $1", tier)
	require.NoError(t, err)

	// Market switched off: taking the account back is always allowed.
	f.setMarketEnabled(t, false)
	view, err = svc.OwnerGet(ctx, owner, acc)
	require.NoError(t, err)
	require.True(t, view.CanUnlist, "market off: take-back regardless of the cooldown")
	_, err = svc.OwnerSet(ctx, owner, acc, 0, false)
	require.NoError(t, err)
	require.Equal(t, service.MarketListingStatusUnlisted, marketListingState(t, f, acc).Status)
	f.setMarketEnabled(t, true)
	// ... and it is an effective switch: listing again waits.
	_, err = svc.OwnerSet(ctx, owner, acc, 0, true)
	requireCooldown(t, err, 24*time.Hour)

	// The setting is read after the account lock: lowering it while the PUT
	// waits applies to that PUT.
	holder, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	_, err = lockMarketAccountTx(ctx, holder, acc)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := svc.OwnerSet(ctx, owner, acc, 0, true)
		done <- err
	}()
	waitForLockWait(t)
	f.setCooldownHours(t, 0)
	require.NoError(t, holder.Rollback())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("owner PUT did not finish")
	}
	require.Equal(t, service.MarketListingStatusListed, marketListingState(t, f, acc).Status)
}

func TestMarketOwnerSwitchCooldownIgnoresNonOwnerEvents(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	f.setCooldownHours(t, 24)
	owner := f.user(t)
	tier := f.group(t, true)
	svc := service.NewMarketListingService(f.market, NewSettingRepository(integrationEntClient))

	// Admin revoke + re-grant and a tier demotion neither count nor reset.
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	_, err := svc.OwnerSet(ctx, owner, acc, 0, true)
	require.NoError(t, err)
	listedAt, err := f.market.NextOwnerSwitchAt(ctx, acc, 24)
	require.NoError(t, err)
	require.NotNil(t, listedAt)
	_, err = f.market.AdminRevoke(ctx, acc, 1)
	require.NoError(t, err)
	f.configure(t, acc, tier)
	after, err := f.market.NextOwnerSwitchAt(ctx, acc, 24)
	require.NoError(t, err)
	require.NotNil(t, after, "admin actions do not reset the owner's cooldown")
	require.True(t, listedAt.Equal(*after))
	backdateOwnerSwitches(t, acc, 25*time.Hour)
	_, err = svc.OwnerSet(ctx, owner, acc, 0, true)
	require.NoError(t, err)
	g, err := f.groups.GetByID(ctx, tier)
	require.NoError(t, err)
	g.MarketTier = false
	require.NoError(t, f.groups.Update(ctx, g))
	require.Equal(t, service.MarketListingStatusUnlisted, marketListingState(t, f, acc).Status)
	next, err := f.market.NextOwnerSwitchAt(ctx, acc, 24)
	require.NoError(t, err)
	require.NotNil(t, next, "the owner_list still counts; the demotion does not")

	// Withdrawal unlists with its own action: not an owner switch.
	tier2 := f.group(t, true)
	withdrawn := f.account(t, owner)
	f.configure(t, withdrawn, tier2)
	_, err = svc.OwnerSet(ctx, owner, withdrawn, 0, true)
	require.NoError(t, err)
	backdateOwnerSwitches(t, withdrawn, 25*time.Hour)
	_, err = f.apps.SetOwnerAccountState(ctx, owner, withdrawn, service.HostedOwnerActionWithdraw)
	require.NoError(t, err)
	require.Equal(t, service.MarketListingStatusUnlisted, marketListingState(t, f, withdrawn).Status)
	actions := marketEventActions(t, withdrawn)
	require.Equal(t, service.MarketListingActionOwnerWithdraw, actions[len(actions)-1])
	require.Equal(t, 1, ownerSwitchCount(t, withdrawn))
	next, err = f.market.NextOwnerSwitchAt(ctx, withdrawn, 24)
	require.NoError(t, err)
	require.Nil(t, next, "the withdrawal unlist is not counted")
	// Pause writes nothing at all.
	paused := f.account(t, owner)
	f.configure(t, paused, tier2)
	_, err = svc.OwnerSet(ctx, owner, paused, 0, true)
	require.NoError(t, err)
	backdateOwnerSwitches(t, paused, 25*time.Hour)
	_, err = f.apps.SetOwnerAccountState(ctx, owner, paused, service.HostedOwnerActionPause)
	require.NoError(t, err)
	next, err = f.market.NextOwnerSwitchAt(ctx, paused, 24)
	require.NoError(t, err)
	require.Nil(t, next)
	_, err = svc.OwnerSet(ctx, owner, paused, 0, false)
	require.NoError(t, err, "a paused owner can take the account back")
}

func TestMarketOwnerSwitchConcurrentPuts(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	f.setCooldownHours(t, 24)
	owner := f.user(t)
	tierA := f.group(t, true)
	tierB := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tierA, tierB) // one plan, two tiers
	svc := service.NewMarketListingService(f.market, NewSettingRepository(integrationEntClient))

	_, err := svc.OwnerSet(ctx, owner, acc, 0, true)
	requireReason(t, err, "MARKET_TIER_REQUIRED")
	require.Zero(t, ownerSwitchCount(t, acc))

	// Two PUTs for different tiers queue on the account row lock: the first
	// switches, the second hits the cooldown.
	holder, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	_, err = lockMarketAccountTx(ctx, holder, acc)
	require.NoError(t, err)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, g := range []int64{tierA, tierB} {
		wg.Add(1)
		go func(groupID int64) {
			defer wg.Done()
			_, err := svc.OwnerSet(ctx, owner, acc, groupID, true)
			errs <- err
		}(g)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if hostedCountRows(t, "SELECT COUNT(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()") >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, holder.Rollback())
	wg.Wait()
	close(errs)
	var ok, cooldown int
	for err := range errs {
		if err == nil {
			ok++
			continue
		}
		requireReason(t, err, "MARKET_SWITCH_COOLDOWN")
		cooldown++
	}
	require.Equal(t, 1, ok)
	require.Equal(t, 1, cooldown)
	require.Equal(t, 1, ownerSwitchCount(t, acc), "exactly one effective switch")
	require.Equal(t, service.MarketListingStatusListed, marketListingState(t, f, acc).Status)
	require.Len(t, marketMemberships(t, acc), 1)

	// A tier of another plan is never allowed.
	other := f.group(t, true)
	backdateOwnerSwitches(t, acc, 25*time.Hour)
	_, err = svc.OwnerSet(ctx, owner, acc, other, true)
	requireReason(t, err, "MARKET_GROUP_NOT_ALLOWED")
	// An inactive tier of the plan is not offered either.
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET status = 'inactive' WHERE id = $1", tierB)
	require.NoError(t, err)
	view, err := svc.OwnerGet(ctx, owner, acc)
	require.NoError(t, err)
	available := 0
	for _, tier := range view.AllowedTiers {
		if tier.Available {
			available++
			require.Equal(t, tierA, tier.GroupID)
		}
	}
	require.Equal(t, 1, available)
}

func TestMarketOwnerViewUpstreamWindows(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	svc := service.NewMarketListingService(f.market, NewSettingRepository(integrationEntClient))

	view, err := svc.OwnerGet(ctx, owner, acc)
	require.NoError(t, err)
	require.Empty(t, view.UpstreamWindows, "no sample: unknown")
	end := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	reset := time.Now().Add(72 * time.Hour).Unix()
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET session_window_end = $2,
		extra = extra || jsonb_build_object('session_window_utilization', 0.4, 'passive_usage_7d_utilization', 0.25,
			'passive_usage_7d_reset', $3::bigint, 'passive_usage_sampled_at', to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))
		WHERE id = $1`, acc, end, reset)
	require.NoError(t, err)
	view, err = svc.OwnerGet(ctx, owner, acc)
	require.NoError(t, err)
	keys := map[string]service.HostedQuotaWindow{}
	for _, w := range view.UpstreamWindows {
		keys[w.Key] = w
	}
	require.InDelta(t, 40, keys["five_hour"].Utilization, 1e-9)
	require.NotNil(t, keys["five_hour"].ResetsAt)
	require.True(t, end.Equal(*keys["five_hour"].ResetsAt))
	require.InDelta(t, 25, keys["seven_day"].Utilization, 1e-9)
	require.Equal(t, reset, keys["seven_day"].ResetsAt.Unix())
}

func TestMarketApprovalCreatesListingInSameTransaction(t *testing.T) {
	ctx := context.Background()
	market := newMarketFixture(t)
	f := newHostedFixture(t)
	tier := market.group(t, true)
	plan := marketGroupPlan(t, tier)
	owner := f.user(t)
	proxy := f.proxy(t, service.StatusActive)
	cleanupListing := func(accountID int64) {
		t.Cleanup(func() {
			bg := context.Background()
			if tx, err := integrationDB.BeginTx(bg, nil); err == nil {
				_, _ = tx.ExecContext(bg, "SET LOCAL session_replication_role = replica")
				_, _ = tx.ExecContext(bg, "DELETE FROM account_market_listing_events WHERE account_id = $1", accountID)
				_ = tx.Commit()
			}
			_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_market_listings WHERE account_id = $1", accountID)
		})
	}

	// Unknown plan: the whole approval rolls back (no account, still pending).
	bad := f.submit(t, owner.ID, uuid.NewString(), nil)
	_, err := f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: bad.ID, AdminUserID: 1, ExistingProxyID: proxy.ID,
		Build: hostedTestSpec, MarketPlan: "no-such-plan"})
	requireReason(t, err, "MARKET_PLAN_UNKNOWN")
	require.Zero(t, hostedCountRows(t, "SELECT COUNT(*) FROM accounts WHERE owner_user_id = $1", owner.ID))
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM hosted_account_applications WHERE id = $1 AND status = 'pending'", bad.ID))
	// A plan whose only tier is inactive is unknown too.
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET status = 'inactive' WHERE id = $1", tier)
	require.NoError(t, err)
	_, err = f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: bad.ID, AdminUserID: 1, ExistingProxyID: proxy.ID,
		Build: hostedTestSpec, MarketPlan: plan})
	requireReason(t, err, "MARKET_PLAN_UNKNOWN")
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET status = 'active' WHERE id = $1", tier)
	require.NoError(t, err)

	res, err := f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: bad.ID, AdminUserID: 1, ExistingProxyID: proxy.ID,
		Build: hostedTestSpec, MarketPlan: plan})
	require.NoError(t, err)
	cleanupListing(res.AccountID)
	l, err := market.market.GetListing(ctx, res.AccountID)
	require.NoError(t, err)
	require.NotNil(t, l, "the approval created the listing")
	require.Equal(t, service.MarketListingStatusUnlisted, l.Status, "自用")
	require.Equal(t, plan, *l.Plan)
	require.Equal(t, owner.ID, l.OwnerUserID)
	require.Equal(t, int64(1), l.Version)
	require.Equal(t, [3]int{0, 0, 0}, marketReservations(t, res.AccountID))
	require.Equal(t, []string{service.MarketListingActionAdminConfigure}, marketEventActions(t, res.AccountID))
	require.Empty(t, marketMemberships(t, res.AccountID)[tier], "not listed")

	// Replay: the original result, no second listing or event.
	again, err := f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: bad.ID, AdminUserID: 1, ExistingProxyID: proxy.ID,
		Build: hostedTestSpec, MarketPlan: plan})
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.Equal(t, res.AccountID, again.AccountID)
	require.Equal(t, []string{service.MarketListingActionAdminConfigure}, marketEventActions(t, res.AccountID))

	// The owner can share it right away (no per-account admin setup).
	svc := service.NewMarketListingService(market.market, NewSettingRepository(integrationEntClient))
	view, err := svc.OwnerGet(ctx, owner.ID, res.AccountID)
	require.NoError(t, err)
	require.True(t, view.CanList, view.UnavailableReasons)
	_, err = svc.OwnerSet(ctx, owner.ID, res.AccountID, 0, true)
	require.NoError(t, err)
	require.Equal(t, service.MarketListingStatusListed, marketListingState(t, market, res.AccountID).Status)

	// Without a plan: no listing row.
	plain := f.submit(t, owner.ID, uuid.NewString(), nil)
	res2, err := f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: plain.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
	require.NoError(t, err)
	l, err = market.market.GetListing(ctx, res2.AccountID)
	require.NoError(t, err)
	require.Nil(t, l)
}

func migrationsThrough137(t *testing.T) fstest.MapFS {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	result := make(fstest.MapFS)
	for _, name := range files {
		if name >= "138_" {
			continue
		}
		data, readErr := fs.ReadFile(migrations.FS, name)
		require.NoError(t, readErr)
		result[name] = &fstest.MapFile{Data: data}
	}
	return result
}

func TestMigration138NormalizesReservationsAndAddsPlans(t *testing.T) {
	ctx := context.Background()
	db := openIsolatedPaymentMigrationDatabase(t)
	require.NoError(t, applyMigrationsFS(ctx, db, migrationsThrough137(t)))
	_, err := db.ExecContext(ctx, `INSERT INTO account_market_listings (account_id, owner_user_id) VALUES (101, 7), (102, 7)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE account_market_listings SET reserved_concurrency = 2, reserved_devices = 3 WHERE account_id = 102`)
	require.NoError(t, err)
	// Legacy multi-tier permission: listed in 31 with 31 and 32 allowed;
	// unlisted with 33 allowed and nothing selected.
	_, err = db.ExecContext(ctx, `INSERT INTO account_market_listings (account_id, owner_user_id, allowed_group_ids, selected_group_id, status, listed_at)
		VALUES (104, 7, '{31,32}', 31, 'listed', NOW()), (105, 7, '{33}', NULL, 'unlisted', NULL)`)
	require.NoError(t, err)

	require.NoError(t, applyMigrationsFS(ctx, db, migrations.FS))
	require.NoError(t, applyMigrationsFS(ctx, db, migrations.FS), "re-applying is a no-op")
	body, err := migrations.FS.ReadFile("138_market_share_toggle.sql")
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err, "the file itself is idempotent")
	require.NoError(t, tx.Rollback())

	var nonZero int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_market_listings
		WHERE reserved_concurrency <> 0 OR reserved_devices <> 0 OR reserved_admissions <> 0`).Scan(&nonZero))
	require.Zero(t, nonZero, "existing rows normalized")
	var mirrored104, mirrored105 pq.Int64Array
	require.NoError(t, db.QueryRowContext(ctx, `SELECT allowed_group_ids FROM account_market_listings WHERE account_id = 104`).Scan(&mirrored104))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT allowed_group_ids FROM account_market_listings WHERE account_id = 105`).Scan(&mirrored105))
	require.Equal(t, []int64{31}, []int64(mirrored104), "allowed mirrors the selection")
	require.Empty(t, mirrored105)
	_, err = db.ExecContext(ctx, `INSERT INTO account_market_listings (account_id, owner_user_id) VALUES (103, 7)`)
	require.NoError(t, err)
	var r1, r2, r3 int
	var plan sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT reserved_concurrency, reserved_devices, reserved_admissions, plan
		FROM account_market_listings WHERE account_id = 103`).Scan(&r1, &r2, &r3, &plan))
	require.Equal(t, []int{0, 0, 0}, []int{r1, r2, r3}, "defaults are 0")
	require.False(t, plan.Valid)
	// No CHECK forces 0 (the previous binary may still write its values).
	_, err = db.ExecContext(ctx, `UPDATE account_market_listings SET reserved_concurrency = 1 WHERE account_id = 103`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `UPDATE account_market_listings SET plan = 'max' WHERE account_id = 101`)
	require.NoError(t, err)
	for _, bad := range []string{"Max", "-max", "", "a12345678901234567890123456789012"} {
		_, err = db.ExecContext(ctx, `UPDATE account_market_listings SET plan = $1 WHERE account_id = 101`, bad)
		requirePQCode(t, err, "23514", "listing plan "+bad)
	}
	var groupID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups (name, platform, rate_multiplier, subscription_type, status)
		VALUES ('m138', 'anthropic', 1, 'standard', 'active') RETURNING id`).Scan(&groupID))
	_, err = db.ExecContext(ctx, `UPDATE groups SET market_tier = TRUE, market_plan = 'max' WHERE id = $1`, groupID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE groups SET market_plan = 'Max' WHERE id = $1`, groupID)
	requirePQCode(t, err, "23514", "group plan format")
	// The previous binary demotes a tier without touching the plan: allowed.
	_, err = db.ExecContext(ctx, `UPDATE groups SET market_tier = FALSE WHERE id = $1`, groupID)
	require.NoError(t, err)
	var indexes int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_indexes
		WHERE tablename = 'account_market_listing_events' AND indexname = 'account_market_listing_events_owner_switch_idx'`).Scan(&indexes))
	require.Equal(t, 1, indexes)
}

func TestMarketGroupPlanRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	plan := marketTestPlan()
	g := &service.Group{Name: "plan-" + uuid.NewString(), Platform: service.PlatformAnthropic, RateMultiplier: 0.3,
		Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard, MarketTier: true, MarketPlan: &plan}
	require.NoError(t, f.groups.Create(ctx, g))
	f.groupIDs = append(f.groupIDs, g.ID)
	got, err := f.groups.GetByID(ctx, g.ID)
	require.NoError(t, err)
	require.Equal(t, plan, *got.MarketPlan)
	got.MarketPlan, got.MarketPlanWrite = nil, true
	require.NoError(t, f.groups.Update(ctx, got))
	got, err = f.groups.GetByID(ctx, g.ID)
	require.NoError(t, err)
	require.Nil(t, got.MarketPlan, "cleared")
	bad := "Bad Plan"
	got.MarketPlan, got.MarketPlanWrite = &bad, true
	require.Error(t, f.groups.Update(ctx, got), "the format CHECK backs the service validation")
}

// Two admins save the same tier: A changes the plan, B (who read the group
// before A's save) renames it without sending market_plan. B's save must
// not write A's change back (it would also unlist A's accounts).
func TestMarketGroupStaleSaveKeepsPlan(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	stale, err := f.groups.GetByID(ctx, tier) // admin B reads
	require.NoError(t, err)
	acc := f.account(t, owner)
	newPlan := marketTestPlan()
	fresh, err := f.groups.GetByID(ctx, tier) // admin A changes the plan
	require.NoError(t, err)
	fresh.MarketPlan, fresh.MarketPlanWrite = &newPlan, true
	require.NoError(t, f.groups.Update(ctx, fresh))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_market_listings (account_id, owner_user_id, plan) VALUES ($1, $2, $3)
		ON CONFLICT (account_id) DO UPDATE SET plan = EXCLUDED.plan`, acc, owner, newPlan)
	require.NoError(t, err)
	_, err = f.list(acc, owner, tier)
	require.NoError(t, err)

	stale.Description = "renamed by B" // MarketPlanWrite false: the field was not sent
	require.NoError(t, f.groups.Update(ctx, stale))
	require.Equal(t, newPlan, marketGroupPlan(t, tier), "B's save kept A's plan")
	require.Equal(t, service.MarketListingStatusListed, marketListingState(t, f, acc).Status, "no re-plan unlist")
	require.Equal(t, newPlan, *stale.MarketPlan, "the saved object reports the stored plan")
}

// PUT without group_id: only tiers the account can be listed in count.
func TestMarketOwnerImplicitTierSkipsAdminMemberTiers(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tierA := f.group(t, true)
	tierB := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tierA, tierB)
	require.NoError(t, f.accounts.BindGroups(ctx, acc, []int64{tierA})) // admin member of A
	svc := service.NewMarketListingService(f.market, NewSettingRepository(integrationEntClient))
	_, err := svc.OwnerSet(ctx, owner, acc, 0, true)
	require.NoError(t, err, "only B is listable: implied")
	require.Equal(t, tierB, *marketListingState(t, f, acc).SelectedGroupID)
	_, err = svc.OwnerSet(ctx, owner, acc, 0, false)
	require.NoError(t, err)
	_, err = svc.OwnerSet(ctx, owner, acc, tierA, true)
	requireReason(t, err, "MARKET_ADMIN_MEMBERSHIP_CONFLICT")
	require.NoError(t, f.accounts.BindGroups(ctx, acc, []int64{tierA, tierB}))
	_, err = svc.OwnerSet(ctx, owner, acc, 0, true)
	requireReason(t, err, "MARKET_ADMIN_MEMBERSHIP_CONFLICT")
}

// Admin A demotes a tier from a form read while the plan was empty; admin B
// sets a plan and commits first. A's locked write turns market_tier off and
// must clear B's plan with it (no plan on a non-tier).
func TestMarketConcurrentDemotionClearsPlan(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	tier := f.group(t, true)
	_, err := integrationDB.ExecContext(ctx, "UPDATE groups SET market_plan = NULL WHERE id = $1", tier)
	require.NoError(t, err)
	staleA, err := f.groups.GetByID(ctx, tier) // A reads: tier, no plan
	require.NoError(t, err)
	require.Nil(t, staleA.MarketPlan)

	b, err := f.groups.GetByID(ctx, tier) // B sets max and commits first
	require.NoError(t, err)
	plan := marketTestPlan()
	b.MarketPlan, b.MarketPlanWrite = &plan, true
	require.NoError(t, f.groups.Update(ctx, b))
	require.Equal(t, plan, marketGroupPlan(t, tier))

	staleA.MarketTier = false // A demotes (its form never carried a plan)
	require.False(t, staleA.MarketPlanWrite)
	require.NoError(t, f.groups.Update(ctx, staleA))
	var tierNow bool
	var planNow sql.NullString
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT market_tier, market_plan FROM groups WHERE id = $1", tier).Scan(&tierNow, &planNow))
	require.False(t, tierNow)
	require.False(t, planNow.Valid, "the demotion cleared the plan set meanwhile")
	require.Nil(t, staleA.MarketPlan)
}
