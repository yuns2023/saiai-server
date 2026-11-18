//go:build integration

package repository

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// ---- migration 133 ----

func runMigration133InScratchSchema(t *testing.T, tx *sql.Tx, schema, seed string) error {
	t.Helper()
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `CREATE SCHEMA `+schema+`; SET LOCAL search_path=`+schema+`;
		CREATE TABLE users(id BIGINT PRIMARY KEY);
		CREATE TABLE groups(id BIGINT PRIMARY KEY);
		CREATE TABLE accounts(id BIGINT PRIMARY KEY, platform VARCHAR(50) NOT NULL, type VARCHAR(20) NOT NULL,
			extra JSONB NOT NULL DEFAULT '{}', deleted_at TIMESTAMPTZ);`+seed)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("133_hosted_self_onboard.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	return err
}

func TestMigration133BackfillsUpstreamIdentity(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	require.NoError(t, runMigration133InScratchSchema(t, tx, "m133_backfill", `
		INSERT INTO accounts(id, platform, type, extra, deleted_at) VALUES
		 (1, 'anthropic', 'setup-token', '{"account_uuid":" 0B6B8F86-0D9B-4F4E-9F55-6A1F5D7C0A11 "}', NULL),
		 (2, 'anthropic', 'oauth', '{"account_uuid":"1c0e2c1a-6f43-4d5e-8a8b-0f6c3a1d2e3f"}', NULL),
		 (3, 'anthropic', 'oauth', '{"account_uuid":"not-a-uuid"}', NULL),
		 (4, 'anthropic', 'setup-token', '{"account_uuid":""}', NULL),
		 (5, 'anthropic', 'setup-token', '{}', NULL),
		 (6, 'anthropic', 'oauth', '{"account_uuid":"00000000-0000-0000-0000-000000000000"}', NULL),
		 (7, 'openai', 'oauth', '{"account_uuid":"2d1f3e2b-7a54-4e6f-9b9c-1a7d4b2e3f40"}', NULL),
		 (8, 'anthropic', 'oauth', '{"account_uuid":"0b6b8f86-0d9b-4f4e-9f55-6a1f5d7c0a11"}', NOW()),
		 (9, 'anthropic', 'oauth', '{"account_uuid":"{1c0e2c1a-6f43-4d5e-8a8b-0f6c3a1d2e3f}"}', NULL);`))

	uuidOf := func(id int64) sql.NullString {
		var v sql.NullString
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT upstream_account_uuid::text FROM accounts WHERE id=$1`, id).Scan(&v))
		return v
	}
	require.Equal(t, "0b6b8f86-0d9b-4f4e-9f55-6a1f5d7c0a11", uuidOf(1).String, "trimmed + lower-cased")
	require.Equal(t, "1c0e2c1a-6f43-4d5e-8a8b-0f6c3a1d2e3f", uuidOf(2).String)
	for _, id := range []int64{3, 4, 5, 6, 7, 8, 9} {
		require.False(t, uuidOf(id).Valid, "id %d stays NULL (invalid, empty, nil uuid, other platform, deleted, non-canonical)", id)
	}
	// Idempotent re-run.
	migration, err := migrations.FS.ReadFile("133_hosted_self_onboard.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	require.Equal(t, "0b6b8f86-0d9b-4f4e-9f55-6a1f5d7c0a11", uuidOf(1).String)

	// The live index rejects a second live row with the same identity.
	backfilledUUID := uuidOf(1).String
	_, err = tx.ExecContext(ctx, `SAVEPOINT p`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO accounts(id, platform, type, upstream_account_uuid) VALUES (10, 'anthropic', 'oauth', $1)`, backfilledUUID)
	require.ErrorContains(t, err, "accounts_platform_upstream_account_uuid_live_key")
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT p`)
	require.NoError(t, err)
	// Same identity on another platform or on a deleted row is fine.
	_, err = tx.ExecContext(ctx, `INSERT INTO accounts(id, platform, type, upstream_account_uuid) VALUES (11, 'openai', 'oauth', $1)`, backfilledUUID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO accounts(id, platform, type, upstream_account_uuid, deleted_at) VALUES (12, 'anthropic', 'oauth', $1, NOW())`, backfilledUUID)
	require.NoError(t, err)
}

func TestMigration133FailsOnDuplicateIdentity(t *testing.T) {
	ctx := context.Background()
	for name, seed := range map[string]string{
		"m133_dup_case": `INSERT INTO accounts(id, platform, type, extra) VALUES
			(1, 'anthropic', 'oauth', '{"account_uuid":"0B6B8F86-0D9B-4F4E-9F55-6A1F5D7C0A11"}'),
			(2, 'anthropic', 'setup-token', '{"account_uuid":"0b6b8f86-0d9b-4f4e-9f55-6a1f5d7c0a11"}');`,
	} {
		tx, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		err = runMigration133InScratchSchema(t, tx, name, seed)
		require.Error(t, err, name)
		require.Contains(t, err.Error(), "migration 133", name)
		require.Contains(t, err.Error(), "1,2", name)
		_ = tx.Rollback()
	}
}

