package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

// Owner earnings ledger (P3a BE3). See service/market_earnings.go for the
// balance formulas and the convert / reverse rules.
//
// Lock order: owner_earning_wallets rows (user id order) → users row (convert
// only). The billing transaction never touches either for the seller; it
// only inserts accrue entries.
//
// Every ledger insert is conditional on the row it references (DB-level
// guard): an accrue must match its shared_sale event (owner, amount =
// owner_earning, available_at = earning_available_at); a reverse must match
// an accrue of the same owner with the exact negated amount and the same
// available_at. A guard that rejects the insert is an error and the whole
// transaction rolls back.

var errOwnerEarningGuard = errors.New("owner earning ledger guard rejected the insert")

// walletTxOptions: the wallet operations rely on READ COMMITTED — every
// statement after the wallet lock sees what committed before the lock was
// granted (a convert waiting behind a reverse sees the reverse entries). Set
// explicitly so a different connection default cannot change it.
var walletTxOptions = &sql.TxOptions{Isolation: sql.LevelReadCommitted}

// reverseBeforeCommit runs just before a reverse commits (tests only).
var reverseBeforeCommit func()

type ownerEarningRepository struct {
	db *sql.DB
}

func NewOwnerEarningRepository(sqlDB *sql.DB) service.OwnerEarningRepository {
	return &ownerEarningRepository{db: sqlDB}
}

// insertAccrueEntryTx inserts the accrue of a shared_sale event, only if the
// event has exactly this owner, earning and available_at.
func insertAccrueEntryTx(ctx context.Context, q sqlQueryer, ownerUserID int64, amount decimal.Decimal, eventID int64, availableAt time.Time) (int64, error) {
	var id int64
	err := scanSingleRow(ctx, q, `
		INSERT INTO owner_earning_entries (owner_user_id, kind, amount, settlement_event_id, available_at)
		SELECT $1::bigint, 'accrue', $2::numeric, e.id, $4::timestamptz
		FROM usage_settlement_events e
		WHERE e.id = $3
			AND e.category = 'shared_sale'
			AND e.owner_user_id = $1
			AND e.owner_earning > 0
			AND e.owner_earning = $2::numeric
			AND e.earning_available_at = $4
		RETURNING id
	`, []any{ownerUserID, amount.String(), eventID, availableAt}, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: %w: accrue for event %d", service.ErrMarketSettlementIntegrity, errOwnerEarningGuard, eventID)
	}
	return id, err
}

// insertReverseEntryTx inserts the reverse of an accrue, only if the accrue
// belongs to ownerUserID, has exactly -amount and the same available_at.
// amount is the (negative) reverse amount.
func insertReverseEntryTx(ctx context.Context, q sqlQueryer, ownerUserID int64, amount decimal.Decimal, accrueID int64, availableAt time.Time, adminUserID int64, note string) (int64, error) {
	var id int64
	err := scanSingleRow(ctx, q, `
		INSERT INTO owner_earning_entries (owner_user_id, kind, amount, reverses_entry_id, settlement_event_id, available_at, admin_user_id, note)
		SELECT $1::bigint, 'reverse', $2::numeric, a.id, a.settlement_event_id, $4::timestamptz, $5::bigint, $6::text
		FROM owner_earning_entries a
		WHERE a.id = $3
			AND a.kind = 'accrue'
			AND a.owner_user_id = $1
			AND a.amount = -($2::numeric)
			AND a.available_at = $4
		RETURNING id
	`, []any{ownerUserID, amount.String(), accrueID, availableAt, nullableAdminID(adminUserID), nullableNote(note)}, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: reverse of entry %d", errOwnerEarningGuard, accrueID)
	}
	return id, err
}

func nullableAdminID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

func nullableNote(note string) any {
	if note == "" {
		return nil
	}
	return note
}

