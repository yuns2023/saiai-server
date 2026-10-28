//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The user-scoped wrappers sit on generic queries that drop the user condition
// for userID <= 0; they must refuse before reaching the repository.
func TestUserScopedUsageQueriesRequireUser(t *testing.T) {
	svc := NewUsageService(nil, nil, nil, nil)
	ctx, start, end := context.Background(), time.Now().Add(-time.Hour), time.Now()
	for _, userID := range []int64{0, -1} {
		_, err := svc.GetUserUsageStats(ctx, userID, 0, 31, start, end)
		require.Error(t, err)
		_, err = svc.GetUserGroupDashboardStats(ctx, userID, 0, 31)
		require.Error(t, err)
		_, err = svc.GetUserGroupUsageTrend(ctx, userID, 0, 31, start, end, "day")
		require.Error(t, err)
		_, err = svc.GetUserGroupModelStats(ctx, userID, 0, 31, start, end)
		require.Error(t, err)
		_, err = svc.ListUserUsageGroups(ctx, userID, start, end)
		require.Error(t, err)
	}
}
