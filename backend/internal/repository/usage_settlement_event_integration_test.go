//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type settlementFixture struct {
	user    *service.User
	key     *service.APIKey
	account *service.Account
}

func newSettlementFixture(t *testing.T, platform string) settlementFixture {
	t.Helper()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "settlement-" + uuid.NewString() + "@example.invalid", PasswordHash: "TEST_ONLY", Balance: 100})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "TEST_ONLY_" + uuid.NewString(), Name: "settlement", Quota: 50})
	account := mustCreateAccount(t, client, &service.Account{Name: "settlement-" + uuid.NewString(), Type: service.AccountTypeOAuth, Platform: platform})
	return settlementFixture{user: user, key: key, account: account}
}

func (f settlementFixture) setOwner(t *testing.T, owner *int64) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), "UPDATE accounts SET owner_user_id=$1 WHERE id=$2", owner, f.account.ID)
	require.NoError(t, err)
}

func (f settlementFixture) hostedCmd(requestID string, charge, standard, rate float64) *service.UsageBillingCommand {
	groupID := int64(4242)
	in := &service.UsageSettlementInput{GroupID: &groupID, Hosted: true, FeeRate: &rate, StandardCost: standard, ChargeSource: service.SettlementChargeSourceNone}
	if charge > 0 {
		in.ChargeInput = charge
		in.ChargeSource = service.SettlementChargeSourceBalance
	} else {
		in.FreeGroup = true
	}
	return &service.UsageBillingCommand{RequestID: requestID, APIKeyID: f.key.ID, UserID: f.user.ID, AccountID: f.account.ID,
		AccountType: f.account.Type, Model: "TEST_ONLY_model", BalanceCost: charge, APIKeyQuotaCost: standard, Settlement: in}
}

type settlementEventRow struct {
	Category      string
	Anomaly       bool
	BuyerUserID   int64
	AccountID     int64
	OwnerUserID   sql.NullInt64
	OwnerSource   string
	SelfPurchase  bool
	GroupID       sql.NullInt64
	FeeRate       sql.NullFloat64
	StandardCost  float64
	ChargeInput   float64
	ChargeSource  string
	ZeroReason    sql.NullString
	InvalidFields pq.StringArray
	OwnerEarning  float64
	RuleVersion   int16
}

func loadSettlementEvent(t *testing.T, requestID string, keyID int64) (settlementEventRow, bool) {
	t.Helper()
	var row settlementEventRow
	err := integrationDB.QueryRowContext(context.Background(), `
		SELECT category, anomaly, buyer_user_id, account_id, owner_user_id, owner_source, self_purchase,
			group_id, fee_rate, standard_cost, charge_input, charge_source, zero_reason, invalid_fields, owner_earning, rule_version
		FROM usage_settlement_events WHERE request_id=$1 AND api_key_id=$2`, requestID, keyID).Scan(
		&row.Category, &row.Anomaly, &row.BuyerUserID, &row.AccountID, &row.OwnerUserID, &row.OwnerSource, &row.SelfPurchase,
		&row.GroupID, &row.FeeRate, &row.StandardCost, &row.ChargeInput, &row.ChargeSource, &row.ZeroReason, &row.InvalidFields, &row.OwnerEarning, &row.RuleVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false
	}
	require.NoError(t, err)
	return row, true
}

func countSettlementEvents(t *testing.T, keyID int64) int {
	t.Helper()
	var n int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT count(*) FROM usage_settlement_events WHERE api_key_id=$1", keyID).Scan(&n))
	return n
}

type settlementBillingState struct {
	Balance, Quota float64
	Dedup          int
}

func loadSettlementBillingState(t *testing.T, f settlementFixture) settlementBillingState {
	t.Helper()
	var s settlementBillingState
	ctx := context.Background()
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", f.user.ID).Scan(&s.Balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id=$1", f.key.ID).Scan(&s.Quota))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1", f.key.ID).Scan(&s.Dedup))
	return s
}

