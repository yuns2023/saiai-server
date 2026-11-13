package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.APIKeySpendingRankingRepository = (*usageLogRepository)(nil)

// requestBodySizeAggregates is shared by the user and API Key rankings (alias
// u = usage_logs). NULL sizes are unknown and excluded: SUM over no known row
// is 0, AVG/MAX are NULL. SUM/AVG of bigint return numeric, so cast back.
const requestBodySizeAggregates = `COALESCE(SUM(u.request_body_bytes), 0)::bigint AS request_body_bytes_total,
				ROUND(AVG(u.request_body_bytes))::bigint AS request_body_bytes_avg,
				MAX(u.request_body_bytes) AS request_body_bytes_max,
				COUNT(u.request_body_bytes) AS request_body_bytes_known_count`

// subscriptionRevenueAggregates prices subscription consumption (billing_type
// = 1 by history, hosted rows excluded) with each group's current
// statistics-only revenue rate; it needs "LEFT JOIN groups g ON g.id =
// u.group_id". Consumption whose group has no rate (or no group) is unpriced.
// It also splits the billed actual_cost (hosted rows excluded) into balance,
// subscription and unknown billing types for the admin revenue tooltip.
const subscriptionRevenueAggregates = `COALESCE(SUM(u.actual_cost * g.subscription_revenue_rate) FILTER (WHERE u.billing_type = 1 AND u.metered_cost IS NULL), 0) AS subscription_revenue,
				COALESCE(SUM(u.actual_cost) FILTER (WHERE u.billing_type = 1 AND u.metered_cost IS NULL AND g.subscription_revenue_rate IS NULL), 0) AS subscription_revenue_unpriced_cost,
				COALESCE(SUM(u.actual_cost) FILTER (WHERE u.billing_type = 0 AND u.metered_cost IS NULL), 0) AS balance_cost,
				COALESCE(SUM(u.actual_cost) FILTER (WHERE u.billing_type = 1 AND u.metered_cost IS NULL), 0) AS subscription_cost,
				COALESCE(SUM(u.actual_cost) FILTER (WHERE (u.billing_type IS NULL OR u.billing_type NOT IN (0, 1)) AND u.metered_cost IS NULL), 0) AS unclassified_cost`

// spendingRankingOrderBy returns a fixed ORDER BY list for a whitelisted sort.
// It is applied before LIMIT and again on the final SELECT; idOrder is the
// stable tie-break. prefix qualifies columns (e.g. "r.").
func spendingRankingOrderBy(sort usagestats.SpendingRankingSort, prefix, idOrder string) (string, error) {
	costOrder := prefix + "cost DESC, " + prefix + "actual_cost DESC, " + prefix + "tokens DESC, " + idOrder
	switch sort {
	case "", usagestats.SpendingRankingSortCost:
		return costOrder, nil
	case usagestats.SpendingRankingSortRequestBodyBytes:
		return prefix + "request_body_bytes_total DESC, " + prefix + "request_body_bytes_max DESC NULLS LAST, " + costOrder, nil
	default:
		return "", fmt.Errorf("unsupported ranking sort %q", sort)
	}
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}

