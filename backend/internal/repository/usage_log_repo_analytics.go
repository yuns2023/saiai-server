package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var (
	_ service.UsageAnalyticsRepository = (*usageLogRepository)(nil)
	_ service.UsageRecordsRepository   = (*usageLogRepository)(nil)
)

// usageAnalyticsEffortExpr normalizes the recorded reasoning effort; NULL
// means the record has none. Grouping and filtering use the same expression.
const usageAnalyticsEffortExpr = "NULLIF(lower(btrim(reasoning_effort)), '')"

// usageAnalyticsDimensions returns the SELECT and GROUP BY expressions for a
// whitelisted dimension. Unused dimension columns are typed NULLs so every
// query has the same shape.
func usageAnalyticsDimensions(groupBy string) (selectExpr, groupExpr string, err error) {
	switch groupBy {
	case usagestats.UsageAnalyticsByModel:
		return "model AS d_model, NULL::text AS d_effort, NULL::bigint AS d_key", "model", nil
	case usagestats.UsageAnalyticsByReasoningEffort:
		return "NULL::text AS d_model, " + usageAnalyticsEffortExpr + " AS d_effort, NULL::bigint AS d_key", usageAnalyticsEffortExpr, nil
	case usagestats.UsageAnalyticsByAPIKey:
		return "NULL::text AS d_model, NULL::text AS d_effort, api_key_id AS d_key", "api_key_id", nil
	case usagestats.UsageAnalyticsByAPIKeyModel:
		return "model AS d_model, NULL::text AS d_effort, api_key_id AS d_key", "api_key_id, model", nil
	default:
		return "", "", fmt.Errorf("unsupported usage analytics dimension %q", groupBy)
	}
}

func usageAnalyticsOrderBy(sortValue string) string {
	var primary string
	switch sortValue {
	case usagestats.UsageAnalyticsSortCost:
		primary = "cost DESC, actual_cost DESC"
	case usagestats.UsageAnalyticsSortUsageCost:
		primary = "usage_cost DESC, actual_cost DESC"
	case usagestats.UsageAnalyticsSortTokens:
		primary = "total_tokens DESC, actual_cost DESC"
	case usagestats.UsageAnalyticsSortRequests:
		primary = "requests DESC, actual_cost DESC"
	default:
		primary = "actual_cost DESC, total_tokens DESC"
	}
	// Dimension columns make the order total, so pages are stable.
	return primary + ", d_key ASC NULLS FIRST, d_model ASC NULLS FIRST, d_effort ASC NULLS LAST"
}

