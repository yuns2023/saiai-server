package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccountgroup "github.com/Wei-Shaw/sub2api/ent/accountgroup"
	dbpredicate "github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// dispatchableAccountGroupPredicate selects admin bindings. Admin views and
// Account.GroupIDs use it (isDispatchableAccountGroupSource on loaded rows);
// scheduling reads by group also return market rows and leave the decision
// to the gateway's market classification (service/market_dispatch.go).
func dispatchableAccountGroupPredicate() dbpredicate.AccountGroup {
	return dbaccountgroup.SourceEQ(service.AccountGroupSourceAdmin)
}

func isDispatchableAccountGroupSource(source string) bool {
	return source == service.AccountGroupSourceAdmin
}

// translateMarketTierGuardError maps the migration 134 tier guards (CHECK
// and triggers) to the service errors; other errors pass through.
func translateMarketTierGuardError(err error) error {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr.Code != "23514" {
		return err
	}
	switch pqErr.Constraint {
	case "market_tier_fallback":
		return service.ErrMarketTierFallback
	case "market_tier_rate_override":
		return service.ErrMarketTierRateOverride
	case "groups_market_tier_check":
		return service.ErrMarketTierInvalid
	}
	return err
}

type marketListingRepository struct {
	db       *sql.DB
	accounts *accountRepository
	// afterAdmissionAccountRead is a test hook (nil in production) run
	// between the account read and the remaining reads of AdmitMarket.
	afterAdmissionAccountRead func()
}

// NewMarketListingRepository serves the listing lifecycle: one transaction
// per change, account row locked, outbox rows in the same transaction.
func NewMarketListingRepository(client *dbent.Client, sqlDB *sql.DB, schedulerCache service.SchedulerCache) service.MarketListingRepository {
	return newMarketListingRepository(client, sqlDB, schedulerCache)
}

// NewMarketAdmissionRepository is the market admission store (same
// implementation; it has no state besides the connections).
func NewMarketAdmissionRepository(client *dbent.Client, sqlDB *sql.DB, schedulerCache service.SchedulerCache) service.MarketAdmissionRepository {
	return newMarketListingRepository(client, sqlDB, schedulerCache)
}

func newMarketListingRepository(client *dbent.Client, sqlDB *sql.DB, schedulerCache service.SchedulerCache) *marketListingRepository {
	return &marketListingRepository{
		db:       sqlDB,
		accounts: newAccountRepositoryWithSQL(client, sqlDB, schedulerCache),
	}
}

// marketListingColumns: allowed_group_ids and reserved_* are not read (the
// plan is the only permission, reservations are always 0).
const marketListingColumns = `account_id, owner_user_id, plan, selected_group_id, status, version,
	listed_at, created_at, updated_at`

func scanMarketListing(rows *sql.Rows) (*service.MarketListing, error) {
	var (
		l        service.MarketListing
		plan     sql.NullString
		selected sql.NullInt64
		listedAt sql.NullTime
	)
	if err := rows.Scan(&l.AccountID, &l.OwnerUserID, &plan, &selected, &l.Status, &l.Version,
		&listedAt, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	l.AllowedGroupIDs = []int64{}
	if plan.Valid {
		v := plan.String
		l.Plan = &v
	}
	if selected.Valid {
		v := selected.Int64
		l.SelectedGroupID = &v
	}
	if listedAt.Valid {
		v := listedAt.Time
		l.ListedAt = &v
	}
	return &l, nil
}

func queryMarketListing(ctx context.Context, q sqlQueryer, query string, args ...any) (*service.MarketListing, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, rows.Err()
	}
	l, err := scanMarketListing(rows)
	if err != nil {
		return nil, err
	}
	return l, rows.Err()
}

func (r *marketListingRepository) GetListing(ctx context.Context, accountID int64) (*service.MarketListing, error) {
	return queryMarketListing(ctx, r.db,
		"SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1", accountID)
}

// marketAccountColumns are the account fields the listing checks need.
const marketAccountColumns = `id, platform, type, extra, concurrency, status, schedulable,
	owner_user_id, owner_paused_at, owner_withdraw_requested_at, self_onboarded, proxy_id,
	(SELECT p.status FROM proxies p WHERE p.id = accounts.proxy_id AND p.deleted_at IS NULL),
	(SELECT p.host FROM proxies p WHERE p.id = accounts.proxy_id AND p.deleted_at IS NULL),
	temp_unschedulable_until, rate_limit_reset_at, overload_until, expires_at, auto_pause_on_expired,
	jsonb_build_object('expires_at', credentials->'expires_at'),
	ARRAY(SELECT u.user_id FROM claude_oauth_account_users u WHERE u.account_id = accounts.id),
	ARRAY(SELECT k.api_key_id FROM claude_oauth_account_api_keys k JOIN api_keys ak ON ak.id = k.api_key_id
		WHERE k.account_id = accounts.id AND ak.deleted_at IS NULL),
	session_window_end`

