//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// ---- fixture (committed rows, hard-deleted in t.Cleanup) ----

type marketFixture struct {
	market   *marketListingRepository
	accounts *accountRepository
	groups   *groupRepository
	owners   *hostedOwnerRepository
	apps     *hostedApplicationRepository

	users      []int64
	groupIDs   []int64
	accountIDs []int64
}

func newMarketFixture(t *testing.T) *marketFixture {
	t.Helper()
	ctx := context.Background()
	accounts := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	f := &marketFixture{
		market:   newMarketListingRepository(integrationEntClient, integrationDB, nil),
		accounts: accounts,
		groups:   newGroupRepositoryWithSQL(integrationEntClient, integrationDB),
		owners:   &hostedOwnerRepository{db: integrationDB, accounts: accounts},
		apps:     &hostedApplicationRepository{client: integrationEntClient, db: integrationDB, accounts: accounts},
	}
	var previous sql.NullString
	err := integrationDB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", service.SettingKeyMarketEnabled).Scan(&previous)
	if err != nil {
		require.ErrorIs(t, err, sql.ErrNoRows)
	}
	f.setMarketEnabled(t, true)
	t.Cleanup(func() {
		bg := context.Background()
		if previous.Valid {
			_, _ = integrationDB.ExecContext(bg, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyMarketEnabled, previous.String)
		} else {
			_, _ = integrationDB.ExecContext(bg, "DELETE FROM settings WHERE key=$1", service.SettingKeyMarketEnabled)
		}
		accountIDs := pq.Array(f.accountIDs)
		groupIDs := pq.Array(f.groupIDs)
		users := pq.Array(f.users)
		// History tables reject DELETE (insert-only triggers); test cleanup
		// bypasses triggers as the superuser.
		if tx, err := integrationDB.BeginTx(bg, nil); err == nil {
			_, _ = tx.ExecContext(bg, "SET LOCAL session_replication_role = replica")
			_, _ = tx.ExecContext(bg, "DELETE FROM market_admissions WHERE account_id = ANY($1)", accountIDs)
			_, _ = tx.ExecContext(bg, "DELETE FROM account_market_listing_events WHERE account_id = ANY($1)", accountIDs)
			_ = tx.Commit()
		}
		for _, stmt := range []struct {
			q    string
			args []any
		}{
			{"DELETE FROM account_market_listings WHERE account_id = ANY($1)", []any{accountIDs}},
			{"DELETE FROM account_owner_events WHERE account_id = ANY($1)", []any{accountIDs}},
			{"DELETE FROM scheduler_outbox WHERE account_id = ANY($1) OR group_id = ANY($2)", []any{accountIDs, groupIDs}},
			{"DELETE FROM account_groups WHERE account_id = ANY($1) OR group_id = ANY($2)", []any{accountIDs, groupIDs}},
			{"DELETE FROM user_group_rate_multipliers WHERE group_id = ANY($1)", []any{groupIDs}},
			{"DELETE FROM accounts WHERE id = ANY($1)", []any{accountIDs}},
			{"UPDATE groups SET fallback_group_id = NULL, fallback_group_id_on_invalid_request = NULL WHERE id = ANY($1)", []any{groupIDs}},
			{"DELETE FROM groups WHERE id = ANY($1)", []any{groupIDs}},
			{"DELETE FROM users WHERE id = ANY($1)", []any{users}},
		} {
			_, _ = integrationDB.ExecContext(bg, stmt.q, stmt.args...)
		}
	})
	return f
}

func (f *marketFixture) setMarketEnabled(t *testing.T, on bool) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, service.SettingKeyMarketEnabled, fmt.Sprint(on))
	require.NoError(t, err)
}

func (f *marketFixture) user(t *testing.T) int64 {
	t.Helper()
	u := mustCreateUser(t, integrationEntClient, &service.User{Email: "market-" + uuid.NewString() + "@example.invalid"})
	f.users = append(f.users, u.ID)
	return u.ID
}

func (f *marketFixture) group(t *testing.T, tier bool) int64 {
	t.Helper()
	g := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "market-" + uuid.NewString(), Platform: service.PlatformAnthropic, RateMultiplier: 1.2})
	f.groupIDs = append(f.groupIDs, g.ID)
	if tier {
		_, err := integrationDB.ExecContext(context.Background(), "UPDATE groups SET market_tier = TRUE WHERE id = $1", g.ID)
		require.NoError(t, err)
	}
	return g.ID
}

// account creates a setup-token carpool account (finite device limit) with
// an owner assigned through the API (owner event written).
func (f *marketFixture) account(t *testing.T, owner int64) int64 {
	t.Helper()
	acc := mustCreateAccount(t, integrationEntClient, &service.Account{
		Name: "market " + uuid.NewString(), Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken,
		Concurrency: 4, Extra: map[string]any{"claude_oauth_mode": "carpool", "claude_oauth_carpool_device_limit": 5},
	})
	f.accountIDs = append(f.accountIDs, acc.ID)
	if owner > 0 {
		_, err := f.owners.SetAccountOwner(context.Background(), acc.ID, &owner, 1)
		require.NoError(t, err)
	}
	return acc.ID
}

func (f *marketFixture) configure(t *testing.T, accountID int64, allowed ...int64) *service.MarketListing {
	t.Helper()
	l, err := f.market.AdminConfigure(context.Background(), service.MarketAdminConfigureParams{
		AccountID: accountID, AdminUserID: 1, AllowedGroupIDs: allowed, Defaults: marketTestDefaults(),
	})
	require.NoError(t, err)
	return l
}

// list goes through the service (its account checks run under the lock),
// then returns the stored listing.
func (f *marketFixture) list(accountID, owner, groupID int64) (*service.MarketListing, error) {
	svc := service.NewMarketListingService(f.market, NewSettingRepository(integrationEntClient))
	if _, err := svc.OwnerSet(context.Background(), owner, accountID, groupID, true); err != nil {
		return nil, err
	}
	return f.market.GetListing(context.Background(), accountID)
}

func marketTestDefaults() service.MarketProtectionParams {
	return service.MarketProtectionParams{ReservedConcurrency: 1, ReservedDevices: 2, ReservedAdmissions: 1, Cap5hPct: 70, Cap7dPct: 60, SampleMaxAgeMinutes: 30}
}

func marketMemberships(t *testing.T, accountID int64) map[int64]string {
	t.Helper()
	rows, err := integrationDB.QueryContext(context.Background(), "SELECT group_id, source FROM account_groups WHERE account_id = $1", accountID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var source string
		require.NoError(t, rows.Scan(&id, &source))
		out[id] = source
	}
	require.NoError(t, rows.Err())
	return out
}

func marketListingState(t *testing.T, f *marketFixture, accountID int64) *service.MarketListing {
	t.Helper()
	l, err := f.market.GetListing(context.Background(), accountID)
	require.NoError(t, err)
	require.NotNil(t, l)
	return l
}

func marketEventActions(t *testing.T, accountID int64) []string {
	t.Helper()
	rows, err := integrationDB.QueryContext(context.Background(), "SELECT action FROM account_market_listing_events WHERE account_id = $1 ORDER BY version", accountID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var a string
		require.NoError(t, rows.Scan(&a))
		out = append(out, a)
	}
	return out
}

