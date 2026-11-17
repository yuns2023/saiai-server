package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type settlementReconcileRepository struct {
	db *sql.DB
}

func NewSettlementReconcileRepository(sqlDB *sql.DB) service.SettlementReconcileRepository {
	return &settlementReconcileRepository{db: sqlDB}
}

// SettlementSummary groups the window's events by category, priced group and
// charge source. Amounts exclude rows with normalized (invalid) amounts.
func (r *settlementReconcileRepository) SettlementSummary(ctx context.Context, from, to time.Time) ([]service.SettlementReconcileSummaryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
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
// metering or an owned account), then joins the other side by identity across
// windows and classifies each identity. Amount tolerance 1e-9 (both sides are
// numeric(20,10) written from the same float).
const settlementDiffsCTE = `
	WITH ids AS (
		SELECT request_id, api_key_id FROM usage_settlement_events
		WHERE created_at >= $1 AND created_at < $2
		UNION
		SELECT request_id, api_key_id FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
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

func (r *settlementReconcileRepository) SettlementDiffs(ctx context.Context, q service.SettlementReconcileQuery) ([]service.SettlementReconcileDiff, []service.SettlementReconcileDiffCount, int64, error) {
	graceCutoff := q.Now.Add(-service.SettlementReconcileLogGrace)
	args := []any{q.From, q.To, q.CoverageStart, graceCutoff}

	// One snapshot for the counts and the page.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()

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
