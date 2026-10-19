//go:build integration

package repository

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/migrations"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestClaudeDeviceRepositoryListUserDeviceSummariesUsesUserCapacity(t *testing.T) {
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
		INSERT INTO user_claude_device_quotas (user_id, bonus_devices)
		VALUES ($1, 1)`, user.ID)
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
	require.Equal(t, 4, *summary.EffectiveLimit)
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

func TestClaudeDeviceUserCapacityAcrossGroups(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	g1 := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("user-cap-one-%d", time.Now().UnixNano())})
	g2 := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("user-cap-two-%d", time.Now().UnixNano())})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM claude_user_devices WHERE user_id=$1`, user.ID)
		_ = client.APIKey.DeleteOneID(key.ID).Exec(ctx)
		_ = client.Group.DeleteOneID(g1.ID).Exec(ctx)
		_ = client.Group.DeleteOneID(g2.ID).Exec(ctx)
		_ = client.User.DeleteOneID(user.ID).Exec(ctx)
	})
	repo := &claudeDeviceRepository{client: client, db: integrationDB, encryptor: deviceDisplayTestEncryptor{}}
	summaries, err := repo.ListUserDeviceSummaries(ctx, []int64{user.ID})
	require.NoError(t, err)
	require.Equal(t, 3, *summaries[user.ID].EffectiveLimit)
	reg := func(g int64, n int) *service.ClaudeDeviceRegistrationResult {
		result, err := repo.CheckAndRegister(ctx, user.ID, g, key.ID, fmt.Sprintf("%064x", n), fmt.Sprintf("device-%d", n), "enforce", 99, service.ClientHints{})
		require.NoError(t, err)
		return result
	}
	require.True(t, reg(g1.ID, 1).Registered)
	require.True(t, reg(g2.ID, 1).Existing)
	// Legacy duplicate rows remain historical records, but use only one slot.
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO claude_user_devices(user_id,group_id,device_hash) VALUES($1,$2,$3)`, user.ID, g2.ID, fmt.Sprintf("%064x", 1))
	require.NoError(t, err)
	require.True(t, reg(g2.ID, 2).Allowed)
	require.True(t, reg(g1.ID, 3).Allowed)
	require.False(t, reg(g2.ID, 4).Allowed)
	devices, err := repo.ListUserDevices(ctx, user.ID, nil)
	require.NoError(t, err)
	require.Len(t, devices, 3)
	require.NoError(t, repo.RevokeUserDevice(ctx, user.ID, devices[0].ID))
	require.True(t, reg(g2.ID, 4).Allowed)
	require.NoError(t, repo.AddBonusDevices(ctx, user.ID, 0, 1))
	require.True(t, reg(g1.ID, 5).Allowed)
	_, err = client.User.UpdateOneID(user.ID).SetClaudeDeviceBaseLimit(1).Save(ctx)
	require.NoError(t, err)
	require.True(t, reg(g2.ID, 5).Existing)
	require.False(t, reg(g2.ID, 6).Allowed)
}

func TestCarpoolUserRelationRoundTrip(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	repo := NewAccountRepository(client, integrationDB, nil)
	account := &service.Account{Name: "allowlist-test", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive, CarpoolUserIDs: []int64{user.ID}, Extra: map[string]any{"claude_oauth_carpool_users_only": true}}
	require.NoError(t, repo.Create(ctx, account))
	t.Cleanup(func() {
		_ = client.Account.DeleteOneID(account.ID).Exec(ctx)
		_ = client.User.DeleteOneID(user.ID).Exec(ctx)
	})
	saved, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{user.ID}, saved.CarpoolUserIDs)
	saved.CarpoolUserIDs = []int64{}
	require.NoError(t, repo.Update(ctx, saved))
	saved, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Empty(t, saved.CarpoolUserIDs)
}

func TestClaudeDeviceMigrationPreservesBonusesOnce(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `CREATE SCHEMA device_migration_test; SET LOCAL search_path=device_migration_test;
 CREATE TABLE users(id BIGINT PRIMARY KEY); INSERT INTO users VALUES(1),(2);
 CREATE TABLE claude_user_devices(user_id BIGINT,device_hash TEXT,revoked_at TIMESTAMPTZ);
 CREATE TABLE user_group_claude_device_quotas(user_id BIGINT,group_id BIGINT,bonus_devices INT);
 INSERT INTO user_group_claude_device_quotas VALUES(1,10,2),(1,20,3);`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("106_user_claude_device_capacity.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	var base, bonus int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT u.claude_device_base_limit,q.bonus_devices FROM users u JOIN user_claude_device_quotas q ON q.user_id=u.id WHERE u.id=1`).Scan(&base, &bonus))
	require.Equal(t, 3, base)
	require.Equal(t, 5, bonus)
}

func TestClaudeDeviceConcurrentAdmissionAcrossGroups(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	g1 := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("parallel-one-%d", time.Now().UnixNano())})
	g2 := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("parallel-two-%d", time.Now().UnixNano())})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM claude_user_devices WHERE user_id=$1`, user.ID)
		_ = client.APIKey.DeleteOneID(key.ID).Exec(ctx)
		_ = client.Group.DeleteOneID(g1.ID).Exec(ctx)
		_ = client.Group.DeleteOneID(g2.ID).Exec(ctx)
		_ = client.User.DeleteOneID(user.ID).Exec(ctx)
	})
	repo := &claudeDeviceRepository{client: client, db: integrationDB, encryptor: deviceDisplayTestEncryptor{}}
	for n := 1; n <= 2; n++ {
		result, err := repo.CheckAndRegister(ctx, user.ID, g1.ID, key.ID, fmt.Sprintf("%064x", n), "device", "enforce", 3, service.ClientHints{})
		require.NoError(t, err)
		require.True(t, result.Allowed)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan *service.ClaudeDeviceRegistrationResult, 2)
	errs := make(chan error, 2)
	for i, g := range []int64{g1.ID, g2.ID} {
		wg.Add(1)
		go func(n int, group int64) {
			defer wg.Done()
			<-start
			result, err := repo.CheckAndRegister(ctx, user.ID, group, key.ID, fmt.Sprintf("%064x", n+3), "new-device", "enforce", 99, service.ClientHints{})
			results <- result
			errs <- err
		}(i, g)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	allowed := 0
	for result := range results {
		if result.Allowed {
			allowed++
		}
	}
	require.Equal(t, 1, allowed)
}
