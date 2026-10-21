//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSmartSwitchPersistenceBudgetAndConcurrentEdits(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "smart-" + uuid.NewString() + "@example.invalid", PasswordHash: "TEST_ONLY", Balance: 100})
	key := &service.APIKey{UserID: user.ID, Key: "TEST_ONLY_" + uuid.NewString(), Name: "smart", SmartSwitch: &domain.KeySmartSwitchPolicy{Enabled: true, GroupIDs: []int64{1, 2}, PaygConfirmed: true}, Status: service.StatusActive}
	repo := NewAPIKeyRepository(client, integrationDB)
	require.NoError(t, repo.Create(ctx, key))
	first, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	stale, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	first.Name = "new name"
	require.NoError(t, repo.Update(ctx, first))
	require.Equal(t, int64(1), first.SmartSwitchVersion)
	stale.Name = "stale edit"
	require.ErrorIs(t, repo.Update(ctx, stale), service.ErrSmartSwitchConflict)
	loaded, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, loaded.SmartSwitch.GroupIDs)
	require.Equal(t, int64(1), loaded.SmartSwitchVersion)
	account := mustCreateAccount(t, client, &service.Account{Name: "smart-" + uuid.NewString(), Type: service.AccountTypeAPIKey, Platform: service.PlatformAnthropic})
	billing := NewUsageBillingRepository(client, integrationDB)
	command := &service.UsageBillingCommand{RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID, AccountType: account.Type, BalanceCost: 0.25, SmartSwitchPaygCost: 0.25}
	result, err := billing.Apply(ctx, command)
	require.NoError(t, err)
	require.True(t, result.Applied)
	result, err = billing.Apply(ctx, command)
	require.NoError(t, err)
	require.False(t, result.Applied)
	loaded, err = repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, 0.25, loaded.SmartSwitchPaygUsed)
	loaded.SmartSwitch.Enabled = false
	require.NoError(t, repo.Update(ctx, loaded))
	loaded, err = repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, 0.25, loaded.SmartSwitchPaygUsed)
}
