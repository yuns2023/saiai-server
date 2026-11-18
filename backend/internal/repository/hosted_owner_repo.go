package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type hostedOwnerRepository struct {
	db       *sql.DB
	accounts *accountRepository
}

// NewHostedOwnerRepository serves account ownership writes and owner-scoped
// usage queries. Owner attribution always comes from the database
// (accounts.owner_user_id / usage_logs.account_owner_user_id), never from
// cached account objects.
func NewHostedOwnerRepository(client *dbent.Client, sqlDB *sql.DB, schedulerCache service.SchedulerCache) service.HostedOwnerRepository {
	return &hostedOwnerRepository{
		db:       sqlDB,
		accounts: newAccountRepositoryWithSQL(client, sqlDB, schedulerCache),
	}
}

func (r *hostedOwnerRepository) SetAccountOwner(ctx context.Context, accountID int64, ownerUserID *int64, adminUserID int64) (bool, error) {
	if accountID <= 0 {
		return false, service.ErrAccountNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	changed, err := setAccountOwnerTx(ctx, tx, accountID, ownerUserID, adminUserID)
	if err != nil || !changed {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	// Best-effort refresh of the cached copy (informational only; nothing
	// attributes usage or authorizes from the cached owner).
	r.accounts.syncSchedulerAccountSnapshot(ctx, accountID)
	return true, nil
}

// setAccountOwnerTx applies the ownership state machine inside the caller's
// transaction (exec must be a transaction): lock the account row, no-op on
// the same value, reject a transfer to anyone but a previous owner, write the
// column and the history row. The caller commits and syncs caches.
func setAccountOwnerTx(ctx context.Context, exec sqlExecutor, accountID int64, ownerUserID *int64, adminUserID int64) (bool, error) {
	// FOR NO KEY UPDATE serializes owner changes per account without
	// conflicting with the KEY SHARE locks taken by foreign-key checks of
	// concurrent inserts that reference this account.
	var current sql.NullInt64
	if err := scanSingleRow(ctx, exec,
		"SELECT owner_user_id FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE",
		[]any{accountID}, &current,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, service.ErrAccountNotFound
		}
		return false, err
	}

	if sameOwner(current, ownerUserID) {
		return false, nil
	}

	if ownerUserID != nil {
		target := *ownerUserID
		if current.Valid && current.Int64 != target {
			return false, service.ErrAccountOwnerTransferForbidden
		}
		var foreign bool
		if err := scanSingleRow(ctx, exec, `
			SELECT EXISTS (
				SELECT 1 FROM account_owner_events
				WHERE account_id = $1
				  AND ((old_owner_user_id IS NOT NULL AND old_owner_user_id <> $2)
				    OR (new_owner_user_id IS NOT NULL AND new_owner_user_id <> $2))
			)`, []any{accountID, target}, &foreign); err != nil {
			return false, err
		}
		if foreign {
			return false, service.ErrAccountOwnerTransferForbidden
		}
	}

	var newValue any
	if ownerUserID != nil {
		newValue = *ownerUserID
	}
	if _, err := exec.ExecContext(ctx,
		"UPDATE accounts SET owner_user_id = $2, updated_at = NOW() WHERE id = $1",
		accountID, newValue,
	); err != nil {
		return false, err
	}
	var oldValue any
	if current.Valid {
		oldValue = current.Int64
	}
	var adminValue any
	if adminUserID > 0 {
		adminValue = adminUserID
	}
	if _, err := exec.ExecContext(ctx,
		"INSERT INTO account_owner_events (account_id, old_owner_user_id, new_owner_user_id, admin_user_id) VALUES ($1, $2, $3, $4)",
		accountID, oldValue, newValue, adminValue,
	); err != nil {
		return false, err
	}
	return true, nil
}

func sameOwner(current sql.NullInt64, next *int64) bool {
	if !current.Valid {
		return next == nil
	}
	return next != nil && *next == current.Int64
}

func (r *hostedOwnerRepository) ListCurrentOwnedAccountIDs(ctx context.Context, ownerUserID int64, limit int) ([]int64, error) {
	if ownerUserID <= 0 || limit <= 0 {
		return []int64{}, nil
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT id FROM accounts WHERE owner_user_id = $1 AND deleted_at IS NULL ORDER BY id LIMIT $2",
		ownerUserID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *hostedOwnerRepository) ListEverOwnedAccounts(ctx context.Context, ownerUserID int64, limit int) ([]service.HostedOwnerHistoricalAccount, error) {
	if ownerUserID <= 0 || limit <= 0 {
		return []service.HostedOwnerHistoricalAccount{}, nil
	}
	// Ownership history plus the current column (covers rows assigned
	// outside the API). Deleted accounts stay listed; platform may be NULL.
	rows, err := r.db.QueryContext(ctx, `
		SELECT owned.account_id, a.platform
		FROM (
			SELECT account_id FROM account_owner_events WHERE new_owner_user_id = $1
			UNION
			SELECT id FROM accounts WHERE owner_user_id = $1
		) owned
		LEFT JOIN accounts a ON a.id = owned.account_id
		ORDER BY owned.account_id
		LIMIT $2`, ownerUserID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.HostedOwnerHistoricalAccount, 0)
	for rows.Next() {
		var item service.HostedOwnerHistoricalAccount
		var platform sql.NullString
		if err := rows.Scan(&item.AccountID, &platform); err != nil {
			return nil, err
		}
		if platform.Valid {
			value := platform.String
			item.Platform = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *hostedOwnerRepository) CanViewOwnedAccount(ctx context.Context, ownerUserID, accountID int64) (bool, error) {
	if ownerUserID <= 0 || accountID <= 0 {
		return false, nil
	}
	var allowed bool
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $2 AND owner_user_id = $1)
		    OR EXISTS (SELECT 1 FROM account_owner_events WHERE account_id = $2 AND new_owner_user_id = $1)`,
		ownerUserID, accountID).Scan(&allowed)
	return allowed, err
}

func (r *hostedOwnerRepository) SummarizeOwnedUsage(ctx context.Context, ownerUserID int64, accountIDs []int64, since7d, since24h, until time.Time) (map[int64]service.HostedOwnerUsageTotals, map[int64]service.HostedOwnerUsageTotals, error) {
	last24h := make(map[int64]service.HostedOwnerUsageTotals)
	last7d := make(map[int64]service.HostedOwnerUsageTotals)
	if ownerUserID <= 0 || len(accountIDs) == 0 {
		return last24h, last7d, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT account_id,
		       user_id = $1 AS is_self,
		       COUNT(*) AS requests_7d,
		       COALESCE(SUM(total_cost), 0) AS cost_7d,
		       COUNT(*) FILTER (WHERE created_at >= $4) AS requests_24h,
		       COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $4), 0) AS cost_24h,
		       COALESCE(SUM(actual_cost) FILTER (WHERE metered_cost IS NOT NULL), 0) AS fee_7d,
		       COALESCE(SUM(actual_cost) FILTER (WHERE metered_cost IS NOT NULL AND created_at >= $4), 0) AS fee_24h
		FROM usage_logs
		WHERE account_owner_user_id = $1
		  AND account_id = ANY($2)
		  AND created_at >= $3
		  AND created_at < $5
		GROUP BY account_id, is_self`,
		ownerUserID, pq.Array(accountIDs), since7d, since24h, until)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			accountID          int64
			isSelf             bool
			requests7d, req24h int64
			cost7d, cost24h    float64
			fee7d, fee24h      float64
		)
		if err := rows.Scan(&accountID, &isSelf, &requests7d, &cost7d, &req24h, &cost24h, &fee7d, &fee24h); err != nil {
			return nil, nil, err
		}
		week, day := last7d[accountID], last24h[accountID]
		if isSelf {
			week.SelfRequests, week.SelfStandardCost, week.SelfServiceFee = requests7d, cost7d, fee7d
			day.SelfRequests, day.SelfStandardCost, day.SelfServiceFee = req24h, cost24h, fee24h
		} else {
			week.OthersRequests, week.OthersStandardCost = requests7d, cost7d
			day.OthersRequests, day.OthersStandardCost = req24h, cost24h
		}
		last7d[accountID], last24h[accountID] = week, day
	}
	return last24h, last7d, rows.Err()
}

