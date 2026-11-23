//go:build unit

package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func apiKeyRankingRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"api_key_id", "key_name", "user_id", "email", "cost", "actual_cost", "requests", "hosted_requests", "tokens",
		"input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens",
		"request_body_bytes_total", "request_body_bytes_avg", "request_body_bytes_max", "request_body_bytes_known_count",
		"subscription_revenue", "subscription_revenue_unpriced_cost",
		"balance_cost", "subscription_cost", "unclassified_cost",
		"total_cost", "total_actual_cost", "total_requests", "total_tokens",
		"total_subscription_revenue", "total_subscription_revenue_unpriced_cost",
		"total_balance_cost", "total_subscription_cost", "total_unclassified_cost",
	})
}

func TestGetAPIKeySpendingRankingScopeAndTotals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		userID int64
		limit  int
		want   int
	}{
		{"global", 0, 12, 12},
		{"user", 7, 2, 2},
		{"default limit", 7, 0, 12},
		{"bounded limit", 7, 1000, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}
			start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
			end := start.Add(24 * time.Hour)
			// Enforce a half-open range, user filtering before grouping, and global
			// window totals before LIMIT. Joins retain historical/unnamed Keys.
			// Hosted rows are kept and ranked by standard total_cost.
			query := `(?s)WITH key_spend AS .*SUM\(u.total_cost\).*COUNT\(\*\) FILTER \(WHERE u.metered_cost IS NOT NULL\).*WHERE u.created_at >= \$1 AND u.created_at < \$2`
			if tc.userID > 0 {
				query += ` AND u.user_id = \$3`
			}
			query += `\s+GROUP BY u.api_key_id, u.user_id.*SUM\(cost\) OVER \(\).*SUM\(actual_cost\) OVER \(\).*ORDER BY cost DESC, actual_cost DESC, tokens DESC, api_key_id ASC, user_id ASC\s+LIMIT \$`
			if tc.userID > 0 {
				query += `4`
			} else {
				query += `3`
			}
			query += `.*LEFT JOIN api_keys k ON k.id = r.api_key_id\s+LEFT JOIN users us ON us.id = r.user_id\s+ORDER BY`
			expectation := mock.ExpectQuery(query)
			if tc.userID > 0 {
				expectation.WithArgs(start, end, tc.userID, tc.want)
			} else {
				expectation.WithArgs(start, end, tc.want)
			}
			expectation.WillReturnRows(apiKeyRankingRows().
				AddRow(11, "Work", 7, "user@example.com", 15.0, 0.0, 9, 9, 200, 100, 50, 25, 25, int64(9000), int64(1500), int64(4000), int64(6), 0.0, 0.0, 0.0, 0.0, 0.0, 60.0, 40.0, 30, 2600, 6.4, 3.0, 20.5, 16.0, 1.5).
				AddRow(12, "", 7, "user@example.com", 5.0, 4.25, 5, 0, 300, 0, 0, 100, 200, int64(0), nil, nil, int64(0), 6.4, 3.0, 0.0, 4.25, 0.0, 60.0, 40.0, 30, 2600, 6.4, 3.0, 20.5, 16.0, 1.5))

			got, err := repo.GetAPIKeySpendingRanking(context.Background(), start, end, tc.userID, tc.limit, usagestats.SpendingRankingSortCost, "")
			require.NoError(t, err)
			require.Len(t, got.Ranking, 2)
			require.Equal(t, int64(11), got.Ranking[0].APIKeyID)
			require.Equal(t, "Work", got.Ranking[0].KeyName)
			require.Equal(t, int64(7), got.Ranking[0].UserID)
			require.Equal(t, 15.0, got.Ranking[0].Cost) // Hosted Key: zero actual_cost.
			require.Zero(t, got.Ranking[0].ActualCost)
			require.Equal(t, int64(9), got.Ranking[0].HostedRequests)
			require.Equal(t, 4.25, got.Ranking[1].ActualCost)
			require.Equal(t, int64(100), got.Ranking[0].InputTokens)
			require.Equal(t, int64(50), got.Ranking[0].OutputTokens)
			require.Equal(t, int64(25), got.Ranking[0].CacheCreationTokens)
			require.Equal(t, int64(25), got.Ranking[0].CacheReadTokens)
			require.Equal(t, "", got.Ranking[1].KeyName)
			require.Equal(t, int64(9000), got.Ranking[0].RequestBodyBytesTotal)
			require.Equal(t, int64(1500), *got.Ranking[0].RequestBodyBytesAvg)
			require.Equal(t, int64(4000), *got.Ranking[0].RequestBodyBytesMax)
			require.Equal(t, int64(6), got.Ranking[0].RequestBodyBytesKnownCount)
			// No row with a known size: total 0, avg/max NULL (unknown, not 0).
			require.Zero(t, got.Ranking[1].RequestBodyBytesTotal)
			require.Nil(t, got.Ranking[1].RequestBodyBytesAvg)
			require.Nil(t, got.Ranking[1].RequestBodyBytesMax)
			require.Zero(t, got.Ranking[1].RequestBodyBytesKnownCount)
			require.Equal(t, 60.0, got.TotalCost) // Includes Keys outside Top N.
			require.Equal(t, 40.0, got.TotalActualCost)
			require.Equal(t, int64(30), got.TotalRequests)
			require.Equal(t, int64(2600), got.TotalTokens)
			require.Equal(t, 6.4, got.Ranking[1].SubscriptionRevenue)
			require.Equal(t, 3.0, got.Ranking[1].SubscriptionRevenueUnpricedCost)
			require.Equal(t, 6.4, got.TotalSubscriptionRevenue)
			require.Equal(t, 3.0, got.TotalSubscriptionRevenueUnpricedCost)
			require.Equal(t, 4.25, got.Ranking[1].SubscriptionCost)
			require.Equal(t, 20.5, got.TotalBalanceCost)
			require.Equal(t, 16.0, got.TotalSubscriptionCost)
			require.Equal(t, 1.5, got.TotalUnclassifiedCost)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestGetAPIKeySpendingRankingEmpty(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery("WITH key_spend AS").WillReturnRows(apiKeyRankingRows())
	got, err := repo.GetAPIKeySpendingRanking(context.Background(), time.Now(), time.Now(), 0, 12, usagestats.SpendingRankingSortCost, "")
	require.NoError(t, err)
	require.NotNil(t, got.Ranking)
	require.Empty(t, got.Ranking)
	require.Zero(t, got.TotalActualCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetAPIKeySpendingRankingFailures(t *testing.T) {
	for _, failure := range []string{"query", "row", "scan"} {
		t.Run(failure, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}
			query := mock.ExpectQuery("WITH key_spend AS")
			switch failure {
			case "query":
				query.WillReturnError(errors.New("unavailable"))
			case "row":
				query.WillReturnRows(apiKeyRankingRows().AddRow(1, "", 1, "", 1, 1, 1, 0, 1, 0, 0, 0, 1, 0, nil, nil, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0, 0).RowError(0, errors.New("read failed")))
			case "scan":
				query.WillReturnRows(apiKeyRankingRows().AddRow("bad ID", "", 1, "", 1, 1, 1, 0, 1, 0, 0, 0, 1, 0, nil, nil, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0, 0))
			}
			got, err := repo.GetAPIKeySpendingRanking(context.Background(), time.Now(), time.Now(), 0, 12, usagestats.SpendingRankingSortCost, "")
			require.Error(t, err)
			require.Nil(t, got)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestGetAPIKeySpendingRankingSortByRequestBodyBytes(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	// Aggregates exclude NULL sizes; the size order is applied before LIMIT
	// and again on the final SELECT, with the cost order and ids as tie-break.
	mock.ExpectQuery(`(?s)WITH key_spend AS .*COALESCE\(SUM\(u.request_body_bytes\), 0\)::bigint AS request_body_bytes_total.*ROUND\(AVG\(u.request_body_bytes\)\)::bigint AS request_body_bytes_avg.*MAX\(u.request_body_bytes\) AS request_body_bytes_max.*COUNT\(u.request_body_bytes\) AS request_body_bytes_known_count.*`+
		`ORDER BY request_body_bytes_total DESC, request_body_bytes_max DESC NULLS LAST, cost DESC, actual_cost DESC, tokens DESC, api_key_id ASC, user_id ASC\s+LIMIT \$3.*`+
		`ORDER BY r.request_body_bytes_total DESC, r.request_body_bytes_max DESC NULLS LAST, r.cost DESC, r.actual_cost DESC, r.tokens DESC, r.api_key_id ASC, r.user_id ASC\s*$`).
		WithArgs(start, end, 12).
		WillReturnRows(apiKeyRankingRows())
	_, err := repo.GetAPIKeySpendingRanking(context.Background(), start, end, 0, 12, usagestats.SpendingRankingSortRequestBodyBytes, "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	_, err = repo.GetAPIKeySpendingRanking(context.Background(), start, end, 0, 12, usagestats.SpendingRankingSort("cost; DROP TABLE usage_logs"), "")
	require.Error(t, err)
}

func TestGetAPIKeySpendingRankingSortByCacheReadRate(t *testing.T) {
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	rate := `\(cache_read_tokens::numeric / NULLIF\(input_tokens \+ cache_read_tokens \+ cache_creation_tokens, 0\)\)`
	finalRate := `\(r.cache_read_tokens::numeric / NULLIF\(r.input_tokens \+ r.cache_read_tokens \+ r.cache_creation_tokens, 0\)\)`
	for _, tc := range []struct {
		name      string
		userID    int64
		order     usagestats.SpendingRankingOrder
		direction string
		args      []driver.Value
		minParam  string
		limParam  string
	}{
		{"global asc", 0, usagestats.SpendingRankingOrderAsc, "ASC", []driver.Value{start, end, usagestats.CacheReadRateMinTokens, 20}, "3", "4"},
		{"user desc", 7, usagestats.SpendingRankingOrderDesc, "DESC", []driver.Value{start, end, int64(7), usagestats.CacheReadRateMinTokens, 20}, "4", "5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}
			// Totals are window sums over every Key (scoped) before the
			// token threshold filters the ranked rows.
			mock.ExpectQuery(`(?s)WITH key_spend AS .*\), scoped AS \(\s+SELECT \*,\s+COALESCE\(SUM\(cost\) OVER \(\), 0\) AS total_cost.*FROM key_spend\s+\), ranked AS \(\s+SELECT \* FROM scoped\s+` +
				`WHERE tokens >= \$` + tc.minParam + ` AND input_tokens \+ cache_read_tokens \+ cache_creation_tokens > 0\s+` +
				`ORDER BY ` + rate + ` ` + tc.direction + `, cost DESC, actual_cost DESC, tokens DESC, api_key_id ASC, user_id ASC\s+LIMIT \$` + tc.limParam + `.*` +
				`ORDER BY ` + finalRate + ` ` + tc.direction + `, r.cost DESC, r.actual_cost DESC, r.tokens DESC, r.api_key_id ASC, r.user_id ASC\s*$`).
				WithArgs(tc.args...).
				WillReturnRows(apiKeyRankingRows())
			_, err := repo.GetAPIKeySpendingRanking(context.Background(), start, end, tc.userID, 20, usagestats.SpendingRankingSortCacheReadRate, tc.order)
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}

	db, _ := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	for _, tc := range []struct {
		sort  usagestats.SpendingRankingSort
		order usagestats.SpendingRankingOrder
	}{
		{usagestats.SpendingRankingSortCost, usagestats.SpendingRankingOrderAsc},
		{usagestats.SpendingRankingSortRequestBodyBytes, usagestats.SpendingRankingOrderAsc},
		{usagestats.SpendingRankingSortCacheReadRate, usagestats.SpendingRankingOrder("asc; DROP TABLE usage_logs")},
	} {
		_, err := repo.GetAPIKeySpendingRanking(context.Background(), start, end, 0, 20, tc.sort, tc.order)
		require.Error(t, err, tc)
	}
	_, err := repo.GetUserSpendingRanking(context.Background(), start, end, 12, usagestats.SpendingRankingSortCacheReadRate)
	require.Error(t, err)
}
