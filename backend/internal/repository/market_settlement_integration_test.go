//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ---- fixture: committed rows, hard-deleted in t.Cleanup ----

type mktFixture struct {
	t        *testing.T
	users    []int64
	keys     []int64
	accounts []int64
	groups   []int64
}

func newMktFixture(t *testing.T) *mktFixture {
	t.Helper()
	f := &mktFixture{t: t}
	t.Cleanup(f.cleanup)
	return f
}

func (f *mktFixture) cleanup() {
	bg := context.Background()
	users, keys, accounts := pq.Array(f.users), pq.Array(f.keys), pq.Array(f.accounts)
	// Ledger, admissions: insert-only (triggers bypassed as superuser).
	if tx, err := integrationDB.BeginTx(bg, nil); err == nil {
		_, _ = tx.ExecContext(bg, "SET LOCAL session_replication_role = replica")
		_, _ = tx.ExecContext(bg, `DELETE FROM owner_earning_entries WHERE owner_user_id = ANY($1)
			OR settlement_event_id IN (SELECT id FROM usage_settlement_events WHERE api_key_id = ANY($2))`, users, keys)
		_, _ = tx.ExecContext(bg, "DELETE FROM usage_settlement_events WHERE api_key_id = ANY($1)", keys)
		_, _ = tx.ExecContext(bg, "DELETE FROM market_admissions WHERE account_id = ANY($1) OR buyer_user_id = ANY($2)", accounts, users)
		_ = tx.Commit()
	}
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{"DELETE FROM owner_earning_wallets WHERE user_id = ANY($1)", []any{users}},
		{"DELETE FROM redeem_codes WHERE used_by = ANY($1)", []any{users}},
		{"DELETE FROM usage_billing_dedup WHERE api_key_id = ANY($1)", []any{keys}},
		{"DELETE FROM usage_billing_dedup_archive WHERE api_key_id = ANY($1)", []any{keys}},
		{"DELETE FROM usage_logs WHERE api_key_id = ANY($1)", []any{keys}},
		{"DELETE FROM account_owner_events WHERE account_id = ANY($1)", []any{accounts}},
		{"DELETE FROM api_keys WHERE id = ANY($1)", []any{keys}},
		{"DELETE FROM accounts WHERE id = ANY($1)", []any{accounts}},
		{"DELETE FROM users WHERE id = ANY($1)", []any{users}},
		{"DELETE FROM groups WHERE id = ANY($1)", []any{pq.Array(f.groups)}},
	} {
		_, _ = integrationDB.ExecContext(bg, stmt.q, stmt.args...)
	}
}

func (f *mktFixture) user(balance float64) int64 {
	f.t.Helper()
	u := mustCreateUser(f.t, integrationEntClient, &service.User{Email: "mkt-" + uuid.NewString() + "@example.invalid", PasswordHash: "TEST_ONLY", Balance: balance})
	f.users = append(f.users, u.ID)
	return u.ID
}

func (f *mktFixture) key(userID int64) int64 {
	f.t.Helper()
	k := mustCreateApiKey(f.t, integrationEntClient, &service.APIKey{UserID: userID, Key: "TEST_ONLY_" + uuid.NewString(), Name: "mkt", Quota: 1000})
	f.keys = append(f.keys, k.ID)
	return k.ID
}

// account creates a setup-token account owned by owner with an owner event
// and returns the account id and the owner event id.
func (f *mktFixture) account(owner int64) (int64, int64) {
	f.t.Helper()
	acc := mustCreateAccount(f.t, integrationEntClient, &service.Account{Name: "mkt-" + uuid.NewString(), Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken})
	f.accounts = append(f.accounts, acc.ID)
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET owner_user_id = $1 WHERE id = $2", owner, acc.ID)
	require.NoError(f.t, err)
	var eventID int64
	require.NoError(f.t, integrationDB.QueryRowContext(ctx, `INSERT INTO account_owner_events (account_id, new_owner_user_id) VALUES ($1, $2) RETURNING id`, acc.ID, owner).Scan(&eventID))
	return acc.ID, eventID
}

type mktSale struct {
	buyer, key, account, owner, ownerEvent, group int64
}

func (f *mktFixture) admit(s mktSale) *service.MarketAdmission {
	f.t.Helper()
	return f.admitRef(s, uuid.NewString())
}

// admitRef records an admission for the given request ref (several
// candidate attempts of one request share it).
func (f *mktFixture) admitRef(s mktSale, ref string) *service.MarketAdmission {
	f.t.Helper()
	a := &service.MarketAdmission{AttemptID: "att-" + uuid.NewString(), RequestRef: ref, AccountID: s.account, GroupID: s.group,
		OwnerUserID: s.owner, OwnerEventID: s.ownerEvent, ListingVersion: 1, BuyerUserID: s.buyer, APIKeyID: s.key}
	require.NoError(f.t, integrationDB.QueryRowContext(context.Background(), `
		INSERT INTO market_admissions (attempt_id, request_ref, account_id, group_id, owner_user_id, owner_event_id, listing_version, buyer_user_id, api_key_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id, admitted_at, created_at`,
		a.AttemptID, a.RequestRef, a.AccountID, a.GroupID, a.OwnerUserID, a.OwnerEventID, a.ListingVersion, a.BuyerUserID, a.APIKeyID).
		Scan(&a.ID, &a.AdmittedAt, &a.CreatedAt))
	return a
}

func mktCommand(a *service.MarketAdmission, cost float64) *service.UsageBillingCommand {
	group := a.GroupID
	in := &service.UsageSettlementInput{GroupID: &group, StandardCost: cost, ChargeInput: cost, ChargeSource: service.SettlementChargeSourceNone,
		Market: &service.MarketSettlementProof{AdmissionID: a.ID, AttemptID: a.AttemptID, RequestRef: a.RequestRef, AccountID: a.AccountID,
			GroupID: a.GroupID, BuyerUserID: a.BuyerUserID, APIKeyID: a.APIKeyID}}
	if cost > 0 {
		in.ChargeSource = service.SettlementChargeSourceBalance
	}
	return &service.UsageBillingCommand{RequestID: service.MarketBillingRequestID(a.RequestRef), APIKeyID: a.APIKeyID, UserID: a.BuyerUserID,
		AccountID: a.AccountID, AccountType: service.AccountTypeSetupToken, Model: "TEST_ONLY_model", BalanceCost: cost, APIKeyQuotaCost: cost,
		MarketPath: true, Settlement: in}
}

// setMarketSetting upserts one setting and restores it after the test.
func setMarketSetting(t *testing.T, key, value string) {
	t.Helper()
	ctx := context.Background()
	var previous sql.NullString
	err := integrationDB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = $1", key).Scan(&previous)
	if err != nil {
		require.ErrorIs(t, err, sql.ErrNoRows)
	}
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, key, value)
	require.NoError(t, err)
	t.Cleanup(func() {
		if previous.Valid {
			_, _ = integrationDB.ExecContext(context.Background(), "UPDATE settings SET value = $2 WHERE key = $1", key, previous.String)
		} else {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM settings WHERE key = $1", key)
		}
	})
}

func setMarketCut(t *testing.T, cut string, freezeDays int) {
	t.Helper()
	setMarketSetting(t, service.SettingKeyMarketPlatformCutRate, cut)
	setMarketSetting(t, service.SettingKeyMarketEarningFreezeDays, fmt.Sprint(freezeDays))
}

// mktState is everything a rolled-back Apply must leave unchanged.
type mktState struct {
	Balance  string
	Quota    string
	Dedup    int
	Events   int
	Entries  int
	Wallets  int
	Archived int
}

func loadMktState(t *testing.T, buyer, key, owner int64) mktState {
	t.Helper()
	ctx := context.Background()
	var s mktState
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance::text FROM users WHERE id = $1", buyer).Scan(&s.Balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used::text FROM api_keys WHERE id = $1", key).Scan(&s.Quota))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM usage_billing_dedup WHERE api_key_id = $1", key).Scan(&s.Dedup))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM usage_billing_dedup_archive WHERE api_key_id = $1", key).Scan(&s.Archived))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM usage_settlement_events WHERE api_key_id = $1", key).Scan(&s.Events))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM owner_earning_entries WHERE owner_user_id = $1", owner).Scan(&s.Entries))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM owner_earning_wallets WHERE user_id = $1", owner).Scan(&s.Wallets))
	return s
}

type mktEventRow struct {
	ID                                     int64
	Category, OwnerSource, ChargeSource    string
	OwnerUserID, AdmissionID               int64
	SelfPurchase, Anomaly                  bool
	RuleVersion                            int16
	Charged, Earning, Platform, Cut, Input string
	ZeroReason                             sql.NullString
	CreatedAt, AvailableAt                 time.Time
}

