//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

	"github.com/stretchr/testify/require"
)

type balanceUserRepoStub struct {
	*userRepoStub
	// getCalls counts GetByID; failGetAfter makes calls beyond it fail.
	getCalls     int
	failGetAfter int
	updateErr    error
	updated      []*User
	// stored is the balance in the "database"; it can differ from
	// userRepoStub.user.Balance to model a charge that landed after the read.
	stored      *float64
	adjustCalls int
	// fixedAdjust, when set, is returned as is (models numeric results that
	// float arithmetic cannot reproduce).
	fixedAdjust *BalanceAdjustment
}

func (s *balanceUserRepoStub) GetByID(ctx context.Context, id int64) (*User, error) {
	s.getCalls++
	if s.failGetAfter > 0 && s.getCalls > s.failGetAfter {
		return nil, errors.New("db down")
	}
	return s.userRepoStub.GetByID(ctx, id)
}

// AdjustBalance mirrors the repository contract: compute from the stored
// balance, reject a negative result, return the locked before/after values.
func (s *balanceUserRepoStub) AdjustBalance(ctx context.Context, id int64, op string, amount float64) (BalanceAdjustment, error) {
	s.adjustCalls++
	if s.fixedAdjust != nil {
		return *s.fixedAdjust, nil
	}
	if s.stored == nil {
		v := s.userRepoStub.user.Balance
		s.stored = &v
	}
	old := *s.stored
	next := old
	switch op {
	case BalanceOpSet:
		next = amount
	case BalanceOpAdd:
		next = old + amount
	case BalanceOpSubtract:
		next = old - amount
	}
	if next < 0 {
		return BalanceAdjustment{}, NewNegativeBalanceError(old, next)
	}
	*s.stored = next
	return BalanceAdjustment{OldBalance: old, NewBalance: next, Delta: next - old, Changed: next != old}, nil
}

func (s *balanceUserRepoStub) Update(ctx context.Context, user *User) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	if user == nil {
		return nil
	}
	clone := *user
	s.updated = append(s.updated, &clone)
	if s.userRepoStub != nil {
		s.userRepoStub.user = &clone
	}
	return nil
}

type balanceRedeemRepoStub struct {
	*redeemRepoStub
	created []*RedeemCode
}

func (s *balanceRedeemRepoStub) Create(ctx context.Context, code *RedeemCode) error {
	if code == nil {
		return nil
	}
	clone := *code
	s.created = append(s.created, &clone)
	return nil
}

type authCacheInvalidatorStub struct {
	userIDs  []int64
	groupIDs []int64
	keys     []string
}

func (s *authCacheInvalidatorStub) InvalidateAuthCacheByKey(ctx context.Context, key string) {
	s.keys = append(s.keys, key)
}

func (s *authCacheInvalidatorStub) InvalidateAuthCacheByUserID(ctx context.Context, userID int64) {
	s.userIDs = append(s.userIDs, userID)
}

func (s *authCacheInvalidatorStub) InvalidateAuthCacheByGroupID(ctx context.Context, groupID int64) {
	s.groupIDs = append(s.groupIDs, groupID)
}

func TestAdminService_UpdateUserBalance_InvalidatesAuthCache(t *testing.T) {
	baseRepo := &userRepoStub{user: &User{ID: 7, Balance: 10}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       redeemRepo,
		authCacheInvalidator: invalidator,
	}

	_, err := svc.UpdateUserBalance(context.Background(), 7, 5, "add", "")
	require.NoError(t, err)
	require.Equal(t, []int64{7}, invalidator.userIDs)
	require.Len(t, redeemRepo.created, 1)
}

