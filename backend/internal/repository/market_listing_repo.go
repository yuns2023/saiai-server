package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

const marketListingColumns = `account_id, owner_user_id, allowed_group_ids, selected_group_id, status, version,
	reserved_concurrency, reserved_devices, reserved_admissions, cap_5h_pct, cap_7d_pct,
	sample_max_age_minutes, listed_at, created_at, updated_at`

func scanMarketListing(rows *sql.Rows) (*service.MarketListing, error) {
	var (
		l        service.MarketListing
		allowed  pq.Int64Array
		selected sql.NullInt64
		listedAt sql.NullTime
	)
	if err := rows.Scan(&l.AccountID, &l.OwnerUserID, &allowed, &selected, &l.Status, &l.Version,
		&l.Protection.ReservedConcurrency, &l.Protection.ReservedDevices, &l.Protection.ReservedAdmissions,
		&l.Protection.Cap5hPct, &l.Protection.Cap7dPct, &l.Protection.SampleMaxAgeMinutes,
		&listedAt, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	l.AllowedGroupIDs = []int64(allowed)
	if l.AllowedGroupIDs == nil {
		l.AllowedGroupIDs = []int64{}
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
	(SELECT p.host FROM proxies p WHERE p.id = accounts.proxy_id AND p.deleted_at IS NULL)`

func scanMarketAccount(ctx context.Context, q sqlQueryer, query string, args ...any) (*service.Account, error) {
	var (
		a         service.Account
		extra     []byte
		owner     sql.NullInt64
		paused    sql.NullTime
		withdrawn sql.NullTime
		proxyID   sql.NullInt64
		proxyStat sql.NullString
		proxyHost sql.NullString
	)
	if err := scanSingleRow(ctx, q, query, args, &a.ID, &a.Platform, &a.Type, &extra, &a.Concurrency,
		&a.Status, &a.Schedulable, &owner, &paused, &withdrawn, &a.SelfOnboarded, &proxyID, &proxyStat, &proxyHost); err != nil {
		return nil, err
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

func (r *marketListingRepository) ListTierOptions(ctx context.Context, groupIDs []int64) ([]service.MarketTierOption, error) {
	out := []service.MarketTierOption{}
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, rate_multiplier::float8,
		       (deleted_at IS NULL AND market_tier AND status = 'active')
		FROM groups WHERE id = ANY($1) ORDER BY id`, pq.Array(groupIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var o service.MarketTierOption
		if err := rows.Scan(&o.GroupID, &o.Name, &o.RateMultiplier, &o.Available); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
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
func insertMarketListingEventTx(ctx context.Context, exec sqlExecutor, accountID int64, action, actorRole string, actorUserID int64) error {
	var actor any
	if actorUserID > 0 {
		actor = actorUserID
	}
	_, err := exec.ExecContext(ctx, `
		INSERT INTO account_market_listing_events (
			account_id, version, action, actor_role, actor_user_id, owner_user_id, status,
			allowed_group_ids, selected_group_id, reserved_concurrency, reserved_devices,
			reserved_admissions, cap_5h_pct, cap_7d_pct, sample_max_age_minutes)
		SELECT account_id, version, $2, $3, $4, owner_user_id, status,
			allowed_group_ids, selected_group_id, reserved_concurrency, reserved_devices,
			reserved_admissions, cap_5h_pct, cap_7d_pct, sample_max_age_minutes
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

// marketUnlistAccountTx is the lifecycle hook for owner cleared / paused /
// withdraw requested (revoke=false: listed → unlisted, allowed tiers kept)
// and account deletion (revoke=true: → revoked, permission removed). It must
// run inside the caller's transaction with the account row locked (or the
// account being deleted in that transaction). Market rows are removed and
// the outbox written in the same transaction. No listing / nothing to change
// is a no-op.
func marketUnlistAccountTx(ctx context.Context, exec sqlExecutor, accountID int64, action string, revoke bool, actorRole string, actorUserID int64) error {
	var set, cond string
	if revoke {
		set = "status = 'revoked', allowed_group_ids = '{}', selected_group_id = NULL"
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

// lockMarketTierGroupsTx share-locks the given live tier groups of the
// platform and returns the ids found (group row locks come before listing
// row locks, see marketRemoveGroupTx).
func lockMarketTierGroupsTx(ctx context.Context, q sqlQueryer, groupIDs []int64, platform string, requireActive bool) ([]int64, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	query := `SELECT id FROM groups
		WHERE id = ANY($1) AND deleted_at IS NULL AND market_tier AND platform = $2`
	if requireActive {
		query += " AND status = 'active'"
	}
	query += " ORDER BY id FOR SHARE"
	rows, err := q.QueryContext(ctx, query, pq.Array(groupIDs), platform)
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
	found, err := lockMarketTierGroupsTx(ctx, tx, p.AllowedGroupIDs, account.Platform, false)
	if err != nil {
		return nil, err
	}
	if len(found) != len(p.AllowedGroupIDs) {
		return nil, service.ErrMarketInvalidTier
	}
	current, err := queryMarketListing(ctx, tx,
		"SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1 FOR UPDATE", p.AccountID)
	if err != nil {
		return nil, err
	}
	base := p.Defaults
	if current != nil {
		base = current.Protection
	}
	params := base
	if p.Merge != nil {
		params = p.Merge(base)
	}
	if p.Validate != nil {
		if err := p.Validate(account, params); err != nil {
			return nil, err
		}
	}
	if account.OwnerUserID == nil {
		return nil, service.ErrMarketAccountNoOwner
	}
	owner := *account.OwnerUserID

	var removed []int64
	if current == nil {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO account_market_listings (
				account_id, owner_user_id, allowed_group_ids, status, version,
				reserved_concurrency, reserved_devices, reserved_admissions,
				cap_5h_pct, cap_7d_pct, sample_max_age_minutes)
			VALUES ($1, $2, $3, 'unlisted', 1, $4, $5, $6, $7, $8, $9)`,
			p.AccountID, owner, pq.Array(p.AllowedGroupIDs),
			params.ReservedConcurrency, params.ReservedDevices, params.ReservedAdmissions,
			params.Cap5hPct, params.Cap7dPct, params.SampleMaxAgeMinutes); err != nil {
			return nil, err
		}
	} else {
		status := current.Status
		if status == service.MarketListingStatusRevoked {
			status = service.MarketListingStatusUnlisted
		}
		selected := current.SelectedGroupID
		if selected != nil && !slices.Contains(p.AllowedGroupIDs, *selected) {
			// Permission for the selected tier was revoked: unlist now.
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
			slices.Equal(current.AllowedGroupIDs, p.AllowedGroupIDs) && params == current.Protection &&
			equalOptionalInt64(selected, current.SelectedGroupID)
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
		if _, err := tx.ExecContext(ctx, `
			UPDATE account_market_listings SET
				owner_user_id = $2, allowed_group_ids = $3, selected_group_id = $4, status = $5,
				reserved_concurrency = $6, reserved_devices = $7, reserved_admissions = $8,
				cap_5h_pct = $9, cap_7d_pct = $10, sample_max_age_minutes = $11,
				version = version + 1, updated_at = NOW()
			WHERE account_id = $1`,
			p.AccountID, owner, pq.Array(p.AllowedGroupIDs), selectedArg, status,
			params.ReservedConcurrency, params.ReservedDevices, params.ReservedAdmissions,
			params.Cap5hPct, params.Cap7dPct, params.SampleMaxAgeMinutes); err != nil {
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
	// writer except group removal, which locks the group first (see
	// marketRemoveGroupTx); the tier group is share-locked below before the
	// listing row is written.
	current, err := queryMarketListing(ctx, tx,
		"SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1", p.AccountID)
	if err != nil {
		return nil, err
	}

	if !p.Listed {
		if current == nil || current.Status != service.MarketListingStatusListed {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return current, nil
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
	if p.Validate != nil {
		if err := p.Validate(account, current.Protection); err != nil {
			return nil, err
		}
	}
	if !slices.Contains(current.AllowedGroupIDs, p.GroupID) {
		return nil, service.ErrMarketGroupNotAllowed
	}
	locked, err := lockMarketTierGroupsTx(ctx, tx, []int64{p.GroupID}, account.Platform, true)
	if err != nil {
		return nil, err
	}
	if len(locked) != 1 {
		return nil, service.ErrMarketGroupNotAllowed
	}
	// Re-read after the group lock: a concurrent group removal may have
	// changed the allowed set.
	current, err = queryMarketListing(ctx, tx,
		"SELECT "+marketListingColumns+" FROM account_market_listings WHERE account_id = $1 FOR UPDATE", p.AccountID)
	if err != nil {
		return nil, err
	}
	if current == nil || !slices.Contains(current.AllowedGroupIDs, p.GroupID) {
		return nil, service.ErrMarketGroupNotAllowed
	}
	if current.Status == service.MarketListingStatusListed && equalOptionalInt64(current.SelectedGroupID, &p.GroupID) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return current, nil
	}

	var adminRow bool
	if err := scanSingleRow(ctx, tx, `SELECT EXISTS (
		SELECT 1 FROM account_groups WHERE account_id = $1 AND group_id = $2 AND source = 'admin')`,
		[]any{p.AccountID, p.GroupID}, &adminRow); err != nil {
		return nil, err
	}
	if adminRow {
		return nil, service.ErrMarketAdminMembershipConflict
	}
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
	if _, err := tx.ExecContext(ctx, `
		UPDATE account_market_listings SET
			status = 'listed', selected_group_id = $2, owner_user_id = $3, listed_at = NOW(),
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
	return r.commitListing(ctx, tx, p.AccountID)
}

func (r *marketListingRepository) commitListing(ctx context.Context, tx *sql.Tx, accountID int64) (*service.MarketListing, error) {
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
	if err := scanSingleRow(ctx, tx, "SELECT market_tier, status, platform FROM groups WHERE id = $1 AND deleted_at IS NULL",
		[]any{in.GroupID}, &state.GroupMarketTier, &state.GroupStatus, &state.GroupPlatform); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	} else {
		state.GroupFound = true
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