func scanMarketAccount(ctx context.Context, q sqlQueryer, query string, args ...any) (*service.Account, error) {
	var (
		a                                       service.Account
		extra                                   []byte
		owner                                   sql.NullInt64
		paused                                  sql.NullTime
		withdrawn                               sql.NullTime
		proxyID                                 sql.NullInt64
		proxyStat                               sql.NullString
		proxyHost                               sql.NullString
		tempUntil, rateReset, overload, expires sql.NullTime
		autoPause                               sql.NullBool
		creds                                   []byte
		users, keys                             pq.Int64Array
		sessionEnd                              sql.NullTime
	)
	if err := scanSingleRow(ctx, q, query, args, &a.ID, &a.Platform, &a.Type, &extra, &a.Concurrency,
		&a.Status, &a.Schedulable, &owner, &paused, &withdrawn, &a.SelfOnboarded, &proxyID, &proxyStat, &proxyHost,
		&tempUntil, &rateReset, &overload, &expires, &autoPause, &creds, &users, &keys, &sessionEnd); err != nil {
		return nil, err
	}
	// Scheduling facts (Account.IsSchedulable) and the carpool allowlists
	// (accountAllowsUser) for the market admission re-checks; the 5h window
	// end for the owner view's upstream windows.
	for _, f := range []struct {
		src sql.NullTime
		dst **time.Time
	}{{tempUntil, &a.TempUnschedulableUntil}, {rateReset, &a.RateLimitResetAt}, {overload, &a.OverloadUntil}, {expires, &a.ExpiresAt}, {sessionEnd, &a.SessionWindowEnd}} {
		if f.src.Valid {
			v := f.src.Time
			*f.dst = &v
		}
	}
	a.AutoPauseOnExpired = autoPause.Bool
	if len(creds) > 0 {
		if err := json.Unmarshal(creds, &a.Credentials); err != nil {
			return nil, err
		}
	}
	a.CarpoolUserIDs = []int64(users)
	for _, id := range keys {
		a.CarpoolAPIKeys = append(a.CarpoolAPIKeys, service.CarpoolAPIKeyRef{ID: id})
	}
	// Live proxy (same facts as the scheduling proxy predicate).
	if proxyID.Valid {
		id := proxyID.Int64
		a.ProxyID = &id
		if proxyStat.Valid {
			a.Proxy = &service.Proxy{ID: id, Status: proxyStat.String, Host: proxyHost.String}
		}
	}
	if len(extra) > 0 {
		if err := json.Unmarshal(extra, &a.Extra); err != nil {
			return nil, err
		}
	}
	if owner.Valid {
		v := owner.Int64
		a.OwnerUserID = &v
	}
	if paused.Valid {
		v := paused.Time
		a.OwnerPausedAt = &v
	}
	if withdrawn.Valid {
		v := withdrawn.Time
		a.OwnerWithdrawRequestedAt = &v
	}
	return &a, nil
}

func (r *marketListingRepository) GetAccountFacts(ctx context.Context, accountID int64) (*service.Account, error) {
	a, err := scanMarketAccount(ctx, r.db,
		"SELECT "+marketAccountColumns+" FROM accounts WHERE id = $1 AND deleted_at IS NULL", accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAccountNotFound
	}
	return a, err
}

func (r *marketListingRepository) GetOwnedAccountFacts(ctx context.Context, ownerUserID, accountID int64) (*service.Account, error) {
	if ownerUserID <= 0 || accountID <= 0 {
		return nil, service.ErrHostedOwnedAccountNotFound
	}
	a, err := scanMarketAccount(ctx, r.db,
		"SELECT "+marketAccountColumns+" FROM accounts WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL",
		accountID, ownerUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrHostedOwnedAccountNotFound
	}
	return a, err
}

