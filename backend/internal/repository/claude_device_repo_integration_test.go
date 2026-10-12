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

// Test-only reversible encoding for exercising repository encryption plumbing.
type deviceDisplayTestEncryptor struct{}

func (deviceDisplayTestEncryptor) Encrypt(s string) (string, error) { return "test:" + s, nil }
func (deviceDisplayTestEncryptor) Decrypt(s string) (string, error) {
	return strings.TrimPrefix(s, "test:"), nil
}

func TestClaudeDeviceRepositoryClientHints(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	group := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("device-hints-%d", time.Now().UnixNano())})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM claude_user_devices WHERE user_id=$1`, user.ID)
		_ = client.APIKey.DeleteOneID(key.ID).Exec(ctx)
		_ = client.Group.DeleteOneID(group.ID).Exec(ctx)
		_ = client.User.DeleteOneID(user.ID).Exec(ctx)
	})
	repo := &claudeDeviceRepository{client: client, db: integrationDB, encryptor: deviceDisplayTestEncryptor{}}
	hash := strings.Repeat("a", 64)
	hints := service.ClientHints{UserAgent: "claude-cli/2.1.296 (external, cli)", OS: "Windows", Arch: "x64", Runtime: "node", RuntimeVersion: "v26.3.0", SDKVersion: "0.128.0"}
	register := func(h service.ClientHints) *service.ClaudeDeviceRegistrationResult {
		result, err := repo.CheckAndRegister(ctx, user.ID, group.ID, key.ID, hash, "test-device", "enforce", 1, h)
		require.NoError(t, err)
		require.True(t, result.Allowed)
		return result
	}
	require.True(t, register(hints).Registered)
	require.True(t, register(service.ClientHints{SDKVersion: "0.129.0"}).Existing)
	require.True(t, register(service.ClientHints{}).Existing)
	check := func() []service.ClaudeUserDevice {
		devices, err := repo.ListUserDevices(ctx, user.ID, &group.ID)
		require.NoError(t, err)
		require.Len(t, devices, 1)
		require.Equal(t, "test-device", devices[0].DeviceID)
		return devices
	}
	hints.SDKVersion = "0.129.0"
	require.Equal(t, hints, check()[0].ClientHints)
	// Legacy registrations are filled on their next request without consuming a slot.
	_, err := integrationDB.ExecContext(ctx, `UPDATE claude_user_devices SET client_hints='{}', device_id_encrypted=NULL WHERE user_id=$1`, user.ID)
	require.NoError(t, err)
	require.True(t, register(hints).Existing)
	devices := check()
	require.Equal(t, hints, devices[0].ClientHints)
	require.NoError(t, repo.RevokeUserDevice(ctx, user.ID, devices[0].ID))
	require.True(t, register(service.ClientHints{OS: "Linux"}).Registered)
	hints.OS = "Linux"
	require.Equal(t, hints, check()[0].ClientHints)
}
