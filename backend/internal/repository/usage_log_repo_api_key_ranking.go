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
// It also splits the billed actual_cost into balance, subscription and unknown
// billing types for the admin revenue tooltip. Balance includes hosted rows
// (their actual_cost is the hosting service fee, zero for free hosted groups);
// the subscription and unknown splits exclude hosted rows.
const subscriptionRevenueAggregates = `COALESCE(SUM(u.actual_cost * g.subscription_revenue_rate) FILTER (WHERE u.billing_type = 1 AND u.metered_cost IS NULL), 0) AS subscription_revenue,
				COALESCE(SUM(u.actual_cost) FILTER (WHERE u.billing_type = 1 AND u.metered_cost IS NULL AND g.subscription_revenue_rate IS NULL), 0) AS subscription_revenue_unpriced_cost,
				COALESCE(SUM(u.actual_cost) FILTER (WHERE u.billing_type = 0), 0) AS balance_cost,
				COALESCE(SUM(u.actual_cost) FILTER (WHERE u.billing_type = 1 AND u.metered_cost IS NULL), 0) AS subscription_cost,
				COALESCE(SUM(u.actual_cost) FILTER (WHERE (u.billing_type IS NULL OR u.billing_type NOT IN (0, 1)) AND u.metered_cost IS NULL), 0) AS unclassified_cost`

// spendingRankingOrderBy returns a fixed ORDER BY list for a whitelisted sort.
// It is applied before LIMIT and again on the final SELECT; idOrder is the
// stable tie-break. prefix qualifies columns (e.g. "r."). The cache read rate
// sort needs input_tokens/cache_read_tokens/cache_creation_tokens columns and
// cacheReadRateEligible applied first (so the denominator is positive); the
// user ranking does not offer it.
func spendingRankingOrderBy(sort usagestats.SpendingRankingSort, order usagestats.SpendingRankingOrder, prefix, idOrder string) (string, error) {
	costOrder := prefix + "cost DESC, " + prefix + "actual_cost DESC, " + prefix + "tokens DESC, " + idOrder
	if order != "" && order != usagestats.SpendingRankingOrderDesc && sort != usagestats.SpendingRankingSortCacheReadRate {
		return "", fmt.Errorf("ranking sort %q only supports descending order", sort)
	}
	switch sort {
	case "", usagestats.SpendingRankingSortCost:
		return costOrder, nil
	case usagestats.SpendingRankingSortRequestBodyBytes:
		return prefix + "request_body_bytes_total DESC, " + prefix + "request_body_bytes_max DESC NULLS LAST, " + costOrder, nil
	case usagestats.SpendingRankingSortCacheReadRate:
		direction := "ASC" // default: worst caching first, as ParseSpendingRankingOrder
		switch order {
		case "", usagestats.SpendingRankingOrderAsc:
		case usagestats.SpendingRankingOrderDesc:
			direction = "DESC"
		default:
			return "", fmt.Errorf("unsupported ranking order %q", order)
		}
		return cacheReadRateExpr(prefix) + " " + direction + ", " + costOrder, nil
	default:
		return "", fmt.Errorf("unsupported ranking sort %q", sort)
	}
}

// cacheReadRateExpr mirrors the frontend cacheReadRate(): cache read divided by
// uncached input + cache read + cache write, output excluded.
func cacheReadRateExpr(prefix string) string {
	return "(" + prefix + "cache_read_tokens::numeric / NULLIF(" + prefix + "input_tokens + " + prefix + "cache_read_tokens + " + prefix + "cache_creation_tokens, 0))"
}

