//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestClaudeDeviceRepositoryListUserDeviceSummariesAggregatesPerGroup(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	user := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("device-summary-%d@example.com", suffix),
	})
	groupOne := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("device-summary-one-%d", suffix)})
	groupTwo := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("device-summary-two-%d", suffix)})

	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM claude_user_devices WHERE user_id=$1`, user.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM user_group_claude_device_quotas WHERE user_id=$1`, user.ID)
		_ = client.Group.DeleteOneID(groupOne.ID).Exec(ctx)
		_ = client.Group.DeleteOneID(groupTwo.ID).Exec(ctx)
		_ = client.User.DeleteOneID(user.ID).Exec(ctx)
	})

	_, err := client.Group.UpdateOneID(groupOne.ID).
		SetClaudeDeviceLimitMode("enforce").
		SetClaudeDeviceBaseLimit(3).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.Group.UpdateOneID(groupTwo.ID).
		SetClaudeDeviceLimitMode("enforce").
		SetClaudeDeviceBaseLimit(2).
		Save(ctx)
	require.NoError(t, err)

	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO user_group_claude_device_quotas (user_id, group_id, bonus_devices)
		VALUES ($1, $2, 1)`, user.ID, groupOne.ID)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err = integrationDB.ExecContext(ctx, `
			INSERT INTO claude_user_devices (user_id, group_id, device_hash)
			VALUES ($1, $2, $3)`, user.ID, groupOne.ID, fmt.Sprintf("%064x", i+1))
		require.NoError(t, err)
	}
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO claude_user_devices (user_id, group_id, device_hash)
		VALUES ($1, $2, $3)`, user.ID, groupTwo.ID, strings.Repeat("b", 64))
	require.NoError(t, err)

	repo := &claudeDeviceRepository{client: client, db: integrationDB}
	summaries, err := repo.ListUserDeviceSummaries(ctx, []int64{user.ID})
	require.NoError(t, err)
	summary, ok := summaries[user.ID]
	require.True(t, ok)
	require.Equal(t, 4, summary.ActiveDevices)
	require.NotNil(t, summary.EffectiveLimit)
	require.Equal(t, 6, *summary.EffectiveLimit)
}
