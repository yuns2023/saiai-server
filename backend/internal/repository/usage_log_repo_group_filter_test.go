package repository

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserStatsAggregatedWithFiltersAppendsKeyAndGroup(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	ctx, start, end := context.Background(), time.Now().Add(-time.Hour), time.Now()
	mock.ExpectQuery(`SUM\(metered_cost\) as total_metered_cost\s+FROM usage_logs\s+WHERE user_id = \$1 AND created_at >= \$2 AND created_at < \$3 AND api_key_id = \$4 AND group_id = \$5$`).
		WithArgs(int64(42), start, end, int64(7), int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{
			"requests", "input", "output", "cache_write", "cache_read", "cache", "cost", "actual_cost", "balance_cost", "subscription_cost", "unclassified_cost", "duration", "metered_cost",
		}).AddRow(3, 100, 10, 5, 20, 25, 1.5, 0.25, 0.25, 0, 0, 50, nil))

	stats, err := repo.GetUserStatsAggregatedWithFilters(ctx, 42, 7, 31, start, end)
	require.NoError(t, err)
	require.Equal(t, int64(3), stats.TotalRequests)
	require.Equal(t, int64(135), stats.TotalTokens)
	require.Nil(t, stats.TotalMeteredCost, "NULL metered cost stays distinct from zero")
	require.NoError(t, mock.ExpectationsWereMet())

	// The group alone takes the next placeholder.
	mock.ExpectQuery(`WHERE user_id = \$1 AND created_at >= \$2 AND created_at < \$3 AND group_id = \$4$`).
		WithArgs(int64(42), start, end, int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{
			"requests", "input", "output", "cache_write", "cache_read", "cache", "cost", "actual_cost", "balance_cost", "subscription_cost", "unclassified_cost", "duration", "metered_cost",
		}).AddRow(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, nil))
	_, err = repo.GetUserStatsAggregatedWithFilters(ctx, 42, 0, 31, start, end)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserDashboardStatsWithFiltersScopesEveryAggregate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		apiKeyID  int64
		where     string
		args      []driver.Value
		tokenExpr string
	}{
		{
			name:      "group",
			where:     `user_id = \$1 AND group_id = \$2`,
			args:      []driver.Value{int64(42), int64(31)},
			tokenExpr: `SUM\(input_tokens \+ output_tokens\), 0\)`,
		},
		{
			name:      "key and group",
			apiKeyID:  7,
			where:     `user_id = \$1 AND api_key_id = \$2 AND group_id = \$3`,
			args:      []driver.Value{int64(42), int64(7), int64(31)},
			tokenExpr: `SUM\(input_tokens \+ output_tokens \+ cache_creation_tokens \+ cache_read_tokens\), 0\)`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}
			next := `\$` + string(rune('0'+len(tc.args)+1))
			withSince := append(append([]driver.Value{}, tc.args...), sqlmock.AnyArg())

			mock.ExpectQuery(`FROM api_keys WHERE user_id = \$1 AND deleted_at IS NULL`).
				WithArgs(int64(42), service.StatusActive).
				WillReturnRows(sqlmock.NewRows([]string{"total", "active"}).AddRow(4, 3))
			mock.ExpectQuery(`as total_requests[\s\S]*FROM usage_logs\s+WHERE ` + tc.where + `$`).
				WithArgs(tc.args...).
				WillReturnRows(sqlmock.NewRows([]string{"r", "i", "o", "cw", "cr", "c", "ac", "d"}).AddRow(5, 10, 20, 30, 40, 1.0, 0.5, 100))
			mock.ExpectQuery(`as today_requests[\s\S]*FROM usage_logs\s+WHERE ` + tc.where + ` AND created_at >= ` + next + `$`).
				WithArgs(withSince...).
				WillReturnRows(sqlmock.NewRows([]string{"r", "i", "o", "cw", "cr", "c", "ac"}).AddRow(1, 1, 2, 3, 4, 0.1, 0.05))
			mock.ExpectQuery(`SELECT COUNT\(\*\), COALESCE\(` + tc.tokenExpr + ` FROM usage_logs WHERE ` + tc.where + ` AND created_at >= ` + next + `$`).
				WithArgs(withSince...).
				WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens"}).AddRow(10, 500))

			stats, err := repo.GetUserDashboardStatsWithFilters(context.Background(), 42, tc.apiKeyID, 31)
			require.NoError(t, err)
			require.Equal(t, int64(4), stats.TotalAPIKeys)
			require.Equal(t, int64(3), stats.ActiveAPIKeys)
			require.Equal(t, int64(100), stats.TotalTokens)
			require.Equal(t, int64(10), stats.TodayTokens)
			require.Equal(t, int64(2), stats.Rpm)
			require.Equal(t, int64(100), stats.Tpm)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestGetUserAPIKeyUsageBreakdownGroupFilterNarrowsUsageOnly(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	startTime := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	endTime := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`WHERE user_id = \$1 AND created_at >= \$2 AND created_at < \$3 AND group_id = \$4\s+GROUP BY api_key_id[\s\S]*FROM api_keys k\s+LEFT JOIN usage_by_key u ON u.api_key_id = k.id\s+WHERE k.user_id = \$1 AND k.deleted_at IS NULL`).
		WithArgs(int64(42), startTime, endTime, int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{
			"key_count", "requests", "total_tokens", "total_cost", "actual_cost",
		}).AddRow(int64(2), int64(0), int64(0), 0.0, 0.0))
	mock.ExpectQuery(`AND group_id = \$4\s+GROUP BY api_key_id[\s\S]*LIMIT \$5 OFFSET \$6`).
		WithArgs(int64(42), startTime, endTime, int64(31), 20, 20).
		WillReturnRows(sqlmock.NewRows([]string{
			"api_key_id", "key_name", "status", "last_used_at", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens", "total_cost", "actual_cost",
		}))

	result, err := repo.GetUserAPIKeyUsageBreakdown(
		context.Background(),
		42,
		31,
		startTime,
		endTime,
		pagination.PaginationParams{Page: 2, PageSize: 20},
		"actual_cost_desc",
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Total, "zero-usage keys stay in the inventory")
	require.NoError(t, mock.ExpectationsWereMet())
}