func TestSettlementEvent_HostedChargedAndFree(t *testing.T) {
	ctx := context.Background()
	f := newSettlementFixture(t, service.PlatformAnthropic)
	f.setOwner(t, &f.user.ID)
	repo := NewUsageBillingRepository(nil, integrationDB)

	res, err := repo.Apply(ctx, f.hostedCmd("charged-"+uuid.NewString(), 0.2, 1, 0.2))
	require.NoError(t, err)
	require.True(t, res.Applied)
	freeID := "free-" + uuid.NewString()
	res, err = repo.Apply(ctx, f.hostedCmd(freeID, 0, 1.5, 0))
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, 2, countSettlementEvents(t, f.key.ID))

	rows, err := integrationDB.QueryContext(ctx, "SELECT request_id FROM usage_settlement_events WHERE api_key_id=$1 ORDER BY id", f.key.ID)
	require.NoError(t, err)
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	charged, ok := loadSettlementEvent(t, ids[0], f.key.ID)
	require.True(t, ok)
	require.Equal(t, service.SettlementCategoryHostedSelf, charged.Category)
	require.False(t, charged.Anomaly)
	require.Equal(t, f.user.ID, charged.BuyerUserID)
	require.Equal(t, f.account.ID, charged.AccountID)
	require.Equal(t, f.user.ID, charged.OwnerUserID.Int64)
	require.Equal(t, "settlement", charged.OwnerSource)
	require.True(t, charged.SelfPurchase)
	require.Equal(t, int64(4242), charged.GroupID.Int64)
	require.InDelta(t, 0.2, charged.FeeRate.Float64, 1e-12)
	require.InDelta(t, 1, charged.StandardCost, 1e-12)
	require.InDelta(t, 0.2, charged.ChargeInput, 1e-12)
	require.Equal(t, "balance", charged.ChargeSource)
	require.False(t, charged.ZeroReason.Valid)
	require.Nil(t, charged.InvalidFields)
	require.Zero(t, charged.OwnerEarning)
	require.Equal(t, int16(1), charged.RuleVersion)

	free, ok := loadSettlementEvent(t, freeID, f.key.ID)
	require.True(t, ok)
	require.Zero(t, free.ChargeInput)
	require.Equal(t, "none", free.ChargeSource)
	require.Equal(t, "free_group", free.ZeroReason.String)
	require.InDelta(t, 1.5, free.StandardCost, 1e-12)

	state := loadSettlementBillingState(t, f)
	require.InDelta(t, 99.8, state.Balance, 1e-9)
	require.InDelta(t, 2.5, state.Quota, 1e-9)
}

func TestSettlementEvent_PlatformSaleHasNoEventAndOwnedOtherIsRecorded(t *testing.T) {
	ctx := context.Background()
	f := newSettlementFixture(t, service.PlatformAnthropic)
	repo := NewUsageBillingRepository(nil, integrationDB)
	platformCmd := func(id string) *service.UsageBillingCommand {
		cmd := f.hostedCmd(id, 1, 1, 0)
		cmd.Settlement.Hosted = false
		cmd.Settlement.FeeRate = nil
		return cmd
	}
	_, err := repo.Apply(ctx, platformCmd("platform-"+uuid.NewString()))
	require.NoError(t, err)
	require.Zero(t, countSettlementEvents(t, f.key.ID))
	require.InDelta(t, 99, loadSettlementBillingState(t, f).Balance, 1e-9)

	other := mustCreateUser(t, testEntClient(t), &service.User{Email: "settlement-owner-" + uuid.NewString() + "@example.invalid", PasswordHash: "TEST_ONLY"})
	f.setOwner(t, &other.ID)
	ownedID := "owned-" + uuid.NewString()
	_, err = repo.Apply(ctx, platformCmd(ownedID))
	require.NoError(t, err)
	ev, ok := loadSettlementEvent(t, ownedID, f.key.ID)
	require.True(t, ok)
	require.Equal(t, service.SettlementCategoryOwnedOther, ev.Category)
	require.True(t, ev.Anomaly)
	require.Equal(t, other.ID, ev.OwnerUserID.Int64)
	require.False(t, ev.SelfPurchase)
	require.False(t, ev.FeeRate.Valid)
}