func loadMktEvent(t *testing.T, a *service.MarketAdmission) (mktEventRow, bool) {
	t.Helper()
	var r mktEventRow
	err := integrationDB.QueryRowContext(context.Background(), `
		SELECT id, category, owner_source, charge_source, owner_user_id, admission_id, self_purchase, anomaly, rule_version,
			charged_amount::text, owner_earning::text, platform_amount::text, platform_cut_rate::text, charge_input::text,
			earning_zero_reason, created_at, earning_available_at
		FROM usage_settlement_events WHERE request_id = $1 AND api_key_id = $2`, service.MarketBillingRequestID(a.RequestRef), a.APIKeyID).
		Scan(&r.ID, &r.Category, &r.OwnerSource, &r.ChargeSource, &r.OwnerUserID, &r.AdmissionID, &r.SelfPurchase, &r.Anomaly, &r.RuleVersion,
			&r.Charged, &r.Earning, &r.Platform, &r.Cut, &r.Input, &r.ZeroReason, &r.CreatedAt, &r.AvailableAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false
	}
	require.NoError(t, err)
	return r, true
}

type mktEntryRow struct {
	ID, Owner   int64
	Amount      string
	AvailableAt time.Time
}

func loadAccrue(t *testing.T, eventID int64) (mktEntryRow, bool) {
	t.Helper()
	var r mktEntryRow
	err := integrationDB.QueryRowContext(context.Background(), `
		SELECT id, owner_user_id, amount::text, available_at FROM owner_earning_entries WHERE kind = 'accrue' AND settlement_event_id = $1`, eventID).
		Scan(&r.ID, &r.Owner, &r.Amount, &r.AvailableAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false
	}
	require.NoError(t, err)
	return r, true
}

func dec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	require.NoError(t, err)
	return d
}

// basicSale: buyer (balance 100) buying from an owner's account.
func basicSale(f *mktFixture, buyerBalance float64) mktSale {
	owner := f.user(0)
	buyer := f.user(buyerBalance)
	key := f.key(buyer)
	account, ownerEvent := f.account(owner)
	return mktSale{buyer: buyer, key: key, account: account, owner: owner, ownerEvent: ownerEvent, group: 900000 + account}
}

// ---- settlement ----

func TestMarketSettlementAccruesAtomically(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	a := f.admit(s)

	before := loadMktState(t, s.buyer, s.key, s.owner)
	res, err := repo.Apply(ctx, mktCommand(a, 1.23456789))
	require.NoError(t, err)
	require.True(t, res.Applied)

	ev, ok := loadMktEvent(t, a)
	require.True(t, ok)
	require.Equal(t, service.SettlementCategorySharedSale, ev.Category)
	require.Equal(t, service.SettlementOwnerSourceAdmission, ev.OwnerSource)
	require.Equal(t, service.SettlementChargeSourceBalance, ev.ChargeSource)
	require.Equal(t, s.owner, ev.OwnerUserID)
	require.Equal(t, a.ID, ev.AdmissionID)
	require.False(t, ev.SelfPurchase)
	require.False(t, ev.Anomaly)
	require.Equal(t, int16(service.MarketSettlementRuleVersion), ev.RuleVersion)
	after := loadMktState(t, s.buyer, s.key, s.owner)
	charged := dec(t, before.Balance).Sub(dec(t, after.Balance))
	require.True(t, dec(t, ev.Charged).Equal(charged), "charged %s = balance delta %s", ev.Charged, charged)
	require.True(t, charged.Equal(dec(t, "1.23456789")))
	require.True(t, dec(t, ev.Earning).Equal(dec(t, "0.987654312")), ev.Earning)
	require.True(t, dec(t, ev.Platform).Equal(dec(t, "0.246913578")), ev.Platform)
	require.True(t, dec(t, ev.Cut).Equal(dec(t, "0.2")))
	require.False(t, ev.ZeroReason.Valid)
	require.True(t, ev.CreatedAt.Add(7*24*time.Hour).Equal(ev.AvailableAt), "available_at = event time + 7 days")

	accrue, ok := loadAccrue(t, ev.ID)
	require.True(t, ok)
	require.Equal(t, s.owner, accrue.Owner)
	require.True(t, dec(t, accrue.Amount).Equal(dec(t, ev.Earning)))
	require.True(t, ev.AvailableAt.Equal(accrue.AvailableAt))
	require.Equal(t, before.Dedup+1, after.Dedup)
	require.Equal(t, before.Events+1, after.Events)
	require.Equal(t, before.Entries+1, after.Entries)
	require.Zero(t, after.Wallets, "billing never touches the seller wallet")

	// Same command again: nothing new.
	res, err = repo.Apply(ctx, mktCommand(a, 1.23456789))
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Equal(t, after, loadMktState(t, s.buyer, s.key, s.owner))
}

func TestMarketSettlementIntegrityRollsBackEverything(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	otherAccount, otherEvent := f.account(s.owner)
	repo := NewUsageBillingRepository(nil, integrationDB)

	cases := map[string]func() *service.UsageBillingCommand{
		"settlement missing": func() *service.UsageBillingCommand { c := mktCommand(f.admit(s), 1); c.Settlement = nil; return c },
		"proof missing": func() *service.UsageBillingCommand {
			c := mktCommand(f.admit(s), 1)
			c.Settlement.Market = nil
			return c
		},
		"proof without path": func() *service.UsageBillingCommand { c := mktCommand(f.admit(s), 1); c.MarketPath = false; return c },
		"admission missing": func() *service.UsageBillingCommand {
			c := mktCommand(f.admit(s), 1)
			c.Settlement.Market.AdmissionID = 1 << 60
			return c
		},
		"attempt differs": func() *service.UsageBillingCommand {
			c := mktCommand(f.admit(s), 1)
			c.Settlement.Market.AttemptID = "forged"
			return c
		},
		// Proof and command agree with each other but not with the stored
		// admission (another account / priced group / request).
		"admission of another account": func() *service.UsageBillingCommand {
			a := f.admit(mktSale{buyer: s.buyer, key: s.key, account: otherAccount, owner: s.owner, ownerEvent: otherEvent, group: s.group})
			c := mktCommand(a, 1)
			c.AccountID, c.Settlement.Market.AccountID = s.account, s.account
			return c
		},
		"admission of another group": func() *service.UsageBillingCommand {
			c := mktCommand(f.admit(s), 1)
			g := s.group + 1
			c.Settlement.GroupID, c.Settlement.Market.GroupID = &g, g
			return c
		},
		"admission of another request": func() *service.UsageBillingCommand {
			c := mktCommand(f.admit(s), 1)
			c.Settlement.Market.RequestRef = uuid.NewString()
			c.RequestID = service.MarketBillingRequestID(c.Settlement.Market.RequestRef)
			return c
		},
		// An admission is bound to its request identity: reusing a settled
		// one for another identity is refused by the identity check (the
		// admission_id unique index is the backstop).
		"settled admission reused by another identity": func() *service.UsageBillingCommand {
			a := f.admit(s)
			_, err := repo.Apply(ctx, mktCommand(a, 1))
			require.NoError(t, err)
			c := mktCommand(a, 1)
			c.RequestID = service.MarketBillingRequestID(uuid.NewString())
			c.Settlement.Market.RequestRef = c.RequestID[len("client:"):]
			return c
		},
		"settled admission reused by another key": func() *service.UsageBillingCommand {
			a := f.admit(s)
			_, err := repo.Apply(ctx, mktCommand(a, 1))
			require.NoError(t, err)
			c := mktCommand(a, 1)
			other := f.key(s.buyer)
			c.APIKeyID, c.Settlement.Market.APIKeyID = other, other
			return c
		},
		"subscription charge": func() *service.UsageBillingCommand {
			c := mktCommand(f.admit(s), 1)
			c.Settlement.ChargeSource = service.SettlementChargeSourceSubscription
			return c
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := build()
			before := loadMktState(t, s.buyer, cmd.APIKeyID, s.owner)
			metrics := service.GetUsageSettlementMetricsSnapshot()
			_, err := repo.Apply(ctx, cmd)
			require.ErrorIs(t, err, service.ErrMarketSettlementIntegrity)
			require.Equal(t, before, loadMktState(t, s.buyer, cmd.APIKeyID, s.owner))
			require.Equal(t, metrics.MarketIntegrityErrors+1, service.GetUsageSettlementMetricsSnapshot().MarketIntegrityErrors)
		})
	}
}

func TestMarketSettlementReplayConflicts(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	a1 := f.admit(s)
	_, err := repo.Apply(ctx, mktCommand(a1, 1))
	require.NoError(t, err)
	settled := loadMktState(t, s.buyer, s.key, s.owner)

	// Another admission of the same request (same ref → same billing
	// identity and fingerprint): the replay must not settle it.
	a2 := f.admitRef(s, a1.RequestRef)
	_, err = repo.Apply(ctx, mktCommand(a2, 1))
	require.ErrorIs(t, err, service.ErrMarketSettlementConflict)
	require.Equal(t, settled, loadMktState(t, s.buyer, s.key, s.owner))

	// An identity billed by a non-market command, replayed as a market one:
	// conflict too (the identity has no market event).
	ref := uuid.NewString()
	plain := service.UsageBillingCommand{RequestID: service.MarketBillingRequestID(ref), APIKeyID: s.key, UserID: s.buyer, AccountID: s.account,
		AccountType: service.AccountTypeSetupToken, Model: "TEST_ONLY_model", BalanceCost: 1, APIKeyQuotaCost: 1}
	_, err = repo.Apply(ctx, &plain)
	require.NoError(t, err)
	beforePlain := loadMktState(t, s.buyer, s.key, s.owner)
	_, err = repo.Apply(ctx, mktCommand(f.admitRef(s, ref), 1))
	require.ErrorIs(t, err, service.ErrMarketSettlementConflict)
	require.Equal(t, beforePlain, loadMktState(t, s.buyer, s.key, s.owner))
}

