//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type hostedOwnerFixture struct {
	repo      *hostedOwnerRepository
	logs      *usageLogRepository
	ownerA    *service.User
	ownerB    *service.User
	buyer     *service.User
	ownerKey  *service.APIKey
	buyerKey  *service.APIKey
	account   *service.Account
	unowned   *service.Account
	newLog    func(user *service.User, key *service.APIKey, account *service.Account, cost float64, at time.Time) *service.UsageLog
	storedFor func(log *service.UsageLog) sql.NullInt64
}

func newHostedOwnerFixture(t *testing.T) *hostedOwnerFixture {
	t.Helper()
	ctx := context.Background()
	client := testEntClient(t)
	f := &hostedOwnerFixture{
		repo: &hostedOwnerRepository{db: integrationDB, accounts: newAccountRepositoryWithSQL(client, integrationDB, nil)},
		logs: newUsageLogRepositoryWithSQL(client, integrationDB),
	}
	suffix := uuid.NewString()
	f.ownerA = mustCreateUser(t, client, &service.User{Email: "owner-a-" + suffix + "@example.invalid"})
	f.ownerB = mustCreateUser(t, client, &service.User{Email: "owner-b-" + suffix + "@example.invalid"})
	f.buyer = mustCreateUser(t, client, &service.User{Email: "buyer-" + suffix + "@example.invalid"})
	f.ownerKey = mustCreateApiKey(t, client, &service.APIKey{UserID: f.ownerA.ID, Key: "sk-owner-" + suffix, Name: "owner laptop"})
	f.buyerKey = mustCreateApiKey(t, client, &service.APIKey{UserID: f.buyer.ID, Key: "sk-buyer-" + suffix, Name: "buyer secret name"})
	f.account = mustCreateAccount(t, client, &service.Account{Name: "seller@example.invalid 5x " + suffix, Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken})
	f.unowned = mustCreateAccount(t, client, &service.Account{Name: "platform " + suffix, Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken})
	f.newLog = func(user *service.User, key *service.APIKey, account *service.Account, cost float64, at time.Time) *service.UsageLog {
		return &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(),
			Model: "claude-sonnet-5", InputTokens: 10, OutputTokens: 2, TotalCost: cost, ActualCost: cost * 1.5, CreatedAt: at.UTC()}
	}
	f.storedFor = func(log *service.UsageLog) sql.NullInt64 {
		var value sql.NullInt64
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			"SELECT account_owner_user_id FROM usage_logs WHERE request_id = $1 AND api_key_id = $2", log.RequestID, log.APIKeyID).Scan(&value))
		return value
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, id := range []int64{f.ownerKey.ID, f.buyerKey.ID} {
			_, _ = integrationDB.ExecContext(bg, "DELETE FROM usage_logs WHERE api_key_id = $1", id)
		}
		_, _ = integrationDB.ExecContext(bg, "DELETE FROM account_owner_events WHERE account_id = ANY($1)", fmt.Sprintf("{%d,%d}", f.account.ID, f.unowned.ID))
	})
	return f
}

