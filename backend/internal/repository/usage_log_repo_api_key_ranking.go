package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.APIKeySpendingRankingRepository = (*usageLogRepository)(nil)

// GetAPIKeySpendingRanking includes historical usage of soft-deleted Keys.
// A zero userID selects global usage; a positive ID filters by the recorded user.
// Keys are ranked by standard total_cost so hosted Keys (actual_cost is always 0,
// total_cost holds the reference price) rank alongside billed Keys.
func (r *usageLogRepository) GetAPIKeySpendingRanking(ctx context.Context, startTime, endTime time.Time, userID int64, limit int) (result *usagestats.APIKeySpendingRankingResponse, err error) {
	if userID < 0 {
		return nil, fmt.Errorf("user ID must not be negative")
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
				COALESCE(SUM(u.input_tokens + u.output_tokens + u.cache_creation_tokens + u.cache_read_tokens), 0) AS tokens
			FROM usage_logs u
			WHERE u.created_at >= $1 AND u.created_at < $2%s
			GROUP BY u.api_key_id, u.user_id
		), ranked AS (
			SELECT *,
				COALESCE(SUM(cost) OVER (), 0) AS total_cost,
				COALESCE(SUM(actual_cost) OVER (), 0) AS total_actual_cost,
				COALESCE(SUM(requests) OVER (), 0) AS total_requests,
				COALESCE(SUM(tokens) OVER (), 0) AS total_tokens
			FROM key_spend
			ORDER BY cost DESC, actual_cost DESC, tokens DESC, api_key_id ASC, user_id ASC
			LIMIT $%d
		)
		SELECT r.api_key_id, COALESCE(k.name, ''), r.user_id, COALESCE(us.email, ''),
			r.cost, r.actual_cost, r.requests, r.hosted_requests, r.tokens, r.input_tokens, r.output_tokens,
			r.cache_creation_tokens, r.cache_read_tokens,
			r.total_cost, r.total_actual_cost, r.total_requests, r.total_tokens
		FROM ranked r
		LEFT JOIN api_keys k ON k.id = r.api_key_id
		LEFT JOIN users us ON us.id = r.user_id
		ORDER BY r.cost DESC, r.actual_cost DESC, r.tokens DESC, r.api_key_id ASC, r.user_id ASC
	`, userFilter, len(args))
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
		if err = rows.Scan(
			&item.APIKeyID, &item.KeyName, &item.UserID, &item.Email,
			&item.Cost, &item.ActualCost, &item.Requests, &item.HostedRequests, &item.Tokens, &item.InputTokens, &item.OutputTokens,
			&item.CacheCreationTokens, &item.CacheReadTokens,
			&result.TotalCost, &result.TotalActualCost, &result.TotalRequests, &result.TotalTokens,
		); err != nil {
			return nil, err
		}
		result.Ranking = append(result.Ranking, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