// ListPlanTierOptions: the live tiers of the plan (Available = also
// active), plus the given extra group when it is a live tier of another plan
// (shown as unavailable).
func (r *marketListingRepository) ListPlanTierOptions(ctx context.Context, plan, platform string, extraGroupID *int64) ([]service.MarketTierOption, error) {
	out := []service.MarketTierOption{}
	var extra any
	if extraGroupID != nil {
		extra = *extraGroupID
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, rate_multiplier::float8, market_plan,
		       COALESCE(status = 'active' AND $1 <> '' AND market_plan = $1, FALSE)
		FROM groups
		WHERE deleted_at IS NULL AND market_tier AND platform = $2
		  AND ((market_plan = $1 AND $1 <> '') OR id = $3)
		ORDER BY id`, plan, platform, extra)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			o         service.MarketTierOption
			groupPlan sql.NullString
		)
		if err := rows.Scan(&o.GroupID, &o.Name, &o.RateMultiplier, &groupPlan, &o.Available); err != nil {
			return nil, err
		}
		if groupPlan.Valid {
			v := groupPlan.String
			o.Plan = &v
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// NextOwnerSwitchAt reads the last effective owner switch (owner_list /
// owner_unlist / owner_switch; never pause, withdrawal, admin or system
// events) and returns its time + cooldown when that is still in the future
// of the database clock.
func (r *marketListingRepository) NextOwnerSwitchAt(ctx context.Context, accountID int64, cooldownHours int) (*time.Time, error) {
	next, _, err := marketOwnerSwitchCooldownTx(ctx, r.db, accountID, cooldownHours)
	return next, err
}

// marketOwnerSwitchCooldownTx returns (next switch time, database now) when
// the cooldown still runs, else (nil, now). Database time only.
func marketOwnerSwitchCooldownTx(ctx context.Context, q sqlQueryer, accountID int64, cooldownHours int) (*time.Time, time.Time, error) {
	var (
		next sql.NullTime
		now  time.Time
	)
	if cooldownHours <= 0 {
		err := scanSingleRow(ctx, q, "SELECT clock_timestamp()", nil, &now)
		return nil, now, err
	}
	err := scanSingleRow(ctx, q, `
		SELECT (SELECT e.created_at + make_interval(hours => $2)
		        FROM account_market_listing_events e
		        WHERE e.account_id = $1 AND e.action = ANY($3)
		        ORDER BY e.created_at DESC, e.id DESC LIMIT 1),
		       clock_timestamp()`,
		[]any{accountID, cooldownHours, pq.Array(service.MarketOwnerSwitchActions)}, &next, &now)
	if err != nil {
		return nil, now, err
	}
	// Equal to the boundary = allowed.
	if !next.Valid || !now.Before(next.Time) {
		return nil, now, nil
	}
	v := next.Time
	return &v, now, nil
}

func (r *marketListingRepository) ListAdminMemberGroupIDs(ctx context.Context, accountID int64, groupIDs []int64) ([]int64, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT group_id FROM account_groups
		WHERE account_id = $1 AND group_id = ANY($2) AND source = 'admin' ORDER BY group_id`, accountID, pq.Array(groupIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *marketListingRepository) HasAdminMembership(ctx context.Context, accountID, groupID int64) (bool, error) {
	var exists bool
	err := scanSingleRow(ctx, r.db, `SELECT EXISTS (
		SELECT 1 FROM account_groups WHERE account_id = $1 AND group_id = $2 AND source = 'admin')`,
		[]any{accountID, groupID}, &exists)
	return exists, err
}

// lockMarketAccountTx locks a live account row with the same lock mode as
// owner changes (FOR NO KEY UPDATE), so listing writes, owner changes and
// owner pause/withdraw serialize per account.
func lockMarketAccountTx(ctx context.Context, q sqlQueryer, accountID int64) (*service.Account, error) {
	a, err := scanMarketAccount(ctx, q,
		"SELECT "+marketAccountColumns+" FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE", accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAccountNotFound
	}
	return a, err
}

// insertMarketListingEventTx appends the current row as a history entry.
// The retired share-cap and reservation columns are NOT NULL without a
// default in the event table, so they are still copied from the listing row
// (kept for rollback to the previous binary; nothing reads them). created_at
// is clock_timestamp(): taken after the caller's locks, so the owner switch
// cooldown measures from the moment the switch was decided.
func insertMarketListingEventTx(ctx context.Context, exec sqlExecutor, accountID int64, action, actorRole string, actorUserID int64) error {
	var actor any
	if actorUserID > 0 {
		actor = actorUserID
	}
	_, err := exec.ExecContext(ctx, `
		INSERT INTO account_market_listing_events (
			account_id, version, action, actor_role, actor_user_id, owner_user_id, status,
			allowed_group_ids, selected_group_id, reserved_concurrency, reserved_devices,
			reserved_admissions, cap_5h_pct, cap_7d_pct, sample_max_age_minutes, plan, created_at)
		SELECT account_id, version, $2, $3, $4, owner_user_id, status,
			allowed_group_ids, selected_group_id, reserved_concurrency, reserved_devices,
			reserved_admissions, cap_5h_pct, cap_7d_pct, sample_max_age_minutes, plan, clock_timestamp()
		FROM account_market_listings WHERE account_id = $1`,
		accountID, action, actorRole, actor)
	return err
}

// deleteMarketMembershipsTx removes the account's market rows (all, or only
// the given group) and returns the affected group ids.
func deleteMarketMembershipsTx(ctx context.Context, q sqlQueryer, accountID int64, groupID *int64) ([]int64, error) {
	query := "DELETE FROM account_groups WHERE account_id = $1 AND source = 'market' RETURNING group_id"
	args := []any{accountID}
	if groupID != nil {
		query = "DELETE FROM account_groups WHERE account_id = $1 AND group_id = $2 AND source = 'market' RETURNING group_id"
		args = append(args, *groupID)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// enqueueMarketMembershipOutboxTx writes the scheduler outbox rows for a
// membership change inside the caller's transaction. Errors are returned
// (the whole change rolls back).
//
// Two events (account_groups_changed is never deduplicated): one rebuilds
// the changed market groups, one without a group list refreshes the account
// snapshot and rebuilds its current admin groups — or the ungrouped bucket
// when it has none, so a market-only account leaves (and later rejoins) the
// cached ungrouped pool.
func enqueueMarketMembershipOutboxTx(ctx context.Context, exec sqlExecutor, accountID int64, groupIDs []int64) error {
	if ids := mergeGroupIDs(groupIDs, nil); len(ids) > 0 {
		if err := enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, buildSchedulerGroupPayload(ids)); err != nil {
			return err
		}
	}
	return enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, nil)
}

// marketUnlistAccountTx is the lifecycle hook for owner cleared / withdraw
// requested / owner switch to self-use (revoke=false: listed → unlisted,
// plan kept) and admin revoke / account deletion (revoke=true: → revoked,
// plan and selection removed). Owner pause no longer unlists. It must
// run inside the caller's transaction with the account row locked (or the
// account being deleted in that transaction). Market rows are removed and
// the outbox written in the same transaction. No listing / nothing to change
// is a no-op.
func marketUnlistAccountTx(ctx context.Context, exec sqlExecutor, accountID int64, action string, revoke bool, actorRole string, actorUserID int64) error {
	var set, cond string
	if revoke {
		set = "status = 'revoked', plan = NULL, allowed_group_ids = '{}', selected_group_id = NULL"
		cond = "status <> 'revoked'"
	} else {
		set = "status = 'unlisted'"
		cond = "status = 'listed'"
	}
	res, err := exec.ExecContext(ctx, `
		UPDATE account_market_listings SET `+set+`, version = version + 1, updated_at = NOW()
		WHERE account_id = $1 AND `+cond, accountID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	removed, err := deleteMarketMembershipsTx(ctx, exec, accountID, nil)
	if err != nil {
		return err
	}
	if n > 0 {
		if err := insertMarketListingEventTx(ctx, exec, accountID, action, actorRole, actorUserID); err != nil {
			return err
		}
	}
	if n > 0 || len(removed) > 0 {
		return enqueueMarketMembershipOutboxTx(ctx, exec, accountID, removed)
	}
	return nil
}

// marketRemoveGroupTx runs when a group is deleted or stops being a market
// tier: the group leaves every listing's allowed set, a listing selling into
// it is unlisted, its market rows are removed. Caller holds the group row
// lock (FOR UPDATE) in the same transaction. Lock order everywhere: group
// row before listing rows (owner/admin listing writes lock the tier group
// FOR SHARE before touching the listing row).
func marketRemoveGroupTx(ctx context.Context, exec sqlExecutor, groupID int64, action string) error {
	// Lock the affected listings in account order first, so two group
	// removals sharing listings cannot deadlock on the UPDATE below.
	if _, err := exec.ExecContext(ctx, `
		SELECT account_id FROM account_market_listings
		WHERE $1 = ANY (allowed_group_ids) OR selected_group_id = $1
		ORDER BY account_id FOR UPDATE`, groupID); err != nil {
		return err
	}
	rows, err := exec.QueryContext(ctx, `
		UPDATE account_market_listings
		SET allowed_group_ids = array_remove(allowed_group_ids, $1::bigint),
		    selected_group_id = CASE WHEN selected_group_id = $1 THEN NULL ELSE selected_group_id END,
		    status = CASE WHEN status = 'listed' AND selected_group_id = $1 THEN 'unlisted' ELSE status END,
		    version = version + 1, updated_at = NOW()
		WHERE $1 = ANY (allowed_group_ids) OR selected_group_id = $1
		RETURNING account_id`, groupID)
	if err != nil {
		return err
	}
	var accountIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		accountIDs = append(accountIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	memberRows, err := exec.QueryContext(ctx,
		"DELETE FROM account_groups WHERE group_id = $1 AND source = 'market' RETURNING account_id", groupID)
	if err != nil {
		return err
	}
	var members []int64
	for memberRows.Next() {
		var id int64
		if err := memberRows.Scan(&id); err != nil {
			_ = memberRows.Close()
			return err
		}
		members = append(members, id)
	}
	if err := memberRows.Close(); err != nil {
		return err
	}
	if err := memberRows.Err(); err != nil {
		return err
	}
	for _, id := range accountIDs {
		if err := insertMarketListingEventTx(ctx, exec, id, action, service.MarketListingActorSystem, 0); err != nil {
			return err
		}
	}
	affected := mergeGroupIDs(accountIDs, members)
	for _, id := range affected {
		if err := enqueueMarketMembershipOutboxTx(ctx, exec, id, []int64{groupID}); err != nil {
			return err
		}
	}
	return nil
}

// marketReplanGroupTx runs when a tier's plan changes (the group stays a
// tier): every listing selected in the tier whose plan no longer matches is
// unlisted with the demotion semantics (selection cleared, market row
// removed, history kept, outbox written). Caller holds the group row lock
// (FOR UPDATE); lock order group → listings as in marketRemoveGroupTx.
func marketReplanGroupTx(ctx context.Context, exec sqlExecutor, groupID int64, plan *string) error {
	var planArg any
	if plan != nil {
		planArg = *plan
	}
	if _, err := exec.ExecContext(ctx, `
		SELECT account_id FROM account_market_listings
		WHERE selected_group_id = $1 AND plan IS DISTINCT FROM $2::text
		ORDER BY account_id FOR UPDATE`, groupID, planArg); err != nil {
		return err
	}
	rows, err := exec.QueryContext(ctx, `
		UPDATE account_market_listings
		SET selected_group_id = NULL, allowed_group_ids = '{}',
		    status = CASE WHEN status = 'listed' THEN 'unlisted' ELSE status END,
		    version = version + 1, updated_at = NOW()
		WHERE selected_group_id = $1 AND plan IS DISTINCT FROM $2::text
		RETURNING account_id`, groupID, planArg)
	if err != nil {
		return err
	}
	var accountIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		accountIDs = append(accountIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(accountIDs) == 0 {
		return nil
	}
	if _, err := exec.ExecContext(ctx,
		"DELETE FROM account_groups WHERE group_id = $1 AND source = 'market' AND account_id = ANY($2)",
		groupID, pq.Array(accountIDs)); err != nil {
		return err
	}
	for _, id := range accountIDs {
		if err := insertMarketListingEventTx(ctx, exec, id, service.MarketListingActionGroupReplanned, service.MarketListingActorSystem, 0); err != nil {
			return err
		}
		if err := enqueueMarketMembershipOutboxTx(ctx, exec, id, []int64{groupID}); err != nil {
			return err
		}
	}
	return nil
}

func equalNullableString(a sql.NullString, b *string) bool {
	if !a.Valid || b == nil {
		return !a.Valid && b == nil
	}
	return a.String == *b
}

// ensureMarketOwnerVersionTx makes sure account_owner_events has a row whose
// new owner is the account's current owner and returns its id (the owner
// version pinned by admissions). Accounts that got their owner outside the
// API (no history) get a baseline row. Caller holds the account row lock.
func ensureMarketOwnerVersionTx(ctx context.Context, q sqlExecutor, accountID, ownerUserID int64) (int64, error) {
	var (
		lastID    sql.NullInt64
		lastOwner sql.NullInt64
	)
	err := scanSingleRow(ctx, q, `
		SELECT id, new_owner_user_id FROM account_owner_events
		WHERE account_id = $1 ORDER BY id DESC LIMIT 1`, []any{accountID}, &lastID, &lastOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if lastID.Valid && lastOwner.Valid && lastOwner.Int64 == ownerUserID {
		return lastID.Int64, nil
	}
	var oldOwner any
	if lastOwner.Valid {
		oldOwner = lastOwner.Int64
	}
	var id int64
	if err := scanSingleRow(ctx, q, `
		INSERT INTO account_owner_events (account_id, old_owner_user_id, new_owner_user_id, admin_user_id)
		VALUES ($1, $2, $3, NULL) RETURNING id`, []any{accountID, oldOwner, ownerUserID}, &id); err != nil {
		return 0, err
	}
	return id, nil
}

// lockMarketPlanTiersTx share-locks the live, active market tiers of the
// platform with the plan (only groupID when > 0) and returns their ids
// (group row locks come before listing row locks, see marketRemoveGroupTx;
// a concurrent demotion, re-plan or deletion of these tiers waits).
func lockMarketPlanTiersTx(ctx context.Context, q sqlQueryer, plan, platform string, groupID int64) ([]int64, error) {
	if plan == "" {
		return nil, nil
	}
	query := `SELECT id FROM groups
		WHERE deleted_at IS NULL AND market_tier AND status = 'active' AND market_plan = $1 AND platform = $2`
	args := []any{plan, platform}
	if groupID > 0 {
		query += " AND id = $3"
		args = append(args, groupID)
	}
	query += " ORDER BY id FOR SHARE"
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *marketListingRepository) AdminConfigure(ctx context.Context, p service.MarketAdminConfigureParams) (*service.MarketListing, error) {
	if p.AccountID <= 0 {
		return nil, service.ErrAccountNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	account, err := lockMarketAccountTx(ctx, tx, p.AccountID)
	if err != nil {
		return nil, err
	}
	if p.Validate != nil {
		if err := p.Validate(account); err != nil {
			return nil, err
		}
	}
	if account.OwnerUserID == nil {
		return nil, service.ErrMarketAccountNoOwner
	}
	owner := *account.OwnerUserID
	// Group locks before the listing row (see marketRemoveGroupTx): the
	// plan's tiers cannot be demoted, re-planned or deleted meanwhile.
	tiers, err := lockMarketPlanTiersTx(ctx, tx, p.Plan, account.Platform, 0)
	if err != nil {
		return nil, err
	}
	if len(tiers) == 0 {
		return nil, service.ErrMarketPlanUnknown
	}
	current, err := queryMarketListing(ctx, tx,
		"SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1 FOR UPDATE", p.AccountID)
	if err != nil {
		return nil, err
	}

	var removed []int64
	if current == nil {
		if err := insertMarketListingRowTx(ctx, tx, p.AccountID, owner, p.Plan); err != nil {
			return nil, err
		}
	} else {
		status := current.Status
		if status == service.MarketListingStatusRevoked {
			// Explicit admin re-grant.
			status = service.MarketListingStatusUnlisted
		}
		selected := current.SelectedGroupID
		matches := selected == nil || slices.Contains(tiers, *selected)
		if !matches {
			// Not among the plan's active tiers: an inactive tier of the
			// plan still matches (deactivation never unlists). Read without
			// a lock: a concurrent re-plan or removal of that group waits for
			// this listing row and re-checks it after the commit.
			if err := scanSingleRow(ctx, tx, `SELECT EXISTS (SELECT 1 FROM groups WHERE id = $1 AND deleted_at IS NULL
				AND market_tier AND market_plan = $2 AND platform = $3)`,
				[]any{*selected, p.Plan, account.Platform}, &matches); err != nil {
				return nil, err
			}
		}
		if !matches {
			// The selected tier no longer matches the plan: unlist with the
			// semantics of a deleted tier (selection cleared, market row
			// removed).
			if status == service.MarketListingStatusListed {
				status = service.MarketListingStatusUnlisted
			}
			removed, err = deleteMarketMembershipsTx(ctx, tx, p.AccountID, selected)
			if err != nil {
				return nil, err
			}
			selected = nil
		}
		unchanged := status == current.Status && current.OwnerUserID == owner &&
			current.Plan != nil && *current.Plan == p.Plan && equalOptionalInt64(selected, current.SelectedGroupID)
		if unchanged {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return current, nil
		}
		var selectedArg any
		if selected != nil {
			selectedArg = *selected
		}
		// allowed_group_ids only mirrors the selection (listed CHECK);
		// reservations are written as 0.
		if _, err := tx.ExecContext(ctx, `
			UPDATE account_market_listings SET
				owner_user_id = $2, plan = $3, selected_group_id = $4, status = $5,
				allowed_group_ids = CASE WHEN $4::bigint IS NULL THEN '{}'::bigint[] ELSE ARRAY[$4::bigint] END,
				reserved_concurrency = 0, reserved_devices = 0, reserved_admissions = 0,
				version = version + 1, updated_at = NOW()
			WHERE account_id = $1`,
			p.AccountID, owner, p.Plan, selectedArg, status); err != nil {
			return nil, err
		}
	}
	if err := insertMarketListingEventTx(ctx, tx, p.AccountID, service.MarketListingActionAdminConfigure, service.MarketListingActorAdmin, p.AdminUserID); err != nil {
		return nil, err
	}
	if err := enqueueMarketMembershipOutboxTx(ctx, tx, p.AccountID, removed); err != nil {
		return nil, err
	}
	out, err := queryMarketListing(ctx, tx, "SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1", p.AccountID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.accounts.syncSchedulerAccountSnapshot(ctx, p.AccountID)
	return out, nil
}

// insertMarketListingRowTx creates the listing of an account in 自用 state
// (unlisted) with its plan and zero reservations.
func insertMarketListingRowTx(ctx context.Context, exec sqlExecutor, accountID, ownerUserID int64, plan string) error {
	_, err := exec.ExecContext(ctx, `
		INSERT INTO account_market_listings (
			account_id, owner_user_id, plan, allowed_group_ids, status, version,
			reserved_concurrency, reserved_devices, reserved_admissions)
		VALUES ($1, $2, $3, '{}', 'unlisted', 1, 0, 0, 0)`,
		accountID, ownerUserID, plan)
	return err
}

// CreateApprovedMarketListingTx is the listing creation of a self-onboard
// approval, inside the approval transaction (the account and its owner
// history exist; lock order group → listing). The plan must name at least
// one live, active anthropic tier.
func CreateApprovedMarketListingTx(ctx context.Context, q sqlQueryer, exec sqlExecutor, accountID, ownerUserID, adminUserID int64, plan string) error {
	tiers, err := lockMarketPlanTiersTx(ctx, q, plan, service.PlatformAnthropic, 0)
	if err != nil {
		return err
	}
	if len(tiers) == 0 {
		return service.ErrMarketPlanUnknown
	}
	if err := insertMarketListingRowTx(ctx, exec, accountID, ownerUserID, plan); err != nil {
		return err
	}
	return insertMarketListingEventTx(ctx, exec, accountID, service.MarketListingActionAdminConfigure, service.MarketListingActorAdmin, adminUserID)
}

func (r *marketListingRepository) AdminRevoke(ctx context.Context, accountID, adminUserID int64) (*service.MarketListing, error) {
	if accountID <= 0 {
		return nil, service.ErrAccountNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockMarketAccountTx(ctx, tx, accountID); err != nil {
		return nil, err
	}
	current, err := queryMarketListing(ctx, tx,
		"SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1 FOR UPDATE", accountID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, service.ErrMarketListingNotFound
	}
	if err := marketUnlistAccountTx(ctx, tx, accountID, service.MarketListingActionAdminRevoke, true, service.MarketListingActorAdmin, adminUserID); err != nil {
		return nil, err
	}
	out, err := queryMarketListing(ctx, tx, "SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1", accountID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.accounts.syncSchedulerAccountSnapshot(ctx, accountID)
	return out, nil
}

// OwnerSet is the owner's 共享/自用 switch, one transaction under the account
// row lock:
//  1. a request for the state the listing is already in (same tier when
//     listing) is an idempotent no-op (no event, no cooldown use);
//  2. the last effective owner switch (owner_list / owner_unlist /
//     owner_switch, database time) must be at least CooldownHours old, except
//     a take-back to self-use while market_enabled is off;
//  3. state, event (clock_timestamp()) and outbox are written.
func (r *marketListingRepository) OwnerSet(ctx context.Context, p service.MarketOwnerSetParams) (*service.MarketListing, error) {
	if p.OwnerUserID <= 0 || p.AccountID <= 0 {
		return nil, service.ErrHostedOwnedAccountNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Only the CURRENT owner of a live account.
	account, err := scanMarketAccount(ctx, tx,
		"SELECT "+marketAccountColumns+" FROM accounts WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL FOR NO KEY UPDATE",
		p.AccountID, p.OwnerUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrHostedOwnedAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	// Read without a row lock: the account lock serializes every listing
	// writer except group removal / re-plan, which lock the group first (see
	// marketRemoveGroupTx); the tier group is share-locked below before the
	// listing row is written.
	current, err := queryMarketListing(ctx, tx,
		"SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1", p.AccountID)
	if err != nil {
		return nil, err
	}

	if !p.Listed {
		if current == nil || current.Status != service.MarketListingStatusListed {
			// Already self-use: idempotent.
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return current, nil
		}
		enabled, err := r.marketEnabled(ctx, tx)
		if err != nil {
			return nil, err
		}
		if enabled {
			// With the market off a take-back is always allowed (the account
			// could neither sell nor be used otherwise).
			if err := checkOwnerSwitchCooldownTx(ctx, tx, p.AccountID, p.CooldownHours); err != nil {
				return nil, err
			}
		}
		if err := marketUnlistAccountTx(ctx, tx, p.AccountID, service.MarketListingActionOwnerUnlist, false, service.MarketListingActorOwner, p.OwnerUserID); err != nil {
			return nil, err
		}
		return r.commitListing(ctx, tx, p.AccountID)
	}

	var enabled string
	// Share lock, like P2c: a concurrent "switch off" save waits for this
	// listing instead of racing it.
	if err := scanSingleRow(ctx, tx, "SELECT value FROM settings WHERE key = $1 FOR SHARE",
		[]any{service.SettingKeyMarketEnabled}, &enabled); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if enabled != "true" {
		return nil, service.ErrMarketDisabled
	}
	if current == nil {
		return nil, service.NewMarketAccountNotEligibleError(service.MarketReasonNotConfigured)
	}
	if current.Status == service.MarketListingStatusRevoked {
		return nil, service.ErrMarketListingRevoked
	}
	if current.Plan == nil {
		return nil, service.NewMarketAccountNotEligibleError(service.MarketReasonNoPlan)
	}
	plan := *current.Plan
	locked, err := lockMarketPlanTiersTx(ctx, tx, plan, account.Platform, p.GroupID)
	if err != nil {
		return nil, err
	}
	groupID := p.GroupID
	switch {
	case len(locked) == 0:
		return nil, service.ErrMarketGroupNotAllowed
	case groupID <= 0 && len(locked) > 1:
		return nil, service.ErrMarketTierRequired
	case groupID <= 0:
		groupID = locked[0]
	}
	// Re-read after the group lock: a concurrent re-plan or removal of the
	// selected tier may have changed the row.
	current, err = queryMarketListing(ctx, tx,
		"SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1 FOR UPDATE", p.AccountID)
	if err != nil {
		return nil, err
	}
	if current == nil || current.Status == service.MarketListingStatusRevoked || current.Plan == nil || *current.Plan != plan {
		return nil, service.ErrMarketGroupNotAllowed
	}
	if current.Status == service.MarketListingStatusListed && equalOptionalInt64(current.SelectedGroupID, &groupID) {
		// Already shared in this tier: idempotent (also while paused).
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return current, nil
	}
	if p.Validate != nil {
		if err := p.Validate(account); err != nil {
			return nil, err
		}
	}
	if err := checkOwnerSwitchCooldownTx(ctx, tx, p.AccountID, p.CooldownHours); err != nil {
		return nil, err
	}

	var adminRow bool
	if err := scanSingleRow(ctx, tx, `SELECT EXISTS (
		SELECT 1 FROM account_groups WHERE account_id = $1 AND group_id = $2 AND source = 'admin')`,
		[]any{p.AccountID, groupID}, &adminRow); err != nil {
		return nil, err
	}
	if adminRow {
		return nil, service.ErrMarketAdminMembershipConflict
	}
	p.GroupID = groupID
	if p.BeforeListWrite != nil {
		p.BeforeListWrite()
		reachedCommit := false
		defer func() {
			if !reachedCommit && p.ListingRolledBack != nil {
				p.ListingRolledBack()
			}
		}()
		listCommit := func() (*service.MarketListing, error) {
			// Any failure before the commit is an explicit rollback; from
			// the commit on, the outcome may be unknown: keep the mark.
			return r.commitListingMarked(ctx, tx, p.AccountID, func() { reachedCommit = true })
		}
		return r.ownerListWrite(ctx, tx, p, current, listCommit)
	}
	return r.ownerListWrite(ctx, tx, p, current, func() (*service.MarketListing, error) {
		return r.commitListing(ctx, tx, p.AccountID)
	})
}

// checkOwnerSwitchCooldownTx refuses an owner switch inside the cooldown
// (429 MARKET_SWITCH_COOLDOWN with next_switch_at in database time).
func checkOwnerSwitchCooldownTx(ctx context.Context, q sqlQueryer, accountID int64, cooldownHours int) error {
	next, now, err := marketOwnerSwitchCooldownTx(ctx, q, accountID, cooldownHours)
	if err != nil {
		return err
	}
	if next == nil {
		return nil
	}
	wait := next.Sub(now)
	return service.NewMarketSwitchCooldownError(*next, int64((wait+time.Second-1)/time.Second))
}

// ownerListWrite performs the listing writes of OwnerSet (all checks done).
func (r *marketListingRepository) ownerListWrite(ctx context.Context, tx *sql.Tx, p service.MarketOwnerSetParams, current *service.MarketListing,
	commit func() (*service.MarketListing, error)) (*service.MarketListing, error) {
	if _, err := ensureMarketOwnerVersionTx(ctx, tx, p.AccountID, p.OwnerUserID); err != nil {
		return nil, err
	}
	changed := []int64{p.GroupID}
	action := service.MarketListingActionOwnerList
	if current.Status == service.MarketListingStatusListed && current.SelectedGroupID != nil {
		removed, err := deleteMarketMembershipsTx(ctx, tx, p.AccountID, current.SelectedGroupID)
		if err != nil {
			return nil, err
		}
		changed = append(changed, removed...)
		action = service.MarketListingActionOwnerSwitch
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO account_groups (account_id, group_id, priority, created_at, source)
		VALUES ($1, $2, 50, NOW(), 'market')
		ON CONFLICT (account_id, group_id) DO NOTHING`, p.AccountID, p.GroupID)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n != 1 {
		// A row committed concurrently: an admin binding wins.
		var source string
		if err := scanSingleRow(ctx, tx, "SELECT source FROM account_groups WHERE account_id = $1 AND group_id = $2",
			[]any{p.AccountID, p.GroupID}, &source); err != nil {
			return nil, err
		}
		if source != service.AccountGroupSourceMarket {
			return nil, service.ErrMarketAdminMembershipConflict
		}
	}
	// allowed_group_ids mirrors the selection (listed CHECK); reservations 0.
	if _, err := tx.ExecContext(ctx, `
		UPDATE account_market_listings SET
			status = 'listed', selected_group_id = $2, allowed_group_ids = ARRAY[$2::bigint],
			owner_user_id = $3, listed_at = NOW(),
			reserved_concurrency = 0, reserved_devices = 0, reserved_admissions = 0,
			version = version + 1, updated_at = NOW()
		WHERE account_id = $1`, p.AccountID, p.GroupID, p.OwnerUserID); err != nil {
		return nil, err
	}
	if err := insertMarketListingEventTx(ctx, tx, p.AccountID, action, service.MarketListingActorOwner, p.OwnerUserID); err != nil {
		return nil, err
	}
	if err := enqueueMarketMembershipOutboxTx(ctx, tx, p.AccountID, changed); err != nil {
		return nil, err
	}
	return commit()
}

func (r *marketListingRepository) commitListing(ctx context.Context, tx *sql.Tx, accountID int64) (*service.MarketListing, error) {
	return r.commitListingMarked(ctx, tx, accountID, nil)
}

// commitListingMarked reads the final listing and commits; beforeCommit
// (optional) runs after that read, right before Commit.
func (r *marketListingRepository) commitListingMarked(ctx context.Context, tx *sql.Tx, accountID int64, beforeCommit func()) (*service.MarketListing, error) {
	out, err := queryMarketListing(ctx, tx, "SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1", accountID)
	if err != nil {
		return nil, err
	}
	if beforeCommit != nil {
		beforeCommit()
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.accounts.syncSchedulerAccountSnapshot(ctx, accountID)
	return out, nil
}

func equalOptionalInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// --- market admissions (contract for the selection part) ---

const marketAdmissionColumns = `id, attempt_id, request_ref, account_id, group_id, owner_user_id, owner_event_id,
	listing_version, buyer_user_id, api_key_id, admitted_at, created_at`

// insertMarketAdmissionTx inserts one admission (caller's transaction or the
// pool). The database sets id, admitted_at (clock_timestamp()) and created_at.
func insertMarketAdmissionTx(ctx context.Context, q sqlQueryer, in *service.MarketAdmission) (*service.MarketAdmission, error) {
	if in == nil {
		return nil, errors.New("nil market admission")
	}
	out := *in
	err := scanSingleRow(ctx, q, `
		INSERT INTO market_admissions (attempt_id, request_ref, account_id, group_id, owner_user_id, owner_event_id,
			listing_version, buyer_user_id, api_key_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, admitted_at, created_at`,
		[]any{in.AttemptID, in.RequestRef, in.AccountID, in.GroupID, in.OwnerUserID, in.OwnerEventID,
			in.ListingVersion, in.BuyerUserID, in.APIKeyID},
		&out.ID, &out.AdmittedAt, &out.CreatedAt)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == "market_admissions_attempt_key" {
			return nil, service.ErrMarketAdmissionAttemptExists
		}
		return nil, err
	}
	return &out, nil
}

// AdmitMarket runs at REPEATABLE READ so every eligibility fact comes from
// one snapshot. A listing row updated concurrently after that snapshot makes
// the share lock fail with 40001: that attempt inserts nothing and the whole
// transaction is rerun once, judged again on its new snapshot.
//
// Promise: eligibility is decided on the consistent snapshot of the
// admission transaction that finally commits. A disable (owner, account,
// listing outside the listing row) committed after that snapshot counts as
// happening after the admission: the request continues as in flight and
// settles normally. admitted_at is the database time of the insert, so it
// can be later than such a disable — audits must not flag an admission only
// because its timestamp follows a disable. The window has no hard bound:
// every concurrent request that obtained an eligible snapshot and commits
// may continue.
//
// Each attempt works on its own copy of in, so fields the first check
// filled in never leak into the retry.
func (r *marketListingRepository) AdmitMarket(ctx context.Context, in *service.MarketAdmission,
	check func(state *service.MarketAdmissionState, in *service.MarketAdmission) error) (*service.MarketAdmission, error) {
	if in == nil || check == nil {
		return nil, errors.New("market admission: missing input or check")
	}
	attempt := *in
	out, err := r.admitMarketOnce(ctx, &attempt, check)
	if isSerializationFailure(err) {
		attempt = *in
		out, err = r.admitMarketOnce(ctx, &attempt, check)
	}
	return out, err
}

func isSerializationFailure(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "40001"
}

func (r *marketListingRepository) admitMarketOnce(ctx context.Context, in *service.MarketAdmission,
	check func(state *service.MarketAdmissionState, in *service.MarketAdmission) error) (*service.MarketAdmission, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	state := &service.MarketAdmissionState{}
	// Share lock: conflicts with every listing write (they all UPDATE this
	// row), not with other admissions. Lock order: listing row only.
	if state.Listing, err = queryMarketListing(ctx, tx,
		"SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1 FOR SHARE", in.AccountID); err != nil {
		return nil, err
	}
	account, err := scanMarketAccount(ctx, tx,
		"SELECT "+marketAccountColumns+" FROM accounts WHERE id = $1 AND deleted_at IS NULL", in.AccountID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		state.Account = account
	}
	if r.afterAdmissionAccountRead != nil {
		r.afterAdmissionAccountRead()
	}
	var lastID, lastOwner sql.NullInt64
	if err := scanSingleRow(ctx, tx, `SELECT id, new_owner_user_id FROM account_owner_events
		WHERE account_id = $1 ORDER BY id DESC LIMIT 1`, []any{in.AccountID}, &lastID, &lastOwner); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	state.OwnerEventID = lastID.Int64
	if lastOwner.Valid {
		v := lastOwner.Int64
		state.OwnerEventUserID = &v
	}
	if state.Account != nil && state.Account.OwnerUserID != nil {
		if err := scanSingleRow(ctx, tx, "SELECT status FROM users WHERE id = $1 AND deleted_at IS NULL",
			[]any{*state.Account.OwnerUserID}, &state.OwnerUserStatus); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	var (
		modelRates []byte
		groupPlan  sql.NullString
	)
	if err := scanSingleRow(ctx, tx, `SELECT market_tier, status, platform, rate_multiplier, model_rate_multipliers, market_plan
		FROM groups WHERE id = $1 AND deleted_at IS NULL`, []any{in.GroupID},
		&state.GroupMarketTier, &state.GroupStatus, &state.GroupPlatform, &state.GroupRateMultiplier, &modelRates, &groupPlan); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	} else {
		state.GroupFound = true
		if groupPlan.Valid {
			v := groupPlan.String
			state.GroupMarketPlan = &v
		}
		// Decoded like the ent field (map[string]float64); a value ent
		// could not load either fails the admission (no dispatch).
		if len(modelRates) > 0 {
			if err := json.Unmarshal(modelRates, &state.GroupModelRateMultipliers); err != nil {
				return nil, fmt.Errorf("market admission: decode model_rate_multipliers of group %d: %w", in.GroupID, err)
			}
		}
	}
	// The buyer's own rate override on the group (a 0 override on a tier is
	// allowed and makes the sale unpriced).
	var override sql.NullFloat64
	if err := scanSingleRow(ctx, tx, `SELECT rate_multiplier FROM user_group_rate_multipliers
		WHERE user_id = $1 AND group_id = $2`, []any{in.BuyerUserID, in.GroupID}, &override); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if override.Valid {
		v := override.Float64
		state.BuyerRateOverride = &v
	}
	if err := scanSingleRow(ctx, tx, `SELECT
			COALESCE(bool_or(source = 'admin'), FALSE), COALESCE(bool_or(source = 'market'), FALSE), clock_timestamp()
		FROM account_groups WHERE account_id = $1 AND group_id = $2`,
		[]any{in.AccountID, in.GroupID}, &state.AdminMember, &state.MarketMember, &state.DBNow); err != nil {
		return nil, err
	}
	var enabled sql.NullString
	if err := scanSingleRow(ctx, tx, "SELECT value FROM settings WHERE key = $1",
		[]any{service.SettingKeyMarketEnabled}, &enabled); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	state.MarketEnabled = enabled.Valid && enabled.String == "true"
	if err := check(state, in); err != nil {
		return nil, err
	}
	out, err := insertMarketAdmissionTx(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *marketListingRepository) GetMarketAdmission(ctx context.Context, id int64) (*service.MarketAdmission, error) {
	return r.getMarketAdmission(ctx, "id = $1", id)
}

func (r *marketListingRepository) GetMarketAdmissionByAttempt(ctx context.Context, attemptID string) (*service.MarketAdmission, error) {
	return r.getMarketAdmission(ctx, "attempt_id = $1", attemptID)
}

func (r *marketListingRepository) getMarketAdmission(ctx context.Context, where string, arg any) (*service.MarketAdmission, error) {
	var a service.MarketAdmission
	var admittedAt, createdAt time.Time
	err := scanSingleRow(ctx, r.db, "SELECT "+marketAdmissionColumns+" FROM market_admissions WHERE "+where, []any{arg},
		&a.ID, &a.AttemptID, &a.RequestRef, &a.AccountID, &a.GroupID, &a.OwnerUserID, &a.OwnerEventID,
		&a.ListingVersion, &a.BuyerUserID, &a.APIKeyID, &admittedAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrMarketAdmissionNotFound
	}
	if err != nil {
		return nil, err
	}
	a.AdmittedAt = admittedAt
	a.CreatedAt = createdAt
	return &a, nil
}