// A replay is checked against the stored admission too: a forged proof is
// an integrity error, never a successful replay (active and archived dedup).
func TestMarketSettlementReplayWithForgedProof(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	a := f.admit(s)
	cmd := mktCommand(a, 1)
	_, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	forge := func() *service.UsageBillingCommand {
		c := mktCommand(a, 1)
		c.Settlement.Market.AttemptID = "forged"
		return c
	}
	before := loadMktState(t, s.buyer, s.key, s.owner)
	_, err = repo.Apply(ctx, forge())
	require.ErrorIs(t, err, service.ErrMarketSettlementIntegrity)
	require.Equal(t, before, loadMktState(t, s.buyer, s.key, s.owner))

	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO usage_billing_dedup_archive (request_id, api_key_id, request_fingerprint, created_at)
		SELECT request_id, api_key_id, request_fingerprint, created_at FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2`, cmd.RequestID, cmd.APIKeyID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", cmd.RequestID, cmd.APIKeyID)
	require.NoError(t, err)
	archived := loadMktState(t, s.buyer, s.key, s.owner)
	_, err = repo.Apply(ctx, forge())
	require.ErrorIs(t, err, service.ErrMarketSettlementIntegrity)
	require.Equal(t, archived, loadMktState(t, s.buyer, s.key, s.owner))
}

func TestMarketSettlementArchivedDedupReplay(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	a := f.admit(s)
	cmd := mktCommand(a, 2)
	_, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO usage_billing_dedup_archive (request_id, api_key_id, request_fingerprint, created_at)
		SELECT request_id, api_key_id, request_fingerprint, created_at FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2`, cmd.RequestID, cmd.APIKeyID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", cmd.RequestID, cmd.APIKeyID)
	require.NoError(t, err)
	archived := loadMktState(t, s.buyer, s.key, s.owner)
	require.Equal(t, 1, archived.Archived)
	require.Zero(t, archived.Dedup)

	res, err := repo.Apply(ctx, mktCommand(a, 2))
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Equal(t, archived, loadMktState(t, s.buyer, s.key, s.owner), "nothing new, the claim row rolled back")
}

// The market branch is chosen before the shadow event's 3 s budget check: a
// nearly expired billing context still settles atomically.
func TestMarketSettlementLowBudgetStillAtomic(t *testing.T) {
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	a := f.admit(s)
	skipped := service.GetUsageSettlementMetricsSnapshot().SkippedLowBudget
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := repo.Apply(ctx, mktCommand(a, 1))
	require.NoError(t, err)
	require.True(t, res.Applied)
	ev, ok := loadMktEvent(t, a)
	require.True(t, ok, "shared_sale event written")
	_, ok = loadAccrue(t, ev.ID)
	require.True(t, ok)
	require.Equal(t, skipped, service.GetUsageSettlementMetricsSnapshot().SkippedLowBudget)
}

func TestMarketSettlementEntryFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	fn := "mkt_fail_" + fmt.Sprint(s.owner)
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.owner_user_id = %d THEN RAISE EXCEPTION 'injected entry failure'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE INSERT ON owner_earning_entries FOR EACH ROW EXECUTE FUNCTION %s();`, fn, s.owner, fn, fn))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON owner_earning_entries; DROP FUNCTION IF EXISTS %s();", fn, fn))
	})
	before := loadMktState(t, s.buyer, s.key, s.owner)
	_, err = repo.Apply(ctx, mktCommand(f.admit(s), 1))
	require.ErrorContains(t, err, "injected entry failure")
	require.Equal(t, before, loadMktState(t, s.buyer, s.key, s.owner))
}

func TestMarketSettlementZeroEarningCases(t *testing.T) {
	ctx := context.Background()
	f := newMktFixture(t)
	repo := NewUsageBillingRepository(nil, integrationDB)

	t.Run("self purchase", func(t *testing.T) {
		setMarketCut(t, "0.2", 7)
		owner := f.user(10)
		key := f.key(owner)
		account, ownerEvent := f.account(owner)
		a := f.admit(mktSale{buyer: owner, key: key, account: account, owner: owner, ownerEvent: ownerEvent, group: 7})
		_, err := repo.Apply(ctx, mktCommand(a, 1))
		require.NoError(t, err)
		ev, ok := loadMktEvent(t, a)
		require.True(t, ok)
		require.True(t, ev.SelfPurchase)
		require.Equal(t, service.SettlementEarningZeroReasonSelfPurchase, ev.ZeroReason.String)
		require.True(t, dec(t, ev.Charged).Equal(decimal.NewFromInt(1)), "the owner still pays")
		require.True(t, dec(t, ev.Earning).IsZero())
		require.True(t, dec(t, ev.Platform).Equal(decimal.NewFromInt(1)))
		_, ok = loadAccrue(t, ev.ID)
		require.False(t, ok)
	})
	t.Run("zero charge", func(t *testing.T) {
		setMarketCut(t, "0.2", 7)
		s := basicSale(f, 100)
		a := f.admit(s)
		_, err := repo.Apply(ctx, mktCommand(a, 0))
		require.NoError(t, err)
		ev, ok := loadMktEvent(t, a)
		require.True(t, ok)
		require.Equal(t, service.SettlementEarningZeroReasonZeroCharge, ev.ZeroReason.String)
		require.True(t, dec(t, ev.Charged).IsZero())
		require.Equal(t, service.SettlementChargeSourceNone, ev.ChargeSource)
		require.True(t, ev.CreatedAt.Add(7*24*time.Hour).Equal(ev.AvailableAt), "available_at on zero earnings too")
		_, ok = loadAccrue(t, ev.ID)
		require.False(t, ok)
	})
	t.Run("cut 0 honored", func(t *testing.T) {
		setMarketCut(t, "0", 0)
		s := basicSale(f, 100)
		a := f.admit(s)
		_, err := repo.Apply(ctx, mktCommand(a, 1.5))
		require.NoError(t, err)
		ev, ok := loadMktEvent(t, a)
		require.True(t, ok)
		require.True(t, dec(t, ev.Earning).Equal(dec(t, "1.5")))
		require.True(t, dec(t, ev.Platform).IsZero())
		require.True(t, dec(t, ev.Cut).IsZero())
		require.True(t, ev.CreatedAt.Equal(ev.AvailableAt), "freeze 0")
		_, ok = loadAccrue(t, ev.ID)
		require.True(t, ok)
	})
	t.Run("cut 1", func(t *testing.T) {
		setMarketCut(t, "1", 7)
		s := basicSale(f, 100)
		a := f.admit(s)
		_, err := repo.Apply(ctx, mktCommand(a, 1.5))
		require.NoError(t, err)
		ev, ok := loadMktEvent(t, a)
		require.True(t, ok)
		require.True(t, dec(t, ev.Earning).IsZero())
		require.Equal(t, service.SettlementEarningZeroReasonOther, ev.ZeroReason.String)
		_, ok = loadAccrue(t, ev.ID)
		require.False(t, ok)
	})
	t.Run("full overdraft", func(t *testing.T) {
		setMarketCut(t, "0.2", 7)
		s := basicSale(f, 0.5)
		a := f.admit(s)
		_, err := repo.Apply(ctx, mktCommand(a, 2))
		require.NoError(t, err)
		ev, ok := loadMktEvent(t, a)
		require.True(t, ok)
		require.True(t, dec(t, ev.Charged).Equal(decimal.NewFromInt(2)), "earnings on the full charge")
		require.True(t, dec(t, ev.Earning).Equal(dec(t, "1.6")))
		require.True(t, dec(t, loadMktState(t, s.buyer, s.key, s.owner).Balance).Equal(dec(t, "-1.5")))
	})
	// The actual charge is the balance delta after the column's 8-decimal
	// rounding, not the command's cost.
	t.Run("sub-cent cost rounds to zero", func(t *testing.T) {
		setMarketCut(t, "0.2", 7)
		s := basicSale(f, 100)
		a := f.admit(s)
		_, err := repo.Apply(ctx, mktCommand(a, 0.000000004))
		require.NoError(t, err)
		ev, ok := loadMktEvent(t, a)
		require.True(t, ok)
		require.True(t, dec(t, ev.Charged).IsZero(), ev.Charged)
		require.Equal(t, service.SettlementEarningZeroReasonZeroCharge, ev.ZeroReason.String)
		require.True(t, dec(t, loadMktState(t, s.buyer, s.key, s.owner).Balance).Equal(decimal.NewFromInt(100)))
	})
	t.Run("sub-cent cost rounds up from zero balance", func(t *testing.T) {
		setMarketCut(t, "0.2", 7)
		s := basicSale(f, 0)
		a := f.admit(s)
		_, err := repo.Apply(ctx, mktCommand(a, 0.000000006))
		require.NoError(t, err)
		ev, ok := loadMktEvent(t, a)
		require.True(t, ok)
		require.True(t, dec(t, ev.Charged).Equal(dec(t, "0.00000001")), ev.Charged)
		require.True(t, dec(t, ev.Earning).Add(dec(t, ev.Platform)).Equal(dec(t, ev.Charged)))
		require.True(t, dec(t, ev.Earning).Equal(dec(t, "0.000000008")), ev.Earning)
		require.True(t, dec(t, loadMktState(t, s.buyer, s.key, s.owner).Balance).Equal(dec(t, "-0.00000001")))
	})
	t.Run("owner version mismatch", func(t *testing.T) {
		setMarketCut(t, "0.2", 7)
		s := basicSale(f, 100)
		other := f.user(0)
		var wrongEvent int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO account_owner_events (account_id, new_owner_user_id) VALUES ($1, $2) RETURNING id`, s.account, other).Scan(&wrongEvent))
		s.ownerEvent = wrongEvent
		a := f.admit(s)
		_, err := repo.Apply(ctx, mktCommand(a, 1))
		require.NoError(t, err)
		ev, ok := loadMktEvent(t, a)
		require.True(t, ok)
		require.Equal(t, service.SettlementEarningZeroReasonOwnerMismatch, ev.ZeroReason.String)
		require.True(t, ev.Anomaly)
		_, ok = loadAccrue(t, ev.ID)
		require.False(t, ok)
	})
}