func TestHostedApplicationPendingIdentityIndex(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	user := mustCreateUser(t, client, &service.User{Email: "m133-idx-" + uuid.NewString() + "@example.invalid"})
	upstream := uuid.NewString()
	insert := func() error {
		_, err := client.ExecContext(ctx, `INSERT INTO hosted_account_applications (user_id, display_name, upstream_account_uuid, credentials)
			VALUES ($1, 'n', $2, '{"access_token":"x"}')`, user.ID, upstream)
		return err
	}
	require.NoError(t, insert())
	_, err := client.ExecContext(ctx, "SAVEPOINT p")
	require.NoError(t, err)
	require.ErrorContains(t, insert(), "hosted_account_applications_pending_upstream_key")
	_, err = client.ExecContext(ctx, "ROLLBACK TO SAVEPOINT p")
	require.NoError(t, err)

	// Terminal rows must be wiped (CHECK) and free the identity.
	_, err = client.ExecContext(ctx, "SAVEPOINT p")
	require.NoError(t, err)
	_, err = client.ExecContext(ctx, `UPDATE hosted_account_applications SET status='cancelled' WHERE user_id=$1`, user.ID)
	require.ErrorContains(t, err, "hosted_account_applications_terminal_wiped")
	_, err = client.ExecContext(ctx, "ROLLBACK TO SAVEPOINT p")
	require.NoError(t, err)
	_, err = client.ExecContext(ctx, `UPDATE hosted_account_applications SET status='cancelled', credentials=NULL WHERE user_id=$1`, user.ID)
	require.NoError(t, err)
	require.NoError(t, insert())
}

func TestAccountCreateWritesUpstreamIdentity(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, client, nil)
	upstream := uuid.NewString()

	a := &service.Account{Name: "id-a", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, Extra: map[string]any{"account_uuid": strings.ToUpper(upstream)}}
	require.NoError(t, repo.Create(ctx, a))
	loaded, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.UpstreamAccountUUID)
	require.Equal(t, upstream, *loaded.UpstreamAccountUUID)
	require.False(t, service.AccountIdentityUnknown(loaded))

	_, err = client.ExecContext(ctx, "SAVEPOINT p")
	require.NoError(t, err)
	b := &service.Account{Name: "id-b", Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, Extra: map[string]any{"account_uuid": upstream}}
	require.ErrorIs(t, repo.Create(ctx, b), service.ErrUpstreamAccountAlreadyExists)
	_, err = client.ExecContext(ctx, "ROLLBACK TO SAVEPOINT p")
	require.NoError(t, err)

	unknown := &service.Account{Name: "id-c", Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, Extra: map[string]any{}}
	require.NoError(t, repo.Create(ctx, unknown))
	loaded, err = repo.GetByID(ctx, unknown.ID)
	require.NoError(t, err)
	require.Nil(t, loaded.UpstreamAccountUUID)
	require.True(t, service.AccountIdentityUnknown(loaded))
}

// ---- approval unit of work ----

type hostedFixture struct {
	repo    *hostedApplicationRepository
	owners  *hostedOwnerRepository
	users   []int64
	proxies []int64
}

func newHostedFixture(t *testing.T) *hostedFixture {
	t.Helper()
	ctx := context.Background()
	f := &hostedFixture{
		repo:   &hostedApplicationRepository{client: integrationEntClient, db: integrationDB, accounts: newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)},
		owners: &hostedOwnerRepository{db: integrationDB, accounts: newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)},
	}
	var previous sql.NullString
	err := integrationDB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", service.SettingKeyHostedSelfOnboardEnabled).Scan(&previous)
	if err != nil {
		require.ErrorIs(t, err, sql.ErrNoRows)
	}
	f.setSwitch(t, true)
	t.Cleanup(func() {
		bg := context.Background()
		if previous.Valid {
			_, _ = integrationDB.ExecContext(bg, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyHostedSelfOnboardEnabled, previous.String)
		} else {
			_, _ = integrationDB.ExecContext(bg, "DELETE FROM settings WHERE key=$1", service.SettingKeyHostedSelfOnboardEnabled)
		}
		users := pq.Array(f.users)
		var accountIDs, groupIDs, proxyIDs []int64
		collect := func(dst *[]int64, query string, args ...any) {
			rows, err := integrationDB.QueryContext(bg, query, args...)
			if err != nil {
				return
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var id int64
				if rows.Scan(&id) == nil {
					*dst = append(*dst, id)
				}
			}
		}
		collect(&accountIDs, `SELECT id FROM accounts WHERE owner_user_id = ANY($1)
			UNION SELECT account_id FROM account_owner_events WHERE new_owner_user_id = ANY($1) OR old_owner_user_id = ANY($1)
			UNION SELECT account_id FROM hosted_account_applications WHERE user_id = ANY($1) AND account_id IS NOT NULL`, users)
		collect(&groupIDs, `SELECT group_id FROM hosted_owner_groups WHERE user_id = ANY($1)
			UNION SELECT group_id FROM user_allowed_groups WHERE user_id = ANY($1)`, users)
		collect(&proxyIDs, `SELECT proxy_id FROM accounts WHERE id = ANY($1) AND proxy_id IS NOT NULL`, pq.Array(accountIDs))
		proxyIDs = append(proxyIDs, f.proxies...)
		for _, stmt := range []struct {
			q    string
			args []any
		}{
			{"DELETE FROM hosted_account_applications WHERE user_id = ANY($1)", []any{users}},
			{"DELETE FROM account_owner_events WHERE account_id = ANY($1)", []any{pq.Array(accountIDs)}},
			{"DELETE FROM scheduler_outbox WHERE account_id = ANY($1) OR group_id = ANY($2)", []any{pq.Array(accountIDs), pq.Array(groupIDs)}},
			{"DELETE FROM account_groups WHERE account_id = ANY($1) OR group_id = ANY($2)", []any{pq.Array(accountIDs), pq.Array(groupIDs)}},
			{"DELETE FROM user_allowed_groups WHERE user_id = ANY($1) OR group_id = ANY($2)", []any{users, pq.Array(groupIDs)}},
			{"DELETE FROM accounts WHERE id = ANY($1)", []any{pq.Array(accountIDs)}},
			{"DELETE FROM hosted_owner_groups WHERE user_id = ANY($1)", []any{users}},
			{"DELETE FROM groups WHERE id = ANY($1)", []any{pq.Array(groupIDs)}},
			{"DELETE FROM proxies WHERE id = ANY($1)", []any{pq.Array(proxyIDs)}},
			{"DELETE FROM users WHERE id = ANY($1)", []any{users}},
		} {
			_, _ = integrationDB.ExecContext(bg, stmt.q, stmt.args...)
		}
	})
	return f
}

