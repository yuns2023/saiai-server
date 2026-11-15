//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func userInviteFixture(t *testing.T, tag string) (context.Context, service.UserInviteRepository, *service.User) {
	t.Helper()
	ctx := context.Background()
	owner := mustCreateUser(t, integrationEntClient, &service.User{
		Email: fmt.Sprintf("invite-owner-%s-%d@example.com", tag, time.Now().UnixNano()),
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM redeem_codes WHERE owner_user_id = $1 OR used_by = $1", owner.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM payment_orders WHERE user_id = $1", owner.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = $1 OR invited_by_user_id = $1", owner.ID)
	})
	return ctx, NewUserInviteRepository(integrationEntClient), owner
}

func usedCode(t *testing.T, userID int64, typ string, value float64) {
	t.Helper()
	now := time.Now()
	mustCreateRedeemCode(t, integrationEntClient, &service.RedeemCode{
		Code:   fmt.Sprintf("T%d", time.Now().UnixNano()),
		Type:   typ,
		Value:  value,
		Status: service.StatusUsed,
		UsedBy: &userID,
		UsedAt: &now,
	})
}

func TestUserInviteRepo_Eligibility(t *testing.T) {
	ctx, repo, owner := userInviteFixture(t, "elig")

	ok, err := repo.HasQualifyingTopUp(ctx, owner.ID)
	require.NoError(t, err)
	require.False(t, ok, "new user")

	usedCode(t, owner.ID, service.RedeemTypeInvitation, 0)
	usedCode(t, owner.ID, domain.AdjustmentTypeAdminBalance, 50)
	usedCode(t, owner.ID, service.RedeemTypeBalance, -5)
	ok, err = repo.HasQualifyingTopUp(ctx, owner.ID)
	require.NoError(t, err)
	require.False(t, ok, "sign-up invitation, admin top-up and a deduction do not count")

	usedCode(t, owner.ID, service.RedeemTypeConcurrency, 1)
	ok, err = repo.HasQualifyingTopUp(ctx, owner.ID)
	require.NoError(t, err)
	require.True(t, ok, "self-redeemed concurrency code")
}

func TestUserInviteRepo_EligibilityByCompletedPayment(t *testing.T) {
	ctx, repo, owner := userInviteFixture(t, "pay")
	stamp := time.Now().UnixNano()
	provider, err := integrationEntClient.PaymentProviderInstance.Create().
		SetProviderKey("test").
		SetName(fmt.Sprintf("invite-test-%d", stamp)).
		SetConfigEncrypted("x").
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM payment_orders WHERE provider_instance_id = $1", provider.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM payment_provider_instances WHERE id = $1", provider.ID)
	})
	create := func(status string, n int) {
		_, err := integrationEntClient.PaymentOrder.Create().
			SetUserID(owner.ID).
			SetUserEmail(owner.Email).
			SetAmount(10).
			SetPayAmount(10).
			SetRechargeCode(fmt.Sprintf("P%d-%d", stamp, n)).
			SetOutTradeNo(fmt.Sprintf("O%d-%d", stamp, n)).
			SetPaymentType("alipay").
			SetProviderKey("test").
			SetProviderInstanceID(provider.ID).
			SetProviderSnapshotEncrypted("x").
			SetExpiresAt(time.Now().Add(time.Hour)).
			SetStatus(status).
			Save(ctx)
		require.NoError(t, err)
	}
	create(payment.OrderStatusPending, 1)
	ok, err := repo.HasQualifyingTopUp(ctx, owner.ID)
	require.NoError(t, err)
	require.False(t, ok, "pending order")

	create(payment.OrderStatusCompleted, 2)
	ok, err = repo.HasQualifyingTopUp(ctx, owner.ID)
	require.NoError(t, err)
	require.True(t, ok, "completed order counts even without a redeem record")
}

func TestUserInviteRepo_CreateLimitsAndConcurrency(t *testing.T) {
	ctx, repo, owner := userInviteFixture(t, "limit")
	limits := service.UserInviteLimits{MaxUnused: 3}

	const attempts = 8
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("C%d-%d", owner.ID, i), limits)
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, service.ErrUserInviteUnusedLimit)
	}
	require.Equal(t, 3, ok, "the row lock serializes the unused-count check")

	counts, err := repo.Counts(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, service.UserInviteCounts{Unused: 3, Invited: 0, CreatedToday: 3, CreatedThisWeek: 3}, counts)

	// Lifetime cap counts unused codes too.
	_, err = repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("D%d", owner.ID), service.UserInviteLimits{MaxUnused: 10, MaxTotal: 3})
	require.ErrorIs(t, err, service.ErrUserInviteTotalLimit)

	// Rolling 24h cap survives generate→revoke churn.
	codes, _, err := repo.ListByOwner(ctx, owner.ID, pagination.PaginationParams{Page: 1, PageSize: 50})
	require.NoError(t, err)
	for _, c := range codes {
		require.NoError(t, repo.RevokeByOwner(ctx, owner.ID, c.Code))
	}
	for i := 3; i < service.MaxUserInviteCreatesPerDay; i++ {
		_, err := repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("E%d-%d", owner.ID, i), service.UserInviteLimits{MaxUnused: 100})
		require.NoError(t, err)
	}
	_, err = repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("F%d", owner.ID), service.UserInviteLimits{MaxUnused: 100})
	require.ErrorIs(t, err, service.ErrUserInviteDailyLimit)
}