// The earning goes to the admission owner even when the account lost its
// owner or was deleted between the admission and the settlement.
func TestMarketSettlementOwnerFromAdmission(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	a := f.admit(s)
	newOwner := f.user(0)
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET owner_user_id = $1 WHERE id = $2", newOwner, s.account)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_owner_events (account_id, old_owner_user_id, new_owner_user_id) VALUES ($1, $2, $3)`, s.account, s.owner, newOwner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET owner_user_id = NULL, deleted_at = NOW() WHERE id = $1", s.account)
	require.NoError(t, err)

	_, err = repo.Apply(ctx, mktCommand(a, 1))
	require.NoError(t, err)
	ev, ok := loadMktEvent(t, a)
	require.True(t, ok)
	require.Equal(t, s.owner, ev.OwnerUserID)
	accrue, ok := loadAccrue(t, ev.ID)
	require.True(t, ok)
	require.Equal(t, s.owner, accrue.Owner)
}

// Two users buying from each other's accounts at the same time: the billing
// transaction locks only the buyer, so there is no lock cycle.
func TestMarketSettlementMutualBuysNoDeadlock(t *testing.T) {
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	userA, userB := f.user(1000), f.user(1000)
	keyA, keyB := f.key(userA), f.key(userB)
	accA, evA := f.account(userA)
	accB, evB := f.account(userB)
	repo := NewUsageBillingRepository(nil, integrationDB)
	const rounds = 15
	var cmds []*service.UsageBillingCommand
	for i := 0; i < rounds; i++ {
		cmds = append(cmds,
			mktCommand(f.admit(mktSale{buyer: userA, key: keyA, account: accB, owner: userB, ownerEvent: evB, group: 1}), 0.5),
			mktCommand(f.admit(mktSale{buyer: userB, key: keyB, account: accA, owner: userA, ownerEvent: evA, group: 1}), 0.5))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, len(cmds))
	start := make(chan struct{})
	for _, cmd := range cmds {
		wg.Add(1)
		go func(cmd *service.UsageBillingCommand) {
			defer wg.Done()
			<-start
			_, err := repo.Apply(ctx, cmd)
			errs <- err
		}(cmd)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for _, u := range []int64{userA, userB} {
		var n int
		var sum string
		require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT count(*), COALESCE(SUM(amount), 0)::text FROM owner_earning_entries WHERE owner_user_id = $1", u).Scan(&n, &sum))
		require.Equal(t, rounds, n)
		require.True(t, dec(t, sum).Equal(dec(t, "6")), sum)
	}
}

// DB-level guards of ledger inserts: any mismatch fails and the whole
// transaction rolls back.
func TestOwnerEarningLedgerGuards(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	a := f.admit(s)
	_, err := repo.Apply(ctx, mktCommand(a, 1))
	require.NoError(t, err)
	ev, ok := loadMktEvent(t, a)
	require.True(t, ok)
	accrue, ok := loadAccrue(t, ev.ID)
	require.True(t, ok)
	amount := dec(t, accrue.Amount)
	other := f.user(0)

	// A shadow (P2b) event of the same key.
	var shadowID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO usage_settlement_events (request_id, api_key_id, category, rule_version, buyer_user_id, account_id, owner_user_id,
			owner_source, self_purchase, standard_cost, charge_input, charge_source)
		VALUES ($1, $2, 'owned_other', 1, $3, $4, $5, 'settlement', FALSE, 1, 1, 'balance') RETURNING id`,
		"shadow-"+uuid.NewString(), s.key, s.buyer, s.account, s.owner).Scan(&shadowID))

	// A fresh positive event without an accrue yet (to test accrue guards).
	b := f.admit(s)
	cmd := mktCommand(b, 1)
	var freshID int64
	var freshAt time.Time
	{
		tx, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		ev := service.BuildSharedSaleEvent(cmd, b, service.SharedSaleSplit{Charged: decimal.NewFromInt(1), OwnerEarning: dec(t, "0.8"),
			PlatformAmount: dec(t, "0.2"), CutRate: dec(t, "0.2")}, 7)
		freshID, freshAt, err = insertSharedSaleEventTx(ctx, tx, ev)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}

	accrueCases := map[string]struct {
		owner  int64
		amount string
		event  int64
		at     time.Time
	}{
		"wrong owner":     {other, "0.8", freshID, freshAt},
		"wrong amount":    {s.owner, "0.81", freshID, freshAt},
		"wrong available": {s.owner, "0.8", freshID, freshAt.Add(time.Second)},
		"shadow event":    {s.owner, "1", shadowID, freshAt},
	}
	for name, c := range accrueCases {
		t.Run("accrue "+name, func(t *testing.T) {
			tx, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			_, err = tx.ExecContext(ctx, "INSERT INTO owner_earning_wallets (user_id) VALUES ($1) ON CONFLICT DO NOTHING", other)
			require.NoError(t, err)
			_, err = insertAccrueEntryTx(ctx, tx, c.owner, dec(t, c.amount), c.event, c.at)
			require.ErrorIs(t, err, errOwnerEarningGuard)
			require.NoError(t, tx.Rollback())
			var wallets int
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM owner_earning_wallets WHERE user_id = $1", other).Scan(&wallets))
			require.Zero(t, wallets, "earlier statements of the transaction rolled back")
		})
	}

	reverseCases := map[string]struct {
		owner  int64
		amount decimal.Decimal
		ref    int64
		at     time.Time
	}{
		"wrong owner":     {other, amount.Neg(), accrue.ID, accrue.AvailableAt},
		"wrong amount":    {s.owner, amount.Neg().Add(dec(t, "0.0000000001")), accrue.ID, accrue.AvailableAt},
		"wrong available": {s.owner, amount.Neg(), accrue.ID, accrue.AvailableAt.Add(-time.Second)},
	}
	for name, c := range reverseCases {
		t.Run("reverse "+name, func(t *testing.T) {
			tx, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			_, err = insertReverseEntryTx(ctx, tx, c.owner, c.amount, c.ref, c.at, 1, "x")
			require.ErrorIs(t, err, errOwnerEarningGuard)
		})
	}
	t.Run("reverse of a non-accrue", func(t *testing.T) {
		tx, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		revID, err := insertReverseEntryTx(ctx, tx, s.owner, amount.Neg(), accrue.ID, accrue.AvailableAt, 1, "x")
		require.NoError(t, err, "the matching reverse passes")
		_, err = insertReverseEntryTx(ctx, tx, s.owner, amount, revID, accrue.AvailableAt, 1, "x")
		require.ErrorIs(t, err, errOwnerEarningGuard)
	})
	var entries int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM owner_earning_entries WHERE owner_user_id = ANY($1)", pq.Array([]int64{s.owner, other})).Scan(&entries))
	require.Equal(t, 1, entries, "only the settled accrue")
}

// ---- wallet: convert / reverse ----

var mktSeedSeq atomic.Int64

// seedAccrue writes a shared_sale event (cut 0) and its accrue with an
// explicit available_at, through the guarded insert.
func seedAccrue(t *testing.T, s mktSale, amount string, availableAt time.Time) (eventID, entryID int64) {
	t.Helper()
	ctx := context.Background()
	admission := (time.Now().UnixNano() & 0x7fffffffffff) + mktSeedSeq.Add(1)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO usage_settlement_events (request_id, api_key_id, category, rule_version, buyer_user_id, account_id, owner_user_id,
			owner_source, self_purchase, standard_cost, charge_input, charge_source, owner_earning, admission_id, charged_amount,
			platform_amount, platform_cut_rate, earning_available_at)
		VALUES ($1, $2, 'shared_sale', 2, $3, $4, $5, 'admission', FALSE, $6::numeric, $6::numeric, 'balance', $6::numeric, $7, $6::numeric, 0, 0, $8)
		RETURNING id`, "seed-"+uuid.NewString(), s.key, s.buyer, s.account, s.owner, amount, admission, availableAt).Scan(&eventID))
	var stored time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT earning_available_at FROM usage_settlement_events WHERE id = $1", eventID).Scan(&stored))
	entryID, err := insertAccrueEntryTx(ctx, integrationDB, s.owner, dec(t, amount), eventID, stored)
	require.NoError(t, err)
	return eventID, entryID
}

