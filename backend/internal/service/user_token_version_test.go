//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/stretchr/testify/require"
)

func TestAdminUpdateUserEndsSessionsOnDisableAndPasswordChange(t *testing.T) {
	repo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 7, Status: StatusActive}}}
	svc := &adminServiceImpl{userRepo: repo}

	_, err := svc.UpdateUser(context.Background(), 7, &UpdateUserInput{Status: StatusDisabled})
	require.NoError(t, err)
	saved := repo.updated[len(repo.updated)-1]
	require.True(t, saved.RevokeSessionsOnSave, "disabling ends sessions")
	require.False(t, saved.PasswordChangedOnSave)

	repo.userRepoStub.user = &User{ID: 7, Status: StatusDisabled}
	_, err = svc.UpdateUser(context.Background(), 7, &UpdateUserInput{Status: StatusActive})
	require.NoError(t, err)
	saved = repo.updated[len(repo.updated)-1]
	require.False(t, saved.RevokeSessionsOnSave, "re-enabling does not")

	repo.userRepoStub.user = &User{ID: 7, Status: StatusActive}
	_, err = svc.UpdateUser(context.Background(), 7, &UpdateUserInput{Password: "admin-set-password"})
	require.NoError(t, err)
	saved = repo.updated[len(repo.updated)-1]
	require.True(t, saved.PasswordChangedOnSave, "an admin password change ends sessions")
	require.True(t, saved.CheckPassword("admin-set-password"))

	repo.userRepoStub.user = &User{ID: 7, Status: StatusActive}
	username := "renamed"
	_, err = svc.UpdateUser(context.Background(), 7, &UpdateUserInput{Username: &username})
	require.NoError(t, err)
	saved = repo.updated[len(repo.updated)-1]
	require.False(t, saved.PasswordChangedOnSave)
	require.False(t, saved.RevokeSessionsOnSave)
}

func TestUserChangePasswordEndsSessions(t *testing.T) {
	user := &User{ID: 7, Status: StatusActive}
	require.NoError(t, user.SetPassword("old-password"))
	user.PasswordChangedOnSave = false // as loaded from the database
	repo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: user}}
	svc := &UserService{userRepo: repo}

	require.NoError(t, svc.ChangePassword(context.Background(), 7, ChangePasswordRequest{
		CurrentPassword: "old-password", NewPassword: "new-password",
	}))
	require.Len(t, repo.updated, 1)
	require.True(t, repo.updated[0].PasswordChangedOnSave)
	require.True(t, repo.updated[0].CheckPassword("new-password"))
}

func TestLogoutEverywhereIncrementsTokenVersion(t *testing.T) {
	client := newPaymentTestClient(t)
	ctx := context.Background()
	u, err := client.User.Create().SetEmail("logout@test.com").SetPasswordHash("h").Save(ctx)
	require.NoError(t, err)
	svc := &AuthService{entClient: client}

	require.NoError(t, svc.LogoutEverywhere(ctx, u.ID))
	require.NoError(t, svc.LogoutEverywhere(ctx, u.ID))
	got, err := client.User.Get(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), got.TokenVersion, "each call increments")

	require.ErrorIs(t, svc.LogoutEverywhere(ctx, u.ID+1000), ErrUserNotFound)

	_, err = client.User.UpdateOneID(u.ID).SetDeletedAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, svc.LogoutEverywhere(ctx, u.ID), ErrUserNotFound, "a deleted user is not touched")
	raw, err := client.User.Query().Where(dbuser.IDEQ(u.ID)).Only(mixins.SkipSoftDelete(ctx))
	require.NoError(t, err)
	require.Equal(t, int64(2), raw.TokenVersion)
}

func TestUserUpdateStatusEndsSessionsOnDisable(t *testing.T) {
	repo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 7, Status: StatusActive}}}
	svc := &UserService{userRepo: repo}
	require.NoError(t, svc.UpdateStatus(context.Background(), 7, StatusDisabled))
	require.True(t, repo.updated[0].RevokeSessionsOnSave)
}