func TestHostedOwnerSnapshotInEveryInsertPath(t *testing.T) {
	ctx := context.Background()
	f := newHostedOwnerFixture(t)
	now := time.Now()

	before := f.newLog(f.buyer, f.buyerKey, f.account, 0.1, now)
	_, err := f.logs.createSingle(ctx, integrationDB, before)
	require.NoError(t, err)
	require.False(t, f.storedFor(before).Valid, "no owner yet → NULL")

	changed, err := f.repo.SetAccountOwner(ctx, f.account.ID, &f.ownerA.ID, 0)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, f.storedFor(before).Valid, "assigning an owner never backfills earlier rows")

	want := sql.NullInt64{Int64: f.ownerA.ID, Valid: true}
	// The owner and buyer IDs differ from every other bigint in the row, so a
	// misaligned column would show up here.
	batched := f.newLog(f.buyer, f.buyerKey, f.account, 0.2, now)
	_, err = f.logs.Create(ctx, batched)
	require.NoError(t, err)
	require.Equal(t, want, f.storedFor(batched))
	got, err := f.logs.GetByID(ctx, batched.ID)
	require.NoError(t, err)
	require.NotNil(t, got.AccountOwnerUserID)
	require.Equal(t, f.ownerA.ID, *got.AccountOwnerUserID)
	require.Equal(t, f.buyer.ID, got.UserID)
	require.Equal(t, f.account.ID, got.AccountID)

	single := f.newLog(f.buyer, f.buyerKey, f.account, 0.3, now)
	_, err = f.logs.createSingle(ctx, integrationDB, single)
	require.NoError(t, err)
	require.Equal(t, want, f.storedFor(single))

	fallback := f.newLog(f.buyer, f.buyerKey, f.account, 0.4, now)
	require.NoError(t, execUsageLogInsertNoResult(ctx, integrationDB, prepareUsageLogInsert(fallback)))
	require.Equal(t, want, f.storedFor(fallback))

	bestEffort := f.newLog(f.buyer, f.buyerKey, f.account, 0.5, now)
	require.NoError(t, f.logs.CreateBestEffort(ctx, bestEffort))
	require.Eventually(t, func() bool {
		var value sql.NullInt64
		err := integrationDB.QueryRowContext(ctx, "SELECT account_owner_user_id FROM usage_logs WHERE request_id = $1 AND api_key_id = $2",
			bestEffort.RequestID, bestEffort.APIKeyID).Scan(&value)
		return err == nil && value == want
	}, 3*time.Second, 20*time.Millisecond)

	// A caller-supplied value on the struct is ignored: the INSERT reads accounts.
	forged := f.newLog(f.buyer, f.buyerKey, f.unowned, 0.6, now)
	forged.AccountOwnerUserID = &f.ownerB.ID
	_, err = f.logs.Create(ctx, forged)
	require.NoError(t, err)
	require.False(t, f.storedFor(forged).Valid)
}

func TestHostedOwnerStateMachine(t *testing.T) {
	ctx := context.Background()
	f := newHostedOwnerFixture(t)

	changed, err := f.repo.SetAccountOwner(ctx, f.account.ID, &f.ownerA.ID, 1)
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = f.repo.SetAccountOwner(ctx, f.account.ID, &f.ownerA.ID, 1)
	require.NoError(t, err)
	require.False(t, changed, "same owner is idempotent")

	_, err = f.repo.SetAccountOwner(ctx, f.account.ID, &f.ownerB.ID, 1)
	require.ErrorIs(t, err, service.ErrAccountOwnerTransferForbidden, "direct A→B")

	changed, err = f.repo.SetAccountOwner(ctx, f.account.ID, nil, 1)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = f.repo.SetAccountOwner(ctx, f.account.ID, &f.ownerB.ID, 1)
	require.ErrorIs(t, err, service.ErrAccountOwnerTransferForbidden, "A→NULL→B must be refused from history")
	changed, err = f.repo.SetAccountOwner(ctx, f.account.ID, &f.ownerA.ID, 1)
	require.NoError(t, err)
	require.True(t, changed, "setting back to A is allowed")

	_, err = f.repo.SetAccountOwner(ctx, 0, &f.ownerA.ID, 1)
	require.ErrorIs(t, err, service.ErrAccountNotFound)
	_, err = f.repo.SetAccountOwner(ctx, f.unowned.ID+1_000_000, &f.ownerA.ID, 1)
	require.ErrorIs(t, err, service.ErrAccountNotFound)

	var events int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_owner_events WHERE account_id = $1", f.account.ID).Scan(&events))
	require.Equal(t, 3, events, "set A, clear, set A again; no-ops and refusals write nothing")

	// A generic whole-row update with a stale object must not revert the owner.
	accounts := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	stale, err := accounts.GetByID(ctx, f.account.ID)
	require.NoError(t, err)
	_, err = f.repo.SetAccountOwner(ctx, f.account.ID, nil, 1)
	require.NoError(t, err)
	stale.Notes = ptrString("touched")
	require.NoError(t, accounts.Update(ctx, stale))
	var owner sql.NullInt64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT owner_user_id FROM accounts WHERE id = $1", f.account.ID).Scan(&owner))
	require.False(t, owner.Valid, "Update never writes owner_user_id")
}

func TestHostedOwnerQueriesUseSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newHostedOwnerFixture(t)
	now := time.Now().UTC()

	// Usage before the assignment is never attributed to the owner.
	pre := f.newLog(f.buyer, f.buyerKey, f.account, 9, now.Add(-time.Hour))
	_, err := f.logs.Create(ctx, pre)
	require.NoError(t, err)

	_, err = f.repo.SetAccountOwner(ctx, f.account.ID, &f.ownerA.ID, 0)
	require.NoError(t, err)
	selfLog := f.newLog(f.ownerA, f.ownerKey, f.account, 1, now.Add(-2*time.Hour))
	otherRecent := f.newLog(f.buyer, f.buyerKey, f.account, 2, now.Add(-30*time.Minute))
	otherOld := f.newLog(f.buyer, f.buyerKey, f.account, 4, now.Add(-3*24*time.Hour))
	for _, log := range []*service.UsageLog{selfLog, otherRecent, otherOld} {
		_, err := f.logs.createSingle(ctx, integrationDB, log)
		require.NoError(t, err)
	}

	ids, err := f.repo.ListCurrentOwnedAccountIDs(ctx, f.ownerA.ID, 10)
	require.NoError(t, err)
	require.Equal(t, []int64{f.account.ID}, ids)

	last24h, last7d, err := f.repo.SummarizeOwnedUsage(ctx, f.ownerA.ID, []int64{f.account.ID}, now.Add(-7*24*time.Hour), now.Add(-24*time.Hour), now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, service.HostedOwnerUsageTotals{SelfRequests: 1, SelfStandardCost: 1, OthersRequests: 1, OthersStandardCost: 2}, last24h[f.account.ID])
	require.Equal(t, service.HostedOwnerUsageTotals{SelfRequests: 1, SelfStandardCost: 1, OthersRequests: 2, OthersStandardCost: 6}, last7d[f.account.ID])

	records, err := f.repo.ListOwnedUsage(ctx, service.HostedOwnerUsageQuery{OwnerUserID: f.ownerA.ID, AccountID: f.account.ID,
		Start: now.Add(-7 * 24 * time.Hour), End: now.Add(time.Minute), Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 3, "the pre-assignment row is excluded")
	for _, record := range records {
		if record.Row.IsSelf {
			require.NotNil(t, record.Row.APIKeyName)
			require.Equal(t, "owner laptop", *record.Row.APIKeyName)
			require.NotNil(t, record.Row.Charged)
		} else {
			require.Nil(t, record.Row.APIKeyName)
			require.Nil(t, record.Row.Charged)
		}
	}

	// Other users can neither list nor read it.
	ok, err := f.repo.CanViewOwnedAccount(ctx, f.ownerB.ID, f.account.ID)
	require.NoError(t, err)
	require.False(t, ok)
	records, err = f.repo.ListOwnedUsage(ctx, service.HostedOwnerUsageQuery{OwnerUserID: f.ownerB.ID, AccountID: f.account.ID,
		Start: now.Add(-7 * 24 * time.Hour), End: now.Add(time.Minute), Limit: 10})
	require.NoError(t, err)
	require.Empty(t, records)

	// After clearing, A keeps read access to the history; the account moves to the past list.
	_, err = f.repo.SetAccountOwner(ctx, f.account.ID, nil, 0)
	require.NoError(t, err)
	ids, err = f.repo.ListCurrentOwnedAccountIDs(ctx, f.ownerA.ID, 10)
	require.NoError(t, err)
	require.Empty(t, ids)
	ever, err := f.repo.ListEverOwnedAccounts(ctx, f.ownerA.ID, 10)
	require.NoError(t, err)
	require.Len(t, ever, 1)
	require.Equal(t, f.account.ID, ever[0].AccountID)
	require.NotNil(t, ever[0].Platform)
	ok, err = f.repo.CanViewOwnedAccount(ctx, f.ownerA.ID, f.account.ID)
	require.NoError(t, err)
	require.True(t, ok)
	afterClear := f.newLog(f.buyer, f.buyerKey, f.account, 7, now)
	_, err = f.logs.createSingle(ctx, integrationDB, afterClear)
	require.NoError(t, err)
	require.False(t, f.storedFor(afterClear).Valid)

	// A soft-deleted account stays readable in the history of every past owner.
	_, err = f.repo.SetAccountOwner(ctx, f.account.ID, &f.ownerA.ID, 0)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET deleted_at = NOW() WHERE id = $1", f.account.ID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "UPDATE accounts SET deleted_at = NULL, owner_user_id = NULL WHERE id = $1", f.account.ID)
	})
	ids, err = f.repo.ListCurrentOwnedAccountIDs(ctx, f.ownerA.ID, 10)
	require.NoError(t, err)
	require.Empty(t, ids, "deleted accounts are never current")
	ever, err = f.repo.ListEverOwnedAccounts(ctx, f.ownerA.ID, 10)
	require.NoError(t, err)
	require.Len(t, ever, 1)
	ok, err = f.repo.CanViewOwnedAccount(ctx, f.ownerA.ID, f.account.ID)
	require.NoError(t, err)
	require.True(t, ok)
	records, err = f.repo.ListOwnedUsage(ctx, service.HostedOwnerUsageQuery{OwnerUserID: f.ownerA.ID, AccountID: f.account.ID,
		Start: now.Add(-7 * 24 * time.Hour), End: now.Add(time.Minute), Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 3)
	_, err = f.repo.SetAccountOwner(ctx, f.account.ID, nil, 0)
	require.ErrorIs(t, err, service.ErrAccountNotFound, "deleted accounts cannot be reassigned")
}