func (f *hostedFixture) setSwitch(t *testing.T, on bool) {
	t.Helper()
	value := "false"
	if on {
		value = "true"
	}
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, service.SettingKeyHostedSelfOnboardEnabled, value)
	require.NoError(t, err)
}

func (f *hostedFixture) user(t *testing.T) *service.User {
	t.Helper()
	u := mustCreateUser(t, integrationEntClient, &service.User{Email: "hosted-p2c-" + uuid.NewString() + "@example.invalid"})
	f.users = append(f.users, u.ID)
	return u
}

func (f *hostedFixture) proxy(t *testing.T, status string) *service.Proxy {
	t.Helper()
	p := mustCreateProxy(t, integrationEntClient, &service.Proxy{Name: "p2c-" + uuid.NewString()[:8], Protocol: "socks5h", Host: "203.0.113.10", Port: 1080, Status: status})
	f.proxies = append(f.proxies, p.ID)
	return p
}

func (f *hostedFixture) submit(t *testing.T, userID int64, upstream string, proxy *service.HostedProxyInput) *service.HostedApplication {
	t.Helper()
	app, err := f.repo.CreatePending(context.Background(), &service.HostedApplicationCreate{
		UserID: userID, DisplayName: "mine", Platform: service.PlatformAnthropic, UpstreamAccountUUID: upstream,
		UpstreamOrgUUID: "org", UpstreamEmail: "o@example.com", MaxPending: service.HostedSelfOnboardMaxPending,
		Credentials: map[string]any{"access_token": "sk-ant-oat01-" + "INTEGRATIONSECRET", "expires_at": time.Now().Add(300 * 24 * time.Hour).Unix(), "scope": "user:inference"},
		Proxy:       proxy,
	})
	require.NoError(t, err)
	return app
}

func hostedTestSpec(app *service.HostedApplicationSecrets) (*service.HostedApprovedAccountSpec, error) {
	return &service.HostedApprovedAccountSpec{
		Name: "p2c test", Credentials: app.Credentials,
		Extra:       map[string]any{"account_uuid": app.UpstreamAccountUUID, "claude_oauth_mode": "carpool"},
		Concurrency: 2, Priority: 1, GroupName: "托管-" + uuid.NewString()[:8], GroupDescription: "test", FeeRate: 0.1,
	}, nil
}

func requireReason(t *testing.T, err error, reason string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, infraerrors.FromError(err).Reason, err.Error())
}

func hostedCountRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), query, args...).Scan(&n))
	return n
}

