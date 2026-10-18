package repository

import (
	"context"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAccountStatsCalendarDays(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		start, end time.Time
		want       int
	}{
		{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), 30},
		{time.Date(2026, 10, 17, 18, 13, 0, 0, time.UTC), time.Date(2026, 10, 18, 18, 13, 0, 0, time.UTC), 2},
		{time.Date(2026, 3, 8, 0, 0, 0, 0, loc), time.Date(2026, 3, 9, 0, 0, 0, 0, loc), 1},
		{time.Date(2026, 11, 1, 0, 0, 0, 0, loc), time.Date(2026, 11, 2, 0, 0, 0, 0, loc), 1},
	} {
		if got := accountStatsCalendarDays(tc.start, tc.end); got != tc.want {
			t.Errorf("%s - %s: got %d, want %d", tc.start, tc.end, got, tc.want)
		}
	}
}

func TestAccountStatsRangeSummary(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprint(populated), func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}
			loc, err := time.LoadLocation("Asia/Shanghai")
			require.NoError(t, err)
			start := time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
			end := start.AddDate(0, 0, 30)
			rows := sqlmock.NewRows([]string{"date", "requests", "tokens", "cost", "actual_cost", "user_cost"})
			if populated {
				rows.AddRow("2026-10-17", 2, 100, 3.0, 2.0, 1.0)
			}
			mock.ExpectQuery(`TO_CHAR\(created_at AT TIME ZONE \$4, 'YYYY-MM-DD'\).*created_at >= \$2 AND created_at < \$3`).WithArgs(int64(1), start, end, "Asia/Shanghai").WillReturnRows(rows)
			mock.ExpectQuery(`SELECT COALESCE\(AVG\(duration_ms\)`).WithArgs(int64(1), start, end).WillReturnRows(sqlmock.NewRows([]string{"duration"}).AddRow(1200))
			// Model and endpoint distributions use the same bounds and return no rows here.
			for i := 0; i < 3; i++ {
				mock.ExpectQuery(`SELECT`).WillReturnRows(sqlmock.NewRows([]string{"unused"}))
			}
			result, err := repo.GetAccountUsageStats(context.Background(), 1, start, end)
			require.NoError(t, err)
			require.Equal(t, 30, result.Summary.Days)
			if populated {
				require.Equal(t, 1, result.Summary.ActualDaysUsed)
				require.Equal(t, 2.0, result.Summary.AvgDailyCost)
				require.Equal(t, 1.0, result.Summary.AvgDailyUserCost)
			} else {
				require.Zero(t, result.Summary.ActualDaysUsed)
				require.Zero(t, result.Summary.AvgDailyCost)
				require.Zero(t, result.Summary.AvgDailyTokens)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