func TestSettlementEvent_ReplayArchiveAndConflictWriteNoEvent(t *testing.T) {
	ctx := context.Background()
	f := newSettlementFixture(t, service.PlatformAnthropic)
	f.setOwner(t, &f.user.ID)
	repo := NewUsageBillingRepository(nil, integrationDB)
	id := "replay-" + uuid.NewString()
	_, err := repo.Apply(ctx, f.hostedCmd(id, 1, 5, 0.2))
	require.NoError(t, err)
	before := loadSettlementBillingState(t, f)

	res, err := repo.Apply(ctx, f.hostedCmd(id, 1, 5, 0.2))
	require.NoError(t, err)
	require.False(t, res.Applied)
	_, err = repo.Apply(ctx, f.hostedCmd(id, 2, 5, 0.2))
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	require.Equal(t, 1, countSettlementEvents(t, f.key.ID))
	require.Equal(t, before, loadSettlementBillingState(t, f))

	// Archived identity (dedup row moved to the archive).
	archived := "archived-" + uuid.NewString()
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO usage_billing_dedup_archive (request_id, api_key_id, request_fingerprint, created_at, archived_at)
		VALUES ($1, $2, $3, NOW(), NOW())`, archived, f.key.ID, fingerprintOf(f.hostedCmd(archived, 1, 5, 0.2)))
	require.NoError(t, err)
	res, err = repo.Apply(ctx, f.hostedCmd(archived, 1, 5, 0.2))
	require.NoError(t, err)
	require.False(t, res.Applied)
	_, err = repo.Apply(ctx, f.hostedCmd(archived, 3, 5, 0.2))
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	_, ok := loadSettlementEvent(t, archived, f.key.ID)
	require.False(t, ok)
	after := loadSettlementBillingState(t, f)
	require.Equal(t, before.Balance, after.Balance)
	require.Equal(t, before.Quota, after.Quota)
}

func fingerprintOf(cmd *service.UsageBillingCommand) string {
	cmd.Normalize()
	return cmd.RequestFingerprint
}

func TestSettlementEvent_ExistingEventWithoutDedupRollsBackBilling(t *testing.T) {
	ctx := context.Background()
	f := newSettlementFixture(t, service.PlatformAnthropic)
	f.setOwner(t, &f.user.ID)
	repo := NewUsageBillingRepository(nil, integrationDB)
	id := "orphan-" + uuid.NewString()
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_settlement_events (request_id, api_key_id, category, rule_version, buyer_user_id, account_id, owner_source, self_purchase, standard_cost, charge_input, charge_source)
		VALUES ($1, $2, 'hosted_self', 1, $3, $4, 'settlement', false, 1, 1, 'balance')`, id, f.key.ID, f.user.ID, f.account.ID)
	require.NoError(t, err)
	before := loadSettlementBillingState(t, f)

	cmd := f.hostedCmd(id, 1, 5, 0.2)
	cmd.APIKeyRateLimitCost = 1
	_, err = repo.Apply(ctx, cmd)
	require.ErrorIs(t, err, service.ErrUsageSettlementEventConflict)
	require.Equal(t, before, loadSettlementBillingState(t, f), "balance, quota and dedup unchanged")
	var usage5h float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT usage_5h FROM api_keys WHERE id=$1", f.key.ID).Scan(&usage5h))
	require.Zero(t, usage5h)
	require.Equal(t, 1, countSettlementEvents(t, f.key.ID))
}

// holdSettlementTableLock takes an ACCESS EXCLUSIVE lock on the event table
// from another connection until the returned release function runs.
func holdSettlementTableLock(t *testing.T) func() {
	t.Helper()
	ctx := context.Background()
	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "BEGIN")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "LOCK TABLE usage_settlement_events IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
	}
	t.Cleanup(release)
	return release
}