func (r *hostedOwnerRepository) ListOwnedUsage(ctx context.Context, q service.HostedOwnerUsageQuery) ([]service.HostedOwnerUsageRecord, error) {
	if q.OwnerUserID <= 0 || q.AccountID <= 0 || q.Limit <= 0 {
		return []service.HostedOwnerUsageRecord{}, nil
	}
	args := []any{q.OwnerUserID, q.AccountID, q.Start, q.End}
	keyset := ""
	if q.After != nil {
		args = append(args, q.After.CreatedAt, q.After.ID)
		keyset = fmt.Sprintf(" AND (ul.created_at, ul.id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, q.Limit)
	query := fmt.Sprintf(`
		SELECT ul.id,
		       ul.created_at,
		       ul.model,
		       ul.input_tokens,
		       ul.output_tokens,
		       ul.cache_creation_tokens,
		       ul.cache_read_tokens,
		       ul.total_cost,
		       ul.user_id = $1 AS is_self,
		       CASE WHEN ul.user_id = $1 THEN ul.actual_cost END AS charged,
		       CASE WHEN ul.user_id = $1 THEN k.name END AS key_name
		FROM usage_logs ul
		LEFT JOIN api_keys k ON k.id = ul.api_key_id AND k.user_id = $1
		WHERE ul.account_owner_user_id = $1
		  AND ul.account_id = $2
		  AND ul.created_at >= $3
		  AND ul.created_at < $4%s
		ORDER BY ul.created_at DESC, ul.id DESC
		LIMIT $%d`, keyset, len(args))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.HostedOwnerUsageRecord, 0, q.Limit)
	for rows.Next() {
		var (
			record  service.HostedOwnerUsageRecord
			charged sql.NullFloat64
			keyName sql.NullString
		)
		if err := rows.Scan(
			&record.ID,
			&record.Row.CreatedAt,
			&record.Row.Model,
			&record.Row.InputTokens,
			&record.Row.OutputTokens,
			&record.Row.CacheCreationTokens,
			&record.Row.CacheReadTokens,
			&record.Row.StandardCost,
			&record.Row.IsSelf,
			&charged,
			&keyName,
		); err != nil {
			return nil, err
		}
		record.Row.CreatedAt = record.Row.CreatedAt.UTC()
		if record.Row.IsSelf {
			if charged.Valid {
				value := charged.Float64
				record.Row.Charged = &value
			}
			if keyName.Valid {
				value := keyName.String
				record.Row.APIKeyName = &value
			}
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (r *hostedOwnerRepository) GetUserEmails(ctx context.Context, userIDs []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, "SELECT id, email FROM users WHERE id = ANY($1)", pq.Array(userIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, err
		}
		out[id] = email
	}
	return out, rows.Err()
}

func (r *hostedOwnerRepository) UserExists(ctx context.Context, userID int64) (bool, error) {
	if userID <= 0 {
		return false, nil
	}
	var exists bool
	err := r.db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND deleted_at IS NULL)", userID).Scan(&exists)
	return exists, err
}