func newEarningRepo() *ownerEarningRepository {
	return &ownerEarningRepository{db: integrationDB}
}

func mktUserBalance(t *testing.T, id int64) decimal.Decimal {
	t.Helper()
	var b string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT balance::text FROM users WHERE id = $1", id).Scan(&b))
	return dec(t, b)
}

func TestOwnerEarningConvert(t *testing.T) {
	ctx := context.Background()
	setMarketSetting(t, service.SettingKeyMarketMinConvertUSD, "1")
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := newEarningRepo()
	past := time.Now().Add(-time.Hour)
	seedAccrue(t, s, "3.5", past)
	seedAccrue(t, s, "2", time.Now().Add(time.Hour)) // frozen

	b, err := repo.Balances(ctx, s.owner)
	require.NoError(t, err)
	require.True(t, b.Available.Equal(dec(t, "3.5")))
	require.True(t, b.Frozen.Equal(dec(t, "2")))

	id1 := uuid.NewString()
	rec, err := repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "2.5"), RequestID: id1})
	require.NoError(t, err)
	require.False(t, rec.Replayed)
	require.True(t, mktUserBalance(t, s.owner).Equal(dec(t, "2.5")))
	var code, typ, status string
	var value string
	var usedBy int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT code, type, value::text, status, used_by FROM redeem_codes WHERE used_by = $1", s.owner).
		Scan(&code, &typ, &value, &status, &usedBy))
	require.Equal(t, service.MarketEarningCodePrefix+fmt.Sprint(rec.EntryID), code)
	require.Equal(t, service.RedeemTypeMarketEarning, typ)
	require.True(t, dec(t, value).Equal(dec(t, "2.5")))
	require.Equal(t, service.StatusUsed, status)
	// Not a real top-up.
	paid, err := NewRedeemCodeRepository(integrationEntClient).SumPositiveBalanceByUser(ctx, s.owner)
	require.NoError(t, err)
	require.Zero(t, paid, "total recharged (admin user balance history) excludes market_earning")
	qualifies, err := NewUserInviteRepository(integrationEntClient).HasQualifyingTopUp(ctx, s.owner)
	require.NoError(t, err)
	require.False(t, qualifies, "an earnings conversion is not a top-up for invitations")

	// Below the minimum, more than available, conflicting replay.
	_, err = repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "0.5"), RequestID: uuid.NewString()})
	require.ErrorIs(t, err, service.ErrOwnerEarningBelowMinimum)
	_, err = repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "1.01"), RequestID: uuid.NewString()})
	require.ErrorIs(t, err, service.ErrOwnerEarningInsufficient)
	_, err = repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "1"), RequestID: id1})
	require.ErrorIs(t, err, service.ErrOwnerEarningConvertConflict)

	// Spend the rest, then replay the first request: the original success,
	// before any availability (or minimum) check.
	_, err = repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "1"), RequestID: uuid.NewString()})
	require.NoError(t, err)
	setMarketSetting(t, service.SettingKeyMarketMinConvertUSD, "100")
	rec2, err := repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "2.50"), RequestID: id1})
	require.NoError(t, err)
	require.True(t, rec2.Replayed)
	require.Equal(t, rec.EntryID, rec2.EntryID)
	require.True(t, mktUserBalance(t, s.owner).Equal(dec(t, "3.5")))

	b, err = repo.Balances(ctx, s.owner)
	require.NoError(t, err)
	require.True(t, b.Available.IsZero(), b.Available.String())
	require.True(t, b.Converted.Equal(dec(t, "3.5")))
	require.True(t, b.Frozen.Equal(dec(t, "2")))
}

// The available sum can exceed what one numeric(20,10) entry stores.
func TestOwnerEarningConvertEntryOutOfRange(t *testing.T) {
	ctx := context.Background()
	setMarketSetting(t, service.SettingKeyMarketMinConvertUSD, "1")
	f := newMktFixture(t)
	s := basicSale(f, 100)
	seedAccrue(t, s, "5000000000", time.Now().Add(-time.Minute))
	seedAccrue(t, s, "5000000000", time.Now().Add(-time.Minute))
	_, err := newEarningRepo().Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "10000000000"), RequestID: uuid.NewString()})
	require.ErrorIs(t, err, service.ErrBalanceOutOfRange)
	require.True(t, mktUserBalance(t, s.owner).IsZero())
}

func TestOwnerEarningConvertConcurrency(t *testing.T) {
	ctx := context.Background()
	setMarketSetting(t, service.SettingKeyMarketMinConvertUSD, "1")
	f := newMktFixture(t)

	t.Run("same request id, new wallet", func(t *testing.T) {
		s := basicSale(f, 100)
		seedAccrue(t, s, "5", time.Now().Add(-time.Minute))
		repo := newEarningRepo()
		id := uuid.NewString()
		results := runConcurrently(8, func(int) error {
			_, err := repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "3"), RequestID: id})
			return err
		})
		for _, err := range results {
			require.NoError(t, err)
		}
		require.True(t, mktUserBalance(t, s.owner).Equal(dec(t, "3")))
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM owner_earning_entries WHERE owner_user_id = $1 AND kind = 'convert'", s.owner).Scan(&n))
		require.Equal(t, 1, n)
	})
	t.Run("different request ids never overspend", func(t *testing.T) {
		s := basicSale(f, 100)
		seedAccrue(t, s, "5", time.Now().Add(-time.Minute))
		repo := newEarningRepo()
		// An existing wallet: only its row lock serializes the converts (a
		// new wallet would also serialize them on the insert).
		_, err := integrationDB.ExecContext(ctx, "INSERT INTO owner_earning_wallets (user_id) VALUES ($1)", s.owner)
		require.NoError(t, err)
		results := runConcurrently(8, func(int) error {
			_, err := repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "2"), RequestID: uuid.NewString()})
			return err
		})
		ok, insufficient := 0, 0
		for _, err := range results {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, service.ErrOwnerEarningInsufficient):
				insufficient++
			default:
				require.NoError(t, err)
			}
		}
		require.Equal(t, 2, ok)
		require.Equal(t, 6, insufficient)
		require.True(t, mktUserBalance(t, s.owner).Equal(dec(t, "4")))
	})
}

// Two converts that both get past the availability read before either
// commits would overspend; the wallet lock makes the second wait and read
// the first one's entry. The owner's users row is held so the first convert
// cannot commit before the second has started.
func TestOwnerEarningConvertWalletLockSerializes(t *testing.T) {
	ctx := context.Background()
	setMarketSetting(t, service.SettingKeyMarketMinConvertUSD, "1")
	f := newMktFixture(t)
	s := basicSale(f, 100)
	seedAccrue(t, s, "5", time.Now().Add(-time.Minute))
	_, err := integrationDB.ExecContext(ctx, "INSERT INTO owner_earning_wallets (user_id) VALUES ($1)", s.owner)
	require.NoError(t, err)
	holder, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = holder.ExecContext(ctx, "SELECT 1 FROM users WHERE id = $1 FOR UPDATE", s.owner)
	require.NoError(t, err)
	repo := newEarningRepo()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "3"), RequestID: uuid.NewString()})
			results <- err
		}()
	}
	time.Sleep(500 * time.Millisecond)
	require.NoError(t, holder.Rollback())
	var okCount int
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			okCount++
		} else {
			require.ErrorIs(t, err, service.ErrOwnerEarningInsufficient)
		}
	}
	require.Equal(t, 1, okCount)
	require.True(t, mktUserBalance(t, s.owner).Equal(dec(t, "3")))
}