// cacheReadRateEligible filters the cache read rate ranking to Keys with enough
// tokens for the rate to mean something and a positive denominator.
func cacheReadRateEligible(minTokensParam int) string {
	return fmt.Sprintf("tokens >= $%d AND input_tokens + cache_read_tokens + cache_creation_tokens > 0", minTokensParam)
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
// Keys are ranked by standard total_cost so hosted Keys (actual_cost is only the
// hosting service fee, 0 for free hosted groups; total_cost holds the standard
// price) rank alongside billed Keys.
//
// The cache read rate sort only ranks Keys with at least
// usagestats.CacheReadRateMinTokens tokens; the totals still cover every Key in
// the scope: they are window sums over every Key, read from a one-row
// subquery LEFT JOINed to the ranked rows in the same statement, so a filtered
// empty ranking still carries them (as one row with a NULL api_key_id).
func (r *usageLogRepository) GetAPIKeySpendingRanking(ctx context.Context, startTime, endTime time.Time, userID int64, limit int, sort usagestats.SpendingRankingSort, order usagestats.SpendingRankingOrder) (result *usagestats.APIKeySpendingRankingResponse, err error) {
	if userID < 0 {
		return nil, fmt.Errorf("user ID must not be negative")
	}
	rankedOrder, err := spendingRankingOrderBy(sort, order, "", "api_key_id ASC, user_id ASC")
	if err != nil {
		return nil, err
	}
	finalOrder, err := spendingRankingOrderBy(sort, order, "r.", "r.api_key_id ASC, r.user_id ASC")
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
	eligibleFilter := "TRUE"
	if sort == usagestats.SpendingRankingSortCacheReadRate {
		args = append(args, usagestats.CacheReadRateMinTokens)
		eligibleFilter = cacheReadRateEligible(len(args))
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
		), scoped AS (
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
		), ranked AS (
			SELECT * FROM scoped
			WHERE %s
			ORDER BY %s
			LIMIT $%d
		)
		SELECT r.api_key_id, COALESCE(k.name, ''), COALESCE(r.user_id, 0), COALESCE(us.email, ''),
			COALESCE(r.cost, 0), COALESCE(r.actual_cost, 0), COALESCE(r.requests, 0), COALESCE(r.hosted_requests, 0),
			COALESCE(r.tokens, 0), COALESCE(r.input_tokens, 0), COALESCE(r.output_tokens, 0),
			COALESCE(r.cache_creation_tokens, 0), COALESCE(r.cache_read_tokens, 0),
			COALESCE(r.request_body_bytes_total, 0), r.request_body_bytes_avg, r.request_body_bytes_max, COALESCE(r.request_body_bytes_known_count, 0),
			COALESCE(r.subscription_revenue, 0), COALESCE(r.subscription_revenue_unpriced_cost, 0),
			COALESCE(r.balance_cost, 0), COALESCE(r.subscription_cost, 0), COALESCE(r.unclassified_cost, 0),
			t.total_cost, t.total_actual_cost, t.total_requests, t.total_tokens,
			t.total_subscription_revenue, t.total_subscription_revenue_unpriced_cost,
			t.total_balance_cost, t.total_subscription_cost, t.total_unclassified_cost
		FROM (
			SELECT total_cost, total_actual_cost, total_requests, total_tokens,
				total_subscription_revenue, total_subscription_revenue_unpriced_cost,
				total_balance_cost, total_subscription_cost, total_unclassified_cost
			FROM scoped LIMIT 1
		) t
		LEFT JOIN ranked r ON TRUE
		LEFT JOIN api_keys k ON k.id = r.api_key_id
		LEFT JOIN users us ON us.id = r.user_id
		ORDER BY %s
	`, requestBodySizeAggregates, subscriptionRevenueAggregates, userFilter, eligibleFilter, rankedOrder, len(args), finalOrder)
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
		var keyID, sizeAvg, sizeMax sql.NullInt64
		if err = rows.Scan(
			&keyID, &item.KeyName, &item.UserID, &item.Email,
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
		if !keyID.Valid {
			continue // scope totals only: no Key passed the cache read rate threshold
		}
		item.APIKeyID = keyID.Int64
		item.RequestBodyBytesAvg, item.RequestBodyBytesMax = nullInt64Ptr(sizeAvg), nullInt64Ptr(sizeMax)
		result.Ranking = append(result.Ranking, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