func TestAdminService_UpdateUserBalance_NoChangeNoInvalidate(t *testing.T) {
	baseRepo := &userRepoStub{user: &User{ID: 7, Balance: 10}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{
		userRepo:             repo,
		redeemCodeRepo:       redeemRepo,
		authCacheInvalidator: invalidator,
	}

	_, err := svc.UpdateUserBalance(context.Background(), 7, 10, "set", "")
	require.NoError(t, err)
	require.Empty(t, invalidator.userIDs)
	require.Empty(t, redeemRepo.created)
}

func TestAdminService_UpdateUserPaygDiscount_InvalidatesAuthCache(t *testing.T) {
	one := 1.0
	discount := 0.75
	baseRepo := &userRepoStub{user: &User{ID: 7, Role: RoleUser, Status: StatusActive, PaygDiscountMultiplier: &one}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: repo, authCacheInvalidator: invalidator}

	updated, err := svc.UpdateUser(context.Background(), 7, &UpdateUserInput{PaygDiscountMultiplier: &discount})
	require.NoError(t, err)
	require.Equal(t, discount, updated.PaygDiscountRate())
	require.Equal(t, []int64{7}, invalidator.userIDs)
}

func TestAdminService_UpdateUserPaygDiscount_RejectsInvalidValue(t *testing.T) {
	one := 1.0
	invalid := 0.12345
	baseRepo := &userRepoStub{user: &User{ID: 7, Role: RoleUser, Status: StatusActive, PaygDiscountMultiplier: &one}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	svc := &adminServiceImpl{userRepo: repo}

	_, err := svc.UpdateUser(context.Background(), 7, &UpdateUserInput{PaygDiscountMultiplier: &invalid})
	require.Error(t, err)
	require.Empty(t, repo.updated)
}

func newBalanceTestService(balance float64) (*adminServiceImpl, *balanceUserRepoStub, *balanceRedeemRepoStub) {
	repo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 7, Role: RoleUser, Status: StatusActive, Balance: balance, Concurrency: 5}}}
	redeemRepo := &balanceRedeemRepoStub{redeemRepoStub: &redeemRepoStub{}}
	return &adminServiceImpl{userRepo: repo, redeemCodeRepo: redeemRepo, authCacheInvalidator: &authCacheInvalidatorStub{}}, repo, redeemRepo
}

func TestAdminService_UpdateUserBalance_KeepsConcurrentCharge(t *testing.T) {
	// The admin page read 10; a 3.00 charge landed before the adjustment.
	svc, repo, redeemRepo := newBalanceTestService(10)
	stored := 7.0
	repo.stored = &stored

	_, err := svc.UpdateUserBalance(context.Background(), 7, 5, BalanceOpAdd, "top-up")
	require.NoError(t, err)
	require.Equal(t, 12.0, *repo.stored, "the charge must survive the top-up")
	require.Empty(t, repo.updated, "balance must not go through the whole-row Update")
	require.Len(t, redeemRepo.created, 1)
	require.Equal(t, 5.0, redeemRepo.created[0].Value, "record is exactly this adjustment")
	require.Equal(t, "top-up", redeemRepo.created[0].Notes)
}

func TestAdminService_UpdateUserBalance_SetRecordsLockedDifference(t *testing.T) {
	svc, repo, redeemRepo := newBalanceTestService(10)
	stored := 7.0
	repo.stored = &stored

	_, err := svc.UpdateUserBalance(context.Background(), 7, 20, BalanceOpSet, "")
	require.NoError(t, err)
	require.Equal(t, 20.0, *repo.stored)
	require.Len(t, redeemRepo.created, 1)
	require.Equal(t, 13.0, redeemRepo.created[0].Value)
}

func TestAdminService_UpdateUserBalance_RejectsNegativeResult(t *testing.T) {
	// Billing may overdraw; even "add" must not leave the balance negative.
	svc, repo, redeemRepo := newBalanceTestService(-10)

	_, err := svc.UpdateUserBalance(context.Background(), 7, 5, BalanceOpAdd, "")
	require.Error(t, err)
	require.Equal(t, 400, infraerrors.Code(err))
	require.Equal(t, -10.0, *repo.stored)
	require.Empty(t, redeemRepo.created)
}