func runConcurrently(n int, fn func(i int) error) []error {
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

func TestOwnerEarningReverse(t *testing.T) {
	ctx := context.Background()
	setMarketSetting(t, service.SettingKeyMarketMinConvertUSD, "1")
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := newEarningRepo()
	_, availableEntry := seedAccrue(t, s, "1.5", time.Now().Add(-time.Hour))
	_, frozenEntry := seedAccrue(t, s, "2", time.Now().Add(time.Hour))
	// A second owner of the same account (owner changed later).
	owner2 := f.user(0)
	s2 := s
	s2.owner = owner2
	_, frozen2 := seedAccrue(t, s2, "4", time.Now().Add(2*time.Hour))
	// Another account of owner 1, not in the account scope.
	acc3, ev3 := f.account(s.owner)
	s3 := s
	s3.account, s3.ownerEvent = acc3, ev3
	_, otherAccountEntry := seedAccrue(t, s3, "8", time.Now().Add(time.Hour))

	res, err := repo.Reverse(ctx, service.OwnerEarningReverseInput{AsOf: time.Now(), AccountID: &s.account, AdminUserID: 1, Note: "fraud"})
	require.NoError(t, err)
	require.Equal(t, 2, res.ReversedCount)
	require.True(t, dec(t, res.ReversedAmount).Equal(dec(t, "6")))
	require.Len(t, res.Owners, 2)
	require.Equal(t, min(s.owner, owner2), res.Owners[0].OwnerUserID)

	reversedOf := func(entry int64) bool {
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM owner_earning_entries WHERE kind = 'reverse' AND reverses_entry_id = $1", entry).Scan(&n))
		return n == 1
	}
	require.True(t, reversedOf(frozenEntry))
	require.True(t, reversedOf(frozen2))
	require.False(t, reversedOf(availableEntry), "available earnings are not clawed back")
	require.False(t, reversedOf(otherAccountEntry), "other account out of scope")

	// Idempotent: nothing left to reverse in this scope.
	res, err = repo.Reverse(ctx, service.OwnerEarningReverseInput{AsOf: time.Now(), AccountID: &s.account, AdminUserID: 1})
	require.NoError(t, err)
	require.Zero(t, res.ReversedCount)

	b, err := repo.Balances(ctx, s.owner)
	require.NoError(t, err)
	require.True(t, b.Available.Equal(dec(t, "1.5")))
	require.True(t, b.Frozen.Equal(dec(t, "8")), "only the other account remains frozen")
	require.True(t, b.Reversed.Equal(dec(t, "2")))

	// Owner scope reverses the rest of owner 1.
	res, err = repo.Reverse(ctx, service.OwnerEarningReverseInput{AsOf: time.Now(), OwnerUserID: &s.owner, AdminUserID: 1})
	require.NoError(t, err)
	require.Equal(t, 1, res.ReversedCount)
	require.True(t, reversedOf(otherAccountEntry))

	entries, err := repo.ListEntries(ctx, s.owner, 0, 100)
	require.NoError(t, err)
	statuses := map[int64]string{}
	for _, e := range entries {
		statuses[e.ID] = e.Status
		require.NotNil(t, e.AccountID)
	}
	require.Equal(t, service.OwnerEarningStatusAvailable, statuses[availableEntry])
	require.Equal(t, service.OwnerEarningStatusReversed, statuses[frozenEntry])

	sums, total, _, err := repo.AdminSummaries(ctx, &s.owner, 0, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, "1.5", sums[0].Available)
	require.Equal(t, "10", sums[0].ReversedTotal)
	require.Equal(t, "0", sums[0].Frozen)
}

// Reverse covers the accrues visible when it starts: one committed while it
// waits for the wallet lock is not reversed.
func TestOwnerEarningReversePinsScope(t *testing.T) {
	ctx := context.Background()
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := newEarningRepo()
	_, first := seedAccrue(t, s, "1", time.Now().Add(time.Hour))
	_, err := integrationDB.ExecContext(ctx, "INSERT INTO owner_earning_wallets (user_id) VALUES ($1)", s.owner)
	require.NoError(t, err)
	holder, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Rollback() })
	var holderPID int
	require.NoError(t, holder.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&holderPID))
	_, err = holder.ExecContext(ctx, "SELECT 1 FROM owner_earning_wallets WHERE user_id = $1 FOR UPDATE", s.owner)
	require.NoError(t, err)
	done := make(chan *service.OwnerEarningReverseResult, 1)
	errc := make(chan error, 1)
	go func() {
		res, err := repo.Reverse(ctx, service.OwnerEarningReverseInput{AsOf: time.Now(), AccountID: &s.account, AdminUserID: 1})
		errc <- err
		done <- res
	}()
	// Wait until the reverse is blocked by the holder: it has pinned its
	// scope and waits for the wallet lock.
	require.Eventually(t, func() bool {
		var blocked bool
		err := integrationDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&blocked)
		return err == nil && blocked
	}, 10*time.Second, 20*time.Millisecond)
	_, second := seedAccrue(t, s, "2", time.Now().Add(time.Hour))
	require.NoError(t, holder.Rollback())
	require.NoError(t, <-errc)
	res := <-done
	require.Equal(t, 1, res.ReversedCount)
	var reversed []int64
	rows, err := integrationDB.QueryContext(ctx, "SELECT reverses_entry_id FROM owner_earning_entries WHERE kind = 'reverse' AND owner_user_id = $1", s.owner)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		reversed = append(reversed, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []int64{first}, reversed)
	require.NotEqual(t, first, second)
}

// Convert and reverse across the maturity instant: the wallet lock
// serializes them and each sees one side of available_at, so an accrue is
// either converted or reversed, never both.
func TestOwnerEarningConvertVsReverseAtMaturity(t *testing.T) {
	ctx := context.Background()
	setMarketSetting(t, service.SettingKeyMarketMinConvertUSD, "1")
	f := newMktFixture(t)
	repo := newEarningRepo()

	t.Run("reverse before maturity, convert after", func(t *testing.T) {
		s := basicSale(f, 100)
		at := time.Now().Add(1500 * time.Millisecond)
		seedAccrue(t, s, "3", at)
		res, err := repo.Reverse(ctx, service.OwnerEarningReverseInput{AsOf: time.Now(), OwnerUserID: &s.owner})
		require.NoError(t, err)
		require.Equal(t, 1, res.ReversedCount)
		time.Sleep(time.Until(at) + 100*time.Millisecond)
		_, err = repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "3"), RequestID: uuid.NewString()})
		require.ErrorIs(t, err, service.ErrOwnerEarningInsufficient)
	})
	t.Run("convert after maturity, reverse finds nothing", func(t *testing.T) {
		s := basicSale(f, 100)
		at := time.Now().Add(800 * time.Millisecond)
		seedAccrue(t, s, "3", at)
		time.Sleep(time.Until(at) + 100*time.Millisecond)
		_, err := repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "3"), RequestID: uuid.NewString()})
		require.NoError(t, err)
		res, err := repo.Reverse(ctx, service.OwnerEarningReverseInput{AsOf: time.Now(), OwnerUserID: &s.owner})
		require.NoError(t, err)
		require.Zero(t, res.ReversedCount)
	})
	t.Run("racing at the instant", func(t *testing.T) {
		s := basicSale(f, 100)
		at := time.Now().Add(1200 * time.Millisecond)
		seedAccrue(t, s, "3", at)
		// Hold the wallet so both operations queue behind it, then release
		// right at the maturity instant.
		_, err := integrationDB.ExecContext(ctx, "INSERT INTO owner_earning_wallets (user_id) VALUES ($1) ON CONFLICT DO NOTHING", s.owner)
		require.NoError(t, err)
		holder, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = holder.ExecContext(ctx, "SELECT 1 FROM owner_earning_wallets WHERE user_id = $1 FOR UPDATE", s.owner)
		require.NoError(t, err)
		results := make(chan error, 2)
		go func() {
			_, err := repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "3"), RequestID: uuid.NewString()})
			results <- err
		}()
		go func() {
			_, err := repo.Reverse(ctx, service.OwnerEarningReverseInput{AsOf: time.Now(), OwnerUserID: &s.owner})
			results <- err
		}()
		time.Sleep(time.Until(at))
		require.NoError(t, holder.Rollback())
		for i := 0; i < 2; i++ {
			if err := <-results; err != nil {
				require.ErrorIs(t, err, service.ErrOwnerEarningInsufficient)
			}
		}
		b, err := repo.Balances(ctx, s.owner)
		require.NoError(t, err)
		require.True(t, b.Converted.Add(b.Reversed).Equal(dec(t, "3")), "converted %s reversed %s", b.Converted, b.Reversed)
		require.True(t, b.Available.IsZero())
		require.True(t, b.Frozen.IsZero())
		require.True(t, mktUserBalance(t, s.owner).Equal(b.Converted))
	})
}

// ---- reconcile ----

