package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newUserRepoSQLite(t *testing.T) (*userRepository, *dbent.Client) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	return newUserRepositoryWithSQL(client, db), client
}

func createTokenVersionUser(t *testing.T, repo *userRepository) *service.User {
	t.Helper()
	u := &service.User{Email: "tv@test.com", Role: service.RoleUser, Status: service.StatusActive, Concurrency: 5}
	require.NoError(t, u.SetPassword("old-password"))
	require.NoError(t, repo.Create(context.Background(), u))
	require.False(t, u.PasswordChangedOnSave, "create spends the password intent")
	require.Zero(t, u.TokenVersion)
	return u
}

// Production (pre-124): the version lived only in memory, so a password change
// never ended issued sessions.
func TestUserRepositoryUpdate_PasswordChangeIncrementsTokenVersion(t *testing.T) {
	ctx := context.Background()
	repo, _ := newUserRepoSQLite(t)
	u := createTokenVersionUser(t, repo)

	loaded, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.NoError(t, loaded.SetPassword("new-password"))
	require.NoError(t, repo.Update(ctx, loaded))
	require.Equal(t, int64(1), loaded.TokenVersion)
	require.False(t, loaded.PasswordChangedOnSave)

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.TokenVersion)
	require.True(t, got.CheckPassword("new-password"))
}

// An ordinary save holding an older copy must not restore the old password or
// roll the version back.
func TestUserRepositoryUpdate_OrdinarySaveKeepsPasswordAndVersion(t *testing.T) {
	ctx := context.Background()
	repo, _ := newUserRepoSQLite(t)
	u := createTokenVersionUser(t, repo)

	stale, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	changer, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.NoError(t, changer.SetPassword("new-password"))
	require.NoError(t, repo.Update(ctx, changer))

	stale.Username = "renamed"
	require.NoError(t, repo.Update(ctx, stale))
	require.Equal(t, int64(1), stale.TokenVersion, "the saved object learns the stored version")

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Username)
	require.True(t, got.CheckPassword("new-password"), "old password must not come back")
	require.Equal(t, int64(1), got.TokenVersion)

	got.TokenVersion = 0 // a stray in-memory value is never written
	require.NoError(t, repo.Update(ctx, got))
	again, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), again.TokenVersion)
}

func TestUserRepositoryUpdate_RevokeSessionsIncrementsWithoutPasswordChange(t *testing.T) {
	ctx := context.Background()
	repo, _ := newUserRepoSQLite(t)
	u := createTokenVersionUser(t, repo)

	loaded, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	loaded.Status = service.StatusDisabled
	loaded.RevokeSessionsOnSave = true
	require.NoError(t, repo.Update(ctx, loaded))
	require.False(t, loaded.RevokeSessionsOnSave)

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.TokenVersion)
	require.True(t, got.CheckPassword("old-password"))
}

// The version only ever increments in the database: two saves holding the
// same loaded version both count.
func TestUserRepositoryUpdate_PasswordChangesIncrementAtomically(t *testing.T) {
	ctx := context.Background()
	repo, _ := newUserRepoSQLite(t)
	u := createTokenVersionUser(t, repo)

	first, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	second, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.NoError(t, first.SetPassword("first"))
	require.NoError(t, second.SetPassword("second"))
	require.NoError(t, repo.Update(ctx, first))
	require.NoError(t, repo.Update(ctx, second))

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), got.TokenVersion)
	require.Equal(t, int64(2), second.TokenVersion)
}