// GetAPIKeySpendingRanking includes historical usage of soft-deleted Keys.
// A zero userID selects global usage; a positive ID filters by the recorded user.
// Keys are ranked by standard total_cost so hosted Keys (actual_cost is always 0,
// total_cost holds the reference price) rank alongside billed Keys.
func (r *usageLogRepository) GetAPIKeySpendingRanking(ctx context.Context, startTime, endTime time.Time, userID int64, limit int, sort usagestats.SpendingRankingSort) (result *usagestats.APIKeySpendingRankingResponse, err error) {
	if userID < 0 {
		return nil, fmt.Errorf("user ID must not be negative")
	}
	rankedOrder, err := spendingRankingOrderBy(sort, "", "api_key_id ASC, user_id ASC")
	if err != nil {
		return nil, err
	}
	finalOrder, err := spendingRankingOrderBy(sort, "r.", "r.api_key_id ASC, r.user_id ASC")
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 12
	} else if limit > 50 {
		limit = 50
	}
	args := []any{startTime, endTime}
	userFilter := ""
	if userID > 0 {
		args = append(args, userID)
		userFilter = " AND u.user_id = $3"
	}
	args = append(args, limit)
	query := fmt.Sprintf(`
		WITH key_spend AS (
			SELECT u.api_key_id, u.user_id,
				COALESCE(SUM(u.total_cost), 0) AS cost,
				COALESCE(SUM(u.actual_cost), 0) AS actual_cost,
				COUNT(*) AS requests,
				COUNT(*) FILTER (WHERE u.metered_cost IS NOT NULL) AS hosted_requests,
				COALESCE(SUM(u.input_tokens), 0) AS input_tokens,
				COALESCE(SUM(u.output_tokens), 0) AS output_tokens,
				COALESCE(SUM(u.cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(u.cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(u.input_tokens + u.output_tokens + u.cache_creation_tokens + u.cache_read_tokens), 0) AS tokens,
				%s,
				%s
			FROM usage_logs u
			LEFT JOIN groups g ON g.id = u.group_id
			WHERE u.created_at >= $1 AND u.created_at < $2%s
			GROUP BY u.api_key_id, u.user_id
		), ranked AS (
			SELECT *,
				COALESCE(SUM(cost) OVER (), 0) AS total_cost,
				COALESCE(SUM(actual_cost) OVER (), 0) AS total_actual_cost,
				COALESCE(SUM(requests) OVER (), 0) AS total_requests,
				COALESCE(SUM(tokens) OVER (), 0) AS total_tokens,
				COALESCE(SUM(subscription_revenue) OVER (), 0) AS total_subscription_revenue,
				COALESCE(SUM(subscription_revenue_unpriced_cost) OVER (), 0) AS total_subscription_revenue_unpriced_cost,
				COALESCE(SUM(balance_cost) OVER (), 0) AS total_balance_cost,
				COALESCE(SUM(subscription_cost) OVER (), 0) AS total_subscription_cost,
				COALESCE(SUM(unclassified_cost) OVER (), 0) AS total_unclassified_cost
			FROM key_spend
			ORDER BY %s
			LIMIT $%d
		)
		SELECT r.api_key_id, COALESCE(k.name, ''), r.user_id, COALESCE(us.email, ''),
			r.cost, r.actual_cost, r.requests, r.hosted_requests, r.tokens, r.input_tokens, r.output_tokens,
			r.cache_creation_tokens, r.cache_read_tokens,
			r.request_body_bytes_total, r.request_body_bytes_avg, r.request_body_bytes_max, r.request_body_bytes_known_count,
			r.subscription_revenue, r.subscription_revenue_unpriced_cost,
			r.balance_cost, r.subscription_cost, r.unclassified_cost,
			r.total_cost, r.total_actual_cost, r.total_requests, r.total_tokens,
			r.total_subscription_revenue, r.total_subscription_revenue_unpriced_cost,
			r.total_balance_cost, r.total_subscription_cost, r.total_unclassified_cost
		FROM ranked r
		LEFT JOIN api_keys k ON k.id = r.api_key_id
		LEFT JOIN users us ON us.id = r.user_id
		ORDER BY %s
	`, requestBodySizeAggregates, subscriptionRevenueAggregates, userFilter, rankedOrder, len(args), finalOrder)
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			result = nil
		}
	}()
	result = &usagestats.APIKeySpendingRankingResponse{
		Ranking: make([]usagestats.APIKeySpendingRankingItem, 0),
	}
	for rows.Next() {
		var item usagestats.APIKeySpendingRankingItem
		var sizeAvg, sizeMax sql.NullInt64
		if err = rows.Scan(
			&item.APIKeyID, &item.KeyName, &item.UserID, &item.Email,
			&item.Cost, &item.ActualCost, &item.Requests, &item.HostedRequests, &item.Tokens, &item.InputTokens, &item.OutputTokens,
			&item.CacheCreationTokens, &item.CacheReadTokens,
			&item.RequestBodyBytesTotal, &sizeAvg, &sizeMax, &item.RequestBodyBytesKnownCount,
			&item.SubscriptionRevenue, &item.SubscriptionRevenueUnpricedCost,
			&item.BalanceCost, &item.SubscriptionCost, &item.UnclassifiedCost,
			&result.TotalCost, &result.TotalActualCost, &result.TotalRequests, &result.TotalTokens,
			&result.TotalSubscriptionRevenue, &result.TotalSubscriptionRevenueUnpricedCost,
			&result.TotalBalanceCost, &result.TotalSubscriptionCost, &result.TotalUnclassifiedCost,
		); err != nil {
			return nil, err
		}
		item.RequestBodyBytesAvg, item.RequestBodyBytesMax = nullInt64Ptr(sizeAvg), nullInt64Ptr(sizeMax)
		result.Ranking = append(result.Ranking, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