// lockOwnerWalletsTx creates the wallet rows idempotently, locks them in user
// id order and returns the database time read after the locks.
func lockOwnerWalletsTx(ctx context.Context, tx *sql.Tx, owners []int64) (time.Time, error) {
	ids := slices.Clone(owners)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO owner_earning_wallets (user_id)
		SELECT u FROM unnest($1::bigint[]) AS u ORDER BY u
		ON CONFLICT (user_id) DO NOTHING
	`, pq.Array(ids)); err != nil {
		return time.Time{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT user_id FROM owner_earning_wallets WHERE user_id = ANY($1) ORDER BY user_id FOR UPDATE
	`, pq.Array(ids))
	if err != nil {
		return time.Time{}, err
	}
	locked := 0
	for rows.Next() {
		locked++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return time.Time{}, err
	}
	if err := rows.Close(); err != nil {
		return time.Time{}, err
	}
	if locked != len(ids) {
		return time.Time{}, fmt.Errorf("locked %d of %d owner earning wallets", locked, len(ids))
	}
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

// ownerEarningBalancesAt sums one owner's ledger at t (one statement).
func ownerEarningBalancesAt(ctx context.Context, q sqlQueryer, ownerUserID int64, t time.Time) (*service.OwnerEarningBalances, error) {
	var frozen, available, converted, reversed, accrued string
	err := scanSingleRow(ctx, q, `
		SELECT
			COALESCE(SUM(amount) FILTER (WHERE kind IN ('accrue', 'reverse') AND available_at > $2), 0)::text,
			COALESCE(SUM(amount) FILTER (WHERE (kind IN ('accrue', 'reverse') AND available_at <= $2) OR kind = 'convert'), 0)::text,
			COALESCE(-SUM(amount) FILTER (WHERE kind = 'convert'), 0)::text,
			COALESCE(-SUM(amount) FILTER (WHERE kind = 'reverse'), 0)::text,
			COALESCE(SUM(amount) FILTER (WHERE kind = 'accrue'), 0)::text
		FROM owner_earning_entries
		WHERE owner_user_id = $1
	`, []any{ownerUserID, t}, &frozen, &available, &converted, &reversed, &accrued)
	if err != nil {
		return nil, err
	}
	out := &service.OwnerEarningBalances{AsOf: t}
	for _, f := range []struct {
		dst *decimal.Decimal
		raw string
	}{{&out.Frozen, frozen}, {&out.Available, available}, {&out.Converted, converted}, {&out.Reversed, reversed}, {&out.Accrued, accrued}} {
		v, err := decimal.NewFromString(f.raw)
		if err != nil {
			return nil, fmt.Errorf("parse ledger sum: %w", err)
		}
		*f.dst = v
	}
	return out, nil
}

// Balances reads the owner's sums at the database time in one read-only
// snapshot.
func (r *ownerEarningRepository) Balances(ctx context.Context, ownerUserID int64) (*service.OwnerEarningBalances, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	return ownerEarningBalancesAt(ctx, tx, ownerUserID, now)
}