func TestUserInviteRepo_WeeklyLimitCountsNonRevokedCodesInWindow(t *testing.T) {
	ctx, repo, owner := userInviteFixture(t, "week")
	_, _, other := userInviteFixture(t, "week-other")
	limits := service.UserInviteLimits{MaxUnused: 100, MaxPerWeek: 3}
	create := func(tag string) (*service.UserInviteCode, error) {
		return repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("W%s%d", tag, owner.ID), limits)
	}

	first, err := create("a")
	require.NoError(t, err)
	revoked, err := create("b")
	require.NoError(t, err)
	require.NoError(t, repo.RevokeByOwner(ctx, owner.ID, revoked.Code))
	used, err := create("c")
	require.NoError(t, err)
	// A used code still counts: the cap bounds people invited per week.
	_, err = integrationDB.ExecContext(ctx, "UPDATE redeem_codes SET status = 'used', used_by = $1, used_at = NOW() WHERE code = $2", other.ID, used.Code)
	require.NoError(t, err)
	_, err = create("d")
	require.NoError(t, err)

	counts, err := repo.Counts(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, 3, counts.CreatedThisWeek, "revoked code excluded, unused + used counted")
	require.Equal(t, 4, counts.CreatedToday, "the 24h churn cap still counts revoked codes")

	_, err = create("e")
	require.ErrorIs(t, err, service.ErrUserInviteWeeklyLimit)
	_, err = repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("Wf%d", owner.ID), service.UserInviteLimits{MaxUnused: 100})
	require.NoError(t, err, "0 = unlimited")

	// Codes older than 7 days drop out of the window.
	_, err = integrationDB.ExecContext(ctx, "UPDATE redeem_codes SET created_at = NOW() - INTERVAL '8 days' WHERE code IN ($1, $2)", first.Code, used.Code)
	require.NoError(t, err)
	counts, err = repo.Counts(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, 2, counts.CreatedThisWeek)
	_, err = create("g")
	require.NoError(t, err)
	_, err = create("h")
	require.ErrorIs(t, err, service.ErrUserInviteWeeklyLimit)
}

func TestUserInviteRepo_WeeklyLimitLastSlotUnderConcurrency(t *testing.T) {
	ctx, repo, owner := userInviteFixture(t, "week-race")
	limits := service.UserInviteLimits{MaxUnused: 100, MaxPerWeek: 3}
	for i := 0; i < 2; i++ {
		_, err := repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("X%d-%d", owner.ID, i), limits)
		require.NoError(t, err)
	}

	// One slot left: two concurrent generates, the row lock lets exactly one through.
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("Y%d-%d", owner.ID, i), limits)
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	ok, limited := 0, 0
	for err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, service.ErrUserInviteWeeklyLimit)
		limited++
	}
	require.Equal(t, 1, ok)
	require.Equal(t, 1, limited)
	counts, err := repo.Counts(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, 3, counts.CreatedThisWeek)
}

func TestUserInviteRepo_InactiveOrDeletedOwnerCannotCreate(t *testing.T) {
	ctx, repo, owner := userInviteFixture(t, "inactive")
	_, err := integrationDB.ExecContext(ctx, "UPDATE users SET status = 'disabled' WHERE id = $1", owner.ID)
	require.NoError(t, err)
	_, err = repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("G%d", owner.ID), service.UserInviteLimits{MaxUnused: 5})
	require.ErrorIs(t, err, service.ErrUserNotActive)

	_, err = integrationDB.ExecContext(ctx, "UPDATE users SET status = 'active', deleted_at = NOW() WHERE id = $1", owner.ID)
	require.NoError(t, err)
	_, err = repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("H%d", owner.ID), service.UserInviteLimits{MaxUnused: 5})
	require.ErrorIs(t, err, service.ErrUserNotFound)
}

