package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestGetUserBreakdownStatsAppliesSelectedUserAndUsageFilters(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	billingType := int8(1)
	dim := usagestats.UserBreakdownDimension{
		UserID: 42, APIKeyID: 7, AccountID: 9, GroupID: 3,
		Model: "claude-opus", ModelFilter: "claude-opus", ModelType: usagestats.ModelSourceRequested,
		BillingType: &billingType,
	}
	query := `(?s)AND ul\.user_id = \$3.*AND ul\.api_key_id = \$4.*AND ul\.account_id = \$5.*AND ul\.group_id = \$6.*AND model = \$7.*AND ul\.model = \$8.*AND ul\.billing_type = \$9`
	mock.ExpectQuery(query).
		WithArgs(start, end, int64(42), int64(7), int64(9), int64(3), "claude-opus", "claude-opus", int16(1)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "email", "requests", "total_tokens", "cost", "actual_cost"}).
			AddRow(int64(42), "selected@example.com", int64(2), int64(100), 1.0, 0.8))

	users, err := repo.GetUserBreakdownStats(context.Background(), start, end, dim, 50)
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Equal(t, int64(42), users[0].UserID)
	require.NoError(t, mock.ExpectationsWereMet())
}
