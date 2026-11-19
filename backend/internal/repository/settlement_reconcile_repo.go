package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

type settlementReconcileRepository struct {
	db *sql.DB
}

func NewSettlementReconcileRepository(sqlDB *sql.DB) service.SettlementReconcileRepository {
	return &settlementReconcileRepository{db: sqlDB}
}

// settlementReconcileStatementTimeout bounds each reconcile query on the
// database side (replaced in tests).
var settlementReconcileStatementTimeout = "15s"

// Reconcile reads the summary, the diff counts and one diff page in a single
// read-only snapshot with a database-side statement timeout.
func (r *settlementReconcileRepository) Reconcile(ctx context.Context, q service.SettlementReconcileQuery) (*service.SettlementReconcileData, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, mapSettlementReconcileError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('statement_timeout', $1, true)`, settlementReconcileStatementTimeout); err != nil {
		return nil, mapSettlementReconcileError(err)
	}
	out := &service.SettlementReconcileData{}
	if out.Summary, err = settlementSummary(ctx, tx, q.From, q.To); err != nil {
		return nil, mapSettlementReconcileError(err)
	}
	if out.Diffs, out.Counts, out.Total, err = settlementDiffs(ctx, tx, q); err != nil {
		return nil, mapSettlementReconcileError(err)
	}
	if out.Market, err = settlementMarketReconcile(ctx, tx, q); err != nil {
		return nil, mapSettlementReconcileError(err)
	}
	return out, nil
}

func mapSettlementReconcileError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "57014" { // query_canceled: statement_timeout, or a cancel request
		return service.ErrSettlementReconcileTimeout
	}
	// The request context was cancelled or hit its deadline before or while
	// the driver talked to the database.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return service.ErrSettlementReconcileTimeout
	}
	return err
}

// settlementSummary groups the window's events by category, priced group and
// charge source. Amounts exclude rows with normalized (invalid) amounts.
func settlementSummary(ctx context.Context, tx *sql.Tx, from, to time.Time) ([]service.SettlementReconcileSummaryRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT category, group_id, charge_source,
			COUNT(*),
			COUNT(*) FILTER (WHERE anomaly),
			COUNT(*) FILTER (WHERE COALESCE(cardinality(invalid_fields), 0) > 0),
			COALESCE(SUM(standard_cost) FILTER (WHERE COALESCE(cardinality(invalid_fields), 0) = 0), 0),
			COALESCE(SUM(charge_input) FILTER (WHERE COALESCE(cardinality(invalid_fields), 0) = 0), 0)
		FROM usage_settlement_events
		WHERE created_at >= $1 AND created_at < $2
		GROUP BY category, group_id, charge_source
		ORDER BY category, group_id NULLS FIRST, charge_source
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.SettlementReconcileSummaryRow{}
	for rows.Next() {
		var row service.SettlementReconcileSummaryRow
		var groupID sql.NullInt64
		if err := rows.Scan(&row.Category, &groupID, &row.ChargeSource, &row.EventCount, &row.AnomalyCount, &row.InvalidAmountCount, &row.StandardCost, &row.ChargeInput); err != nil {
			return nil, err
		}
		row.GroupID = nullInt64Ptr(groupID)
		out = append(out, row)
	}
	return out, rows.Err()
}

// settlementDiffsCTE selects candidate identities whose event or usage log
// falls in [from, to) (usage logs only when they should have an event: hosted
// metering or an owned account; legacy logs without a request_id have no
// identity and are skipped), then joins the other side by identity across
// windows and classifies each identity. Amount tolerance 1e-9 (both sides are
// numeric(20,10) written from the same float).
const settlementDiffsCTE = `
	WITH ids AS (
		SELECT request_id, api_key_id FROM usage_settlement_events
		WHERE created_at >= $1 AND created_at < $2
		UNION
		SELECT request_id, api_key_id FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
			AND request_id IS NOT NULL
			AND (metered_cost IS NOT NULL OR account_owner_user_id IS NOT NULL)
	),
	joined AS (
		SELECT ids.request_id, ids.api_key_id,
			e.id AS event_id, e.category, e.group_id AS event_group_id, e.account_id AS event_account_id,
			e.owner_user_id AS event_owner, e.standard_cost, e.charge_input, e.invalid_fields,
			e.created_at AS event_created_at,
			l.id AS log_id, l.group_id AS log_group_id, l.account_id AS log_account_id,
			l.account_owner_user_id AS log_owner, l.total_cost, l.actual_cost, l.created_at AS log_created_at,
			COALESCE(cardinality(e.invalid_fields), 0) > 0 AS invalid_amounts
		FROM ids
		LEFT JOIN usage_settlement_events e ON e.request_id = ids.request_id AND e.api_key_id = ids.api_key_id
		LEFT JOIN usage_logs l ON l.request_id = ids.request_id AND l.api_key_id = ids.api_key_id
	),
	classified AS (
		SELECT j.*,
			(j.event_id IS NOT NULL AND j.log_id IS NOT NULL AND j.event_owner IS DISTINCT FROM j.log_owner) AS owner_differs,
			CASE
				WHEN j.event_id IS NULL THEN 'log_only'
				WHEN j.log_id IS NULL THEN 'event_only'
				WHEN NOT j.invalid_amounts AND (abs(j.standard_cost - j.total_cost) > 1e-9
					OR abs(j.charge_input - GREATEST(j.actual_cost, 0)) > 1e-9) THEN 'amount_mismatch'
				WHEN j.event_owner IS DISTINCT FROM j.log_owner THEN 'owner_mismatch'
			END AS kind
		FROM joined j
	),
	diffs AS (
		SELECT c.*,
			CASE c.kind
				WHEN 'log_only' THEN CASE WHEN c.log_created_at < $3 THEN 'coverage_unknown' ELSE 'missing_event' END
				WHEN 'event_only' THEN CASE WHEN c.event_created_at > $4 THEN 'pending' ELSE 'missing_log' END
				WHEN 'amount_mismatch' THEN 'mismatch'
				ELSE 'possible_owner_timing_difference'
			END AS status
		FROM classified c
		WHERE c.kind IS NOT NULL
	)
`

func settlementDiffs(ctx context.Context, tx *sql.Tx, q service.SettlementReconcileQuery) ([]service.SettlementReconcileDiff, []service.SettlementReconcileDiffCount, int64, error) {
	graceCutoff := q.Now.Add(-service.SettlementReconcileLogGrace)
	args := []any{q.From, q.To, q.CoverageStart, graceCutoff}

	counts := []service.SettlementReconcileDiffCount{}
	var total int64
	countRows, err := tx.QueryContext(ctx, settlementDiffsCTE+`
		SELECT kind, status, COUNT(*) FROM diffs GROUP BY kind, status ORDER BY kind, status
	`, args...)
	if err != nil {
		return nil, nil, 0, err
	}
	for countRows.Next() {
		var c service.SettlementReconcileDiffCount
		if err := countRows.Scan(&c.Kind, &c.Status, &c.Count); err != nil {
			_ = countRows.Close()
			return nil, nil, 0, err
		}
		total += c.Count
		counts = append(counts, c)
	}
	if err := countRows.Err(); err != nil {
		_ = countRows.Close()
		return nil, nil, 0, err
	}
	_ = countRows.Close()

	offset := (q.Page - 1) * q.PageSize
	rows, err := tx.QueryContext(ctx, settlementDiffsCTE+`
		SELECT request_id, api_key_id, kind, status, owner_differs, category,
			event_group_id, log_group_id, COALESCE(event_account_id, log_account_id),
			event_owner, log_owner, standard_cost, total_cost, charge_input, actual_cost,
			invalid_fields, event_created_at, log_created_at
		FROM diffs
		ORDER BY COALESCE(event_created_at, log_created_at), request_id, api_key_id
		LIMIT $5 OFFSET $6
	`, append(args, q.PageSize, offset)...)
	if err != nil {
		return nil, nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	diffs := []service.SettlementReconcileDiff{}
	for rows.Next() {
		var d service.SettlementReconcileDiff
		var category sql.NullString
		var eventGroup, logGroup, accountID, eventOwner, logOwner sql.NullInt64
		var standard, total, charge, actual sql.NullFloat64
		var invalid pq.StringArray
		var eventAt, logAt sql.NullTime
		if err := rows.Scan(&d.RequestID, &d.APIKeyID, &d.Kind, &d.Status, &d.OwnerDiffers, &category,
			&eventGroup, &logGroup, &accountID, &eventOwner, &logOwner, &standard, &total, &charge, &actual,
			&invalid, &eventAt, &logAt); err != nil {
			return nil, nil, 0, err
		}
		if category.Valid {
			v := category.String
			d.Category = &v
		}
		d.EventGroupID = nullInt64Ptr(eventGroup)
		d.LogGroupID = nullInt64Ptr(logGroup)
		d.AccountID = nullInt64Ptr(accountID)
		d.EventOwnerUserID = nullInt64Ptr(eventOwner)
		d.LogOwnerUserID = nullInt64Ptr(logOwner)
		d.EventStandardCost = nullFloat64Ptr(standard)
		d.LogTotalCost = nullFloat64Ptr(total)
		d.EventChargeInput = nullFloat64Ptr(charge)
		d.LogActualCost = nullFloat64Ptr(actual)
		d.InvalidFields = []string(invalid)
		if eventAt.Valid {
			v := eventAt.Time
			d.EventCreatedAt = &v
		}
		if logAt.Valid {
			v := logAt.Time
			d.LogCreatedAt = &v
		}
		diffs = append(diffs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}
	return diffs, counts, total, nil
}

// settlementMarketIssuesCTE lists the shared_sale ↔ ledger findings of the
// window ($1 from, $2 to; $3 = now − grace for admissions):
//   - a positive earning without exactly one matching accrue (owner, amount,
//     available_at), an accrue on a zero-earning or non-market event;
//   - an event whose split is not charged = owner_earning + platform_amount;
//   - a reverse whose original is not a matching accrue of the same owner
//     and event;
//   - a convert without its market_earning history row (code MKTEARN-<id>,
//     used by the owner, value = amount) and such a row without a convert;
//   - an admission without an event (pending within the grace, then
//     undetermined; never a reason to pay).
const settlementMarketIssuesCTE = `
	WITH issues AS (
		SELECT CASE WHEN a.id IS NULL THEN 'accrue_missing' ELSE 'accrue_mismatch' END AS kind, 'mismatch' AS status,
			e.id AS event_id, a.id AS entry_id, e.admission_id, e.owner_user_id, e.account_id, e.request_id, e.created_at AS at
		FROM usage_settlement_events e
		LEFT JOIN owner_earning_entries a ON a.settlement_event_id = e.id AND a.kind = 'accrue'
		WHERE e.category = 'shared_sale' AND e.created_at >= $1 AND e.created_at < $2 AND e.owner_earning > 0
			AND (a.id IS NULL OR a.owner_user_id IS DISTINCT FROM e.owner_user_id OR a.amount <> e.owner_earning
				OR a.available_at IS DISTINCT FROM e.earning_available_at)
		UNION ALL
		SELECT 'accrue_unexpected', 'mismatch', e.id, a.id, e.admission_id, a.owner_user_id, e.account_id, e.request_id, a.created_at
		FROM owner_earning_entries a
		JOIN usage_settlement_events e ON e.id = a.settlement_event_id
		WHERE a.kind = 'accrue' AND a.created_at >= $1 AND a.created_at < $2
			AND (e.category <> 'shared_sale' OR e.owner_earning = 0)
		UNION ALL
		SELECT 'split_mismatch', 'mismatch', e.id, NULL, e.admission_id, e.owner_user_id, e.account_id, e.request_id, e.created_at
		FROM usage_settlement_events e
		WHERE e.category = 'shared_sale' AND e.created_at >= $1 AND e.created_at < $2
			AND e.charged_amount IS DISTINCT FROM e.owner_earning + e.platform_amount
		UNION ALL
		SELECT 'reverse_mismatch', 'mismatch', r.settlement_event_id, r.id, NULL, r.owner_user_id, NULL, NULL, r.created_at
		FROM owner_earning_entries r
		LEFT JOIN owner_earning_entries a ON a.id = r.reverses_entry_id
		WHERE r.kind = 'reverse' AND r.created_at >= $1 AND r.created_at < $2
			AND (a.id IS NULL OR a.kind <> 'accrue' OR a.owner_user_id <> r.owner_user_id OR a.amount <> -r.amount
				OR a.available_at IS DISTINCT FROM r.available_at
				OR r.settlement_event_id IS DISTINCT FROM a.settlement_event_id)
		UNION ALL
		SELECT 'convert_history_missing', 'mismatch', NULL, c.id, NULL, c.owner_user_id, NULL, NULL, c.created_at
		FROM owner_earning_entries c
		LEFT JOIN redeem_codes h ON h.code = 'MKTEARN-' || c.id::text
		WHERE c.kind = 'convert' AND c.created_at >= $1 AND c.created_at < $2
			AND (h.id IS NULL OR h.type <> 'market_earning' OR h.status <> 'used'
				OR h.used_by IS DISTINCT FROM c.owner_user_id OR h.value <> -c.amount)
		UNION ALL
		SELECT 'convert_history_orphan', 'mismatch', NULL, NULL, NULL, h.used_by, NULL, NULL, h.created_at
		FROM redeem_codes h
		LEFT JOIN owner_earning_entries c ON c.kind = 'convert'
			AND c.id = CASE WHEN h.code ~ '^MKTEARN-[1-9][0-9]{0,17}$' THEN substr(h.code, 9)::bigint END
			AND h.code = 'MKTEARN-' || c.id::text
		WHERE h.type = 'market_earning' AND h.created_at >= $1 AND h.created_at < $2 AND c.id IS NULL
		UNION ALL
		SELECT 'admission_without_event', CASE WHEN m.admitted_at > $3 THEN 'pending' ELSE 'undetermined' END,
			NULL, NULL, m.id, m.owner_user_id, m.account_id, 'client:' || btrim(m.request_ref), m.admitted_at
		FROM market_admissions m
		WHERE m.admitted_at >= $1 AND m.admitted_at < $2
			AND NOT EXISTS (SELECT 1 FROM usage_settlement_events e WHERE e.admission_id = m.id)
	)
`

func settlementMarketReconcile(ctx context.Context, tx *sql.Tx, q service.SettlementReconcileQuery) (*service.SettlementMarketReconcile, error) {
	out := &service.SettlementMarketReconcile{Scope: service.SettlementMarketScopeOwnerBacked}
	s := &out.Summary
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE owner_earning > 0),
			COALESCE(SUM(charged_amount), 0)::text, COALESCE(SUM(owner_earning), 0)::text, COALESCE(SUM(platform_amount), 0)::text
		FROM usage_settlement_events
		WHERE category = 'shared_sale' AND created_at >= $1 AND created_at < $2
	`, q.From, q.To).Scan(&s.SaleCount, &s.EarningSaleCount, &s.ChargedAmount, &s.OwnerEarning, &s.PlatformAmount); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount) FILTER (WHERE kind = 'accrue'), 0)::text,
			COALESCE(-SUM(amount) FILTER (WHERE kind = 'reverse'), 0)::text,
			COALESCE(-SUM(amount) FILTER (WHERE kind = 'convert'), 0)::text
		FROM owner_earning_entries
		WHERE created_at >= $1 AND created_at < $2
	`, q.From, q.To).Scan(&s.AccruedAmount, &s.ReversedAmount, &s.ConvertedAmount); err != nil {
		return nil, err
	}
	for _, p := range []*string{&s.ChargedAmount, &s.OwnerEarning, &s.PlatformAmount, &s.AccruedAmount, &s.ReversedAmount, &s.ConvertedAmount} {
		d, err := decimal.NewFromString(*p)
		if err != nil {
			return nil, err
		}
		*p = d.String()
	}

	args := []any{q.From, q.To, q.Now.Add(-service.SettlementReconcileLogGrace)}
	countRows, err := tx.QueryContext(ctx, settlementMarketIssuesCTE+`
		SELECT kind, status, COUNT(*) FROM issues GROUP BY kind, status ORDER BY kind, status
	`, args...)
	if err != nil {
		return nil, err
	}
	out.IssueCounts = []service.SettlementReconcileDiffCount{}
	out.Issues = []service.SettlementMarketIssue{}
	var issueTotal int64
	for countRows.Next() {
		var c service.SettlementReconcileDiffCount
		if err := countRows.Scan(&c.Kind, &c.Status, &c.Count); err != nil {
			_ = countRows.Close()
			return nil, err
		}
		issueTotal += c.Count
		out.IssueCounts = append(out.IssueCounts, c)
	}
	if err := countRows.Err(); err != nil {
		_ = countRows.Close()
		return nil, err
	}
	_ = countRows.Close()
	if issueTotal == 0 {
		return out, nil
	}

	// Money findings first, admissions awaiting a decision last.
	rows, err := tx.QueryContext(ctx, settlementMarketIssuesCTE+`
		SELECT kind, status, event_id, entry_id, admission_id, owner_user_id, account_id, request_id, at
		FROM issues
		ORDER BY (kind = 'admission_without_event'), at, kind, COALESCE(event_id, entry_id, admission_id)
		LIMIT $4
	`, append(args, service.SettlementMarketMaxIssues)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var it service.SettlementMarketIssue
		var eventID, entryID, admissionID, ownerID, accountID sql.NullInt64
		var requestID sql.NullString
		if err := rows.Scan(&it.Kind, &it.Status, &eventID, &entryID, &admissionID, &ownerID, &accountID, &requestID, &it.At); err != nil {
			return nil, err
		}
		it.EventID = nullInt64Ptr(eventID)
		it.EntryID = nullInt64Ptr(entryID)
		it.AdmissionID = nullInt64Ptr(admissionID)
		it.OwnerUserID = nullInt64Ptr(ownerID)
		it.AccountID = nullInt64Ptr(accountID)
		it.RequestID = nullStringPtr(requestID)
		out.Issues = append(out.Issues, it)
	}
	return out, rows.Err()
}