func TestUserInviteRepo_RevokeOnlyOwnUnused(t *testing.T) {
	ctx, repo, owner := userInviteFixture(t, "revoke")
	_, _, other := userInviteFixture(t, "revoke-other")
	mine, err := repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("R%d", owner.ID), service.UserInviteLimits{MaxUnused: 5})
	require.NoError(t, err)

	require.ErrorIs(t, repo.RevokeByOwner(ctx, other.ID, mine.Code), service.ErrUserInviteNotFound, "someone else's code")

	adminCode := mustCreateRedeemCode(t, integrationEntClient, &service.RedeemCode{
		Code: fmt.Sprintf("A%d", owner.ID), Type: service.RedeemTypeInvitation, Status: service.StatusUnused,
	})
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, "DELETE FROM redeem_codes WHERE id = $1", adminCode.ID) })
	require.ErrorIs(t, repo.RevokeByOwner(ctx, owner.ID, adminCode.Code), service.ErrUserInviteNotFound, "admin code")

	require.NoError(t, repo.RevokeByOwner(ctx, owner.ID, mine.Code))
	require.ErrorIs(t, repo.RevokeByOwner(ctx, owner.ID, mine.Code), service.ErrUserInviteNotFound, "already revoked")

	redeemRepo := NewRedeemCodeRepository(integrationEntClient)
	revoked, err := redeemRepo.GetByCode(ctx, mine.Code)
	require.NoError(t, err)
	require.Equal(t, service.StatusExpired, revoked.Status)
	require.ErrorIs(t, redeemRepo.Use(ctx, revoked.ID, other.ID), service.ErrRedeemCodeUsed, "a revoked code cannot be consumed")
}

func TestUserInviteRegistration_RecordsInviterEndToEnd(t *testing.T) {
	ctx, repo, owner := userInviteFixture(t, "e2e")
	cfg := &config.Config{
		JWT:     config.JWTConfig{Secret: "integration-test-secret", ExpireHour: 1},
		Default: config.DefaultConfig{UserConcurrency: 1},
	}
	settingService := service.NewSettingService(&invitationRegistrationSettingRepo{values: map[string]string{
		service.SettingKeyRegistrationEnabled:   "true",
		service.SettingKeyInvitationCodeEnabled: "true",
	}}, cfg)
	userRepo := NewUserRepository(integrationEntClient, integrationDB)
	redeemRepo := NewRedeemCodeRepository(integrationEntClient)
	authService := service.NewAuthService(integrationEntClient, userRepo, redeemRepo, nil, cfg, settingService, nil, nil, nil, nil, nil)

	code, err := repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("U%d", owner.ID), service.UserInviteLimits{MaxUnused: 5})
	require.NoError(t, err)
	inviteeEmail := fmt.Sprintf("invitee-%d@example.com", owner.ID)
	_, invitee, err := authService.RegisterWithVerification(ctx, inviteeEmail, "test-password", "", "", code.Code)
	require.NoError(t, err)
	require.NotNil(t, invitee.InvitedByUserID)
	require.Equal(t, owner.ID, *invitee.InvitedByUserID)

	stored, err := userRepo.GetByID(ctx, invitee.ID)
	require.NoError(t, err)
	require.Equal(t, owner.ID, *stored.InvitedByUserID)

	// Admin filter "invited by".
	users, _, err := userRepo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, service.UserListFilters{InvitedByUserID: &owner.ID})
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Equal(t, invitee.ID, users[0].ID)

	list, _, err := repo.ListByOwner(ctx, owner.ID, pagination.PaginationParams{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, service.StatusUsed, list[0].Status)
	require.Equal(t, inviteeEmail, list[0].InviteeEmail, "repository returns the raw email; the service masks it")

	// Deleting the used code or soft-deleting the invitee keeps the relationship and the count.
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM redeem_codes WHERE code = $1", code.Code)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE users SET deleted_at = NOW() WHERE id = $1", invitee.ID)
	require.NoError(t, err)
	counts, err := repo.Counts(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, 1, counts.Invited)

	refs, err := repo.InviterRefs(ctx, []int64{owner.ID, invitee.ID})
	require.NoError(t, err)
	require.Equal(t, owner.Email, refs[owner.ID].Email)
	require.False(t, refs[owner.ID].Deleted)
	require.True(t, refs[invitee.ID].Deleted)

	// A disabled owner's codes stop working on every sign-up path (shared lookup).
	second, err := repo.CreateForOwner(ctx, owner.ID, fmt.Sprintf("V%d", owner.ID), service.UserInviteLimits{MaxUnused: 5})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE users SET status = 'disabled' WHERE id = $1", owner.ID)
	require.NoError(t, err)
	_, _, err = authService.RegisterWithVerification(ctx, fmt.Sprintf("invitee2-%d@example.com", owner.ID), "test-password", "", "", second.Code)
	require.ErrorIs(t, err, service.ErrInvitationCodeInvalid)

	// Admin codes record no inviter.
	adminCode := mustCreateRedeemCode(t, integrationEntClient, &service.RedeemCode{
		Code: fmt.Sprintf("W%d", owner.ID), Type: service.RedeemTypeInvitation, Status: service.StatusUnused,
	})
	adminInviteeEmail := fmt.Sprintf("invitee3-%d@example.com", owner.ID)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM redeem_codes WHERE id = $1", adminCode.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE email = $1", adminInviteeEmail)
	})
	_, adminInvitee, err := authService.RegisterWithVerification(ctx, adminInviteeEmail, "test-password", "", "", adminCode.Code)
	require.NoError(t, err)
	require.Nil(t, adminInvitee.InvitedByUserID)
}
