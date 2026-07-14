package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestAppendAccountSwitchWhereConditionDisabled(t *testing.T) {
	t.Parallel()

	conditions, args := appendAccountSwitchWhereCondition(nil, nil, "usage_logs", usagestats.UsageLogFilters{})

	require.Empty(t, conditions)
	require.Empty(t, args)
}

func TestAppendAccountSwitchWhereConditionEnabled(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	conditions, args := appendAccountSwitchWhereCondition(
		[]string{"api_key_id = $1"},
		[]any{int64(249)},
		"usage_logs",
		usagestats.UsageLogFilters{
			StartTime:     &start,
			EndTime:       &end,
			AccountSwitch: true,
		},
	)

	require.Len(t, conditions, 2)
	require.Len(t, args, 3)
	require.Equal(t, int64(249), args[0])
	require.Equal(t, start, args[1])
	require.Equal(t, end, args[2])
	require.Contains(t, conditions[1], "usage_logs.session_id IS NOT NULL")
	require.Contains(t, conditions[1], "sw.session_id = usage_logs.session_id")
	require.Contains(t, conditions[1], "sw.api_key_id = usage_logs.api_key_id")
	require.Contains(t, conditions[1], "COUNT(DISTINCT sw.account_id) > 1")
	require.Contains(t, conditions[1], "sw.created_at >= $2")
	require.Contains(t, conditions[1], "sw.created_at < $3")
}