func TestSettlementEvent_InsertFailureStillCommitsBilling(t *testing.T) {
	ctx := context.Background()
	f := newSettlementFixture(t, service.PlatformAnthropic)
	f.setOwner(t, &f.user.ID)
	repo := NewUsageBillingRepository(nil, integrationDB)
	release := holdSettlementTableLock(t)
	failuresBefore := service.GetUsageSettlementMetricsSnapshot().WriteFailures

	id := "locked-" + uuid.NewString()
	started := time.Now()
	res, err := repo.Apply(ctx, f.hostedCmd(id, 1.5, 7, 0.2))
	elapsed := time.Since(started)
	release()
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Less(t, elapsed, 5*time.Second, "segment lock_timeout bounds the wait")
	require.Equal(t, failuresBefore+1, service.GetUsageSettlementMetricsSnapshot().WriteFailures)
	_, ok := loadSettlementEvent(t, id, f.key.ID)
	require.False(t, ok)
	state := loadSettlementBillingState(t, f)
	require.InDelta(t, 98.5, state.Balance, 1e-9)
	require.InDelta(t, 7, state.Quota, 1e-9)
	require.Equal(t, 1, state.Dedup)
}

func TestSettlementEvent_RollbackToFailureReturnsError(t *testing.T) {
	ctx := context.Background()
	f := newSettlementFixture(t, service.PlatformAnthropic)
	f.setOwner(t, &f.user.ID)
	repo := NewUsageBillingRepository(nil, integrationDB)
	release := holdSettlementTableLock(t)
	defer release()
	id := "terminated-" + uuid.NewString()

	// Kill the billing backend while its event INSERT waits on the lock: the
	// INSERT fails and ROLLBACK TO cannot run on the dead connection.
	killed := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(900 * time.Millisecond)
		for time.Now().Before(deadline) {
			var pid sql.NullInt64
			err := integrationDB.QueryRowContext(context.Background(), `SELECT pid FROM pg_stat_activity
				WHERE wait_event_type = 'Lock' AND query LIKE '%INSERT INTO usage_settlement_events%' LIMIT 1`).Scan(&pid)
			if err == nil && pid.Valid {
				_, err = integrationDB.ExecContext(context.Background(), "SELECT pg_terminate_backend($1)", pid.Int64)
				killed <- err
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		killed <- errors.New("billing backend never waited on the event lock")
	}()
	_, err := repo.Apply(ctx, f.hostedCmd(id, 2, 3, 0.2))
	require.NoError(t, <-killed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rollback to settlement savepoint")
	release()
	state := loadSettlementBillingState(t, f)
	require.InDelta(t, 100, state.Balance, 1e-9, "nothing committed")
	require.Zero(t, state.Quota)
	require.Zero(t, state.Dedup)
}

func TestSettlementEvent_SegmentRestoresSessionTimeouts(t *testing.T) {
	ctx := context.Background()
	f := newSettlementFixture(t, service.PlatformAnthropic)
	f.setOwner(t, &f.user.ID)
	readTimeouts := func(tx *sql.Tx) (string, string) {
		var lock, stmt string
		require.NoError(t, tx.QueryRowContext(ctx, "SELECT current_setting('lock_timeout'), current_setting('statement_timeout')").Scan(&lock, &stmt))
		return lock, stmt
	}

	setTimeouts := func(tx *sql.Tx) {
		_, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '7s'; SET LOCAL statement_timeout = '9s'")
		require.NoError(t, err)
	}

	// Failure path: the insert hits the segment lock_timeout, ROLLBACK TO, and
	// the pre-segment values are back.
	release := holdSettlementTableLock(t)
	failTx := testTx(t)
	setTimeouts(failTx)
	failID := "restore-fail-" + uuid.NewString()
	require.NoError(t, recordUsageSettlementEvent(ctx, failTx, f.hostedCmd(failID, 1, 1, 0.2)))
	release()
	lock, stmt := readTimeouts(failTx)
	require.Equal(t, "7s", lock)
	require.Equal(t, "9s", stmt)
	var n int
	require.NoError(t, failTx.QueryRowContext(ctx, "SELECT count(*) FROM usage_settlement_events WHERE request_id=$1", failID).Scan(&n), "transaction still usable")
	require.Zero(t, n)

	// Success path: RELEASE keeps the segment's SET LOCAL values unless restored.
	okTx := testTx(t)
	setTimeouts(okTx)
	okID := "restore-ok-" + uuid.NewString()
	require.NoError(t, recordUsageSettlementEvent(ctx, okTx, f.hostedCmd(okID, 1, 1, 0.2)))
	require.NoError(t, okTx.QueryRowContext(ctx, "SELECT count(*) FROM usage_settlement_events WHERE request_id=$1", okID).Scan(&n))
	require.Equal(t, 1, n)
	lock, stmt = readTimeouts(okTx)
	require.Equal(t, "7s", lock)
	require.Equal(t, "9s", stmt)
}

func TestSettlementEvent_LowContextBudgetSkipsEvent(t *testing.T) {
	f := newSettlementFixture(t, service.PlatformAnthropic)
	f.setOwner(t, &f.user.ID)
	repo := NewUsageBillingRepository(nil, integrationDB)
	skippedBefore := service.GetUsageSettlementMetricsSnapshot().SkippedLowBudget
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	id := "budget-" + uuid.NewString()
	res, err := repo.Apply(ctx, f.hostedCmd(id, 1, 1, 0.2))
	require.NoError(t, err)
	require.True(t, res.Applied)
	_, ok := loadSettlementEvent(t, id, f.key.ID)
	require.False(t, ok)
	require.Equal(t, skippedBefore+1, service.GetUsageSettlementMetricsSnapshot().SkippedLowBudget)
	require.InDelta(t, 99, loadSettlementBillingState(t, f).Balance, 1e-9)
}

func TestSettlementEvent_InvalidAmountsNormalizedOnlyInEvent(t *testing.T) {
	ctx := context.Background()
	f := newSettlementFixture(t, service.PlatformAnthropic)
	f.setOwner(t, &f.user.ID)
	repo := NewUsageBillingRepository(nil, integrationDB)
	id := "invalid-" + uuid.NewString()
	cmd := f.hostedCmd(id, 2e10, 1, 0.2)
	cmd.Settlement.StandardCost = -3
	res, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, res.Applied)
	ev, ok := loadSettlementEvent(t, id, f.key.ID)
	require.True(t, ok)
	require.Zero(t, ev.ChargeInput)
	require.Zero(t, ev.StandardCost)
	require.Equal(t, pq.StringArray{"standard_cost", "charge_input"}, ev.InvalidFields)
	require.True(t, ev.Anomaly)
	require.Equal(t, "invalid_amount", ev.ZeroReason.String)
	require.Equal(t, "balance", ev.ChargeSource)
	// The effects used the original amount.
	require.InDelta(t, 100-2e10, loadSettlementBillingState(t, f).Balance, 1e-3)
}

func TestSettlementEvent_OwnerClearedBetweenRequests(t *testing.T) {
	ctx := context.Background()
	f := newSettlementFixture(t, service.PlatformAnthropic)
	f.setOwner(t, &f.user.ID)
	repo := NewUsageBillingRepository(nil, integrationDB)
	first, second := "owner-a-"+uuid.NewString(), "owner-b-"+uuid.NewString()
	_, err := repo.Apply(ctx, f.hostedCmd(first, 1, 1, 0.2))
	require.NoError(t, err)
	f.setOwner(t, nil)
	_, err = repo.Apply(ctx, f.hostedCmd(second, 1, 1, 0.2))
	require.NoError(t, err)
	a, ok := loadSettlementEvent(t, first, f.key.ID)
	require.True(t, ok)
	b, ok := loadSettlementEvent(t, second, f.key.ID)
	require.True(t, ok)
	require.Equal(t, f.user.ID, a.OwnerUserID.Int64)
	require.False(t, a.Anomaly)
	require.False(t, b.OwnerUserID.Valid)
	require.True(t, b.Anomaly)
	require.False(t, b.SelfPurchase)
}

func TestSettlementEvent_SubscriptionHostedGroupRejected(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	_, err := tx.ExecContext(ctx, `INSERT INTO groups (name, platform, status, subscription_type, rate_multiplier, is_exclusive, hosted_usage_enabled)
		VALUES ($1, 'anthropic', 'active', 'subscription', 0.2, true, true)`, "TEST_ONLY_sub_hosted_"+uuid.NewString())
	require.ErrorContains(t, err, "groups_hosted_usage_policy")
}

func TestSettlementEvent_ServicePathsWSTurnsAndNativeChat(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	f := newSettlementFixture(t, service.PlatformOpenAI)
	f.setOwner(t, &f.user.ID)
	group := mustCreateGroup(t, client, &service.Group{Name: "TEST_ONLY_hosted_" + uuid.NewString(), Platform: service.PlatformOpenAI, IsExclusive: true, RateMultiplier: 0.25})
	_, err := integrationDB.ExecContext(ctx, "UPDATE groups SET hosted_usage_enabled=true WHERE id=$1", group.ID)
	require.NoError(t, err)
	group.HostedUsageEnabled = true
	require.True(t, group.UsesHostedUsage())
	f.key.GroupID = &group.ID
	f.key.Group = group

	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	cache := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(cache.Stop)
	svc := service.NewOpenAIGatewayService(nil, nil, NewUsageBillingRepository(client, integrationDB), nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, cache, nil, &service.DeferredService{}, nil)

	turn := func(requestID string) error {
		return svc.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result: &service.OpenAIForwardResult{RequestID: requestID, Model: "gpt-5.1", OpenAIWSMode: true, Duration: time.Second,
				Usage: service.OpenAIUsage{InputTokens: 1000, OutputTokens: 200}},
			APIKey: f.key, User: f.user, Account: f.account, APIKeyService: nativeChatQuotaUpdater{},
		})
	}
	turn1, turn2 := "ws-turn-1-"+uuid.NewString(), "ws-turn-2-"+uuid.NewString()
	require.NoError(t, turn(turn1))
	require.NoError(t, turn(turn2))
	require.NoError(t, turn(turn1), "same turn replay")
	require.Equal(t, 2, countSettlementEvents(t, f.key.ID))
	ev, ok := loadSettlementEvent(t, turn1, f.key.ID)
	require.True(t, ok)
	require.Equal(t, group.ID, ev.GroupID.Int64)
	require.InDelta(t, 0.25, ev.FeeRate.Float64, 1e-12)
	require.True(t, ev.SelfPurchase)

	identity := service.ResolveChatGPTTurnBillingIdentity([]byte(`{"messages":[{"id":"TEST_ONLY_SETTLEMENT_TURN"}],"model":"gpt-6-pro"}`), "")
	input := &service.OpenAIChatGPTTurnUsageInput{RequestID: identity.RequestID, RequestPayloadHash: identity.PayloadHash,
		BasePriceUSD: 0.05, RequestedModel: "gpt-6-pro", Account: f.account, APIKey: f.key, User: f.user,
		InboundEndpoint: "/chatgpt/backend-api/f/conversation", APIKeyService: nativeChatQuotaUpdater{}}
	require.NoError(t, svc.RecordChatGPTTurnUsage(ctx, input))
	require.NoError(t, svc.RecordChatGPTTurnUsage(ctx, input))
	require.Equal(t, 3, countSettlementEvents(t, f.key.ID))
	chat, ok := loadSettlementEvent(t, identity.RequestID, f.key.ID)
	require.True(t, ok)
	require.InDelta(t, 0.05, chat.StandardCost, 1e-12)
	require.InDelta(t, 0.0125, chat.ChargeInput, 1e-12)
	require.Equal(t, "balance", chat.ChargeSource)
}
