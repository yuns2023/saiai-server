package repository

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestUsageAggregatesPreserveCacheBreakdown(t *testing.T) {
	for _, scope := range []string{"user", "api_key", "account", "model"} {
		t.Run(scope, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}
			columns := []string{
				"requests", "input", "output", "cache_write", "cache_read", "cache", "cost", "actual_cost", "balance_cost", "subscription_cost", "unclassified_cost", "duration",
			}
			values := []driver.Value{2, 14906, 54, 160, 6500, 6660, 1.0, 0.5, 0.2, 0.3, 0, 100}
			if scope == "user" || scope == "api_key" {
				columns = append(columns, "metered_cost")
				values = append(values, nil)
			}
			mock.ExpectQuery(`SUM\(cache_creation_tokens\).*SUM\(cache_read_tokens\).*FROM usage_logs`).
				WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
			var stats *usagestats.UsageStats
			var err error
			ctx, start, end := context.Background(), time.Now().Add(-time.Hour), time.Now()
			switch scope {
			case "user":
				stats, err = repo.GetUserStatsAggregated(ctx, 1, start, end)
			case "api_key":
				stats, err = repo.GetAPIKeyStatsAggregated(ctx, 1, start, end)
			case "account":
				stats, err = repo.GetAccountStatsAggregated(ctx, 1, start, end)
			case "model":
				stats, err = repo.GetModelStatsAggregated(ctx, "test-model", start, end)
			}
			require.NoError(t, err)
			require.Equal(t, int64(160), stats.TotalCacheCreationTokens)
			require.Equal(t, int64(6500), stats.TotalCacheReadTokens)
			require.Equal(t, int64(6660), stats.TotalCacheTokens)
			require.Equal(t, int64(21620), stats.TotalTokens)
			require.Equal(t, stats.TotalTokens, stats.TotalInputTokens+stats.TotalOutputTokens+stats.TotalCacheCreationTokens+stats.TotalCacheReadTokens)
			require.Nil(t, stats.TotalMeteredCost)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUserScopedUsageAggregatesIncludeHostedMeteredCost(t *testing.T) {
	for _, scope := range []string{"user", "api_key"} {
		t.Run(scope, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}
			mock.ExpectQuery(`SUM\(metered_cost\) as total_metered_cost\s+FROM usage_logs`).
				WillReturnRows(sqlmock.NewRows([]string{
					"requests", "input", "output", "cache_write", "cache_read", "cache", "cost", "actual_cost", "balance_cost", "subscription_cost", "unclassified_cost", "duration", "metered_cost",
				}).AddRow(3, 100, 10, 0, 0, 0, 1.5, 0.25, 0.25, 0, 0, 50, 1.25))
			var stats *usagestats.UsageStats
			var err error
			ctx, start, end := context.Background(), time.Now().Add(-time.Hour), time.Now()
			if scope == "user" {
				stats, err = repo.GetUserStatsAggregated(ctx, 1, start, end)
			} else {
				stats, err = repo.GetAPIKeyStatsAggregated(ctx, 1, start, end)
			}
			require.NoError(t, err)
			require.NotNil(t, stats.TotalMeteredCost)
			require.InDelta(t, 1.25, *stats.TotalMeteredCost, 1e-9)
			require.InDelta(t, 0.25, stats.TotalActualCost, 1e-9)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