func TestHostedApproveCreatesAccountAndReplays(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	proxy := f.proxy(t, service.StatusActive)
	app := f.submit(t, owner.ID, uuid.NewString(), nil)

	res, err := f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: app.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
	require.NoError(t, err)
	require.False(t, res.Replayed)
	require.Equal(t, service.HostedApplicationStatusApproved, res.Application.Status)
	require.Equal(t, res.AccountID, *res.Application.AccountID)

	acc, err := f.repo.accounts.GetByID(ctx, res.AccountID)
	require.NoError(t, err)
	require.True(t, acc.SelfOnboarded)
	require.Equal(t, app.UpstreamAccountUUID, *acc.UpstreamAccountUUID)
	require.Equal(t, proxy.ID, *acc.ProxyID)
	require.Equal(t, owner.ID, *acc.OwnerUserID)
	require.Equal(t, service.AccountTypeSetupToken, acc.Type)
	require.Equal(t, []int64{res.GroupID}, acc.GroupIDs)
	require.True(t, acc.IsSchedulable())

	var platform string
	var exclusive, hosted, quota bool
	var rate float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT platform, is_exclusive, hosted_usage_enabled, show_account_quota, rate_multiplier
		FROM groups WHERE id=$1`, res.GroupID).Scan(&platform, &exclusive, &hosted, &quota, &rate))
	require.Equal(t, service.PlatformAnthropic, platform)
	require.True(t, exclusive && hosted && quota)
	require.InDelta(t, 0.1, rate, 1e-9)
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM user_allowed_groups WHERE user_id=$1 AND group_id=$2", owner.ID, res.GroupID))
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM hosted_owner_groups WHERE user_id=$1 AND group_id=$2", owner.ID, res.GroupID))
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM account_owner_events WHERE account_id=$1 AND new_owner_user_id=$2 AND admin_user_id=1", res.AccountID, owner.ID))
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM hosted_account_applications WHERE id=$1 AND credentials IS NULL AND proxy_password IS NULL", app.ID))
	require.GreaterOrEqual(t, hostedCountRows(t, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", res.AccountID), 1)

	// Replay: same result, nothing new.
	again, err := f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: app.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.Equal(t, res.Application.UserEmail, again.Application.UserEmail, "replay returns the full stored projection")
	require.NotEmpty(t, again.Application.UserEmail)
	require.NotNil(t, again.Application.ReviewedAt)
	require.Equal(t, res.AccountID, again.AccountID)
	require.Equal(t, res.GroupID, again.GroupID)
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM accounts WHERE owner_user_id=$1", owner.ID))

	// Second application of the owner reuses the group; submitted proxy is created.
	second := f.submit(t, owner.ID, uuid.NewString(), &service.HostedProxyInput{Protocol: "http", Host: "198.51.100.20", Port: 3128, Username: "u", Password: "pw-secret"})
	res2, err := f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: second.ID, AdminUserID: 1, UseSubmitted: true, Build: hostedTestSpec})
	require.NoError(t, err)
	require.Equal(t, res.GroupID, res2.GroupID)
	require.NotZero(t, res2.CreatedProxy)
	acc2, err := f.repo.accounts.GetByID(ctx, res2.AccountID)
	require.NoError(t, err)
	require.Equal(t, res2.CreatedProxy, *acc2.ProxyID)
	require.Equal(t, "pw-secret", acc2.Proxy.Password)
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM hosted_account_applications WHERE id=$1 AND proxy_password IS NULL AND proxy_username = 'u'", second.ID))
}

func TestHostedApproveRollsBackAfterGroupCreation(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	disabled := f.proxy(t, "disabled")
	app := f.submit(t, owner.ID, uuid.NewString(), nil)

	groupsBefore := hostedCountRows(t, "SELECT COUNT(*) FROM groups")
	// The proxy check runs after the owner group was created in the tx.
	_, err := f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: app.ID, AdminUserID: 1, ExistingProxyID: disabled.ID, Build: hostedTestSpec})
	requireReason(t, err, "SELF_ONBOARD_PROXY_REQUIRED")
	require.Zero(t, hostedCountRows(t, "SELECT COUNT(*) FROM hosted_owner_groups WHERE user_id=$1", owner.ID))
	require.Zero(t, hostedCountRows(t, "SELECT COUNT(*) FROM user_allowed_groups WHERE user_id=$1", owner.ID))
	require.Zero(t, hostedCountRows(t, "SELECT COUNT(*) FROM accounts WHERE owner_user_id=$1", owner.ID))
	require.Zero(t, hostedCountRows(t, "SELECT COUNT(*) FROM account_owner_events WHERE new_owner_user_id=$1", owner.ID))
	require.Equal(t, groupsBefore, hostedCountRows(t, "SELECT COUNT(*) FROM groups"), "the created group was rolled back")
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM hosted_account_applications WHERE id=$1 AND status='pending' AND credentials IS NOT NULL", app.ID))

	// A Build failure also leaves nothing behind.
	_, err = f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: app.ID, AdminUserID: 1, ExistingProxyID: disabled.ID,
		Build: func(*service.HostedApplicationSecrets) (*service.HostedApprovedAccountSpec, error) {
			return nil, service.ErrSelfOnboardInvalidState
		}})
	requireReason(t, err, "SELF_ONBOARD_INVALID_STATE")
	require.Zero(t, hostedCountRows(t, "SELECT COUNT(*) FROM accounts WHERE owner_user_id=$1", owner.ID))
}

func TestHostedApproveRefusesKnownIdentityAndInvalidState(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	proxy := f.proxy(t, service.StatusActive)
	upstream := uuid.NewString()
	app := f.submit(t, owner.ID, upstream, nil)

	// An admin imported the same upstream account meanwhile.
	admin := &service.Account{Name: "admin import", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, Extra: map[string]any{"account_uuid": upstream}}
	require.NoError(t, f.repo.accounts.Create(ctx, admin))
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET owner_user_id=$2 WHERE id=$1", admin.ID, owner.ID) // cleanup tracks it
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "INSERT INTO account_owner_events (account_id, new_owner_user_id) VALUES ($1, $2)", admin.ID, owner.ID)
	require.NoError(t, err)

	_, err = f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: app.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
	requireReason(t, err, "SELF_ONBOARD_ALREADY_HOSTED")
	_, err = f.repo.CreatePending(ctx, &service.HostedApplicationCreate{UserID: owner.ID, DisplayName: "x", Platform: service.PlatformAnthropic,
		UpstreamAccountUUID: upstream, Credentials: map[string]any{"access_token": "x"}, MaxPending: 3})
	requireReason(t, err, "SELF_ONBOARD_ALREADY_HOSTED")

	// Rejected → terminal; approve then fails with invalid state.
	other := f.submit(t, owner.ID, uuid.NewString(), nil)
	rejected, err := f.repo.Reject(ctx, 1, other.ID, "no")
	require.NoError(t, err)
	require.Equal(t, service.HostedApplicationStatusRejected, rejected.Status)
	_, err = f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: other.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
	requireReason(t, err, "SELF_ONBOARD_INVALID_STATE")

	// Disabled applicant.
	third := f.submit(t, owner.ID, uuid.NewString(), nil)
	_, err = integrationDB.ExecContext(ctx, "UPDATE users SET status='disabled' WHERE id=$1", owner.ID)
	require.NoError(t, err)
	_, err = f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: third.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
	requireReason(t, err, "SELF_ONBOARD_INVALID_STATE")
}

func TestHostedApproveOwnerGroupReuseChecks(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	stranger := f.user(t)
	proxy := f.proxy(t, service.StatusActive)
	first := f.submit(t, owner.ID, uuid.NewString(), nil)
	res, err := f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: first.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
	require.NoError(t, err)

	// Another user was granted the owner's group: the group is no longer private.
	_, err = integrationDB.ExecContext(ctx, "INSERT INTO user_allowed_groups (user_id, group_id) VALUES ($1, $2)", stranger.ID, res.GroupID)
	require.NoError(t, err)
	second := f.submit(t, owner.ID, uuid.NewString(), nil)
	_, err = f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: second.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
	requireReason(t, err, "SELF_ONBOARD_INVALID_STATE")
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM user_allowed_groups WHERE user_id=$1", stranger.ID)
	require.NoError(t, err)

	// A soft-deleted owner group is replaced by a new one.
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET deleted_at=NOW() WHERE id=$1", res.GroupID)
	require.NoError(t, err)
	res2, err := f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: second.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
	require.NoError(t, err)
	require.NotEqual(t, res.GroupID, res2.GroupID)
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM hosted_owner_groups WHERE user_id=$1 AND group_id=$2", owner.ID, res2.GroupID))
}

func TestHostedCreatePendingLimitsAndSwitch(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	for i := 0; i < 3; i++ {
		f.submit(t, owner.ID, uuid.NewString(), nil)
	}
	create := func(userID int64) error {
		_, err := f.repo.CreatePending(ctx, &service.HostedApplicationCreate{UserID: userID, DisplayName: "x", Platform: service.PlatformAnthropic,
			UpstreamAccountUUID: uuid.NewString(), Credentials: map[string]any{"access_token": "x"}, MaxPending: 3})
		return err
	}
	requireReason(t, create(owner.ID), "SELF_ONBOARD_PENDING_LIMIT")
	n, err := f.repo.CountPending(ctx, owner.ID)
	require.NoError(t, err)
	require.Equal(t, 3, n)

	other := f.user(t)
	f.setSwitch(t, false)
	requireReason(t, create(other.ID), "SELF_ONBOARD_DISABLED")
	// The approval transaction re-reads the switch too.
	proxy := f.proxy(t, service.StatusActive)
	pendingApp := f.submitAs(t, owner.ID)
	_, err = f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: pendingApp, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
	requireReason(t, err, "SELF_ONBOARD_DISABLED")
	f.setSwitch(t, true)
	_, err = integrationDB.ExecContext(ctx, "UPDATE users SET status='disabled' WHERE id=$1", other.ID)
	require.NoError(t, err)
	requireReason(t, create(other.ID), "SELF_ONBOARD_INVALID_STATE")

	// Cancel: only the owner, only while pending; secrets wiped.
	list, err := f.repo.ListByUser(ctx, owner.ID, 10)
	require.NoError(t, err)
	require.Len(t, list, 3)
	require.Greater(t, list[0].ID, list[1].ID, "newest first")
	_, err = f.repo.Cancel(ctx, other.ID, list[0].ID)
	requireReason(t, err, "SELF_ONBOARD_NOT_FOUND")
	cancelled, err := f.repo.Cancel(ctx, owner.ID, list[0].ID)
	require.NoError(t, err)
	require.Equal(t, service.HostedApplicationStatusCancelled, cancelled.Status)
	_, err = f.repo.Cancel(ctx, owner.ID, list[0].ID)
	requireReason(t, err, "SELF_ONBOARD_INVALID_STATE")
	require.NoError(t, create(owner.ID), "a cancelled application frees a pending slot")
}

// Approve, cancel and expire race for one pending application: exactly one wins.
func TestHostedApproveCancelExpireRace(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	proxy := f.proxy(t, service.StatusActive)
	for round := 0; round < 6; round++ {
		app := f.submit(t, owner.ID, uuid.NewString(), nil)
		var wg sync.WaitGroup
		var approveErr, cancelErr error
		var expired int64
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, approveErr = f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: app.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
		}()
		go func() {
			defer wg.Done()
			_, cancelErr = f.repo.Cancel(ctx, owner.ID, app.ID)
		}()
		go func() {
			defer wg.Done()
			// Applications are "older than" a cutoff in the future.
			expired, _ = f.repo.ExpirePending(ctx, time.Now().Add(time.Hour), 1000)
		}()
		wg.Wait()
		var status string
		var accountID sql.NullInt64
		var hasCreds bool
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status, account_id, credentials IS NOT NULL FROM hosted_account_applications WHERE id=$1", app.ID).
			Scan(&status, &accountID, &hasCreds))
		require.False(t, hasCreds, "round %d: terminal state wiped", round)
		winners := 0
		if approveErr == nil {
			winners++
			require.Equal(t, service.HostedApplicationStatusApproved, status)
			require.True(t, accountID.Valid)
		}
		if cancelErr == nil {
			winners++
			require.Equal(t, service.HostedApplicationStatusCancelled, status)
		}
		if status == service.HostedApplicationStatusExpired {
			winners++
			require.GreaterOrEqual(t, expired, int64(1))
		}
		require.Equal(t, 1, winners, "round %d: status=%s approveErr=%v cancelErr=%v", round, status, approveErr, cancelErr)
		if status != service.HostedApplicationStatusApproved {
			require.False(t, accountID.Valid)
		}
	}
	// Every account created belongs to an approved application.
	require.Equal(t,
		hostedCountRows(t, "SELECT COUNT(*) FROM hosted_account_applications WHERE user_id=$1 AND status='approved'", owner.ID),
		hostedCountRows(t, "SELECT COUNT(*) FROM accounts WHERE owner_user_id=$1 AND self_onboarded", owner.ID))
}

func TestHostedOwnerAccountStateConditions(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	stranger := f.user(t)
	acc := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "owned " + uuid.NewString(), Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken})
	_, err := f.owners.SetAccountOwner(ctx, acc.ID, &owner.ID, 1)
	require.NoError(t, err)

	_, err = f.repo.SetOwnerAccountState(ctx, stranger.ID, acc.ID, service.HostedOwnerActionPause)
	requireReason(t, err, "SELF_ONBOARD_NOT_FOUND")

	st, err := f.repo.SetOwnerAccountState(ctx, owner.ID, acc.ID, service.HostedOwnerActionPause)
	require.NoError(t, err)
	require.Equal(t, service.HostedOwnedAccountState{AccountID: acc.ID, OwnerPaused: true}, *st)
	loaded, err := f.repo.accounts.GetByID(ctx, acc.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.OwnerPausedAt)
	require.False(t, loaded.IsSchedulable())

	st, err = f.repo.SetOwnerAccountState(ctx, owner.ID, acc.ID, service.HostedOwnerActionWithdraw)
	require.NoError(t, err)
	require.True(t, st.OwnerPaused && st.WithdrawRequested)
	st, err = f.repo.SetOwnerAccountState(ctx, owner.ID, acc.ID, service.HostedOwnerActionResume)
	require.NoError(t, err)
	require.Equal(t, service.HostedOwnedAccountState{AccountID: acc.ID, OwnerPaused: false, WithdrawRequested: true}, *st, "resume never clears the withdraw request")

	// A historic owner (owner cleared) can view usage but not change the account.
	_, err = f.owners.SetAccountOwner(ctx, acc.ID, nil, 1)
	require.NoError(t, err)
	allowed, err := f.owners.CanViewOwnedAccount(ctx, owner.ID, acc.ID)
	require.NoError(t, err)
	require.True(t, allowed)
	_, err = f.repo.SetOwnerAccountState(ctx, owner.ID, acc.ID, service.HostedOwnerActionResume)
	requireReason(t, err, "SELF_ONBOARD_NOT_FOUND")
	// Deleted accounts are not writable either.
	_, err = f.owners.SetAccountOwner(ctx, acc.ID, &owner.ID, 1)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET deleted_at=NOW() WHERE id=$1", acc.ID)
	require.NoError(t, err)
	_, err = f.repo.SetOwnerAccountState(ctx, owner.ID, acc.ID, service.HostedOwnerActionPause)
	requireReason(t, err, "SELF_ONBOARD_NOT_FOUND")
}

func TestListSchedulableExcludesOwnerBlockedAndProxylessSelfOnboarded(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	active := f.proxy(t, service.StatusActive)
	disabled := f.proxy(t, "disabled")
	deleted := f.proxy(t, service.StatusActive)
	_, err := integrationDB.ExecContext(ctx, "UPDATE proxies SET deleted_at=NOW() WHERE id=$1", deleted.ID)
	require.NoError(t, err)
	group := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "p2c-sched-" + uuid.NewString(), Platform: service.PlatformAnthropic})
	_, err = integrationDB.ExecContext(ctx, "INSERT INTO user_allowed_groups (user_id, group_id) VALUES ($1, $2)", owner.ID, group.ID) // cleanup tracks the group
	require.NoError(t, err)

	now := time.Now()
	type spec struct {
		name     string
		self     bool
		proxy    *int64
		paused   bool
		withdraw bool
		want     bool
	}
	specs := []spec{
		{"direct", false, nil, false, false, true},
		{"self-active-proxy", true, &active.ID, false, false, true},
		{"self-no-proxy", true, nil, false, false, false},
		{"self-disabled-proxy", true, &disabled.ID, false, false, false},
		{"self-deleted-proxy", true, &deleted.ID, false, false, false},
		{"paused", false, nil, true, false, false},
		{"withdrawn", false, &active.ID, false, true, false},
	}
	want := map[int64]bool{}
	for _, s := range specs {
		acc := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "p2c " + s.name, Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, ProxyID: s.proxy})
		var pausedAt, withdrawAt any
		if s.paused {
			pausedAt = now
		}
		if s.withdraw {
			withdrawAt = now
		}
		_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET owner_user_id=$2, self_onboarded=$3, owner_paused_at=$4, owner_withdraw_requested_at=$5 WHERE id=$1`,
			acc.ID, owner.ID, s.self, pausedAt, withdrawAt)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "INSERT INTO account_groups (account_id, group_id, priority) VALUES ($1, $2, 1)", acc.ID, group.ID)
		require.NoError(t, err)
		want[acc.ID] = s.want
	}
	repo := f.repo.accounts
	pick := func(accounts []service.Account) map[int64]bool {
		got := map[int64]bool{}
		for _, a := range accounts {
			if _, ok := want[a.ID]; ok {
				got[a.ID] = true
				require.True(t, a.IsSchedulable(), "listed account %d must also pass IsSchedulable", a.ID)
			}
		}
		return got
	}
	expected := map[int64]bool{}
	for id, ok := range want {
		if ok {
			expected[id] = true
		}
	}
	byGroup, err := repo.ListSchedulableByGroupID(ctx, group.ID)
	require.NoError(t, err)
	require.Equal(t, expected, pick(byGroup))
	byPlatform, err := repo.ListSchedulableByPlatform(ctx, service.PlatformAnthropic)
	require.NoError(t, err)
	require.Equal(t, expected, pick(byPlatform))
	byPlatforms, err := repo.ListSchedulableByPlatforms(ctx, []string{service.PlatformAnthropic})
	require.NoError(t, err)
	require.Equal(t, expected, pick(byPlatforms))
	all, err := repo.ListSchedulable(ctx)
	require.NoError(t, err)
	require.Equal(t, expected, pick(all))
	byGroupPlatform, err := repo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, service.PlatformAnthropic)
	require.NoError(t, err)
	require.Equal(t, expected, pick(byGroupPlatform))
	// IsSchedulable agrees for the excluded ones (loaded fresh).
	for id, ok := range want {
		acc, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, ok, acc.IsSchedulable(), "account %d", id)
	}
}