func marketOutboxCount(t *testing.T, accountID int64, eventType string) int {
	t.Helper()
	return hostedCountRows(t, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1 AND event_type = $2", accountID, eventType)
}

// ---- migration ----

func migrationsThrough133(t *testing.T) fstest.MapFS {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	result := make(fstest.MapFS)
	for _, name := range files {
		if name >= "134_" {
			continue
		}
		data, readErr := fs.ReadFile(migrations.FS, name)
		require.NoError(t, readErr)
		result[name] = &fstest.MapFile{Data: data}
	}
	return result
}

func TestMigration134UpgradeKeepsMembershipsAndShadowEvents(t *testing.T) {
	ctx := context.Background()
	db := openIsolatedPaymentMigrationDatabase(t)
	require.NoError(t, applyMigrationsFS(ctx, db, migrationsThrough133(t)))

	var userID, groupID, accountID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash) VALUES ('m134@example.invalid', 'x') RETURNING id`).Scan(&userID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups (name, platform, rate_multiplier, subscription_type, status) VALUES ('m134', 'anthropic', 1, 'standard', 'active') RETURNING id`).Scan(&groupID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO accounts (name, platform, type, credentials, extra, concurrency, priority, status, schedulable)
		VALUES ('m134', 'anthropic', 'setup-token', '{}', '{}', 1, 1, 'active', TRUE) RETURNING id`).Scan(&accountID))
	_, err := db.ExecContext(ctx, `INSERT INTO account_groups (account_id, group_id, priority) VALUES ($1, $2, 1)`, accountID, groupID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO usage_settlement_events (request_id, api_key_id, category, rule_version, anomaly, buyer_user_id,
		account_id, owner_user_id, owner_source, self_purchase, group_id, fee_rate, standard_cost, charge_input, charge_source)
		VALUES ('m134-req', 1, 'hosted_self', 1, FALSE, $1, $2, $1, 'settlement', TRUE, $3, 0.1, 1.5, 0.15, 'balance')`, userID, accountID, groupID)
	require.NoError(t, err)

	require.NoError(t, applyMigrationsFS(ctx, db, migrations.FS))
	require.NoError(t, applyMigrationsFS(ctx, db, migrations.FS), "re-applying is a no-op")
	// The file itself is idempotent (disaster-recovery replay).
	body, err := migrations.FS.ReadFile("134_market_listings.sql")
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())

	var source string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT source FROM account_groups WHERE account_id = $1 AND group_id = $2", accountID, groupID).Scan(&source))
	require.Equal(t, "admin", source, "existing memberships become admin rows")
	var tier bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT market_tier FROM groups WHERE id = $1", groupID).Scan(&tier))
	require.False(t, tier)
	// The old shadow event still satisfies every constraint (touching the
	// row re-checks them).
	_, err = db.ExecContext(ctx, "UPDATE usage_settlement_events SET anomaly = anomaly WHERE request_id = 'm134-req'")
	require.NoError(t, err)
	var convalidated bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT bool_and(convalidated) FROM pg_constraint
		WHERE conrelid = 'usage_settlement_events'::regclass AND contype = 'c'`).Scan(&convalidated))
	require.True(t, convalidated)

	// Shadow categories stay pinned to 0; shared_sale needs an admission and
	// an exact split.
	insertEvent := func(req, category string, earning string, extra string) error {
		_, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO usage_settlement_events (request_id, api_key_id, category, rule_version,
			buyer_user_id, account_id, owner_source, self_purchase, standard_cost, charge_input, charge_source, owner_earning%s)
			VALUES ($1, 1, $2, 1, $3, $4, 'admission', FALSE, 1, 1, 'balance', %s%s)`, extraCols(extra), earning, extraVals(extra)),
			req, category, userID, accountID)
		return err
	}
	require.Error(t, insertEvent("m134-a", "owned_other", "0.5", ""), "positive earning only on shared_sale")
	require.Error(t, insertEvent("m134-b", "shared_sale", "0.8", ""), "shared_sale needs admission and money columns")
	require.Error(t, insertEvent("m134-c", "shared_sale", "0.8", "admission_id=1,charged_amount=1,platform_amount=0.3,platform_cut_rate=0.2,earning_available_at=NOW()"), "split must be exact")
	require.Error(t, insertEvent("m134-d", "shared_sale", "1.2", "admission_id=2,charged_amount=1,platform_amount=-0.2,platform_cut_rate=0.2,earning_available_at=NOW()"), "earning above charge")
	require.Error(t, insertEvent("m134-e", "shared_sale", "0", "admission_id=3,charged_amount=1,platform_amount=1,platform_cut_rate=0.2,earning_available_at=NOW()"), "zero earning needs a reason")
	require.Error(t, insertEvent("m134-f", "hosted_self", "0", "earning_zero_reason='other'"), "market columns only on shared_sale")
	require.NoError(t, insertEvent("m134-g", "shared_sale", "0.8", "admission_id=4,charged_amount=1,platform_amount=0.2,platform_cut_rate=0.2,earning_available_at=NOW()"))
	require.NoError(t, insertEvent("m134-h", "shared_sale", "0", "admission_id=5,charged_amount=1,platform_amount=1,platform_cut_rate=0.2,earning_zero_reason='self_purchase',earning_available_at=NOW()"))
	err = insertEvent("m134-i", "shared_sale", "0.8", "admission_id=4,charged_amount=1,platform_amount=0.2,platform_cut_rate=0.2,earning_available_at=NOW()")
	requirePQCode(t, err, "23505", "one event per admission")
	require.Error(t, insertEvent("m134-k", "shared_sale", "0.8", "admission_id=7,charged_amount=1,platform_amount=0.2,platform_cut_rate=0.2"), "freeze snapshot required on shared_sale")
	require.Error(t, insertEvent("m134-l", "owned_other", "0", "earning_available_at=NOW()"), "freeze snapshot only on shared_sale")
	require.Error(t, insertEvent("m134-j", "shared_sale", "0", "admission_id=6,charged_amount=1,platform_amount=1,platform_cut_rate=0.2,earning_zero_reason='bogus',earning_available_at=NOW()"))

	// groups.market_tier CHECK.
	for _, bad := range []string{
		"UPDATE groups SET market_tier = TRUE, rate_multiplier = 0 WHERE id = $1",
		"UPDATE groups SET market_tier = TRUE, subscription_type = 'subscription' WHERE id = $1",
		"UPDATE groups SET market_tier = TRUE, platform = 'openai' WHERE id = $1",
		"UPDATE groups SET market_tier = TRUE, hosted_usage_enabled = TRUE WHERE id = $1",
	} {
		_, err := db.ExecContext(ctx, bad, groupID)
		require.Error(t, err, bad)
	}
	_, err = db.ExecContext(ctx, "UPDATE groups SET market_tier = TRUE WHERE id = $1", groupID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE account_groups SET source = 'other' WHERE account_id = $1", accountID)
	require.Error(t, err)
}

func extraCols(extra string) string {
	if extra == "" {
		return ""
	}
	var cols []string
	for _, kv := range strings.Split(extra, ",") {
		cols = append(cols, strings.SplitN(kv, "=", 2)[0])
	}
	return ", " + strings.Join(cols, ", ")
}

func extraVals(extra string) string {
	if extra == "" {
		return ""
	}
	var vals []string
	for _, kv := range strings.Split(extra, ",") {
		vals = append(vals, strings.SplitN(kv, "=", 2)[1])
	}
	return ", " + strings.Join(vals, ", ")
}

func requirePQCode(t *testing.T, err error, code string, msg string) {
	t.Helper()
	require.Error(t, err, msg)
	var pqErr *pq.Error
	require.True(t, errors.As(err, &pqErr), msg)
	require.Equal(t, code, string(pqErr.Code), msg)
}