func (r *ownerEarningRepository) ListEntries(ctx context.Context, ownerUserID, beforeID int64, limit int) ([]service.OwnerEarningEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT x.id, x.owner_user_id, x.kind, x.amount::text, x.available_at, x.created_at,
			CASE
				WHEN x.kind = 'convert' THEN 'converted'
				WHEN x.kind = 'reverse' THEN 'reversed'
				WHEN EXISTS (SELECT 1 FROM owner_earning_entries r
					WHERE r.kind = 'reverse' AND r.reverses_entry_id = x.id) THEN 'reversed'
				WHEN x.available_at > statement_timestamp() THEN 'frozen'
				ELSE 'available'
			END,
			ev.account_id, l.model, x.settlement_event_id, x.reverses_entry_id, x.convert_request_id,
			x.admin_user_id, x.note
		FROM owner_earning_entries x
		LEFT JOIN usage_settlement_events ev ON ev.id = x.settlement_event_id
		LEFT JOIN LATERAL (
			SELECT ul.model FROM usage_logs ul
			WHERE ul.request_id = ev.request_id AND ul.api_key_id = ev.api_key_id
			ORDER BY ul.id LIMIT 1
		) l ON TRUE
		WHERE ($1::bigint = 0 OR x.owner_user_id = $1) AND ($2::bigint = 0 OR x.id < $2)
		ORDER BY x.id DESC
		LIMIT $3
	`, ownerUserID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.OwnerEarningEntry{}
	for rows.Next() {
		var e service.OwnerEarningEntry
		var amount string
		var availableAt sql.NullTime
		var accountID, eventID, reversesID, adminID sql.NullInt64
		var model, requestID, note sql.NullString
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Kind, &amount, &availableAt, &e.CreatedAt, &e.Status,
			&accountID, &model, &eventID, &reversesID, &requestID, &adminID, &note); err != nil {
			return nil, err
		}
		if e.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, fmt.Errorf("parse entry amount: %w", err)
		}
		if availableAt.Valid {
			v := availableAt.Time
			e.AvailableAt = &v
		}
		e.AccountID = nullInt64Ptr(accountID)
		e.SettlementEventID = nullInt64Ptr(eventID)
		e.ReversesEntryID = nullInt64Ptr(reversesID)
		e.AdminUserID = nullInt64Ptr(adminID)
		e.Model = nullStringPtr(model)
		e.ConvertRequestID = nullStringPtr(requestID)
		e.Note = nullStringPtr(note)
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// Convert: wallet lock → t → idempotency → minimum → available → convert
// entry + balance + history, one transaction.
func (r *ownerEarningRepository) Convert(ctx context.Context, in service.OwnerEarningConvertInput) (_ *service.OwnerEarningConvertRecord, err error) {
	tx, err := r.db.BeginTx(ctx, walletTxOptions)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	t, err := lockOwnerWalletsTx(ctx, tx, []int64{in.OwnerUserID})
	if err != nil {
		return nil, err
	}

	// Idempotency first: a retry of a committed conversion returns it even
	// if the earnings are spent by now or the minimum changed since.
	var existingID int64
	var existingAmount string
	var existingAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT id, (-amount)::text, created_at FROM owner_earning_entries
		WHERE owner_user_id = $1 AND kind = 'convert' AND convert_request_id = $2
	`, in.OwnerUserID, in.RequestID).Scan(&existingID, &existingAmount, &existingAt)
	if err == nil {
		amount, perr := decimal.NewFromString(existingAmount)
		if perr != nil {
			return nil, fmt.Errorf("parse convert amount: %w", perr)
		}
		if !amount.Equal(in.Amount) {
			return nil, service.ErrOwnerEarningConvertConflict
		}
		return &service.OwnerEarningConvertRecord{EntryID: existingID, Amount: amount, RequestID: in.RequestID, CreatedAt: existingAt, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	settings, err := loadMarketSettingsTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if in.Amount.LessThan(settings.MinConvertUSD) {
		return nil, service.ErrOwnerEarningBelowMinimum.WithMetadata(map[string]string{"min_convert": settings.MinConvertUSD.String()})
	}
	balances, err := ownerEarningBalancesAt(ctx, tx, in.OwnerUserID, t)
	if err != nil {
		return nil, err
	}
	if in.Amount.GreaterThan(balances.Available) {
		return nil, service.ErrOwnerEarningInsufficient.WithMetadata(map[string]string{"available": balances.Available.String()})
	}

	amount := in.Amount.String()
	rec := &service.OwnerEarningConvertRecord{Amount: in.Amount, RequestID: in.RequestID}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO owner_earning_entries (owner_user_id, kind, amount, convert_request_id)
		VALUES ($1, 'convert', -($2::numeric), $3)
		RETURNING id, created_at
	`, in.OwnerUserID, amount, in.RequestID).Scan(&rec.EntryID, &rec.CreatedAt); err != nil {
		if isNumericOutOfRange(err) {
			// The available sum can exceed one numeric(20,10) entry.
			return nil, service.ErrBalanceOutOfRange
		}
		return nil, err
	}
	var balanceAfter string
	err = tx.QueryRowContext(ctx, `
		UPDATE users SET balance = balance + $1::numeric, updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING balance::text
	`, amount, in.OwnerUserID).Scan(&balanceAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrUserNotFound
	}
	if err != nil {
		if isNumericOutOfRange(err) {
			return nil, service.ErrBalanceOutOfRange
		}
		return nil, fmt.Errorf("add balance: %w", err)
	}
	// History row: shown in the user's balance records, used, never
	// redeemable, not a top-up.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, notes, validity_days, created_at)
		VALUES ($1, $2, $3::numeric, $4, $5, NOW(), $6, 30, NOW())
	`, service.MarketEarningCodePrefix+strconv.FormatInt(rec.EntryID, 10), service.RedeemTypeMarketEarning, amount,
		service.StatusUsed, in.OwnerUserID, fmt.Sprintf("market earnings conversion #%d", rec.EntryID)); err != nil {
		if isNumericOutOfRange(err) {
			return nil, service.ErrBalanceOutOfRange
		}
		return nil, fmt.Errorf("insert market earning history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return rec, nil
}

// reverseScopeFilter selects unreversed accrue entries of the scope
// ($1 account id or NULL, $2 owner id or NULL) recorded at or before the
// cutoff $3 (as_of).
const reverseScopeFilter = `
	a.kind = 'accrue'
	AND ($1::bigint IS NULL OR e.account_id = $1)
	AND ($2::bigint IS NULL OR a.owner_user_id = $2)
	AND a.created_at <= $3::timestamptz
	AND NOT EXISTS (SELECT 1 FROM owner_earning_entries r WHERE r.kind = 'reverse' AND r.reverses_entry_id = a.id)
`

// Reverse: pin the frozen unreversed accrues of the scope visible when this
// call starts (ids and owners), lock their owners' wallets in id order, read
// t, and reverse only those pinned accrues that are still unreversed and
// frozen at t. Accrues committed while waiting for the wallet locks are out
// of scope. Each call selects its scope again (a retry is not a replay of
// the first attempt's entries; see service.OwnerEarningReverseInput).
func (r *ownerEarningRepository) Reverse(ctx context.Context, in service.OwnerEarningReverseInput) (_ *service.OwnerEarningReverseResult, err error) {
	tx, err := r.db.BeginTx(ctx, walletTxOptions)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// The cutoff (as_of) may not lie in the database's future.
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if in.AsOf.IsZero() || in.AsOf.After(now) {
		return nil, service.ErrOwnerEarningReverseAsOf
	}
	cutoff := in.AsOf

	account, owner := nullableInt64(in.AccountID), nullableInt64(in.OwnerUserID)
	rows, err := tx.QueryContext(ctx, `
		SELECT a.id, a.owner_user_id
		FROM owner_earning_entries a
		JOIN usage_settlement_events e ON e.id = a.settlement_event_id
		WHERE `+reverseScopeFilter+` AND a.available_at > clock_timestamp()
	`, account, owner, cutoff)
	if err != nil {
		return nil, err
	}
	var owners, pinned []int64
	for rows.Next() {
		var id, ownerID int64
		if err := rows.Scan(&id, &ownerID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pinned = append(pinned, id)
		owners = append(owners, ownerID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	out := &service.OwnerEarningReverseResult{Cutoff: cutoff, ReversedAmount: "0", Owners: []service.OwnerEarningReverseOwner{}}
	if len(owners) == 0 {
		return out, nil
	}

	t, err := lockOwnerWalletsTx(ctx, tx, owners)
	if err != nil {
		return nil, err
	}
	out.AsOf = &t

	type target struct {
		id          int64
		owner       int64
		amount      decimal.Decimal
		availableAt time.Time
	}
	var targets []target
	trows, err := tx.QueryContext(ctx, `
		SELECT a.id, a.owner_user_id, a.amount::text, a.available_at
		FROM owner_earning_entries a
		JOIN usage_settlement_events e ON e.id = a.settlement_event_id
		WHERE `+reverseScopeFilter+` AND a.id = ANY($4) AND a.available_at > $5
		ORDER BY a.id
	`, account, owner, cutoff, pq.Array(pinned), t)
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var tg target
		var amount string
		if err := trows.Scan(&tg.id, &tg.owner, &amount, &tg.availableAt); err != nil {
			_ = trows.Close()
			return nil, err
		}
		if tg.amount, err = decimal.NewFromString(amount); err != nil {
			_ = trows.Close()
			return nil, fmt.Errorf("parse accrue amount: %w", err)
		}
		targets = append(targets, tg)
	}
	if err := trows.Err(); err != nil {
		_ = trows.Close()
		return nil, err
	}
	_ = trows.Close()

	total := decimal.Zero
	perOwner := map[int64]*service.OwnerEarningReverseOwner{}
	perOwnerAmount := map[int64]decimal.Decimal{}
	for _, tg := range targets {
		if _, err := insertReverseEntryTx(ctx, tx, tg.owner, tg.amount.Neg(), tg.id, tg.availableAt, in.AdminUserID, in.Note); err != nil {
			return nil, err
		}
		total = total.Add(tg.amount)
		row := perOwner[tg.owner]
		if row == nil {
			row = &service.OwnerEarningReverseOwner{OwnerUserID: tg.owner}
			perOwner[tg.owner] = row
		}
		row.Count++
		perOwnerAmount[tg.owner] = perOwnerAmount[tg.owner].Add(tg.amount)
	}
	if reverseBeforeCommit != nil {
		reverseBeforeCommit()
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out.ReversedCount = len(targets)
	out.ReversedAmount = total.String()
	ids := make([]int64, 0, len(perOwner))
	for id := range perOwner {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		row := perOwner[id]
		row.Amount = perOwnerAmount[id].String()
		out.Owners = append(out.Owners, *row)
	}
	return out, nil
}

func (r *ownerEarningRepository) AdminSummaries(ctx context.Context, ownerUserID *int64, offset, limit int) ([]service.AdminOwnerEarningSummary, int64, time.Time, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, 0, time.Time{}, err
	}
	var total int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT owner_user_id) FROM owner_earning_entries WHERE ($1::bigint IS NULL OR owner_user_id = $1)
	`, nullableInt64(ownerUserID)).Scan(&total); err != nil {
		return nil, 0, time.Time{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		WITH s AS (
			SELECT owner_user_id,
				COALESCE(SUM(amount) FILTER (WHERE kind IN ('accrue', 'reverse') AND available_at > $2), 0) AS frozen,
				COALESCE(SUM(amount) FILTER (WHERE (kind IN ('accrue', 'reverse') AND available_at <= $2) OR kind = 'convert'), 0) AS available,
				COALESCE(-SUM(amount) FILTER (WHERE kind = 'convert'), 0) AS converted,
				COALESCE(-SUM(amount) FILTER (WHERE kind = 'reverse'), 0) AS reversed,
				COALESCE(SUM(amount) FILTER (WHERE kind = 'accrue'), 0) AS accrued
			FROM owner_earning_entries
			WHERE ($1::bigint IS NULL OR owner_user_id = $1)
			GROUP BY owner_user_id
		)
		SELECT s.owner_user_id, COALESCE(u.email, ''), s.frozen::text, s.available::text, s.converted::text,
			s.reversed::text, s.accrued::text
		FROM s LEFT JOIN users u ON u.id = s.owner_user_id
		ORDER BY s.owner_user_id
		LIMIT $3 OFFSET $4
	`, nullableInt64(ownerUserID), now, limit, offset)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.AdminOwnerEarningSummary{}
	for rows.Next() {
		var s service.AdminOwnerEarningSummary
		if err := rows.Scan(&s.OwnerUserID, &s.Email, &s.Frozen, &s.Available, &s.ConvertedTotal, &s.ReversedTotal, &s.AccruedTotal); err != nil {
			return nil, 0, time.Time{}, err
		}
		for _, p := range []*string{&s.Frozen, &s.Available, &s.ConvertedTotal, &s.ReversedTotal, &s.AccruedTotal} {
			d, err := decimal.NewFromString(*p)
			if err != nil {
				return nil, 0, time.Time{}, fmt.Errorf("parse ledger sum: %w", err)
			}
			*p = d.String()
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, time.Time{}, err
	}
	return out, total, now, nil
}

// isNumericOutOfRange: SQLSTATE 22003 (numeric_value_out_of_range).
func isNumericOutOfRange(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "22003"
}
