package repository

import (
	"context"
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
			mock.ExpectQuery(`SUM\(cache_creation_tokens\).*SUM\(cache_read_tokens\).*FROM usage_logs`).
				WillReturnRows(sqlmock.NewRows([]string{
					"requests", "input", "output", "cache_write", "cache_read", "cache", "cost", "actual_cost", "balance_cost", "subscription_cost", "unclassified_cost", "duration",
				}).AddRow(2, 14906, 54, 160, 6500, 6660, 1.0, 0.5, 0.2, 0.3, 0, 100))
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
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