func TestMarketLedgerConstraints(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	var eventID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO usage_settlement_events (request_id, api_key_id, category, rule_version,
		buyer_user_id, account_id, owner_source, self_purchase, standard_cost, charge_input, charge_source, owner_earning,
		admission_id, charged_amount, platform_amount, platform_cut_rate, earning_available_at)
		VALUES ($1, 1, 'shared_sale', 1, 7, 8, 'admission', FALSE, 1, 1, 'balance', 0.8, $2, 1, 0.2, 0.2, NOW()) RETURNING id`,
		"ledger-"+uuid.NewString(), time.Now().UnixNano()).Scan(&eventID))

	step := func(name, query string, args ...any) error {
		_, err := tx.ExecContext(ctx, "SAVEPOINT s")
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			_, rbErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT s")
			require.NoError(t, rbErr, name)
		} else {
			_, relErr := tx.ExecContext(ctx, "RELEASE SAVEPOINT s")
			require.NoError(t, relErr, name)
		}
		return err
	}
	var accrueID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, settlement_event_id, available_at)
		VALUES (9, 'accrue', 0.8, $1, NOW() + INTERVAL '7 days') RETURNING id`, eventID).Scan(&accrueID))
	requirePQCode(t, step("accrue twice", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, settlement_event_id, available_at)
		VALUES (9, 'accrue', 0.8, $1, NOW())`, eventID), "23505", "one accrue per event")
	require.Error(t, step("accrue negative", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, settlement_event_id, available_at)
		VALUES (9, 'accrue', -0.8, $1, NOW())`, eventID))
	require.NoError(t, step("reverse", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, reverses_entry_id, settlement_event_id, available_at)
		VALUES (9, 'reverse', -0.8, $1, $2, NOW() + INTERVAL '7 days')`, accrueID, eventID))
	requirePQCode(t, step("reverse twice", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, reverses_entry_id, available_at)
		VALUES (9, 'reverse', -0.8, $1, NOW())`, accrueID), "23505", "one reverse per accrue")
	require.Error(t, step("reverse positive", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, reverses_entry_id, available_at)
		VALUES (9, 'reverse', 0.8, $1, NOW())`, accrueID))
	require.NoError(t, step("convert", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, convert_request_id) VALUES (9, 'convert', -1, 'req-1')`))
	requirePQCode(t, step("convert duplicate", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, convert_request_id) VALUES (9, 'convert', -2, 'req-1')`), "23505", "one convert per request id")
	require.NoError(t, step("convert other owner", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, convert_request_id) VALUES (10, 'convert', -1, 'req-1')`))
	require.Error(t, step("convert positive", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, convert_request_id) VALUES (9, 'convert', 1, 'req-2')`))
	require.Error(t, step("convert without request", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount) VALUES (9, 'convert', -1)`))
	require.Error(t, step("accrue without event", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, available_at) VALUES (9, 'accrue', 1, NOW())`))
	require.Error(t, step("unknown kind", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount) VALUES (9, 'bonus', 1)`))
	require.Error(t, step("update", `UPDATE owner_earning_entries SET note = 'x' WHERE id = $1`, accrueID), "entries are insert-only")
	require.Error(t, step("delete", `DELETE FROM owner_earning_entries WHERE kind = 'convert' AND owner_user_id = 10`), "entries are insert-only")
	var freshEventID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO usage_settlement_events (request_id, api_key_id, category, rule_version,
		buyer_user_id, account_id, owner_source, self_purchase, standard_cost, charge_input, charge_source, owner_earning,
		admission_id, charged_amount, platform_amount, platform_cut_rate, earning_available_at)
		VALUES ($1, 1, 'shared_sale', 1, 7, 8, 'admission', FALSE, 1, 1, 'balance', 0.8, $2, 1, 0.2, 0.2, NOW()) RETURNING id`,
		"ledger-"+uuid.NewString(), time.Now().UnixNano()+1).Scan(&freshEventID))
	requirePQCode(t, step("NaN accrue", `INSERT INTO owner_earning_entries (owner_user_id, kind, amount, settlement_event_id, available_at)
		VALUES (11, 'accrue', 'NaN', $1, NOW())`, freshEventID), "23514", "NaN is not a positive amount")
	requirePQCode(t, step("NaN split", `INSERT INTO usage_settlement_events (request_id, api_key_id, category, rule_version,
		buyer_user_id, account_id, owner_source, self_purchase, standard_cost, charge_input, charge_source, owner_earning,
		admission_id, charged_amount, platform_amount, platform_cut_rate, earning_available_at)
		VALUES ($1, 1, 'shared_sale', 1, 7, 8, 'admission', FALSE, 1, 1, 'balance', 'NaN', $2, 'NaN', 'NaN', 0.2, NOW())`,
		"ledger-"+uuid.NewString(), time.Now().UnixNano()+2), "23514", "NaN split rejected")
	require.NoError(t, step("wallet", `INSERT INTO owner_earning_wallets (user_id) VALUES (9)`))
	requirePQCode(t, step("wallet twice", `INSERT INTO owner_earning_wallets (user_id) VALUES (9)`), "23505", "one wallet per user")
}

func TestMarketAdmissionStore(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	buyer := f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	_, err := f.list(acc, owner, tier)
	require.NoError(t, err)

	// The check sees the authoritative state and fills the pinned versions.
	var seen *service.MarketAdmissionState
	fill := func(st *service.MarketAdmissionState, in *service.MarketAdmission) error {
		seen = st
		if st.Listing == nil || st.Listing.Status != service.MarketListingStatusListed || !st.MarketMember || !st.GroupMarketTier {
			return errors.New("not listed")
		}
		in.OwnerUserID = *st.Account.OwnerUserID
		in.OwnerEventID = st.OwnerEventID
		in.ListingVersion = st.Listing.Version
		return nil
	}
	in := &service.MarketAdmission{AttemptID: "att-" + uuid.NewString(), RequestRef: "req-" + uuid.NewString(), AccountID: acc, GroupID: tier,
		BuyerUserID: buyer, APIKeyID: 3}
	got, err := f.market.AdmitMarket(ctx, in, fill)
	require.NoError(t, err)
	require.Positive(t, got.ID)
	require.WithinDuration(t, time.Now(), got.AdmittedAt, time.Minute)
	require.Equal(t, service.StatusActive, seen.OwnerUserStatus)
	require.False(t, seen.AdminMember)
	require.Equal(t, owner, *seen.OwnerEventUserID)
	require.False(t, seen.DBNow.IsZero())
	require.Equal(t, owner, got.OwnerUserID)
	require.Equal(t, seen.Listing.Version, got.ListingVersion)
	_, err = f.market.AdmitMarket(ctx, in, fill)
	require.ErrorIs(t, err, service.ErrMarketAdmissionAttemptExists)
	read, err := f.market.GetMarketAdmission(ctx, got.ID)
	require.NoError(t, err)
	require.Equal(t, in.AttemptID, read.AttemptID)
	require.Equal(t, in.RequestRef, read.RequestRef)
	require.Equal(t, owner, read.OwnerUserID)
	byAttempt, err := f.market.GetMarketAdmissionByAttempt(ctx, in.AttemptID)
	require.NoError(t, err)
	require.Equal(t, got.ID, byAttempt.ID)
	_, err = f.market.GetMarketAdmission(ctx, -1)
	require.ErrorIs(t, err, service.ErrMarketAdmissionNotFound)
	_, err = integrationDB.ExecContext(ctx, "UPDATE market_admissions SET owner_user_id = 1 WHERE id = $1", got.ID)
	require.Error(t, err, "admissions are immutable")
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM market_admissions WHERE id = $1", got.ID)
	require.Error(t, err, "admissions are never deleted")

	// A failing check inserts nothing.
	_, err = f.market.OwnerSet(ctx, service.MarketOwnerSetParams{OwnerUserID: owner, AccountID: acc, Listed: false})
	require.NoError(t, err)
	refused := &service.MarketAdmission{AttemptID: "att-" + uuid.NewString(), RequestRef: "req-x", AccountID: acc, GroupID: tier, BuyerUserID: buyer, APIKeyID: 3}
	_, err = f.market.AdmitMarket(ctx, refused, fill)
	require.EqualError(t, err, "not listed")
	_, err = f.market.GetMarketAdmissionByAttempt(ctx, refused.AttemptID)
	require.ErrorIs(t, err, service.ErrMarketAdmissionNotFound)

	// The listing share lock serializes with a listing write in flight.
	_, err = f.list(acc, owner, tier)
	require.NoError(t, err)
	writer, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = writer.Rollback() }()
	_, err = lockMarketAccountTx(ctx, writer, acc)
	require.NoError(t, err)
	require.NoError(t, marketUnlistAccountTx(ctx, writer, acc, service.MarketListingActionOwnerUnlist, false, service.MarketListingActorOwner, owner))
	done := make(chan error, 1)
	go func() {
		_, err := f.market.AdmitMarket(ctx, &service.MarketAdmission{AttemptID: "att-" + uuid.NewString(), RequestRef: "req-y", AccountID: acc,
			GroupID: tier, BuyerUserID: buyer, APIKeyID: 3}, fill)
		done <- err
	}()
	waitForLockWait(t)
	require.NoError(t, writer.Commit())
	select {
	case err := <-done:
		require.EqualError(t, err, "not listed", "the admission saw the committed unlist")
	case <-time.After(10 * time.Second):
		t.Fatal("admission did not finish")
	}
}

// ---- listing lifecycle ----

func TestMarketListingBaselineOwnerEventAndList(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, 0)
	// Owner written outside the API: no ownership history.
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET owner_user_id = $2 WHERE id = $1", acc, owner)
	require.NoError(t, err)
	require.Zero(t, hostedCountRows(t, "SELECT COUNT(*) FROM account_owner_events WHERE account_id = $1", acc))

	l := f.configure(t, acc, tier)
	require.Equal(t, service.MarketListingStatusUnlisted, l.Status)
	require.Equal(t, int64(1), l.Version)
	require.Equal(t, owner, l.OwnerUserID)
	require.Equal(t, marketTestDefaults(), l.Protection)

	l, err = f.list(acc, owner, tier)
	require.NoError(t, err)
	require.Equal(t, service.MarketListingStatusListed, l.Status)
	require.Equal(t, tier, *l.SelectedGroupID)
	require.Equal(t, int64(2), l.Version)
	require.NotNil(t, l.ListedAt)
	require.Equal(t, map[int64]string{tier: "market"}, marketMemberships(t, acc))
	var newOwner sql.NullInt64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT new_owner_user_id FROM account_owner_events WHERE account_id = $1", acc).Scan(&newOwner))
	require.Equal(t, owner, newOwner.Int64, "baseline owner event written on first listing")
	require.Equal(t, []string{"admin_configure", "owner_list"}, marketEventActions(t, acc))
	// Listing: one event rebuilds the tier, one (no group list) the
	// account's own buckets — here the ungrouped bucket it leaves. The admin
	// configure before it wrote one snapshot refresh.
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1 AND event_type = $2 AND payload IS NOT NULL",
		acc, service.SchedulerOutboxEventAccountGroupsChanged))
	require.Equal(t, 2, hostedCountRows(t, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1 AND event_type = $2 AND payload IS NULL",
		acc, service.SchedulerOutboxEventAccountGroupsChanged))

	// Listing again is a no-op; the baseline is written once.
	l, err = f.list(acc, owner, tier)
	require.NoError(t, err)
	require.Equal(t, int64(2), l.Version)
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM account_owner_events WHERE account_id = $1", acc))
	// Accounts with history keep using it (no extra baseline).
	acc2 := f.account(t, owner)
	f.configure(t, acc2, tier)
	_, err = f.list(acc2, owner, tier)
	require.NoError(t, err)
	require.Equal(t, 1, hostedCountRows(t, "SELECT COUNT(*) FROM account_owner_events WHERE account_id = $1", acc2))

	// Switching tier moves the market row.
	tier2 := f.group(t, true)
	f.configure(t, acc, tier, tier2)
	l, err = f.list(acc, owner, tier2)
	require.NoError(t, err)
	require.Equal(t, tier2, *l.SelectedGroupID)
	require.Equal(t, map[int64]string{tier2: "market"}, marketMemberships(t, acc))
	require.Equal(t, "owner_switch", marketEventActions(t, acc)[3])

	// Unlist keeps the selection, removes the row.
	l, err = f.market.OwnerSet(ctx, service.MarketOwnerSetParams{OwnerUserID: owner, AccountID: acc, Listed: false})
	require.NoError(t, err)
	require.Equal(t, service.MarketListingStatusUnlisted, l.Status)
	require.Equal(t, tier2, *l.SelectedGroupID)
	require.Empty(t, marketMemberships(t, acc))
}

func TestMarketListingOwnerScopeAndOverreach(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	ownerA := f.user(t)
	ownerB := f.user(t)
	tier := f.group(t, true)
	other := f.group(t, true)
	plain := f.group(t, false)
	accA := f.account(t, ownerA)
	accB := f.account(t, ownerB)
	f.configure(t, accA, tier)
	f.configure(t, accB, tier)

	// Owner of B / non-owner / historic owner / deleted account → 404, and
	// nothing is written (checked at the repository, not only by the view).
	_, err := f.list(accA, ownerB, tier)
	requireReason(t, err, "HOSTED_ACCOUNT_NOT_FOUND")
	_, err = f.market.OwnerSet(ctx, service.MarketOwnerSetParams{OwnerUserID: ownerB, AccountID: accA, GroupID: tier, Listed: true})
	requireReason(t, err, "HOSTED_ACCOUNT_NOT_FOUND")
	require.Equal(t, service.MarketListingStatusUnlisted, marketListingState(t, f, accA).Status)
	require.Empty(t, marketMemberships(t, accA))
	_, err = f.market.GetOwnedAccountFacts(ctx, ownerB, accA)
	requireReason(t, err, "HOSTED_ACCOUNT_NOT_FOUND")
	// Not allowed / not a tier / unknown group.
	_, err = f.list(accA, ownerA, other)
	requireReason(t, err, "MARKET_GROUP_NOT_ALLOWED")
	_, err = f.market.AdminConfigure(ctx, service.MarketAdminConfigureParams{AccountID: accA, AllowedGroupIDs: []int64{tier, plain}, Defaults: marketTestDefaults()})
	requireReason(t, err, "MARKET_INVALID_TIER")
	_, err = f.market.AdminConfigure(ctx, service.MarketAdminConfigureParams{AccountID: accA, AllowedGroupIDs: []int64{tier, 1 << 40}, Defaults: marketTestDefaults()})
	requireReason(t, err, "MARKET_INVALID_TIER")
	// Switch off: listing refused, unlisting always allowed.
	f.setMarketEnabled(t, false)
	_, err = f.list(accA, ownerA, tier)
	requireReason(t, err, "MARKET_DISABLED")
	f.setMarketEnabled(t, true)
	_, err = f.list(accA, ownerA, tier)
	require.NoError(t, err)
	f.setMarketEnabled(t, false)
	l, err := f.market.OwnerSet(ctx, service.MarketOwnerSetParams{OwnerUserID: ownerA, AccountID: accA, Listed: false})
	require.NoError(t, err)
	require.Equal(t, service.MarketListingStatusUnlisted, l.Status)
	f.setMarketEnabled(t, true)

	// Not configured / no owner.
	accC := f.account(t, ownerA)
	_, err = f.list(accC, ownerA, tier)
	requireReason(t, err, "MARKET_ACCOUNT_NOT_ELIGIBLE")
	accNoOwner := f.account(t, 0)
	_, err = f.market.AdminConfigure(ctx, service.MarketAdminConfigureParams{AccountID: accNoOwner, AllowedGroupIDs: []int64{tier}, Defaults: marketTestDefaults()})
	requireReason(t, err, "MARKET_ACCOUNT_NO_OWNER")

	// Deleted account: owner and admin both get not found.
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET deleted_at = NOW() WHERE id = $1", accB)
	require.NoError(t, err)
	_, err = f.list(accB, ownerB, tier)
	requireReason(t, err, "HOSTED_ACCOUNT_NOT_FOUND")
	_, err = f.market.AdminConfigure(ctx, service.MarketAdminConfigureParams{AccountID: accB, AllowedGroupIDs: []int64{tier}, Defaults: marketTestDefaults()})
	require.ErrorIs(t, err, service.ErrAccountNotFound)
}

func TestMarketListingAdminRowConflictAndAssignOwnerNoAutoList(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, 0)
	require.NoError(t, f.accounts.BindGroups(ctx, acc, []int64{tier}))
	// Assigning an owner to an admin tier member does not list it.
	_, err := f.owners.SetAccountOwner(ctx, acc, &owner, 1)
	require.NoError(t, err)
	listing, err := f.market.GetListing(ctx, acc)
	require.NoError(t, err)
	require.Nil(t, listing)
	require.Equal(t, map[int64]string{tier: "admin"}, marketMemberships(t, acc))

	f.configure(t, acc, tier)
	_, err = f.list(acc, owner, tier)
	requireReason(t, err, "MARKET_ADMIN_MEMBERSHIP_CONFLICT")
	require.Equal(t, map[int64]string{tier: "admin"}, marketMemberships(t, acc))
	require.Equal(t, service.MarketListingStatusUnlisted, marketListingState(t, f, acc).Status)
}

func TestMarketListingAdminBindingPathsKeepMarketRows(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	plain := f.group(t, false)
	plain2 := f.group(t, false)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	_, err := f.list(acc, owner, tier)
	require.NoError(t, err)

	// BindGroups (delete-all / recreate) and the bulk edit (same call per account).
	require.NoError(t, f.accounts.BindGroups(ctx, acc, []int64{plain}))
	require.Equal(t, map[int64]string{tier: "market", plain: "admin"}, marketMemberships(t, acc))
	require.NoError(t, f.accounts.BindGroups(ctx, acc, nil))
	require.Equal(t, map[int64]string{tier: "market"}, marketMemberships(t, acc))
	err = f.accounts.BindGroups(ctx, acc, []int64{plain, tier})
	requireReason(t, err, "MARKET_MEMBERSHIP_CONFLICT")
	require.Equal(t, map[int64]string{tier: "market"}, marketMemberships(t, acc))
	// Single bind / unbind.
	requireReason(t, f.accounts.AddToGroup(ctx, acc, tier, 1), "MARKET_MEMBERSHIP_CONFLICT")
	require.NoError(t, f.accounts.RemoveFromGroup(ctx, acc, tier))
	require.Equal(t, map[int64]string{tier: "market"}, marketMemberships(t, acc))

	// Group side: copy skips market rows, replace keeps them, copy into the
	// tier leaves the market row as is.
	copied, err := f.groups.GetAccountIDsByGroupIDs(ctx, []int64{tier})
	require.NoError(t, err)
	require.NotContains(t, copied, acc)
	_, err = f.groups.DeleteAccountGroupsByGroupID(ctx, tier)
	require.NoError(t, err)
	require.Equal(t, map[int64]string{tier: "market"}, marketMemberships(t, acc))
	require.NoError(t, f.groups.BindAccountsToGroup(ctx, tier, []int64{acc}))
	require.Equal(t, map[int64]string{tier: "market"}, marketMemberships(t, acc))
	require.NoError(t, f.groups.BindAccountsToGroup(ctx, plain2, []int64{acc}))
	require.Equal(t, map[int64]string{tier: "market", plain2: "admin"}, marketMemberships(t, acc))

	// Loaded account: dispatchable groups exclude the market row.
	loaded, err := f.accounts.GetByID(ctx, acc)
	require.NoError(t, err)
	require.Equal(t, []int64{plain2}, loaded.GroupIDs)
	require.Equal(t, []int64{tier}, loaded.MarketGroupIDs)
	require.Equal(t, service.MarketListingStatusListed, marketListingState(t, f, acc).Status)
}

func TestMarketListedAccountNotSchedulable(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	listed := f.account(t, owner)
	selfRun := f.account(t, 0)
	require.NoError(t, f.accounts.BindGroups(ctx, selfRun, []int64{tier}))
	f.configure(t, listed, tier)
	_, err := f.list(listed, owner, tier)
	require.NoError(t, err)

	ids := func(accounts []service.Account) []int64 {
		var out []int64
		for _, a := range accounts {
			if a.ID == listed || a.ID == selfRun {
				out = append(out, a.ID)
			}
		}
		return out
	}
	byGroup, err := f.accounts.ListSchedulableByGroupID(ctx, tier)
	require.NoError(t, err)
	require.Equal(t, []int64{selfRun}, ids(byGroup), "market membership is not dispatchable in P3a part 1")
	byGroupPlatform, err := f.accounts.ListSchedulableByGroupIDAndPlatform(ctx, tier, service.PlatformAnthropic)
	require.NoError(t, err)
	require.Equal(t, []int64{selfRun}, ids(byGroupPlatform))
	byGroupPlatforms, err := f.accounts.ListSchedulableByGroupIDAndPlatforms(ctx, tier, []string{service.PlatformAnthropic})
	require.NoError(t, err)
	require.Equal(t, []int64{selfRun}, ids(byGroupPlatforms))
	byGroupAll, err := f.accounts.ListByGroup(ctx, tier)
	require.NoError(t, err)
	require.Equal(t, []int64{selfRun}, ids(byGroupAll))
	// A market-only account is not "ungrouped" either.
	ungrouped, err := f.accounts.ListSchedulableUngroupedByPlatform(ctx, service.PlatformAnthropic)
	require.NoError(t, err)
	require.Empty(t, ids(ungrouped))
	ungroupedMulti, err := f.accounts.ListSchedulableUngroupedByPlatforms(ctx, []string{service.PlatformAnthropic})
	require.NoError(t, err)
	require.Empty(t, ids(ungroupedMulti))
	loaded, err := f.accounts.GetByID(ctx, listed)
	require.NoError(t, err)
	require.Empty(t, loaded.AccountGroups)
	require.Equal(t, []int64{tier}, loaded.MarketGroupIDs)
	// Admin views of a group list and count every member (admin bindings
	// and market listings); only scheduling reads filter.
	page, _, err := f.accounts.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 50}, "", "", "", "", tier)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{selfRun, listed}, ids(page))
	total, _, err := f.groups.GetAccountCount(ctx, tier)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
}

func TestMarketListingOwnerLifecycleHooksUnlist(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	setup := func() int64 {
		acc := f.account(t, owner)
		f.configure(t, acc, tier)
		_, err := f.list(acc, owner, tier)
		require.NoError(t, err)
		return acc
	}
	requireUnlisted := func(acc int64, action string) {
		t.Helper()
		l := marketListingState(t, f, acc)
		require.Equal(t, service.MarketListingStatusUnlisted, l.Status)
		require.Equal(t, []int64{tier}, l.AllowedGroupIDs, "permission kept")
		require.Empty(t, marketMemberships(t, acc))
		actions := marketEventActions(t, acc)
		require.Equal(t, action, actions[len(actions)-1])
		require.GreaterOrEqual(t, marketOutboxCount(t, acc, service.SchedulerOutboxEventAccountGroupsChanged), 2)
	}

	cleared := setup()
	_, err := f.owners.SetAccountOwner(ctx, cleared, nil, 1)
	require.NoError(t, err)
	requireUnlisted(cleared, service.MarketListingActionOwnerCleared)
	// The historic owner can no longer list it; setting the owner back does
	// not relist.
	_, err = f.list(cleared, owner, tier)
	requireReason(t, err, "HOSTED_ACCOUNT_NOT_FOUND")
	_, err = f.owners.SetAccountOwner(ctx, cleared, &owner, 1)
	require.NoError(t, err)
	require.Equal(t, service.MarketListingStatusUnlisted, marketListingState(t, f, cleared).Status)

	paused := setup()
	_, err = f.apps.SetOwnerAccountState(ctx, owner, paused, service.HostedOwnerActionPause)
	require.NoError(t, err)
	requireUnlisted(paused, service.MarketListingActionOwnerPaused)
	_, err = f.list(paused, owner, tier)
	requireReason(t, err, "MARKET_ACCOUNT_NOT_ELIGIBLE") // paused cannot list
	_, err = f.apps.SetOwnerAccountState(ctx, owner, paused, service.HostedOwnerActionResume)
	require.NoError(t, err)
	require.Equal(t, service.MarketListingStatusUnlisted, marketListingState(t, f, paused).Status, "resume never relists")

	withdrawn := setup()
	_, err = f.apps.SetOwnerAccountState(ctx, owner, withdrawn, service.HostedOwnerActionWithdraw)
	require.NoError(t, err)
	requireUnlisted(withdrawn, service.MarketListingActionOwnerWithdraw)

	deleted := setup()
	require.NoError(t, f.accounts.Delete(ctx, deleted))
	l := marketListingState(t, f, deleted)
	require.Equal(t, service.MarketListingStatusRevoked, l.Status)
	require.Empty(t, l.AllowedGroupIDs)
	require.Empty(t, marketMemberships(t, deleted))
	require.Equal(t, service.MarketListingActionAccountDeleted, marketEventActions(t, deleted)[len(marketEventActions(t, deleted))-1])
}

func TestMarketListingAdminRevokeAndSelectedTierRemoval(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	tier2 := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier, tier2)
	_, err := f.list(acc, owner, tier)
	require.NoError(t, err)

	// Dropping the selected tier unlists immediately.
	l := f.configure(t, acc, tier2)
	require.Equal(t, service.MarketListingStatusUnlisted, l.Status)
	require.Nil(t, l.SelectedGroupID)
	require.Empty(t, marketMemberships(t, acc))
	// Unchanged PUT is a no-op (no version bump).
	again := f.configure(t, acc, tier2)
	require.Equal(t, l.Version, again.Version)
	// Protection params: merged and validated against the account.
	p, err := f.market.AdminConfigure(ctx, service.MarketAdminConfigureParams{AccountID: acc, AllowedGroupIDs: []int64{tier2}, Defaults: marketTestDefaults(),
		Merge: func(b service.MarketProtectionParams) service.MarketProtectionParams { b.Cap5hPct = 50; return b }})
	require.NoError(t, err)
	require.Equal(t, 50, p.Protection.Cap5hPct)
	require.Equal(t, 60, p.Protection.Cap7dPct)

	_, err = f.list(acc, owner, tier2)
	require.NoError(t, err)
	revoked, err := f.market.AdminRevoke(ctx, acc, 1)
	require.NoError(t, err)
	require.Equal(t, service.MarketListingStatusRevoked, revoked.Status)
	require.Empty(t, revoked.AllowedGroupIDs)
	require.Empty(t, marketMemberships(t, acc))
	_, err = f.list(acc, owner, tier2)
	requireReason(t, err, "MARKET_LISTING_REVOKED")
	// Re-allowing makes it unlisted again (never auto-listed).
	l = f.configure(t, acc, tier2)
	require.Equal(t, service.MarketListingStatusUnlisted, l.Status)
	_, err = f.market.AdminRevoke(ctx, f.account(t, owner), 1)
	requireReason(t, err, "MARKET_LISTING_NOT_FOUND")
}

func TestMarketListingGroupDeleteAndDemote(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	keep := f.group(t, true)
	acc := f.account(t, owner)
	acc2 := f.account(t, owner)
	f.configure(t, acc, tier, keep)
	f.configure(t, acc2, tier, keep)
	_, err := f.list(acc, owner, tier)
	require.NoError(t, err)
	_, err = f.list(acc2, owner, keep)
	require.NoError(t, err)

	_, err = f.groups.DeleteCascade(ctx, tier)
	require.NoError(t, err)
	l := marketListingState(t, f, acc)
	require.Equal(t, service.MarketListingStatusUnlisted, l.Status)
	require.Equal(t, []int64{keep}, l.AllowedGroupIDs)
	require.Nil(t, l.SelectedGroupID)
	require.Empty(t, marketMemberships(t, acc))
	require.Equal(t, service.MarketListingActionGroupDeleted, marketEventActions(t, acc)[len(marketEventActions(t, acc))-1])
	l2 := marketListingState(t, f, acc2)
	require.Equal(t, service.MarketListingStatusListed, l2.Status, "other tiers unaffected")
	require.Equal(t, []int64{keep}, l2.AllowedGroupIDs)

	// Demoting a tier (market_tier true → false) does the same.
	g, err := f.groups.GetByID(ctx, keep)
	require.NoError(t, err)
	g.MarketTier = false
	require.NoError(t, f.groups.Update(ctx, g))
	l2 = marketListingState(t, f, acc2)
	require.Equal(t, service.MarketListingStatusUnlisted, l2.Status)
	require.Empty(t, l2.AllowedGroupIDs)
	require.Empty(t, marketMemberships(t, acc2))
	require.Equal(t, service.MarketListingActionGroupDemoted, marketEventActions(t, acc2)[len(marketEventActions(t, acc2))-1])
	// Other updates of a tier keep its listings.
	tier3 := f.group(t, true)
	f.configure(t, acc, tier3)
	_, err = f.list(acc, owner, tier3)
	require.NoError(t, err)
	g3, err := f.groups.GetByID(ctx, tier3)
	require.NoError(t, err)
	g3.Description = "renamed"
	require.NoError(t, f.groups.Update(ctx, g3))
	require.Equal(t, service.MarketListingStatusListed, marketListingState(t, f, acc).Status)
	require.Equal(t, map[int64]string{tier3: "market"}, marketMemberships(t, acc))
}

func TestMarketListingConcurrentListAndRevoke(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)

	// An uncommitted revoke holds the account row: the owner's listing waits
	// for it and then refuses.
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = lockMarketAccountTx(ctx, tx, acc)
	require.NoError(t, err)
	require.NoError(t, marketUnlistAccountTx(ctx, tx, acc, service.MarketListingActionAdminRevoke, true, service.MarketListingActorAdmin, 1))

	done := make(chan error, 1)
	go func() {
		_, err := f.list(acc, owner, tier)
		done <- err
	}()
	waitForLockWait(t)
	require.NoError(t, tx.Commit())
	select {
	case err := <-done:
		requireReason(t, err, "MARKET_LISTING_REVOKED")
	case <-time.After(10 * time.Second):
		t.Fatal("listing did not finish")
	}
	require.Empty(t, marketMemberships(t, acc))

	// Account deletion takes the account lock first (same order as the
	// listing writers): it waits for an in-flight owner transaction instead
	// of deadlocking, then revokes.
	victim := f.account(t, owner)
	f.configure(t, victim, tier)
	_, err = f.list(victim, owner, tier)
	require.NoError(t, err)
	holder, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	_, err = lockMarketAccountTx(ctx, holder, victim)
	require.NoError(t, err)
	deleted := make(chan error, 1)
	go func() { deleted <- f.accounts.Delete(ctx, victim) }()
	waitForLockWait(t)
	require.NoError(t, marketUnlistAccountTx(ctx, holder, victim, service.MarketListingActionOwnerUnlist, false, service.MarketListingActorOwner, owner))
	require.NoError(t, holder.Commit())
	select {
	case err := <-deleted:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("delete did not finish")
	}
	require.Equal(t, service.MarketListingStatusRevoked, marketListingState(t, f, victim).Status)

	// Racing list and revoke: always ends revoked with no market row.
	for i := 0; i < 5; i++ {
		a := f.account(t, owner)
		f.configure(t, a, tier)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = f.list(a, owner, tier) }()
		go func() { defer wg.Done(); _, _ = f.market.AdminRevoke(ctx, a, 1) }()
		wg.Wait()
		require.Equal(t, service.MarketListingStatusRevoked, marketListingState(t, f, a).Status)
		require.Empty(t, marketMemberships(t, a))
	}
}

// waitForLockWait waits until some backend waits on a row lock.
func waitForLockWait(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		require.NoError(t, integrationDB.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()").Scan(&waiting))
		if waiting > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no lock wait observed")
}

func TestMarketListingOutboxFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, 0)
	// Owner written outside the API: the listing would add a baseline event.
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET owner_user_id = $2 WHERE id = $1", acc, owner)
	require.NoError(t, err)
	before := f.configure(t, acc, tier)

	constraint := fmt.Sprintf("market_test_block_%d", acc)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		"ALTER TABLE scheduler_outbox ADD CONSTRAINT %s CHECK (account_id IS DISTINCT FROM %d) NOT VALID", constraint, acc))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "ALTER TABLE scheduler_outbox DROP CONSTRAINT IF EXISTS "+constraint)
	})
	_, err = f.list(acc, owner, tier)
	require.Error(t, err)
	after := marketListingState(t, f, acc)
	require.Equal(t, before.Version, after.Version)
	require.Equal(t, service.MarketListingStatusUnlisted, after.Status)
	require.Empty(t, marketMemberships(t, acc))
	require.Equal(t, []string{"admin_configure"}, marketEventActions(t, acc))
	require.Zero(t, hostedCountRows(t, "SELECT COUNT(*) FROM account_owner_events WHERE account_id = $1", acc), "baseline rolled back too")
}

func TestMarketTierGuardQueries(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	tier := f.group(t, true)
	plain := f.group(t, false)
	referrer := f.group(t, false)
	invalidReferrer := f.group(t, false)
	_, err := integrationDB.ExecContext(ctx, "UPDATE groups SET fallback_group_id = $2 WHERE id = $1", referrer, plain)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET fallback_group_id_on_invalid_request = $2 WHERE id = $1", invalidReferrer, plain)
	require.NoError(t, err)
	ids, err := f.groups.ListFallbackReferrers(ctx, plain)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{referrer, invalidReferrer}, ids)
	ids, err = f.groups.ListFallbackReferrers(ctx, tier)
	require.NoError(t, err)
	require.Empty(t, ids)

	u1 := f.user(t)
	u2 := f.user(t)
	rates := &userGroupRateRepository{sql: integrationDB}
	zero, small := 0.0, 0.00001
	require.NoError(t, rates.SyncUserGroupRates(ctx, u1, map[int64]*float64{plain: &zero}))
	require.NoError(t, rates.SyncUserGroupRates(ctx, u2, map[int64]*float64{plain: &small}))
	users, err := rates.ListNonPositiveRateUsers(ctx, plain)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{u1, u2}, users, "0.00001 is stored as 0.0000")

	// Database guards (the backstop for concurrent admin saves).
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET market_tier = TRUE WHERE id = $1", plain)
	require.Equal(t, service.ErrMarketTierFallback, translateMarketTierGuardError(err), "referrers block promotion")
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET fallback_group_id = NULL, fallback_group_id_on_invalid_request = NULL WHERE id = ANY($1)", pq.Array([]int64{referrer, invalidReferrer}))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET market_tier = TRUE WHERE id = $1", plain)
	require.Equal(t, service.ErrMarketTierRateOverride, translateMarketTierGuardError(err), "zero overrides block promotion")
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET fallback_group_id = $2 WHERE id = $1", referrer, tier)
	require.Equal(t, service.ErrMarketTierFallback, translateMarketTierGuardError(err))
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET fallback_group_id_on_invalid_request = $2 WHERE id = $1", referrer, tier)
	require.Equal(t, service.ErrMarketTierFallback, translateMarketTierGuardError(err))
	ok := 1.5
	require.NoError(t, rates.SyncUserGroupRates(ctx, u1, map[int64]*float64{tier: &ok}))
	// A rejected save rolls back as a whole (its deletes included).
	require.ErrorIs(t, rates.SyncUserGroupRates(ctx, u1, map[int64]*float64{plain: nil, tier: &small}), service.ErrMarketTierRateOverride)
	require.ErrorIs(t, rates.SyncGroupRateMultipliers(ctx, tier, []service.GroupRateMultiplierInput{{UserID: u2, RateMultiplier: 0}}), service.ErrMarketTierRateOverride)
	kept, err := rates.GetByUserID(ctx, u1)
	require.NoError(t, err)
	require.Equal(t, map[int64]float64{plain: 0, tier: 1.5}, kept)

	// Saving an unrelated field takes no lock on the fallback target (no
	// lock cycle between groups that reference each other).
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET fallback_group_id = $2 WHERE id = $1", referrer, plain)
	require.NoError(t, err)
	locker, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = locker.ExecContext(ctx, "SELECT 1 FROM groups WHERE id = $1 FOR UPDATE", plain)
	require.NoError(t, err)
	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "SET lock_timeout = '1s'")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "UPDATE groups SET description = 'x' WHERE id = $1", referrer)
	require.NoError(t, err, "unchanged fallback is not re-checked")
	_, _ = conn.ExecContext(ctx, "RESET lock_timeout")
	require.NoError(t, conn.Close())
	require.NoError(t, locker.Rollback())
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET fallback_group_id = NULL WHERE id = $1", referrer)
	require.NoError(t, err)

	// Concurrent promotion vs zero override: the override waits for the
	// promotion's row lock, then sees the tier and fails.
	fresh := f.group(t, false)
	promote, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = promote.Rollback() }()
	_, err = promote.ExecContext(ctx, "UPDATE groups SET market_tier = TRUE WHERE id = $1", fresh)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- rates.SyncUserGroupRates(ctx, u2, map[int64]*float64{fresh: &zero}) }()
	waitForLockWait(t)
	require.NoError(t, promote.Commit())
	select {
	case err := <-done:
		require.ErrorIs(t, err, service.ErrMarketTierRateOverride)
	case <-time.After(10 * time.Second):
		t.Fatal("override save did not finish")
	}
}

func TestMarketListingSelfOnboardedProxyUnavailable(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	owner := f.user(t)
	tier := f.group(t, true)
	acc := f.account(t, owner)
	f.configure(t, acc, tier)
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET self_onboarded = TRUE, proxy_id = NULL WHERE id = $1", acc)
	require.NoError(t, err)

	svc := service.NewMarketListingService(f.market, NewSettingRepository(integrationEntClient))
	view, err := svc.OwnerGet(ctx, owner, acc)
	require.NoError(t, err)
	require.False(t, view.CanList)
	require.Contains(t, view.UnavailableReasons, service.MarketReasonProxyUnavailable)
	_, err = f.list(acc, owner, tier)
	requireReason(t, err, "MARKET_ACCOUNT_NOT_ELIGIBLE")
	require.Empty(t, marketMemberships(t, acc))

	// With a live active proxy it can list.
	proxy := mustCreateProxy(t, integrationEntClient, &service.Proxy{Name: "mkt-" + uuid.NewString()[:8], Protocol: "socks5h", Host: "203.0.113.20", Port: 1080, Status: service.StatusActive})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "UPDATE accounts SET proxy_id = NULL WHERE id = $1", acc)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM proxies WHERE id = $1", proxy.ID)
	})
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET proxy_id = $2 WHERE id = $1", acc, proxy.ID)
	require.NoError(t, err)
	view, err = svc.OwnerGet(ctx, owner, acc)
	require.NoError(t, err)
	require.True(t, view.CanList, view.UnavailableReasons)
	_, err = integrationDB.ExecContext(ctx, "UPDATE proxies SET status = 'disabled' WHERE id = $1", proxy.ID)
	require.NoError(t, err)
	view, err = svc.OwnerGet(ctx, owner, acc)
	require.NoError(t, err)
	require.Contains(t, view.UnavailableReasons, service.MarketReasonProxyUnavailable)
}

func TestAddToGroupDuplicateAdminBindingKeepsError(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	plain := f.group(t, false)
	acc := f.account(t, 0)
	require.NoError(t, f.accounts.AddToGroup(ctx, acc, plain, 1))
	err := f.accounts.AddToGroup(ctx, acc, plain, 1)
	require.Error(t, err)
	require.NotErrorIs(t, err, service.ErrMarketMembershipConflict, "a duplicate admin binding keeps its original error")
}

func TestBindGroupsEmptyListRebuildsBuckets(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	plain := f.group(t, false)
	acc := f.account(t, 0)
	require.NoError(t, f.accounts.BindGroups(ctx, acc, []int64{plain}))

	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)
	cfg := &config.Config{RunMode: config.RunModeStandard, Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
		OutboxPollIntervalSeconds: 1, DbFallbackEnabled: true}}}
	snapshots := service.NewSchedulerSnapshotService(cache, NewSchedulerOutboxRepository(integrationDB), f.accounts, nil, cfg)
	groupBucket := service.SchedulerBucket{GroupID: plain, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeMixed}
	ungroupedBucket := service.SchedulerBucket{GroupID: 0, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeMixed}
	contains := func(bucket service.SchedulerBucket) bool {
		cached, hit, err := cache.GetSnapshot(ctx, bucket)
		if err != nil || !hit {
			return false
		}
		for _, a := range cached {
			if a.ID == acc {
				return true
			}
		}
		return false
	}
	// Warm both buckets: the account is in its group, not ungrouped.
	_, _, err := snapshots.ListSchedulableAccounts(ctx, &plain, service.PlatformAnthropic, false)
	require.NoError(t, err)
	_, _, err = snapshots.ListSchedulableAccounts(ctx, nil, service.PlatformAnthropic, false)
	require.NoError(t, err)
	require.True(t, contains(groupBucket))
	require.False(t, contains(ungroupedBucket))

	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox")
	// Let the startup rebuild of the known buckets finish first (it would
	// otherwise rebuild them regardless of the outbox events under test).
	snapshots.Start()
	t.Cleanup(snapshots.Stop)
	lockKeys := []string{schedulerBucketKey(schedulerLockPrefix, groupBucket), schedulerBucketKey(schedulerLockPrefix, ungroupedBucket)}
	require.Eventually(t, func() bool {
		for _, key := range lockKeys { // one key per command (namespaced client)
			if n, err := rdb.Exists(ctx, key).Result(); err != nil || n != 1 {
				return false
			}
		}
		return true
	}, 10*time.Second, 50*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	require.True(t, contains(groupBucket))
	require.False(t, contains(ungroupedBucket))
	// Lift the 30 s rebuild throttle, then change the bindings: only the
	// outbox events can now move the account.
	for _, key := range lockKeys {
		require.NoError(t, rdb.Del(ctx, key).Err())
	}
	require.NoError(t, f.accounts.BindGroups(ctx, acc, nil))
	require.Eventually(t, func() bool { return contains(ungroupedBucket) && !contains(groupBucket) }, 15*time.Second, 100*time.Millisecond,
		"emptying the bindings rebuilds the old group bucket and the ungrouped bucket")
}

func TestGroupSaveAndSortOrderDeadlockRetries(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	low := f.group(t, false)  // smaller id: the fallback target
	high := f.group(t, false) // saved with fallback → low

	// The sort transaction locks "low" first (as UpdateSortOrders does in id
	// order) and keeps its own deadlock detector slow, so the group save is
	// the one PostgreSQL aborts — and its retry must succeed.
	sorter, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = sorter.Rollback() }()
	_, err = sorter.ExecContext(ctx, "SET LOCAL deadlock_timeout = '10s'")
	require.NoError(t, err)
	_, err = sorter.ExecContext(ctx, "UPDATE groups SET sort_order = 7 WHERE id = $1", low)
	require.NoError(t, err)

	g, err := f.groups.GetByID(ctx, high)
	require.NoError(t, err)
	g.FallbackGroupID = &low
	saved := make(chan error, 1)
	go func() { saved <- f.groups.Update(ctx, g) }()
	waitForLockWait(t) // save holds "high", waits for its share lock on "low"
	sorted := make(chan error, 1)
	go func() {
		_, err := sorter.ExecContext(ctx, "UPDATE groups SET sort_order = 8 WHERE id = $1", high)
		if err == nil {
			err = sorter.Commit()
		}
		sorted <- err
	}()
	for _, ch := range []chan error{sorted, saved} {
		select {
		case err := <-ch:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("deadlock not resolved")
		}
	}
	var fallback sql.NullInt64
	var lowOrder, highOrder int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT fallback_group_id, sort_order FROM groups WHERE id = $1", high).Scan(&fallback, &highOrder))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT sort_order FROM groups WHERE id = $1", low).Scan(&lowOrder))
	require.Equal(t, low, fallback.Int64, "the group save was retried and applied")
	require.Equal(t, 7, lowOrder, "the sort update applied")
	require.Equal(t, 8, highOrder)

	// The repository sort path itself runs in id order with retry.
	require.NoError(t, f.groups.UpdateSortOrders(ctx, []service.GroupSortOrderUpdate{{ID: high, SortOrder: 1}, {ID: low, SortOrder: 2}}))
}