func TestSettlementReconcileMarket(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 0)
	setMarketSetting(t, service.SettingKeyMarketMinConvertUSD, "0.01")
	f := newMktFixture(t)
	s := basicSale(f, 100)
	from := time.Now().Add(-time.Second)
	billing := NewUsageBillingRepository(nil, integrationDB)
	sold := f.admit(s)
	_, err := billing.Apply(ctx, mktCommand(sold, 1))
	require.NoError(t, err)
	_, err = newEarningRepo().Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "0.5"), RequestID: uuid.NewString()})
	require.NoError(t, err)
	unbilled := f.admit(s)
	// A positive earning without its accrue (written around the guards).
	missingEvent, _ := seedAccrue(t, s, "0.3", time.Now())
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "SET LOCAL session_replication_role = replica")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "DELETE FROM owner_earning_entries WHERE settlement_event_id = $1", missingEvent)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	// A market_earning history row without a convert.
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, validity_days)
		VALUES ($1, 'market_earning', 1, 'used', $2, NOW(), 30)`, fmt.Sprintf("MKTEARN-%d", 1<<50+time.Now().UnixNano()%1000000), s.owner)
	require.NoError(t, err)

	out, err := NewSettlementReconcileRepository(integrationDB).Reconcile(ctx, service.SettlementReconcileQuery{
		From: from, To: time.Now().Add(time.Second), CoverageStart: from, Page: 1, PageSize: 10, Now: time.Now()})
	require.NoError(t, err)
	m := out.Market
	require.NotNil(t, m)
	found := map[string]bool{}
	for _, it := range m.Issues {
		switch {
		case it.Kind == service.SettlementMarketIssueAdmissionWithoutEvent && it.AdmissionID != nil && *it.AdmissionID == unbilled.ID:
			require.Equal(t, "pending", it.Status)
			found["admission"] = true
		case it.Kind == service.SettlementMarketIssueAccrueMissing && it.EventID != nil && *it.EventID == missingEvent:
			found["accrue_missing"] = true
		case it.Kind == service.SettlementMarketIssueConvertHistoryOrphan && it.OwnerUserID != nil && *it.OwnerUserID == s.owner:
			found["orphan"] = true
		case it.OwnerUserID != nil && *it.OwnerUserID == s.owner:
			t.Fatalf("unexpected issue for the healthy sale/convert: %+v", it)
		case it.AdmissionID != nil && *it.AdmissionID == sold.ID:
			t.Fatalf("settled admission reported: %+v", it)
		}
	}
	require.Equal(t, map[string]bool{"admission": true, "accrue_missing": true, "orphan": true}, found)
	// Money findings are listed before admissions awaiting a decision.
	seenAdmission := false
	for _, it := range m.Issues {
		if it.Kind == service.SettlementMarketIssueAdmissionWithoutEvent {
			seenAdmission = true
		} else {
			require.False(t, seenAdmission, "money finding %s after an admission", it.Kind)
		}
	}
	require.GreaterOrEqual(t, m.Summary.SaleCount, int64(2))
	require.Equal(t, service.SettlementMarketScopeOwnerBacked, m.Scope)
}

// The wallet transactions run READ COMMITTED explicitly: a convert that waits
// behind a reverse which holds the wallet past the maturity instant must see
// the reverse once it gets the lock (never both reversed and converted).
func TestOwnerEarningConvertWaitingBehindReverseAcrossMaturity(t *testing.T) {
	ctx := context.Background()
	setMarketSetting(t, service.SettingKeyMarketMinConvertUSD, "1")
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := newEarningRepo()
	at := time.Now().Add(1200 * time.Millisecond)
	seedAccrue(t, s, "3", at)
	_, err := integrationDB.ExecContext(ctx, "INSERT INTO owner_earning_wallets (user_id) VALUES ($1)", s.owner)
	require.NoError(t, err)

	reached, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	reverseBeforeCommit = func() {
		close(reached)
		<-release
	}
	var wg sync.WaitGroup
	// Runs even when an assertion below fails: unblock the reverse, wait for
	// both goroutines, then drop the hook.
	t.Cleanup(func() {
		releaseFn()
		wg.Wait()
		reverseBeforeCommit = nil
	})
	reverseErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, err := repo.Reverse(ctx, service.OwnerEarningReverseInput{OwnerUserID: &s.owner, AsOf: time.Now()})
		if err == nil && res.ReversedCount != 1 {
			err = fmt.Errorf("reversed %d", res.ReversedCount)
		}
		reverseErr <- err
	}()
	select {
	case <-reached: // reversed under the wallet lock, not committed
	case err := <-reverseErr:
		t.Fatalf("reverse returned before its commit hook: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("reverse did not reach its commit hook")
	}
	convertErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := repo.Convert(ctx, service.OwnerEarningConvertInput{OwnerUserID: s.owner, Amount: dec(t, "3"), RequestID: uuid.NewString()})
		convertErr <- err
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := integrationDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%owner_earning_wallets%FOR UPDATE%')`).Scan(&waiting)
		return err == nil && waiting
	}, 10*time.Second, 20*time.Millisecond, "convert waits for the wallet")
	time.Sleep(time.Until(at) + 200*time.Millisecond) // past maturity while the reverse holds the lock
	releaseFn()
	require.NoError(t, <-reverseErr)
	require.ErrorIs(t, <-convertErr, service.ErrOwnerEarningInsufficient)
	b, err := repo.Balances(ctx, s.owner)
	require.NoError(t, err)
	require.True(t, b.Converted.IsZero())
	require.True(t, b.Reversed.Equal(dec(t, "3")))
	require.True(t, mktUserBalance(t, s.owner).IsZero())
}

// A retry with the same as_of selects its scope again; earnings recorded
// after as_of are outside it (as_of is a cutoff, not an idempotency key).
func TestOwnerEarningReverseAsOfRetry(t *testing.T) {
	ctx := context.Background()
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := newEarningRepo()
	_, first := seedAccrue(t, s, "1", time.Now().Add(time.Hour))
	asOf := time.Now()
	res, err := repo.Reverse(ctx, service.OwnerEarningReverseInput{AccountID: &s.account, AsOf: asOf})
	require.NoError(t, err)
	require.Equal(t, 1, res.ReversedCount)
	require.True(t, res.Cutoff.Equal(asOf))
	time.Sleep(10 * time.Millisecond)
	_, later := seedAccrue(t, s, "2", time.Now().Add(time.Hour))
	res, err = repo.Reverse(ctx, service.OwnerEarningReverseInput{AccountID: &s.account, AsOf: asOf})
	require.NoError(t, err)
	require.Zero(t, res.ReversedCount)
	var n int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM owner_earning_entries WHERE kind = 'reverse' AND reverses_entry_id = $1", later).Scan(&n))
	require.Zero(t, n, "the later accrue is untouched")
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM owner_earning_entries WHERE kind = 'reverse' AND reverses_entry_id = $1", first).Scan(&n))
	require.Equal(t, 1, n)

	for _, bad := range []time.Time{{}, time.Now().Add(time.Hour)} {
		_, err = repo.Reverse(ctx, service.OwnerEarningReverseInput{AccountID: &s.account, AsOf: bad})
		require.ErrorIs(t, err, service.ErrOwnerEarningReverseAsOf)
	}
}

// A COMMIT that never reached the database (context done before it was
// sent) is reported as rolled back, not as an unknown outcome.
func TestMarketSettlementCommitNotSentIsRolledBack(t *testing.T) {
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marketBeforeCommit = cancel
	t.Cleanup(func() { marketBeforeCommit = nil })
	before := loadMktState(t, s.buyer, s.key, s.owner)
	_, err := repo.Apply(ctx, mktCommand(f.admit(s), 1))
	require.True(t, errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrTxDone), "definite rollback: %v", err)
	require.NotErrorIs(t, err, service.ErrUsageBillingCommitUnknown)
	marketBeforeCommit = nil
	require.Eventually(t, func() bool { return loadMktState(t, s.buyer, s.key, s.owner) == before }, 5*time.Second, 20*time.Millisecond)

	// Errors from inside Commit: only a bare ErrTxDone is a rollback.
	for _, unknown := range []error{errors.New("connection reset"), context.Canceled, context.DeadlineExceeded, fmt.Errorf("pq: %w", context.Canceled),
		fmt.Errorf("wrapped: %w", sql.ErrTxDone)} {
		require.ErrorIs(t, classifyMarketCommitError(unknown), service.ErrUsageBillingCommitUnknown, unknown.Error())
	}
	require.NotErrorIs(t, classifyMarketCommitError(sql.ErrTxDone), service.ErrUsageBillingCommitUnknown)
}

// Owner + account together touch only that owner's earnings on that account;
// account alone covers every owner the account had.
func TestOwnerEarningReverseOwnerAndAccountScope(t *testing.T) {
	ctx := context.Background()
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := newEarningRepo()
	previous := f.user(0)
	sp := s
	sp.owner = previous
	_, prevEntry := seedAccrue(t, sp, "4", time.Now().Add(time.Hour))
	_, curEntry := seedAccrue(t, s, "2", time.Now().Add(time.Hour))
	acc2, ev2 := f.account(s.owner)
	s2 := s
	s2.account, s2.ownerEvent = acc2, ev2
	_, otherAccount := seedAccrue(t, s2, "8", time.Now().Add(time.Hour))

	res, err := repo.Reverse(ctx, service.OwnerEarningReverseInput{AccountID: &s.account, OwnerUserID: &s.owner, AsOf: time.Now()})
	require.NoError(t, err)
	require.Equal(t, 1, res.ReversedCount)
	reversed := func(entry int64) bool {
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM owner_earning_entries WHERE kind = 'reverse' AND reverses_entry_id = $1", entry).Scan(&n))
		return n == 1
	}
	require.True(t, reversed(curEntry))
	require.False(t, reversed(prevEntry), "the account's previous owner is untouched")
	require.False(t, reversed(otherAccount), "the owner's other account is untouched")

	res, err = repo.Reverse(ctx, service.OwnerEarningReverseInput{AccountID: &s.account, AsOf: time.Now()})
	require.NoError(t, err)
	require.Equal(t, 1, res.ReversedCount)
	require.True(t, reversed(prevEntry), "account scope covers its previous owner")
	require.False(t, reversed(otherAccount))
}

