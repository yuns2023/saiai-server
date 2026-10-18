package repository

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
	"regexp"
	"testing"
)

func TestUsageRiskFilterKeepsIdentityAndPagination(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	where := "user_id = $1 AND " + flaggedUsagePredicate
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM usage_logs WHERE " + where)).WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+usageLogSelectColumns+" FROM usage_logs WHERE "+where+" ORDER BY id DESC LIMIT $2 OFFSET $3")).WithArgs(int64(42), 20, 20).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	logs, _, err := repo.ListWithFilters(context.Background(), pagination.PaginationParams{Page: 2, PageSize: 20}, usagestats.UsageLogFilters{UserID: 42, ModerationFlagged: true, ExactTotal: true})
	require.NoError(t, err)
	require.Empty(t, logs)
	require.NoError(t, mock.ExpectationsWereMet())
}
