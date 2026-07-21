//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type invitationRegistrationSettingRepo struct {
	values map[string]string
}

func (r *invitationRegistrationSettingRepo) Get(context.Context, string) (*service.Setting, error) {
	panic("unexpected Get call")
}

func (r *invitationRegistrationSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *invitationRegistrationSettingRepo) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (r *invitationRegistrationSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

func (r *invitationRegistrationSettingRepo) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (r *invitationRegistrationSettingRepo) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (r *invitationRegistrationSettingRepo) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func TestInvitationRegistrationAllowsExactlyOneConcurrentSignup(t *testing.T) {
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	emailPattern := fmt.Sprintf("invite-race-%d-%%@example.com", stamp)
	invitationCode := fmt.Sprintf("INVITE-RACE-%d", stamp)

	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM redeem_codes WHERE code = $1", invitationCode)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE email LIKE $1", emailPattern)
	})

	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:     "integration-test-secret",
			ExpireHour: 1,
		},
		Default: config.DefaultConfig{
			UserBalance:     0,
			UserConcurrency: 1,
		},
	}
	settingService := service.NewSettingService(&invitationRegistrationSettingRepo{values: map[string]string{
		service.SettingKeyRegistrationEnabled:   "true",
		service.SettingKeyInvitationCodeEnabled: "true",
		service.SettingKeyDefaultBalance:        "1.00000000",
	}}, cfg)
	userRepo := NewUserRepository(integrationEntClient, integrationDB)
	redeemRepo := NewRedeemCodeRepository(integrationEntClient)
	authService := service.NewAuthService(
		integrationEntClient,
		userRepo,
		redeemRepo,
		nil,
		cfg,
		settingService,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	redeemCode := &service.RedeemCode{
		Code:   invitationCode,
		Type:   service.RedeemTypeInvitation,
		Status: service.StatusUnused,
	}
	require.NoError(t, redeemRepo.Create(ctx, redeemCode))

	const attempts = 6
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			email := fmt.Sprintf("invite-race-%d-%d@example.com", stamp, index)
			_, _, err := authService.RegisterWithVerification(ctx, email, "test-password", "", "", invitationCode)
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		require.ErrorIs(t, err, service.ErrInvitationCodeInvalid)
	}
	require.Equal(t, 1, successes)

	var userCount int
	var totalBalance float64
	require.NoError(t, integrationDB.QueryRowContext(
		ctx,
		"SELECT COUNT(*), COALESCE(SUM(balance), 0) FROM users WHERE email LIKE $1",
		emailPattern,
	).Scan(&userCount, &totalBalance))
	require.Equal(t, 1, userCount)
	require.InDelta(t, 1, totalBalance, 0.00000001)

	usedCode, err := redeemRepo.GetByCode(ctx, invitationCode)
	require.NoError(t, err)
	require.Equal(t, service.StatusUsed, usedCode.Status)
	require.NotNil(t, usedCode.UsedBy)
	require.NotNil(t, usedCode.UsedAt)
}