// Pilot price promise: the admission snapshot is kept for audit, the charge
// follows the price at recording time (here: changed after the admission,
// then changed to 0), and the split is conserved on the actual charge.
func TestMarketSettlementPriceChangedAfterAdmission(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	audit := newEarningRepo()
	snapshot := &service.MarketPriceSnapshot{Model: "claude-sonnet-4", PriceVersion: "v-admission", GroupRate: 1.2, ModelRate: 1,
		UserDiscount: 1, InputPerToken: 0.000003, OutputPerToken: 0.000015}

	// Admitted at 3e-6/token; the price doubled before the usage was
	// recorded: 1000 input tokens × 6e-6 × 1.2.
	a := f.admit(s)
	cmd := mktCommand(a, 0.0072)
	cmd.Settlement.MarketPrice = snapshot
	_, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	got, err := audit.SaleAudit(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, got.Admission.ID)
	require.Equal(t, service.MarketPriceAuditFrom(snapshot), got.AdmissionPrice, "snapshot persisted unchanged")
	require.NotNil(t, got.Settlement)
	charged := dec(t, got.Settlement.ChargedAmount)
	require.True(t, charged.Equal(dec(t, "0.0072")), "charge follows the new price: %s", charged)
	require.Equal(t, "0.0072", got.Settlement.ChargeInput)
	earning, platform := dec(t, got.Settlement.OwnerEarning), dec(t, got.Settlement.PlatformAmount)
	require.True(t, earning.Equal(charged.Mul(dec(t, "0.8")).Truncate(10)))
	require.True(t, charged.Equal(earning.Add(platform)))
	require.True(t, got.Settlement.SplitConserved)

	// Price changed to 0 after the admission: the sale is kept as a
	// zero-earning event with its snapshot.
	z := f.admit(s)
	zcmd := mktCommand(z, 0)
	zcmd.Settlement.MarketPrice = snapshot
	_, err = repo.Apply(ctx, zcmd)
	require.NoError(t, err)
	got, err = audit.SaleAudit(ctx, z.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Settlement)
	require.Equal(t, service.SettlementEarningZeroReasonZeroCharge, *got.Settlement.EarningZeroReason)
	require.Equal(t, "0", got.Settlement.ChargedAmount)
	require.Equal(t, service.MarketPriceAuditFrom(snapshot), got.AdmissionPrice)

	// An admission without a settlement, and a missing one.
	pending := f.admit(s)
	got, err = audit.SaleAudit(ctx, pending.ID)
	require.NoError(t, err)
	require.Nil(t, got.Settlement)
	require.Nil(t, got.AdmissionPrice)
	_, err = audit.SaleAudit(ctx, 1<<60)
	require.ErrorIs(t, err, service.ErrMarketAdmissionNotFound)

	// Only the shared_sale insert writes admission_price: a shadow event of
	// the same key (owned account, non-hosted group → owned_other) and a
	// market sale without a snapshot stay NULL.
	group := int64(77)
	shadow := &service.UsageBillingCommand{RequestID: "shadow-" + uuid.NewString(), APIKeyID: s.key, UserID: s.buyer, AccountID: s.account,
		AccountType: service.AccountTypeSetupToken, Model: "TEST_ONLY_model", BalanceCost: 1,
		Settlement: &service.UsageSettlementInput{GroupID: &group, StandardCost: 1, ChargeInput: 1, ChargeSource: service.SettlementChargeSourceBalance}}
	_, err = repo.Apply(ctx, shadow)
	require.NoError(t, err)
	_, err = repo.Apply(ctx, mktCommand(f.admit(s), 1))
	require.NoError(t, err)
	var shadowRows, withPrice int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE category <> 'shared_sale'),
		count(*) FILTER (WHERE admission_price IS NOT NULL) FROM usage_settlement_events WHERE api_key_id = $1`, s.key).Scan(&shadowRows, &withPrice))
	require.Equal(t, 1, shadowRows)
	require.Equal(t, 2, withPrice, "only the two sales that carried a snapshot")
}

// A price snapshot with NUL and invalid UTF-8 never fails the sale: the
// charge, the event and the accrue commit, the audit is stored normalized.
func TestMarketSettlementPriceSnapshotWithNULStillSettles(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	repo := NewUsageBillingRepository(nil, integrationDB)
	a := f.admit(s)
	cmd := mktCommand(a, 1)
	cmd.Settlement.MarketPrice = &service.MarketPriceSnapshot{Model: "claude\x00-sonnet", PriceVersion: "v\x00-1\xff", GroupRate: 1.2}
	before := loadMktState(t, s.buyer, s.key, s.owner)
	res, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, res.Applied)
	ev, ok := loadMktEvent(t, a)
	require.True(t, ok)
	_, ok = loadAccrue(t, ev.ID)
	require.True(t, ok)
	require.True(t, dec(t, before.Balance).Sub(dec(t, loadMktState(t, s.buyer, s.key, s.owner).Balance)).Equal(decimal.NewFromInt(1)))
	got, err := newEarningRepo().SaleAudit(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet", got.AdmissionPrice.Model)
	require.Equal(t, "v-1", got.AdmissionPrice.PriceVersion)
}

// The pilot price promise end to end through GatewayService.RecordUsage:
// admitted at the snapshot price (group rate 1.2), the admin then changes
// the group rate (2.4, then 0) before the usage is recorded. RecordUsage
// charges the new price, the event keeps the old snapshot, the split is
// conserved; the 0 price settles as zero_charge.
func TestMarketRecordUsageChargesCurrentPriceKeepsSnapshot(t *testing.T) {
	ctx := context.Background()
	setMarketCut(t, "0.2", 7)
	f := newMktFixture(t)
	s := basicSale(f, 100)
	g := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "mkt-price-" + uuid.NewString(), Platform: service.PlatformAnthropic, RateMultiplier: 1.2})
	f.groups = append(f.groups, g.ID)
	s.group = g.ID

	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	billing := service.NewBillingService(cfg, nil)
	cache := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(cache.Stop)
	usage := newUsageLogRepositoryWithSQL(integrationEntClient, integrationDB)
	usage.db = nil // synchronous log insert
	svc := service.NewGatewayService(nil, nil, usage, NewUsageBillingRepository(integrationEntClient, integrationDB), nil, nil, nil, nil, cfg,
		nil, nil, billing, nil, cache, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil)

	const model = "claude-sonnet-4"
	tokens := service.UsageTokens{InputTokens: 1000, OutputTokens: 200}
	snapshot := &service.MarketPriceSnapshot{Model: model, PriceVersion: "v-admission", GroupRate: 1.2, ModelRate: 1, UserDiscount: 1}
	record := func(groupRate float64) *service.MarketAdmission {
		a := f.admit(s)
		group := g.ID
		key := &service.APIKey{ID: s.key, UserID: s.buyer, GroupID: &group,
			Group: &service.Group{ID: g.ID, Platform: service.PlatformAnthropic, RateMultiplier: groupRate, SubscriptionType: service.SubscriptionTypeStandard, Status: service.StatusActive}}
		err := svc.RecordUsage(ctx, &service.RecordUsageInput{
			Result:           &service.ForwardResult{RequestID: "upstream-" + uuid.NewString(), Model: model, Duration: time.Second, Usage: service.ClaudeUsage{InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens}},
			APIKey:           key,
			User:             &service.User{ID: s.buyer},
			Account:          &service.Account{ID: s.account, Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken},
			MarketPath:       true,
			Market:           &service.MarketSettlementProof{AdmissionID: a.ID, AttemptID: a.AttemptID, RequestRef: a.RequestRef, AccountID: a.AccountID, GroupID: a.GroupID, BuyerUserID: a.BuyerUserID, APIKeyID: a.APIKeyID},
			MarketPrice:      snapshot,
			BillingRequestID: service.ResolveUsageBillingRequestID(context.WithValue(ctx, ctxkey.ClientRequestID, a.RequestRef), ""),
		})
		require.NoError(t, err)
		return a
	}

	atAdmission, err := billing.CalculateCost(model, tokens, 1.2)
	require.NoError(t, err)
	atRecording, err := billing.CalculateCost(model, tokens, 2.4)
	require.NoError(t, err)
	require.Greater(t, atAdmission.ActualCost, 0.0)

	before := loadMktState(t, s.buyer, s.key, s.owner)
	a := record(2.4)
	got, err := newEarningRepo().SaleAudit(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, service.MarketPriceAuditFrom(snapshot), got.AdmissionPrice, "old snapshot kept")
	require.NotNil(t, got.Settlement)
	charged := dec(t, got.Settlement.ChargedAmount)
	require.True(t, charged.Equal(dec(t, before.Balance).Sub(dec(t, loadMktState(t, s.buyer, s.key, s.owner).Balance))))
	require.InDelta(t, atRecording.ActualCost, charged.InexactFloat64(), 1e-8, "charged at the new price")
	require.Greater(t, charged.InexactFloat64(), atAdmission.ActualCost*1.5, "not the admission price")
	earning, platform := dec(t, got.Settlement.OwnerEarning), dec(t, got.Settlement.PlatformAmount)
	require.True(t, earning.Equal(charged.Mul(dec(t, "0.8")).Truncate(10)))
	require.True(t, charged.Equal(earning.Add(platform)))
	require.True(t, got.Settlement.SplitConserved)
	// The final price basis comes from the usage log.
	require.NotNil(t, got.Settlement.Model)
	require.Equal(t, model, *got.Settlement.Model)
	require.NotNil(t, got.Settlement.FinalPrice)
	require.Equal(t, "2.4", got.Settlement.FinalPrice.RateMultiplier)
	require.NotNil(t, got.Settlement.BillingModel)

	z := record(0)
	got, err = newEarningRepo().SaleAudit(ctx, z.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Settlement)
	require.Equal(t, "0", got.Settlement.ChargedAmount)
	require.Equal(t, service.SettlementEarningZeroReasonZeroCharge, *got.Settlement.EarningZeroReason)
	require.Equal(t, service.MarketPriceAuditFrom(snapshot), got.AdmissionPrice)
}