// GetUserUsageAnalytics aggregates one user's usage by a dimension. Groups,
// the summary over all groups and the requested page come from one statement,
// so shares are consistent and a page past the end still returns the summary.
func (r *usageLogRepository) GetUserUsageAnalytics(ctx context.Context, q usagestats.UsageAnalyticsQuery) (*usagestats.UsageAnalyticsResult, error) {
	if q.UserID <= 0 {
		return nil, errors.New("usage analytics requires a user scope")
	}
	dimSelect, dimGroup, err := usageAnalyticsDimensions(q.GroupBy)
	if err != nil {
		return nil, err
	}
	if q.ReasoningEffort != "" && q.ReasoningEffortMissing {
		return nil, errors.New("reasoning effort value and missing filter are mutually exclusive")
	}

	where, args := usageAnalyticsWhere(q)

	pageSize := q.PageSize
	if pageSize < 1 {
		pageSize = 50
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	if page-1 > math.MaxInt32/pageSize {
		return nil, errors.New("usage analytics page is out of range")
	}
	args = append(args, pageSize, (page-1)*pageSize)
	limitArg, offsetArg := len(args)-1, len(args)

	query := fmt.Sprintf(`
		WITH grouped AS (
			SELECT
				%s,
				COUNT(*) AS requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS total_tokens,
				COALESCE(SUM(total_cost), 0) AS cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(COALESCE(metered_cost, actual_cost)), 0) AS usage_cost
			FROM usage_logs
			WHERE %s
			GROUP BY %s
		),
		summary AS (
			SELECT
				COUNT(*) AS total_groups,
				COALESCE(SUM(requests), 0) AS requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_tokens), 0) AS total_tokens,
				COALESCE(SUM(cost), 0) AS cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(usage_cost), 0) AS usage_cost
			FROM grouped
		),
		page AS (
			SELECT
				ROW_NUMBER() OVER (ORDER BY %s) AS rn,
				g.*
			FROM grouped g
			ORDER BY %s
			LIMIT $%d OFFSET $%d
		)
		SELECT
			s.total_groups, s.requests, s.input_tokens, s.output_tokens,
			s.cache_creation_tokens, s.cache_read_tokens, s.total_tokens,
			s.cost, s.actual_cost, s.usage_cost,
			p.d_model, p.d_effort, p.d_key, k.name, k.deleted_at IS NOT NULL,
			p.requests, p.input_tokens, p.output_tokens,
			p.cache_creation_tokens, p.cache_read_tokens, p.total_tokens,
			p.cost, p.actual_cost, p.usage_cost
		FROM summary s
		LEFT JOIN page p ON TRUE
		LEFT JOIN api_keys k ON k.id = p.d_key AND k.user_id = $1
		ORDER BY p.rn
	`, dimSelect, where, dimGroup, usageAnalyticsOrderBy(q.Sort), usageAnalyticsOrderBy(q.Sort), limitArg, offsetArg)

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := &usagestats.UsageAnalyticsResult{Items: make([]usagestats.UsageAnalyticsItem, 0)}
	for rows.Next() {
		var (
			s                             = &result.Summary.UsageAnalyticsMetrics
			dModel, dEffort, keyName      sql.NullString
			dKey                          sql.NullInt64
			keyDeleted                    sql.NullBool
			requests, input, output       sql.NullInt64
			cacheCreation, cacheRead, tot sql.NullInt64
			cost, actualCost, usageCost   sql.NullFloat64
		)
		if err := rows.Scan(
			&result.Total, &s.Requests, &s.InputTokens, &s.OutputTokens,
			&s.CacheCreationTokens, &s.CacheReadTokens, &s.TotalTokens,
			&s.Cost, &s.ActualCost, &s.UsageCost,
			&dModel, &dEffort, &dKey, &keyName, &keyDeleted,
			&requests, &input, &output, &cacheCreation, &cacheRead, &tot,
			&cost, &actualCost, &usageCost,
		); err != nil {
			return nil, err
		}
		if !requests.Valid {
			continue // summary-only row: empty result or a page past the end
		}
		item := usagestats.UsageAnalyticsItem{
			UsageAnalyticsMetrics: usagestats.UsageAnalyticsMetrics{
				Requests:            requests.Int64,
				InputTokens:         input.Int64,
				OutputTokens:        output.Int64,
				CacheCreationTokens: cacheCreation.Int64,
				CacheReadTokens:     cacheRead.Int64,
				TotalTokens:         tot.Int64,
				Cost:                cost.Float64,
				ActualCost:          actualCost.Float64,
				UsageCost:           usageCost.Float64,
			},
		}
		switch q.GroupBy {
		case usagestats.UsageAnalyticsByModel:
			item.Model = dModel.String
		case usagestats.UsageAnalyticsByReasoningEffort:
			item.ReasoningEffort = dEffort.String
			item.ReasoningEffortMissing = !dEffort.Valid
		case usagestats.UsageAnalyticsByAPIKey, usagestats.UsageAnalyticsByAPIKeyModel:
			item.APIKeyID = dKey.Int64
			item.APIKeyName = keyName.String
			item.APIKeyDeleted = keyDeleted.Valid && keyDeleted.Bool
			if q.GroupBy == usagestats.UsageAnalyticsByAPIKeyModel {
				item.Model = dModel.String
			}
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result.Finalize()
	return result, nil
}

// usageAnalyticsWhere builds the user-scoped filter shared by the analytics
// aggregate and the records listing. $1 is always the user ID.
func usageAnalyticsWhere(q usagestats.UsageAnalyticsQuery) (string, []any) {
	where := "user_id = $1 AND created_at >= $2 AND created_at < $3"
	args := []any{q.UserID, q.StartTime, q.EndTime}
	if q.APIKeyID > 0 {
		args = append(args, q.APIKeyID)
		where += fmt.Sprintf(" AND api_key_id = $%d", len(args))
	}
	if q.GroupID > 0 {
		args = append(args, q.GroupID)
		where += fmt.Sprintf(" AND group_id = $%d", len(args))
	}
	if q.Model != "" {
		args = append(args, q.Model)
		where += fmt.Sprintf(" AND model = $%d", len(args))
	}
	if q.BillingType != nil {
		args = append(args, int16(*q.BillingType))
		where += fmt.Sprintf(" AND billing_type = $%d", len(args))
	}
	switch {
	case q.ReasoningEffortMissing:
		where += " AND " + usageAnalyticsEffortExpr + " IS NULL"
	case q.ReasoningEffort != "":
		args = append(args, strings.ToLower(strings.TrimSpace(q.ReasoningEffort)))
		where += fmt.Sprintf(" AND %s = $%d", usageAnalyticsEffortExpr, len(args))
	}
	return appendRequestTypeOrStreamQueryFilter(where, args, q.RequestType, nil)
}

// ListUserUsageRecordsPage returns up to limit records matching q in
// (created_at DESC, id DESC) order, strictly after the given position when
// after is set. It never counts or offsets: each page is a keyset seek.
func (r *usageLogRepository) ListUserUsageRecordsPage(ctx context.Context, q usagestats.UsageAnalyticsQuery, after *usagestats.UsageRecordPosition, limit int) ([]service.UsageLog, error) {
	if q.UserID <= 0 {
		return nil, errors.New("usage records require a user scope")
	}
	if q.ReasoningEffort != "" && q.ReasoningEffortMissing {
		return nil, errors.New("reasoning effort value and missing filter are mutually exclusive")
	}
	if limit < 1 {
		return nil, errors.New("usage records limit must be positive")
	}
	where, args := usageAnalyticsWhere(q)
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, limit)
	query := fmt.Sprintf("SELECT %s FROM usage_logs WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d",
		usageLogSelectColumns, where, len(args))

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	logs := make([]service.UsageLog, 0, limit)
	for rows.Next() {
		log, err := scanUsageLog(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, *log)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return logs, nil
}