func TestAdminService_UpdateUserBalance_RejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		amount float64
		op     string
	}{
		{"unknown op", 5, "multiply"},
		{"empty op", 5, ""},
		{"NaN", math.NaN(), BalanceOpAdd},
		{"+Inf", math.Inf(1), BalanceOpSet},
		{"-Inf", math.Inf(-1), BalanceOpSubtract},
		{"negative", -1, BalanceOpAdd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, redeemRepo := newBalanceTestService(10)
			_, err := svc.UpdateUserBalance(context.Background(), 7, tc.amount, tc.op, "")
			require.Error(t, err)
			require.Equal(t, 400, infraerrors.Code(err))
			require.Zero(t, repo.adjustCalls)
			require.Empty(t, redeemRepo.created)
		})
	}
}

func TestAdminService_UpdateUser_UnchangedConcurrencyIsNotWritten(t *testing.T) {
	svc, repo, redeemRepo := newBalanceTestService(10)
	same := 5
	notes := "vip"

	_, err := svc.UpdateUser(context.Background(), 7, &UpdateUserInput{Concurrency: &same, Notes: &notes})
	require.NoError(t, err)
	require.Len(t, repo.updated, 1)
	require.False(t, repo.updated[0].ConcurrencyChangedOnSave)
	require.Empty(t, redeemRepo.created)
}

func TestAdminService_UpdateUser_ChangedConcurrencyIsWrittenAndRecorded(t *testing.T) {
	svc, repo, redeemRepo := newBalanceTestService(10)
	next := 8

	_, err := svc.UpdateUser(context.Background(), 7, &UpdateUserInput{Concurrency: &next})
	require.NoError(t, err)
	require.Len(t, repo.updated, 1)
	require.True(t, repo.updated[0].ConcurrencyChangedOnSave)
	require.Equal(t, 8, repo.updated[0].Concurrency)
	require.Len(t, redeemRepo.created, 1)
	require.Equal(t, 3.0, redeemRepo.created[0].Value)
}

// Once the adjustment committed, a failed refresh must not fail the request:
// an idempotent retry would apply the adjustment twice.
func TestAdminService_UpdateUserBalance_RefreshFailureStillSucceeds(t *testing.T) {
	svc, repo, redeemRepo := newBalanceTestService(10)
	repo.failGetAfter = 1
	invalidator := svc.authCacheInvalidator.(*authCacheInvalidatorStub)

	user, err := svc.UpdateUserBalance(context.Background(), 7, 5, BalanceOpAdd, "")
	require.NoError(t, err)
	require.Equal(t, 15.0, user.Balance)
	require.Equal(t, 1, repo.adjustCalls)
	require.Len(t, redeemRepo.created, 1)
	require.Equal(t, []int64{7}, invalidator.userIDs)
}

// On a large balance a real change can look like none in float64; the
// service must trust the database's Changed/Delta.
func TestAdminService_UpdateUserBalance_UsesDatabaseDelta(t *testing.T) {
	svc, repo, redeemRepo := newBalanceTestService(1e9)
	repo.fixedAdjust = &BalanceAdjustment{OldBalance: 1e9, NewBalance: 1e9, Delta: 1e-8, Changed: true}
	invalidator := svc.authCacheInvalidator.(*authCacheInvalidatorStub)

	_, err := svc.UpdateUserBalance(context.Background(), 7, 0.00000001, BalanceOpAdd, "")
	require.NoError(t, err)
	require.Equal(t, []int64{7}, invalidator.userIDs)
	require.Len(t, redeemRepo.created, 1)
	require.Equal(t, 1e-8, redeemRepo.created[0].Value)
}

func TestAdminService_UpdateUserBalance_RoundedToNoChange(t *testing.T) {
	svc, repo, redeemRepo := newBalanceTestService(1)
	repo.fixedAdjust = &BalanceAdjustment{OldBalance: 1, NewBalance: 1, Delta: 0, Changed: false}
	invalidator := svc.authCacheInvalidator.(*authCacheInvalidatorStub)

	_, err := svc.UpdateUserBalance(context.Background(), 7, 0.000000001, BalanceOpAdd, "")
	require.NoError(t, err)
	require.Empty(t, invalidator.userIDs)
	require.Empty(t, redeemRepo.created)
}