func TestHostedOwnerUsageKeysetPagination(t *testing.T) {
	ctx := context.Background()
	f := newHostedOwnerFixture(t)
	_, err := f.repo.SetAccountOwner(ctx, f.account.ID, &f.ownerA.ID, 0)
	require.NoError(t, err)
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	for i := 0; i < 5; i++ {
		// Same timestamp for every row: pagination must still be exact.
		_, err := f.logs.createSingle(ctx, integrationDB, f.newLog(f.buyer, f.buyerKey, f.account, float64(i+1), at))
		require.NoError(t, err)
	}
	svc := service.NewHostedOwnerService(f.repo, nil)
	params := service.HostedOwnerUsageParams{Start: at.Add(-time.Hour).Format(time.RFC3339), End: at.Add(time.Hour).Format(time.RFC3339), Limit: "2"}
	seen := map[float64]bool{}
	pages := 0
	for {
		page, err := svc.ListOwnedAccountUsage(ctx, f.ownerA.ID, f.account.ID, params)
		require.NoError(t, err)
		pages++
		for _, row := range page.Items {
			require.False(t, seen[row.StandardCost], "duplicate row across pages")
			seen[row.StandardCost] = true
		}
		if page.NextCursor == nil {
			break
		}
		params.Cursor = *page.NextCursor
		require.Less(t, pages, 10)
	}
	require.Len(t, seen, 5)
	require.Equal(t, 3, pages)

	_, err = svc.ListOwnedAccountUsage(ctx, f.ownerB.ID, f.account.ID, params)
	require.ErrorIs(t, err, service.ErrHostedOwnedAccountNotFound)
}

func ptrString(v string) *string { return &v }
