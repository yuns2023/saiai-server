//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyUsageBreakdownOrderByUsesWhitelist(t *testing.T) {
	require.Equal(t, "requests DESC, actual_cost DESC, api_key_id DESC", apiKeyUsageBreakdownOrderBy("requests_desc"))
	require.Equal(t, "total_tokens DESC, actual_cost DESC, api_key_id DESC", apiKeyUsageBreakdownOrderBy("tokens_desc"))
	require.Equal(t, "last_used_at DESC NULLS LAST, api_key_id DESC", apiKeyUsageBreakdownOrderBy("last_used_desc"))
	require.Equal(t, "LOWER(k.name) ASC, api_key_id ASC", apiKeyUsageBreakdownOrderBy("name_asc"))
	require.Equal(
		t,
		"actual_cost DESC, total_tokens DESC, api_key_id DESC",
		apiKeyUsageBreakdownOrderBy("actual_cost_desc; DROP TABLE api_keys"),
	)
}

func TestGetUserAPIKeyUsageBreakdownReturnsPageAndGlobalSummary(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	startTime := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	endTime := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	lastUsedAt := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("WITH usage_by_key AS").
		WithArgs(int64(42), startTime, endTime).
		WillReturnRows(sqlmock.NewRows([]string{
			"key_count", "requests", "total_tokens", "total_cost", "actual_cost",
		}).AddRow(int64(2), int64(10), int64(1000), 2.5, 2.0))

	mock.ExpectQuery("WITH usage_by_key AS").
		WithArgs(int64(42), startTime, endTime, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"api_key_id",
			"key_name",
			"status",
			"last_used_at",
			"requests",
			"input_tokens",
			"output_tokens",
			"cache_creation_tokens",
			"cache_read_tokens",
			"total_tokens",
			"total_cost",
			"actual_cost",
		}).AddRow(int64(7), "build-agent", "active", lastUsedAt, int64(6), int64(300), int64(100), int64(50), int64(50), int64(500), 1.5, 1.0))

	result, err := repo.GetUserAPIKeyUsageBreakdown(
		context.Background(),
		42,
		startTime,
		endTime,
		pagination.PaginationParams{Page: 1, PageSize: 20},
		"actual_cost_desc",
	)

	require.NoError(t, err)
	require.Equal(t, int64(2), result.Total)
	require.Equal(t, int64(10), result.Summary.Requests)
	require.Equal(t, int64(1000), result.Summary.TotalTokens)
	require.Equal(t, 2.0, result.Summary.ActualCost)
	require.Len(t, result.Items, 1)
	require.Equal(t, int64(7), result.Items[0].APIKeyID)
	require.Equal(t, "build-agent", result.Items[0].KeyName)
	require.Equal(t, 0.5, result.Items[0].ActualCostShare)
	require.Equal(t, lastUsedAt, *result.Items[0].LastUsedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}
