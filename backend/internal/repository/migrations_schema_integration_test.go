//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMigrationsRunner_IsIdempotent_AndSchemaIsUpToDate(t *testing.T) {
	tx := testTx(t)

	// Re-apply migrations to verify idempotency (no errors, no duplicate rows).
	require.NoError(t, ApplyMigrations(context.Background(), integrationDB))

	// schema_migrations should have at least the current migration set.
	var applied int
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&applied))
	require.GreaterOrEqual(t, applied, 7, "expected schema_migrations to contain applied migrations")

	// users: columns required by repository queries
	requireColumn(t, tx, "users", "username", "character varying", 100, false)
	requireColumn(t, tx, "users", "notes", "text", 0, false)

	// accounts: schedulable and rate-limit fields
	requireColumn(t, tx, "accounts", "notes", "text", 0, true)
	requireColumn(t, tx, "accounts", "schedulable", "boolean", 0, false)
	requireColumn(t, tx, "accounts", "rate_limited_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "accounts", "rate_limit_reset_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "accounts", "overload_until", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "accounts", "session_window_status", "character varying", 20, true)

	// api_keys: key length should be 128
	requireColumn(t, tx, "api_keys", "key", "character varying", 128, false)

	// redeem_codes: subscription fields
	requireColumn(t, tx, "redeem_codes", "group_id", "bigint", 0, true)
	requireColumn(t, tx, "redeem_codes", "validity_days", "integer", 0, false)

	// usage_logs: billing_type used by filters/stats
	requireColumn(t, tx, "usage_logs", "billing_type", "smallint", 0, false)
	requireColumn(t, tx, "usage_logs", "request_type", "smallint", 0, false)
	requireColumn(t, tx, "usage_logs", "openai_ws_mode", "boolean", 0, false)

	// usage_billing_dedup: billing idempotency narrow table
	var usageBillingDedupRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.usage_billing_dedup')").Scan(&usageBillingDedupRegclass))
	require.True(t, usageBillingDedupRegclass.Valid, "expected usage_billing_dedup table to exist")
	requireColumn(t, tx, "usage_billing_dedup", "request_fingerprint", "character varying", 64, false)
	requireIndex(t, tx, "usage_billing_dedup", "idx_usage_billing_dedup_request_api_key")
	requireIndex(t, tx, "usage_billing_dedup", "idx_usage_billing_dedup_created_at_brin")

	var usageBillingDedupArchiveRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.usage_billing_dedup_archive')").Scan(&usageBillingDedupArchiveRegclass))
	require.True(t, usageBillingDedupArchiveRegclass.Valid, "expected usage_billing_dedup_archive table to exist")
	requireColumn(t, tx, "usage_billing_dedup_archive", "request_fingerprint", "character varying", 64, false)
	requireIndex(t, tx, "usage_billing_dedup_archive", "usage_billing_dedup_archive_pkey")

	// settings table should exist
	var settingsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.settings')").Scan(&settingsRegclass))
	require.True(t, settingsRegclass.Valid, "expected settings table to exist")

	// security_secrets table should exist
	var securitySecretsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.security_secrets')").Scan(&securitySecretsRegclass))
	require.True(t, securitySecretsRegclass.Valid, "expected security_secrets table to exist")

	var authIdentitiesRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.auth_identities')").Scan(&authIdentitiesRegclass))
	require.True(t, authIdentitiesRegclass.Valid, "expected auth_identities table to exist")
	requireIndex(t, tx, "auth_identities", "auth_identities_provider_subject_key")
	requireIndex(t, tx, "auth_identities", "auth_identities_user_provider_key")

	var oauthSessionsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.oauth_registration_sessions')").Scan(&oauthSessionsRegclass))
	require.True(t, oauthSessionsRegclass.Valid, "expected oauth_registration_sessions table to exist")
	requireIndex(t, tx, "oauth_registration_sessions", "oauth_registration_sessions_token_hash_key")

	// user_allowed_groups table should exist
	var uagRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.user_allowed_groups')").Scan(&uagRegclass))
	require.True(t, uagRegclass.Valid, "expected user_allowed_groups table to exist")

	// user_subscriptions: deleted_at for soft delete support (migration 012)
	requireColumn(t, tx, "user_subscriptions", "deleted_at", "timestamp with time zone", 0, true)

	// orphan_allowed_groups_audit table should exist (migration 013)
	var orphanAuditRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.orphan_allowed_groups_audit')").Scan(&orphanAuditRegclass))
	require.True(t, orphanAuditRegclass.Valid, "expected orphan_allowed_groups_audit table to exist")

	// account_groups: created_at should be timestamptz
	requireColumn(t, tx, "account_groups", "created_at", "timestamp with time zone", 0, false)

	// user_allowed_groups: created_at should be timestamptz
	requireColumn(t, tx, "user_allowed_groups", "created_at", "timestamp with time zone", 0, false)
}