func TestHostedOnboardStoreSessionConsume(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	store := NewHostedOnboardStore(rdb)
	now := time.Now()
	save := func(id string, expires time.Time) {
		require.NoError(t, store.SaveSession(ctx, id, &service.HostedOnboardSession{UserID: 5, Purpose: "setup_token", State: "st", CodeVerifier: "ver", ExpiresAt: expires}, time.Minute))
	}
	save("s1", now.Add(time.Minute))
	require.Error(t, store.SaveSession(ctx, "s1", &service.HostedOnboardSession{UserID: 5}, time.Minute), "collision")

	got, err := store.ConsumeSession(ctx, "s1", 6, "setup_token", "st", now)
	require.NoError(t, err)
	require.Nil(t, got, "foreign user")
	got, err = store.ConsumeSession(ctx, "s1", 5, "other", "st", now)
	require.NoError(t, err)
	require.Nil(t, got, "wrong purpose")
	got, err = store.ConsumeSession(ctx, "s1", 5, "setup_token", "bad", now)
	require.NoError(t, err)
	require.Nil(t, got, "wrong state")
	got, err = store.ConsumeSession(ctx, "s1", 5, "setup_token", "st", now)
	require.NoError(t, err)
	require.NotNil(t, got, "mismatches did not consume it")
	require.Equal(t, "ver", got.CodeVerifier)
	got, err = store.ConsumeSession(ctx, "s1", 5, "setup_token", "st", now)
	require.NoError(t, err)
	require.Nil(t, got, "single use")

	save("s2", now.Add(-time.Second))
	got, err = store.ConsumeSession(ctx, "s2", 5, "setup_token", "st", now)
	require.NoError(t, err)
	require.Nil(t, got, "expired")

	// Concurrent consumers: exactly one wins.
	save("s3", now.Add(time.Minute))
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := store.ConsumeSession(ctx, "s3", 5, "setup_token", "st", now)
			if err == nil && s != nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, wins)

	// The raw session id never appears in a key.
	keys, err := rdb.Keys(ctx, "*s3*").Result()
	require.NoError(t, err)
	require.Empty(t, keys)

	n, err := store.CountSubmit(ctx, 5, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	n, err = store.CountSubmit(ctx, 5, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
}

// A locked admin update (re-authorization) that switches the upstream account
// keeps the identity column in step; a conflict rolls the whole update back;
// an absent/invalid value leaves the column alone.
func TestUpdateLockedSyncsUpstreamIdentity(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	repo := f.repo.accounts
	first, second, taken := uuid.NewString(), uuid.NewString(), uuid.NewString()
	mk := func(name, upstream string) *service.Account {
		a := &service.Account{Name: name, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive,
			Schedulable: true, Concurrency: 1, Priority: 1, Extra: map[string]any{"account_uuid": upstream}}
		require.NoError(t, repo.Create(ctx, a))
		_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET owner_user_id=$2 WHERE id=$1", a.ID, owner.ID) // cleanup tracks it
		require.NoError(t, err)
		return a
	}
	acc := mk("reauth", first)
	mk("other", taken)
	setExtra := func(v string) error {
		_, err := repo.UpdateLocked(ctx, acc.ID, func(a *service.Account) error {
			a.Extra["account_uuid"] = v
			a.Name = "reauth " + v
			return nil
		})
		return err
	}
	column := func() sql.NullString {
		var v sql.NullString
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT upstream_account_uuid::text FROM accounts WHERE id=$1", acc.ID).Scan(&v))
		return v
	}
	require.NoError(t, setExtra(strings.ToUpper(second)))
	require.Equal(t, second, column().String)

	require.ErrorIs(t, setExtra(taken), service.ErrUpstreamAccountAlreadyExists)
	require.Equal(t, second, column().String)
	loaded, err := repo.GetByID(ctx, acc.ID)
	require.NoError(t, err)
	require.Equal(t, strings.ToUpper(second), loaded.Extra["account_uuid"], "the conflicting update was rolled back")

	require.NoError(t, setExtra(""))
	require.Equal(t, second, column().String, "absent value keeps the known identity")
}

// submitAs returns the id of one of the user's pending applications.
func (f *hostedFixture) submitAs(t *testing.T, userID int64) int64 {
	t.Helper()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT id FROM hosted_account_applications WHERE user_id=$1 AND status='pending' ORDER BY id LIMIT 1", userID).Scan(&id))
	return id
}

// A proxy status / address change reaches the cached single-account keys
// (selection, sticky sessions, Forward) and queues outbox events.
func TestRefreshAccountsUsingProxyUpdatesCachedAccounts(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	proxy := f.proxy(t, service.StatusActive)
	cache := NewSchedulerCache(testRedis(t))
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, cache)
	proxies := &proxyRepository{client: integrationEntClient, sql: integrationDB}

	acc := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "p2c cached " + uuid.NewString(), Platform: service.PlatformAnthropic,
		Type: service.AccountTypeSetupToken, ProxyID: &proxy.ID})
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET owner_user_id=$2, self_onboarded=TRUE WHERE id=$1", acc.ID, owner.ID)
	require.NoError(t, err)
	warm, err := repo.GetByID(ctx, acc.ID)
	require.NoError(t, err)
	require.NoError(t, cache.SetAccount(ctx, warm))
	cached, err := cache.GetAccount(ctx, acc.ID)
	require.NoError(t, err)
	require.True(t, cached.IsSchedulable())

	before := hostedCountRows(t, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", acc.ID)
	p, err := proxies.GetByID(ctx, proxy.ID)
	require.NoError(t, err)
	p.Status = "disabled"
	require.NoError(t, proxies.Update(ctx, p))
	require.NoError(t, repo.RefreshAccountsUsingProxy(ctx, proxy.ID))
	cached, err = cache.GetAccount(ctx, acc.ID)
	require.NoError(t, err)
	require.False(t, cached.IsSchedulable(), "disabled proxy seen through the cached key")
	_, err = cached.EgressProxyURL()
	require.ErrorIs(t, err, service.ErrAccountProxyRequired)
	require.Greater(t, hostedCountRows(t, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", acc.ID), before)

	p.Status, p.Host, p.Port = service.StatusActive, "198.51.100.77", 3129
	require.NoError(t, proxies.Update(ctx, p))
	require.NoError(t, repo.RefreshAccountsUsingProxy(ctx, proxy.ID))
	cached, err = cache.GetAccount(ctx, acc.ID)
	require.NoError(t, err)
	require.True(t, cached.IsSchedulable())
	u, err := cached.EgressProxyURL()
	require.NoError(t, err)
	require.Equal(t, "socks5h://198.51.100.77:3129", u, "the new address is used")
}

// Deleting a proxy locks it, counts references and deletes in one
// transaction; it never wins against an approval that binds it.
func TestProxyDeleteIfUnusedVersusApproval(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	proxies := &proxyRepository{client: integrationEntClient, sql: integrationDB}

	unused := f.proxy(t, service.StatusActive)
	require.NoError(t, proxies.DeleteIfUnused(ctx, unused.ID))
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM proxies WHERE id=$1 AND deleted_at IS NOT NULL", unused.ID))
	require.NoError(t, proxies.DeleteIfUnused(ctx, unused.ID), "already deleted: no-op")

	for round := 0; round < 6; round++ {
		proxy := f.proxy(t, service.StatusActive)
		app := f.submit(t, owner.ID, uuid.NewString(), nil)
		var wg sync.WaitGroup
		var approveErr, deleteErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, approveErr = f.repo.Approve(ctx, service.HostedApproveParams{ApplicationID: app.ID, AdminUserID: 1, ExistingProxyID: proxy.ID, Build: hostedTestSpec})
		}()
		go func() {
			defer wg.Done()
			deleteErr = proxies.DeleteIfUnused(ctx, proxy.ID)
		}()
		wg.Wait()
		deleted := hostedCountRows(t, "SELECT COUNT(*) FROM proxies WHERE id=$1 AND deleted_at IS NOT NULL", proxy.ID) == 1
		bound := hostedCountRows(t, "SELECT COUNT(*) FROM accounts WHERE proxy_id=$1 AND deleted_at IS NULL", proxy.ID)
		require.False(t, deleted && bound > 0, "round %d: a deleted proxy is bound to a live account", round)
		if approveErr == nil {
			require.ErrorIs(t, deleteErr, service.ErrProxyInUse, "round %d", round)
		} else {
			requireReason(t, approveErr, "SELF_ONBOARD_PROXY_REQUIRED")
			require.NoError(t, deleteErr)
		}
		// Clean the approved account's group binding for the next round.
		_, err := integrationDB.ExecContext(ctx, "UPDATE hosted_account_applications SET status='cancelled', credentials=NULL, proxy_password=NULL WHERE id=$1 AND status='pending'", app.ID)
		require.NoError(t, err)
	}
}

// After the owner group is gone a self-onboarded account does not fall into
// the ungrouped pool.
func TestUngroupedPoolExcludesSelfOnboarded(t *testing.T) {
	ctx := context.Background()
	f := newHostedFixture(t)
	owner := f.user(t)
	proxy := f.proxy(t, service.StatusActive)
	repo := f.repo.accounts
	plain := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "p2c ungrouped plain", Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, ProxyID: &proxy.ID})
	self := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "p2c ungrouped self", Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, ProxyID: &proxy.ID})
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET owner_user_id=$2 WHERE id = ANY($1)", pq.Array([]int64{plain.ID, self.ID}), owner.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET self_onboarded=TRUE WHERE id=$1", self.ID)
	require.NoError(t, err)
	ids := func(list []service.Account) map[int64]bool {
		out := map[int64]bool{}
		for _, a := range list {
			if a.ID == plain.ID || a.ID == self.ID {
				out[a.ID] = true
			}
		}
		return out
	}
	one, err := repo.ListSchedulableUngroupedByPlatform(ctx, service.PlatformAnthropic)
	require.NoError(t, err)
	require.Equal(t, map[int64]bool{plain.ID: true}, ids(one))
	many, err := repo.ListSchedulableUngroupedByPlatforms(ctx, []string{service.PlatformAnthropic})
	require.NoError(t, err)
	require.Equal(t, map[int64]bool{plain.ID: true}, ids(many))
}
