//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type fakeUserInviteRepo struct {
	eligible       bool
	unused         int
	invited        int
	codes          []UserInviteCode
	createdLimits  *UserInviteLimits
	createErr      error
	revokedOwner   int64
	revokedCode    string
	createAttempts int
}

func (f *fakeUserInviteRepo) HasQualifyingTopUp(context.Context, int64) (bool, error) {
	return f.eligible, nil
}
func (f *fakeUserInviteRepo) Counts(context.Context, int64) (int, int, error) {
	return f.unused, f.invited, nil
}
func (f *fakeUserInviteRepo) ListByOwner(_ context.Context, _ int64, params pagination.PaginationParams) ([]UserInviteCode, *pagination.PaginationResult, error) {
	out := append([]UserInviteCode(nil), f.codes...)
	return out, &pagination.PaginationResult{Total: int64(len(out)), Page: params.Page, PageSize: params.PageSize}, nil
}
func (f *fakeUserInviteRepo) CreateForOwner(_ context.Context, _ int64, code string, limits UserInviteLimits) (*UserInviteCode, error) {
	f.createAttempts++
	f.createdLimits = &limits
	if f.createErr != nil {
		err := f.createErr
		if err == ErrUserInviteCodeTaken {
			f.createErr = nil
		}
		return nil, err
	}
	return &UserInviteCode{Code: code, Status: StatusUnused, CreatedAt: time.Now()}, nil
}
func (f *fakeUserInviteRepo) RevokeByOwner(_ context.Context, owner int64, code string) error {
	f.revokedOwner, f.revokedCode = owner, code
	return nil
}
func (f *fakeUserInviteRepo) InviterRefs(context.Context, []int64) (map[int64]InviterRef, error) {
	return map[int64]InviterRef{}, nil
}

func newUserInviteServiceForTest(values map[string]string, repo *fakeUserInviteRepo) *UserInviteService {
	return NewUserInviteService(repo, NewSettingService(&settingPublicRepoStub{values: values}, &config.Config{}))
}

func TestUserInviteService_CreateRules(t *testing.T) {
	repo := &fakeUserInviteRepo{eligible: true}
	svc := newUserInviteServiceForTest(map[string]string{}, repo)
	_, err := svc.Create(context.Background(), 7)
	require.ErrorIs(t, err, ErrUserInviteDisabled, "off by default")

	svc = newUserInviteServiceForTest(map[string]string{SettingKeyUserInviteEnabled: "true"}, repo)
	repo.eligible = false
	_, err = svc.Create(context.Background(), 7)
	require.ErrorIs(t, err, ErrUserInviteNotEligible)

	repo.eligible = true
	code, err := svc.Create(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, code.Code, 32)
	require.Equal(t, UserInviteLimits{MaxUnused: DefaultUserInviteMaxUnused, MaxTotal: 0}, *repo.createdLimits)

	// One retry on a code collision, then success.
	repo.createErr, repo.createAttempts = ErrUserInviteCodeTaken, 0
	_, err = svc.Create(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, 2, repo.createAttempts)
}

func TestUserInviteService_OverviewMasksAndComputesCanCreate(t *testing.T) {
	now := time.Now()
	repo := &fakeUserInviteRepo{
		eligible: true, unused: 2, invited: 3,
		codes: []UserInviteCode{
			{Code: "a", Status: StatusUsed, UsedAt: &now, InviteeEmail: "alice@qq.com"},
			{Code: "b", Status: StatusUsed, InviteeEmail: "gh-1@github-connect.invalid"},
			{Code: "c", Status: StatusUnused},
		},
	}
	svc := newUserInviteServiceForTest(map[string]string{
		SettingKeyUserInviteEnabled:   "true",
		SettingKeyUserInviteMaxUnused: "2",
		SettingKeyUserInviteMaxTotal:  "10",
	}, repo)
	out, err := svc.Overview(context.Background(), 7, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.True(t, out.Enabled)
	require.False(t, out.CanCreate, "already holds max_unused")
	require.Equal(t, "a***@qq.com", out.Codes[0].InviteeEmail)
	require.Equal(t, "***@github-connect.invalid", out.Codes[1].InviteeEmail)
	require.Empty(t, out.Codes[2].InviteeEmail)

	repo.unused = 1
	out, err = svc.Overview(context.Background(), 7, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.True(t, out.CanCreate)

	repo.invited = 10
	out, err = svc.Overview(context.Background(), 7, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.False(t, out.CanCreate, "lifetime cap reached")
}

func TestUserInviteService_RevokeValidatesInput(t *testing.T) {
	repo := &fakeUserInviteRepo{}
	svc := newUserInviteServiceForTest(map[string]string{}, repo)
	require.ErrorIs(t, svc.Revoke(context.Background(), 7, "  "), ErrUserInviteNotFound)
	require.ErrorIs(t, svc.Revoke(context.Background(), 7, string(make([]byte, 33))), ErrUserInviteNotFound)
	// Revoking still works after the switch is turned off (owner cleans up handed-out codes).
	require.NoError(t, svc.Revoke(context.Background(), 7, " abc "))
	require.Equal(t, int64(7), repo.revokedOwner)
	require.Equal(t, "abc", repo.revokedCode)
}

func TestParseUserInviteLimits(t *testing.T) {
	for raw, want := range map[string]int{"": 5, "x": 5, "0": 5, "-3": 5, "7": 7, "101": 100, " 9 ": 9} {
		require.Equal(t, want, parseUserInviteMaxUnused(raw), raw)
	}
	for raw, want := range map[string]int{"": 0, "x": 0, "-1": 0, "50": 50, "999999": MaxUserInviteMaxTotal} {
		require.Equal(t, want, parseUserInviteMaxTotal(raw), raw)
	}
}

func TestMaskInviteeEmail(t *testing.T) {
	require.Equal(t, "a***@b.c", maskInviteeEmail("a@b.c"))
	require.Equal(t, "张***@qq.com", maskInviteeEmail("张三@qq.com"))
	require.Equal(t, "***", maskInviteeEmail("@nolocal"))
	require.Equal(t, "***", maskInviteeEmail("nodomain"))
}
