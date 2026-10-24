//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func apiKeyRankingRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"api_key_id", "key_name", "user_id", "email", "actual_cost", "requests", "tokens",
		"input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens",
		"total_actual_cost", "total_requests", "total_tokens",
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
			// Hosted rows (metered_cost IS NOT NULL) never count as platform spend.
			query := `(?s)WITH key_spend AS .*WHERE u.created_at >= \$1 AND u.created_at < \$2`
			if tc.userID > 0 {
				query += ` AND u.user_id = \$3`
			}
			query += `\s+AND u.metered_cost IS NULL\s+GROUP BY u.api_key_id, u.user_id.*SUM\(actual_cost\) OVER \(\).*ORDER BY actual_cost DESC, tokens DESC, api_key_id ASC, user_id ASC\s+LIMIT \$`
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
				AddRow(11, "Work", 7, "user@example.com", 12.5, 9, 200, 100, 50, 25, 25, 40.0, 30, 2600).
				AddRow(12, "", 7, "user@example.com", 4.25, 5, 300, 0, 0, 100, 200, 40.0, 30, 2600))

			got, err := repo.GetAPIKeySpendingRanking(context.Background(), start, end, tc.userID, tc.limit)
			require.NoError(t, err)
			require.Len(t, got.Ranking, 2)
			require.Equal(t, int64(11), got.Ranking[0].APIKeyID)
			require.Equal(t, "Work", got.Ranking[0].KeyName)
			require.Equal(t, int64(7), got.Ranking[0].UserID)
			require.Equal(t, int64(100), got.Ranking[0].InputTokens)
			require.Equal(t, int64(50), got.Ranking[0].OutputTokens)
			require.Equal(t, int64(25), got.Ranking[0].CacheCreationTokens)
			require.Equal(t, int64(25), got.Ranking[0].CacheReadTokens)
			require.Equal(t, "", got.Ranking[1].KeyName)
			require.Equal(t, 40.0, got.TotalActualCost) // Includes Keys outside Top N.
			require.Equal(t, int64(30), got.TotalRequests)
			require.Equal(t, int64(2600), got.TotalTokens)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestGetAPIKeySpendingRankingEmpty(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery("WITH key_spend AS").WillReturnRows(apiKeyRankingRows())
	got, err := repo.GetAPIKeySpendingRanking(context.Background(), time.Now(), time.Now(), 0, 12)
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
				query.WillReturnRows(apiKeyRankingRows().AddRow(1, "", 1, "", 1, 1, 1, 0, 0, 0, 1, 1, 1, 1).RowError(0, errors.New("read failed")))
			case "scan":
				query.WillReturnRows(apiKeyRankingRows().AddRow("bad ID", "", 1, "", 1, 1, 1, 0, 0, 0, 1, 1, 1, 1))
			}
			got, err := repo.GetAPIKeySpendingRanking(context.Background(), time.Now(), time.Now(), 0, 12)
			require.Error(t, err)
			require.Nil(t, got)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
