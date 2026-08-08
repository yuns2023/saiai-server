package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

type externalOAuthUserRepoStub struct {
	UserRepository
	user *User
}
type externalOAuthRefreshCacheStub struct{ RefreshTokenCache }

func (r externalOAuthUserRepoStub) GetByID(_ context.Context, id int64) (*User, error) {
	if r.user != nil && r.user.ID == id {
		return r.user, nil
	}
	return nil, ErrUserNotFound
}

func newExternalOAuthTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(1)", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(driver)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestNormalizeExternalOAuthIdentity(t *testing.T) {
	identity, err := normalizeExternalOAuthIdentity(ExternalOAuthIdentity{
		Provider:      " GitHub ",
		Subject:       "12345",
		VerifiedEmail: " Alice@Example.COM ",
		Username:      "alice",
	})
	require.NoError(t, err)
	require.Equal(t, "github", identity.Provider)
	require.Equal(t, "alice@example.com", identity.VerifiedEmail)
}

func TestNormalizeExternalOAuthIdentityRejectsUnsupportedOrReservedIdentity(t *testing.T) {
	_, err := normalizeExternalOAuthIdentity(ExternalOAuthIdentity{Provider: "linuxdo", Subject: "1", VerifiedEmail: "alice@example.com"})
	require.Error(t, err)

	_, err = normalizeExternalOAuthIdentity(ExternalOAuthIdentity{Provider: "google", Subject: "1", VerifiedEmail: "linuxdo-1@linuxdo-connect.invalid"})
	require.Error(t, err)
}

func TestOAuthRegistrationTokenHashIsDeterministicAndOpaque(t *testing.T) {
	hash := hashOAuthRegistrationToken("raw-one-time-token")
	require.Len(t, hash, 64)
	require.Equal(t, hash, hashOAuthRegistrationToken("raw-one-time-token"))
	require.NotContains(t, hash, "raw-one-time-token")
}

func TestResolveExternalOAuthDoesNotBindExistingEmail(t *testing.T) {
	client := newExternalOAuthTestClient(t)
	_, err := client.User.Create().
		SetEmail("alice@example.com").
		SetPasswordHash("not-used").
		SetRole(RoleUser).
		SetBalance(0).
		SetConcurrency(1).
		SetStatus(StatusActive).
		Save(context.Background())
	require.NoError(t, err)

	service := &AuthService{
		entClient:         client,
		userRepo:          externalOAuthUserRepoStub{},
		refreshTokenCache: externalOAuthRefreshCacheStub{},
		cfg:               &config.Config{},
	}
	_, err = service.ResolveExternalOAuth(context.Background(), ExternalOAuthIdentity{
		Provider:      "github",
		Subject:       "12345",
		VerifiedEmail: "alice@example.com",
		Username:      "alice",
	})
	require.ErrorIs(t, err, ErrOAuthAccountLinkRequired)
	count, countErr := client.AuthIdentity.Query().Count(context.Background())
	require.NoError(t, countErr)
	require.Zero(t, count)
}

func TestLinkExternalOAuthIdentityRequiresExplicitUserAndEnforcesUniqueness(t *testing.T) {
	client := newExternalOAuthTestClient(t)
	ctx := context.Background()
	first, err := client.User.Create().
		SetEmail("alice@example.com").
		SetPasswordHash("not-used").
		SetRole(RoleUser).
		SetBalance(0).
		SetConcurrency(1).
		SetStatus(StatusActive).
		Save(ctx)
	require.NoError(t, err)
	identity := ExternalOAuthIdentity{
		Provider:      "github",
		Subject:       "12345",
		VerifiedEmail: "provider@example.net",
		Username:      "alice",
	}
	service := &AuthService{
		entClient: client,
		userRepo:  externalOAuthUserRepoStub{user: &User{ID: first.ID, Email: first.Email, Status: StatusActive}},
	}
	require.NoError(t, service.LinkExternalOAuthIdentity(ctx, first.ID, identity))
	require.NoError(t, service.LinkExternalOAuthIdentity(ctx, first.ID, identity), "same binding should be idempotent")

	linked, err := client.AuthIdentity.Query().Only(ctx)
	require.NoError(t, err)
	require.Equal(t, first.ID, linked.UserID)
	require.Equal(t, "provider@example.net", linked.VerifiedEmail)

	second, err := client.User.Create().
		SetEmail("bob@example.com").
		SetPasswordHash("not-used").
		SetRole(RoleUser).
		SetBalance(0).
		SetConcurrency(1).
		SetStatus(StatusActive).
		Save(ctx)
	require.NoError(t, err)
	service.userRepo = externalOAuthUserRepoStub{user: &User{ID: second.ID, Email: second.Email, Status: StatusActive}}
	require.ErrorIs(t, service.LinkExternalOAuthIdentity(ctx, second.ID, identity), ErrOAuthIdentityConflict)
}