func TestOAuthRegistrationSessionForUpdateAllowsOnlyOneConsumer(t *testing.T) {
	ctx := context.Background()
	tokenHash := fmt.Sprintf("%064x", time.Now().UnixNano())
	var sessionID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO oauth_registration_sessions (token_hash, provider, subject, verified_email, username, expires_at)
VALUES ($1, 'github', 'subject-one-time', 'one-time@example.com', 'one-time', NOW() + INTERVAL '10 minutes')
RETURNING id
`, tokenHash).Scan(&sessionID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM oauth_registration_sessions WHERE id = $1", sessionID)
	})

	first, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = first.Rollback() }()
	var lockedID int64
	require.NoError(t, first.QueryRowContext(ctx, `
SELECT id FROM oauth_registration_sessions WHERE token_hash = $1 FOR UPDATE
`, tokenHash).Scan(&lockedID))

	queryStarted := make(chan struct{})
	secondResult := make(chan error, 1)
	go func() {
		second, beginErr := integrationDB.BeginTx(ctx, nil)
		if beginErr != nil {
			secondResult <- beginErr
			return
		}
		defer func() { _ = second.Rollback() }()
		close(queryStarted)
		var id int64
		queryErr := second.QueryRowContext(ctx, `
SELECT id FROM oauth_registration_sessions WHERE token_hash = $1 FOR UPDATE
`, tokenHash).Scan(&id)
		secondResult <- queryErr
	}()
	<-queryStarted
	require.NoError(t, deleteOAuthRegistrationSession(ctx, first, sessionID))
	require.NoError(t, first.Commit())
	require.ErrorIs(t, <-secondResult, sql.ErrNoRows)
}

func TestAuthIdentityProviderSubjectIsUniqueUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	userIDs := make([]int64, 2)
	for i := range userIDs {
		email := fmt.Sprintf("oauth-concurrency-%d-%d@example.com", suffix, i)
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO users (email, password_hash) VALUES ($1, 'not-used') RETURNING id
`, email).Scan(&userIDs[i]))
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id IN ($1, $2)", userIDs[0], userIDs[1])
	})

	start := make(chan struct{})
	results := make(chan error, len(userIDs))
	var wg sync.WaitGroup
	for _, userID := range userIDs {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			<-start
			_, err := integrationDB.ExecContext(ctx, `
INSERT INTO auth_identities (user_id, provider, subject, verified_email, verified_at)
VALUES ($1, 'google', $2, 'concurrent@example.com', NOW())
`, id, fmt.Sprintf("concurrent-subject-%d", suffix))
			results <- err
		}(userID)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	failures := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, failures)
}

func deleteOAuthRegistrationSession(ctx context.Context, tx *sql.Tx, id int64) error {
	result, err := tx.ExecContext(ctx, "DELETE FROM oauth_registration_sessions WHERE id = $1", id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("oauth registration session was not consumed")
	}
	return nil
}

func requireIndex(t *testing.T, tx *sql.Tx, table, index string) {
	t.Helper()

	var exists bool
	err := tx.QueryRowContext(context.Background(), `
SELECT EXISTS (
	SELECT 1
	FROM pg_indexes
	WHERE schemaname = 'public'
	  AND tablename = $1
	  AND indexname = $2
)
`, table, index).Scan(&exists)
	require.NoError(t, err, "query pg_indexes for %s.%s", table, index)
	require.True(t, exists, "expected index %s on %s", index, table)
}

func requireColumn(t *testing.T, tx *sql.Tx, table, column, dataType string, maxLen int, nullable bool) {
	t.Helper()

	var row struct {
		DataType string
		MaxLen   sql.NullInt64
		Nullable string
	}

	err := tx.QueryRowContext(context.Background(), `
SELECT
  data_type,
  character_maximum_length,
  is_nullable
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = $1
  AND column_name = $2
`, table, column).Scan(&row.DataType, &row.MaxLen, &row.Nullable)
	require.NoError(t, err, "query information_schema.columns for %s.%s", table, column)
	require.Equal(t, dataType, row.DataType, "data_type mismatch for %s.%s", table, column)

	if maxLen > 0 {
		require.True(t, row.MaxLen.Valid, "expected maxLen for %s.%s", table, column)
		require.Equal(t, int64(maxLen), row.MaxLen.Int64, "maxLen mismatch for %s.%s", table, column)
	}

	if nullable {
		require.Equal(t, "YES", row.Nullable, "nullable mismatch for %s.%s", table, column)
	} else {
		require.Equal(t, "NO", row.Nullable, "nullable mismatch for %s.%s", table, column)
	}
}
