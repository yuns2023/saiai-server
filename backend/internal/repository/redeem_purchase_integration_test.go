//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// These tests commit through the shared client (not a per-test rollback
// transaction): the purchase/revoke/redeem paths run their own transactions
// and row locks across concurrent transactions. Every row created here is
// hard-deleted in t.Cleanup so suites that expect empty tables keep working.

type rpSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *rpSettings) LoadRedeemPurchaseSettings(context.Context) (service.RedeemPurchaseSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return service.ParseRedeemPurchaseSettings(s.values), nil
}

func (s *rpSettings) set(kv ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i+1 < len(kv); i += 2 {
		s.values[kv[i]] = kv[i+1]
	}
}

type rpInvalidator struct{ calls atomic.Int64 }

func (r *rpInvalidator) InvalidateAuthCacheByKey(context.Context, string)    {}
func (r *rpInvalidator) InvalidateAuthCacheByUserID(context.Context, int64)  { r.calls.Add(1) }
func (r *rpInvalidator) InvalidateAuthCacheByGroupID(context.Context, int64) {}

type rpFixture struct {
	t        *testing.T
	ctx      context.Context
	client   *dbent.Client
	settings *rpSettings
	svc      *service.RedeemPurchaseService
	inv      *rpInvalidator
	mu       sync.Mutex
	users    []int64
}

func newRPFixture(t *testing.T) *rpFixture {
	t.Helper()
	f := &rpFixture{
		t:      t,
		ctx:    context.Background(),
		client: testEntClient(t),
		settings: &rpSettings{values: map[string]string{
			service.SettingKeyRedeemPurchaseEnabled: "true",
		}},
		inv: &rpInvalidator{},
	}
	// Settings come from memory (the shared settings table stays untouched);
	// TestRedeemPurchaseIntegration_SettingsReadOnTxConnection covers the real read.
	f.svc = service.NewRedeemPurchaseServiceWithTx(
		NewRedeemPurchaseRepository(f.client), f.settings,
		service.EntRedeemPurchaseTxRunner(f.client), f.inv, nil,
	)
	t.Cleanup(f.cleanup)
	return f
}

func (f *rpFixture) cleanup() {
	ctx := context.Background()
	f.mu.Lock()
	ids := pq.Array(f.users)
	f.mu.Unlock()
	var codeIDs []int64
	rows, err := integrationDB.QueryContext(ctx, `SELECT redeem_code_id FROM redeem_code_purchases WHERE buyer_user_id = ANY($1)`, ids)
	if err == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				codeIDs = append(codeIDs, id)
			}
		}
		_ = rows.Close()
	}
	_, _ = integrationDB.ExecContext(ctx, `DELETE FROM redeem_code_purchases WHERE buyer_user_id = ANY($1)`, ids)
	_, _ = integrationDB.ExecContext(ctx, `DELETE FROM redeem_codes WHERE id = ANY($1) OR used_by = ANY($2)`, pq.Array(codeIDs), ids)
	_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id = ANY($1)`, ids)
}

func (f *rpFixture) user(balance string) int64 {
	f.t.Helper()
	tag := strings.NewReplacer("/", "-", " ", "-").Replace(f.t.Name())
	u := mustCreateUser(f.t, f.client, &service.User{Email: fmt.Sprintf("rp-%s-%d@example.com", tag, time.Now().UnixNano())})
	f.mu.Lock()
	f.users = append(f.users, u.ID)
	f.mu.Unlock()
	_, err := integrationDB.ExecContext(f.ctx, `UPDATE users SET balance = $2::numeric WHERE id = $1`, u.ID, balance)
	require.NoError(f.t, err)
	return u.ID
}

func (f *rpFixture) balance(userID int64) decimal.Decimal {
	f.t.Helper()
	var s string
	require.NoError(f.t, integrationDB.QueryRowContext(f.ctx, `SELECT balance::text FROM users WHERE id = $1`, userID).Scan(&s))
	return decimal.RequireFromString(s)
}

func (f *rpFixture) requireBalance(userID int64, want string) {
	f.t.Helper()
	got := f.balance(userID)
	require.True(f.t, got.Equal(decimal.RequireFromString(want)), "balance of %d: got %s want %s", userID, got, want)
}

func (f *rpFixture) buy(buyer int64, amount string) (*service.RedeemPurchaseView, error) {
	v, _, err := f.svc.Purchase(f.ctx, buyer, amount, uuid.NewString())
	return v, err
}

func (f *rpFixture) mustBuy(buyer int64, amount string) *service.RedeemPurchaseView {
	f.t.Helper()
	v, err := f.buy(buyer, amount)
	require.NoError(f.t, err)
	return v
}

type rpRecord struct {
	Type   string
	Value  string
	Status string
	UsedBy *int64
	UsedAt *time.Time
	Notes  *string
}

func (f *rpFixture) records(userID int64, typ string) []rpRecord {
	f.t.Helper()
	rows, err := integrationDB.QueryContext(f.ctx, `
		SELECT type, value::text, status, used_by, used_at, notes FROM redeem_codes
		WHERE used_by = $1 AND type = $2 ORDER BY id`, userID, typ)
	require.NoError(f.t, err)
	defer func() { _ = rows.Close() }()
	var out []rpRecord
	for rows.Next() {
		var r rpRecord
		require.NoError(f.t, rows.Scan(&r.Type, &r.Value, &r.Status, &r.UsedBy, &r.UsedAt, &r.Notes))
		out = append(out, r)
	}
	require.NoError(f.t, rows.Err())
	return out
}

func (f *rpFixture) purchaseCount(buyer int64) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, integrationDB.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM redeem_code_purchases WHERE buyer_user_id = $1`, buyer).Scan(&n))
	return n
}

func (f *rpFixture) redeemService() *service.RedeemService {
	return service.NewRedeemService(NewRedeemCodeRepository(f.client), NewUserRepository(f.client, integrationDB), nil, nil, nil, f.client, nil, nil, nil)
}

func requireDecimal(t *testing.T, want string, got string) {
	t.Helper()
	require.True(t, decimal.RequireFromString(got).Equal(decimal.RequireFromString(want)), "got %s want %s", got, want)
}

func TestRedeemPurchaseIntegration_HappyPath(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("100")

	view, replayed, err := f.svc.Purchase(f.ctx, buyer, "12.34", uuid.NewString())
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, service.RedeemPurchaseViewUnused, view.Status)
	require.Len(t, view.Code, 32)
	f.requireBalance(buyer, "87.66")
	require.EqualValues(t, 1, f.inv.calls.Load(), "auth cache invalidated after commit")

	var (
		codeType, codeValue, codeStatus string
		codeUsedBy                      *int64
		codeNotes                       *string
		amount, reqStatus               string
		revokedAt                       *time.Time
		purchaseCodeID                  int64
	)
	require.NoError(t, integrationDB.QueryRowContext(f.ctx, `
		SELECT c.type, c.value::text, c.status, c.used_by, c.notes, p.amount::text, p.status, p.revoked_at, p.redeem_code_id
		FROM redeem_code_purchases p JOIN redeem_codes c ON c.id = p.redeem_code_id
		WHERE p.id = $1 AND p.buyer_user_id = $2`, view.ID, buyer).
		Scan(&codeType, &codeValue, &codeStatus, &codeUsedBy, &codeNotes, &amount, &reqStatus, &revokedAt, &purchaseCodeID))
	require.Equal(t, service.RedeemTypeBalance, codeType)
	requireDecimal(t, "12.34", codeValue)
	require.Equal(t, service.StatusUnused, codeStatus)
	require.Nil(t, codeUsedBy)
	require.NotNil(t, codeNotes)
	require.Equal(t, fmt.Sprintf("user purchase #%d", view.ID), *codeNotes)
	requireDecimal(t, "12.34", amount)
	require.Equal(t, service.RedeemPurchaseStatusActive, reqStatus)
	require.Nil(t, revokedAt)

	debits := f.records(buyer, service.RedeemTypePurchaseDebit)
	require.Len(t, debits, 1)
	requireDecimal(t, "-12.34", debits[0].Value)
	require.Equal(t, service.StatusUsed, debits[0].Status)
	require.NotNil(t, debits[0].UsedAt, "used_at is set explicitly")
	require.NotNil(t, debits[0].Notes)
	require.Contains(t, *debits[0].Notes, fmt.Sprintf("#%d", view.ID))

	// The buyer's own redeem history (used_by = buyer) shows the debit record.
	hist, err := NewRedeemCodeRepository(f.client).ListByUser(f.ctx, buyer, 10)
	require.NoError(t, err)
	require.Len(t, hist, 1)
	require.Equal(t, service.RedeemTypePurchaseDebit, hist[0].Type)

	out, err := f.svc.Overview(f.ctx, buyer, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.True(t, out.Config.Enabled)
	require.True(t, out.Config.RemainingAmount24h.Equal(decimal.RequireFromString("987.66")))
	require.Equal(t, 19, out.Config.RemainingCount24h)
	require.Len(t, out.Items, 1)
	require.Equal(t, view.Code, out.Items[0].Code)
	require.Equal(t, service.RedeemPurchaseViewUnused, out.Items[0].Status)
}

func TestRedeemPurchaseIntegration_InsufficientBalanceAndExactBalance(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("9.99")
	_, err := f.buy(buyer, "10")
	require.ErrorIs(t, err, service.ErrInsufficientBalance)
	f.requireBalance(buyer, "9.99")
	require.Zero(t, f.purchaseCount(buyer))
	require.Empty(t, f.records(buyer, service.RedeemTypePurchaseDebit))

	f.mustBuy(buyer, "9.99")
	f.requireBalance(buyer, "0")

	overdrawn := f.user("-5")
	_, err = f.buy(overdrawn, "1")
	require.ErrorIs(t, err, service.ErrInsufficientBalance)
	f.requireBalance(overdrawn, "-5")
}

func TestRedeemPurchaseIntegration_AmountHasAtMostTwoDecimals(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("100")
	for _, bad := range []string{"1.001", "1.100", "0", "-1", "1e1"} {
		_, err := f.buy(buyer, bad)
		require.ErrorIs(t, err, service.ErrRedeemPurchaseInvalidAmount, bad)
	}
	v := f.mustBuy(buyer, "1.10")
	require.True(t, v.Amount.Equal(decimal.RequireFromString("1.1")))
	f.requireBalance(buyer, "98.9")
	debits := f.records(buyer, service.RedeemTypePurchaseDebit)
	require.Len(t, debits, 1)
	requireDecimal(t, "-1.1", debits[0].Value)
}

func TestRedeemPurchaseIntegration_RangeAndSwitch(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("1000")
	f.settings.set(service.SettingKeyRedeemPurchaseMinAmount, "5", service.SettingKeyRedeemPurchaseMaxAmount, "50")
	_, err := f.buy(buyer, "4.99")
	require.ErrorIs(t, err, service.ErrRedeemPurchaseAmountBelowMin)
	_, err = f.buy(buyer, "50.01")
	require.ErrorIs(t, err, service.ErrRedeemPurchaseAmountAboveMax)
	f.settings.set(service.SettingKeyRedeemPurchaseEnabled, "false")
	_, err = f.buy(buyer, "10")
	require.ErrorIs(t, err, service.ErrRedeemPurchaseDisabled)
	require.Zero(t, f.purchaseCount(buyer))
	f.requireBalance(buyer, "1000")
}

func TestRedeemPurchaseIntegration_WindowCapsCountRevokedPurchases(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		f := newRPFixture(t)
		buyer := f.user("100")
		f.settings.set(service.SettingKeyRedeemPurchaseWindowMaxCount, "2")
		a := f.mustBuy(buyer, "1")
		_, err := f.svc.Revoke(f.ctx, buyer, a.ID)
		require.NoError(t, err)
		f.mustBuy(buyer, "1")
		_, err = f.buy(buyer, "1")
		require.ErrorIs(t, err, service.ErrRedeemPurchaseWindowCountLimit, "the revoked purchase still counts")
		f.requireBalance(buyer, "99")
	})
	t.Run("amount", func(t *testing.T) {
		f := newRPFixture(t)
		buyer := f.user("1000")
		f.settings.set(service.SettingKeyRedeemPurchaseWindowMaxAmount, "100")
		a := f.mustBuy(buyer, "60")
		_, err := f.svc.Revoke(f.ctx, buyer, a.ID)
		require.NoError(t, err)
		_, err = f.buy(buyer, "40.01")
		require.ErrorIs(t, err, service.ErrRedeemPurchaseWindowAmountLimit, "the revoked purchase still counts")
		f.mustBuy(buyer, "40")
		f.requireBalance(buyer, "960")

		out, err := f.svc.Overview(f.ctx, buyer, pagination.PaginationParams{Page: 1, PageSize: 20})
		require.NoError(t, err)
		require.True(t, out.Config.RemainingAmount24h.IsZero())
	})
	t.Run("outside the window", func(t *testing.T) {
		f := newRPFixture(t)
		buyer := f.user("100")
		f.settings.set(service.SettingKeyRedeemPurchaseWindowMaxCount, "1")
		a := f.mustBuy(buyer, "1")
		_, err := integrationDB.ExecContext(f.ctx, `UPDATE redeem_code_purchases SET created_at = NOW() - INTERVAL '24 hours 1 second' WHERE id = $1`, a.ID)
		require.NoError(t, err)
		f.mustBuy(buyer, "1")
	})
}

func TestRedeemPurchaseIntegration_ConcurrentPurchasesRespectCapsAndBalance(t *testing.T) {
	run := func(t *testing.T, f *rpFixture, buyer int64, n int, amount string) (ok int, errs []error) {
		start := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := f.buy(buyer, amount)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					ok++
				} else {
					errs = append(errs, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		return ok, errs
	}

	t.Run("count cap", func(t *testing.T) {
		f := newRPFixture(t)
		buyer := f.user("1000")
		f.settings.set(service.SettingKeyRedeemPurchaseWindowMaxCount, "5")
		ok, errs := run(t, f, buyer, 12, "10")
		require.Equal(t, 5, ok)
		for _, err := range errs {
			require.ErrorIs(t, err, service.ErrRedeemPurchaseWindowCountLimit)
		}
		f.requireBalance(buyer, "950")
		require.Equal(t, 5, f.purchaseCount(buyer))
	})
	t.Run("amount cap", func(t *testing.T) {
		f := newRPFixture(t)
		buyer := f.user("1000")
		f.settings.set(service.SettingKeyRedeemPurchaseWindowMaxAmount, "35")
		ok, errs := run(t, f, buyer, 10, "10")
		require.Equal(t, 3, ok)
		for _, err := range errs {
			require.ErrorIs(t, err, service.ErrRedeemPurchaseWindowAmountLimit)
		}
		f.requireBalance(buyer, "970")
	})
	t.Run("balance", func(t *testing.T) {
		f := newRPFixture(t)
		buyer := f.user("35")
		ok, errs := run(t, f, buyer, 10, "10")
		require.Equal(t, 3, ok)
		for _, err := range errs {
			require.ErrorIs(t, err, service.ErrInsufficientBalance)
		}
		f.requireBalance(buyer, "5")
		require.Len(t, f.records(buyer, service.RedeemTypePurchaseDebit), 3)
	})
}

func TestRedeemPurchaseIntegration_SameRequestIDConcurrentChargesOnce(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("100")
	reqID := uuid.NewString()
	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	views := make([]*service.RedeemPurchaseView, n)
	replays := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			views[i], replays[i], errs[i] = f.svc.Purchase(f.ctx, buyer, "10", reqID)
		}(i)
	}
	close(start)
	wg.Wait()
	fresh := 0
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, views[0].ID, views[i].ID)
		require.Equal(t, views[0].Code, views[i].Code)
		if !replays[i] {
			fresh++
		}
	}
	require.Equal(t, 1, fresh)
	require.Equal(t, 1, f.purchaseCount(buyer))
	f.requireBalance(buyer, "90")
	require.Len(t, f.records(buyer, service.RedeemTypePurchaseDebit), 1)
}

func TestRedeemPurchaseIntegration_ReplayAfterSwitchOffAndAfterRevoke(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("100")
	reqID := uuid.NewString()
	orig, _, err := f.svc.Purchase(f.ctx, buyer, "10", reqID)
	require.NoError(t, err)

	f.settings.set(service.SettingKeyRedeemPurchaseEnabled, "false", service.SettingKeyRedeemPurchaseWindowMaxCount, "0")
	again, replayed, err := f.svc.Purchase(f.ctx, buyer, "10.00", strings.ToUpper(reqID))
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, orig.ID, again.ID)
	require.Equal(t, orig.Code, again.Code)
	f.requireBalance(buyer, "90")

	_, err = f.svc.Revoke(f.ctx, buyer, orig.ID)
	require.NoError(t, err, "revoke works with the switch off")
	f.requireBalance(buyer, "100")

	again, replayed, err = f.svc.Purchase(f.ctx, buyer, "10", reqID)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, orig.ID, again.ID)
	require.Equal(t, service.RedeemPurchaseViewRevoked, again.Status)
	require.NotNil(t, again.RevokedAt)
	f.requireBalance(buyer, "100")
	require.Equal(t, 1, f.purchaseCount(buyer))

	_, _, err = f.svc.Purchase(f.ctx, buyer, "11", reqID)
	require.ErrorIs(t, err, service.ErrRedeemPurchaseRequestConflict)
	f.requireBalance(buyer, "100")
}

// waitForCodeLockWaiters waits until n backends are blocked on a lock while
// updating redeem_codes (the redeem's Use or the revoke's expire).
func waitForCodeLockWaiters(t *testing.T, n int) {
	t.Helper()
	waitForLockWaiters(t, n, "%update%redeem_codes%")
}

// waitForLockWaiters waits until n backends whose query matches pattern are
// blocked on a lock.
func waitForLockWaiters(t *testing.T, n int, pattern string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		require.NoError(t, integrationDB.QueryRow(`
			SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query ILIKE $1`, pattern).Scan(&waiting))
		if waiting >= n {
			return
		}
		require.True(t, time.Now().Before(deadline), "timed out waiting for %d lock waiters (have %d)", n, waiting)
		time.Sleep(10 * time.Millisecond)
	}
}

// Concurrent redeem (code → user) and revoke (purchase → code → user) of the
// same code: exactly one wins and the balances match the winner. The
// "first" orders are forced: a blocker transaction holds the code row while
// the first contender queues on it, then the second; releasing it lets the
// first one through.
func TestRedeemPurchaseIntegration_RedeemVsRevokeExactlyOneWins(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("1000")
	other := f.user("0")
	redeemSvc := f.redeemService()
	redeemWins, revokeWins, rounds := 0, 0, 0

	race := func(t *testing.T, first string) {
		v := f.mustBuy(buyer, "10")
		rounds++
		var codeID int64
		require.NoError(t, integrationDB.QueryRow(`SELECT redeem_code_id FROM redeem_code_purchases WHERE id = $1`, v.ID).Scan(&codeID))
		var blocker *sql.Tx
		if first != "" {
			var err error
			blocker, err = integrationDB.BeginTx(f.ctx, nil)
			require.NoError(t, err)
			defer func() { _ = blocker.Rollback() }()
			_, err = blocker.Exec(`SELECT id FROM redeem_codes WHERE id = $1 FOR UPDATE`, codeID)
			require.NoError(t, err)
		}
		var wg sync.WaitGroup
		var redeemErr, revokeErr error
		doRedeem := func() {
			defer wg.Done()
			_, redeemErr = redeemSvc.Redeem(f.ctx, other, v.Code)
		}
		doRevoke := func() {
			defer wg.Done()
			_, revokeErr = f.svc.Revoke(f.ctx, buyer, v.ID)
		}
		wg.Add(2)
		switch first {
		case "redeem":
			go doRedeem()
			waitForCodeLockWaiters(t, 1)
			go doRevoke()
			waitForCodeLockWaiters(t, 2)
		case "revoke":
			go doRevoke()
			waitForCodeLockWaiters(t, 1)
			go doRedeem()
			waitForCodeLockWaiters(t, 2)
		default:
			start := make(chan struct{})
			go func() { <-start; doRedeem() }()
			go func() { <-start; doRevoke() }()
			close(start)
		}
		if blocker != nil {
			require.NoError(t, blocker.Rollback())
		}
		wg.Wait()

		require.NotEqual(t, redeemErr == nil, revokeErr == nil, "exactly one wins: redeem=%v revoke=%v", redeemErr, revokeErr)
		if redeemErr == nil {
			require.ErrorIs(t, revokeErr, service.ErrRedeemPurchaseCodeUsed)
			redeemWins++
		} else {
			require.ErrorIs(t, redeemErr, service.ErrRedeemCodeUsed)
			revokeWins++
		}
		switch first {
		case "redeem":
			require.NoError(t, redeemErr)
		case "revoke":
			require.NoError(t, revokeErr)
		}
	}
	for i := 0; i < 3; i++ {
		race(t, "redeem")
		race(t, "revoke")
		race(t, "")
	}

	t.Logf("redeem won %d, revoke won %d", redeemWins, revokeWins)
	require.GreaterOrEqual(t, redeemWins, 3)
	require.GreaterOrEqual(t, revokeWins, 3)
	f.requireBalance(buyer, decimal.NewFromInt(int64(1000-10*rounds+10*revokeWins)).String())
	f.requireBalance(other, decimal.NewFromInt(int64(10*redeemWins)).String())
	require.Len(t, f.records(buyer, service.RedeemTypePurchaseRefund), revokeWins)
	require.Len(t, f.records(other, service.RedeemTypeBalance), redeemWins)

	out, err := f.svc.Overview(f.ctx, buyer, pagination.PaginationParams{Page: 1, PageSize: 50})
	require.NoError(t, err)
	used, revoked := 0, 0
	for _, it := range out.Items {
		switch it.Status {
		case service.RedeemPurchaseViewUsed:
			used++
			require.NotNil(t, it.UsedAt)
		case service.RedeemPurchaseViewRevoked:
			revoked++
		default:
			t.Fatalf("unexpected status %q", it.Status)
		}
	}
	require.Equal(t, redeemWins, used)
	require.Equal(t, revokeWins, revoked)
}

func TestRedeemPurchaseIntegration_RevokeRefundsOnce(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("100")
	v := f.mustBuy(buyer, "25.5")
	f.requireBalance(buyer, "74.5")

	const n = 6
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = f.svc.Revoke(f.ctx, buyer, v.ID)
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err, "revoking a revoked purchase returns it")
	}
	f.requireBalance(buyer, "100")
	refunds := f.records(buyer, service.RedeemTypePurchaseRefund)
	require.Len(t, refunds, 1)
	requireDecimal(t, "25.5", refunds[0].Value)
	require.NotNil(t, refunds[0].UsedAt)
	require.NotNil(t, refunds[0].Notes)
	require.Contains(t, *refunds[0].Notes, fmt.Sprintf("#%d", v.ID))

	var codeStatus, pStatus string
	var revokedAt *time.Time
	require.NoError(t, integrationDB.QueryRowContext(f.ctx, `
		SELECT c.status, p.status, p.revoked_at FROM redeem_code_purchases p JOIN redeem_codes c ON c.id = p.redeem_code_id WHERE p.id = $1`, v.ID).
		Scan(&codeStatus, &pStatus, &revokedAt))
	require.Equal(t, service.StatusExpired, codeStatus)
	require.Equal(t, service.RedeemPurchaseStatusRevoked, pStatus)
	require.NotNil(t, revokedAt)

	_, err := f.redeemService().Redeem(f.ctx, f.user("0"), v.Code)
	require.ErrorIs(t, err, service.ErrRedeemCodeUsed, "a revoked code cannot be redeemed")
}

func TestRedeemPurchaseIntegration_RevokeRefusals(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("100")
	stranger := f.user("100")
	v := f.mustBuy(buyer, "10")

	_, err := f.svc.Revoke(f.ctx, stranger, v.ID)
	require.ErrorIs(t, err, service.ErrRedeemPurchaseNotFound, "another user's purchase is not found")

	// Overdrawn buyers still get their refund.
	_, err = integrationDB.ExecContext(f.ctx, `UPDATE users SET balance = -50 WHERE id = $1`, buyer)
	require.NoError(t, err)
	_, err = f.svc.Revoke(f.ctx, buyer, v.ID)
	require.NoError(t, err)
	f.requireBalance(buyer, "-40")

	used := f.mustBuy(stranger, "10")
	_, err = f.redeemService().Redeem(f.ctx, stranger, used.Code)
	require.NoError(t, err)
	_, err = f.svc.Revoke(f.ctx, stranger, used.ID)
	require.ErrorIs(t, err, service.ErrRedeemPurchaseCodeUsed)
	f.requireBalance(stranger, "100")
	require.Empty(t, f.records(stranger, service.RedeemTypePurchaseRefund))

	// An active purchase whose code was expired outside the feature is shown as
	// unknown and refuses a refund.
	odd := f.mustBuy(stranger, "5")
	_, err = integrationDB.ExecContext(f.ctx, `UPDATE redeem_codes SET status = 'expired' WHERE id = (SELECT redeem_code_id FROM redeem_code_purchases WHERE id = $1)`, odd.ID)
	require.NoError(t, err)
	_, err = f.svc.Revoke(f.ctx, stranger, odd.ID)
	require.ErrorIs(t, err, service.ErrRedeemPurchaseInconsistent)
	f.requireBalance(stranger, "95")
	out, err := f.svc.Overview(f.ctx, stranger, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, odd.ID, out.Items[0].ID)
	require.Equal(t, service.RedeemPurchaseViewUnknown, out.Items[0].Status)
}

func TestRedeemPurchaseIntegration_AdminCannotDeleteOrExpire(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("100")
	redeemer := f.user("0")
	active := f.mustBuy(buyer, "1")
	revoked := f.mustBuy(buyer, "2")
	_, err := f.svc.Revoke(f.ctx, buyer, revoked.ID)
	require.NoError(t, err)
	used := f.mustBuy(buyer, "3")
	_, err = f.redeemService().Redeem(f.ctx, redeemer, used.Code)
	require.NoError(t, err)

	codeRepo := NewRedeemCodeRepository(f.client)
	admin := service.NewAdminService(nil, nil, nil, nil, nil, codeRepo, nil, nil, nil, nil, nil, f.client, nil, nil, nil, nil, nil, nil, nil)
	idOf := func(where string, args ...any) []int64 {
		rows, err := integrationDB.QueryContext(f.ctx, `SELECT id FROM redeem_codes WHERE `+where+` ORDER BY id`, args...)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		var ids []int64
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			ids = append(ids, id)
		}
		return ids
	}
	purchased := idOf(`id IN (SELECT redeem_code_id FROM redeem_code_purchases WHERE buyer_user_id = $1)`, buyer)
	require.Len(t, purchased, 3)
	recordIDs := idOf(`used_by = $1 AND type IN ($2, $3)`, buyer, service.RedeemTypePurchaseDebit, service.RedeemTypePurchaseRefund)
	require.Len(t, recordIDs, 4, "3 debits + 1 refund")
	protected := append(append([]int64{}, purchased...), recordIDs...)

	control := mustCreateRedeemCode(t, f.client, &service.RedeemCode{Code: "rpctl" + uuid.NewString()[:20], Type: service.RedeemTypeBalance, Value: 1})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM redeem_codes WHERE id = $1`, control.ID)
	})

	for _, id := range protected {
		require.ErrorIs(t, admin.DeleteRedeemCode(f.ctx, id), service.ErrRedeemPurchaseRecordImmutable, "delete %d", id)
		_, err := admin.ExpireRedeemCode(f.ctx, id)
		require.ErrorIs(t, err, service.ErrRedeemPurchaseRecordImmutable, "expire %d", id)
	}
	deleted, err := admin.BatchDeleteRedeemCodes(f.ctx, append(append([]int64{}, protected...), control.ID))
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted, "only the ordinary code is deleted")
	var left int
	require.NoError(t, integrationDB.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM redeem_codes WHERE id = ANY($1)`, pq.Array(protected)).Scan(&left))
	require.Equal(t, len(protected), left)

	var activeStatus string
	require.NoError(t, integrationDB.QueryRowContext(f.ctx, `SELECT c.status FROM redeem_codes c JOIN redeem_code_purchases p ON p.redeem_code_id = c.id WHERE p.id = $1`, active.ID).Scan(&activeStatus))
	require.Equal(t, service.StatusUnused, activeStatus, "expire was refused, the code still works")

	// The foreign key is the backstop under the service check.
	_, err = integrationDB.ExecContext(f.ctx, `DELETE FROM redeem_codes WHERE id = $1`, purchased[0])
	var pqErr *pq.Error
	require.True(t, errors.As(err, &pqErr), "%v", err)
	require.Equal(t, pq.ErrorCode("23001"), pqErr.Code, "restrict_violation")
}

func TestRedeemPurchaseIntegration_NotATopUp(t *testing.T) {
	f := newRPFixture(t)
	invites := NewUserInviteRepository(f.client)
	codes := NewRedeemCodeRepository(f.client)
	redeemSvc := f.redeemService()

	// Buyer redeems their own purchased code.
	self := f.user("100")
	v := f.mustBuy(self, "10")
	_, err := redeemSvc.Redeem(f.ctx, self, v.Code)
	require.NoError(t, err)
	f.requireBalance(self, "100")
	ok, err := invites.HasQualifyingTopUp(f.ctx, self)
	require.NoError(t, err)
	require.False(t, ok, "self-redeemed purchased code is not a top-up")
	sum, err := codes.SumPositiveBalanceByUser(f.ctx, self)
	require.NoError(t, err)
	require.Zero(t, sum)

	// A buys, B redeems.
	a := f.user("100")
	b := f.user("0")
	gift := f.mustBuy(a, "20")
	_, err = redeemSvc.Redeem(f.ctx, b, gift.Code)
	require.NoError(t, err)
	f.requireBalance(b, "20")
	ok, err = invites.HasQualifyingTopUp(f.ctx, b)
	require.NoError(t, err)
	require.False(t, ok, "a gifted purchased code is not the redeemer's top-up")
	sum, err = codes.SumPositiveBalanceByUser(f.ctx, b)
	require.NoError(t, err)
	require.Zero(t, sum)

	// The refund record (+amount) is not a top-up either.
	r := f.user("100")
	rv := f.mustBuy(r, "30")
	_, err = f.svc.Revoke(f.ctx, r, rv.ID)
	require.NoError(t, err)
	require.Len(t, f.records(r, service.RedeemTypePurchaseRefund), 1)
	ok, err = invites.HasQualifyingTopUp(f.ctx, r)
	require.NoError(t, err)
	require.False(t, ok)
	sum, err = codes.SumPositiveBalanceByUser(f.ctx, r)
	require.NoError(t, err)
	require.Zero(t, sum)

	// Control: an ordinary admin-generated balance code still counts.
	usedCode(t, b, service.RedeemTypeBalance, 5)
	ok, err = invites.HasQualifyingTopUp(f.ctx, b)
	require.NoError(t, err)
	require.True(t, ok)
	sum, err = codes.SumPositiveBalanceByUser(f.ctx, b)
	require.NoError(t, err)
	require.InDelta(t, 5, sum, 1e-9)
	// Subscription / concurrency codes keep qualifying (unchanged rule).
	usedCode(t, self, service.RedeemTypeConcurrency, 1)
	ok, err = invites.HasQualifyingTopUp(f.ctx, self)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestRedeemPurchaseIntegration_InactiveBuyer(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("100")
	reqID := uuid.NewString()
	_, _, err := f.svc.Purchase(f.ctx, buyer, "10", reqID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(f.ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, buyer)
	require.NoError(t, err)
	_, err = f.buy(buyer, "10")
	require.ErrorIs(t, err, service.ErrUserNotActive)
	f.requireBalance(buyer, "90")
}

// A purchase that queued on the buyer's row lock sees a switch-off made while
// it waited: the switches are read after the lock, not before.
func TestRedeemPurchaseIntegration_SwitchOffWhileQueuedOnTheLock(t *testing.T) {
	f := newRPFixture(t)
	buyer := f.user("100")
	blocker, err := integrationDB.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()
	_, err = blocker.Exec(`SELECT id FROM users WHERE id = $1 FOR UPDATE`, buyer)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := f.buy(buyer, "10")
		done <- err
	}()
	waitForLockWaiters(t, 1, "%from users%for update%")
	f.settings.set(service.SettingKeyRedeemPurchaseEnabled, "false")
	require.NoError(t, blocker.Rollback())
	require.ErrorIs(t, <-done, service.ErrRedeemPurchaseDisabled)
	f.requireBalance(buyer, "100")
	require.Zero(t, f.purchaseCount(buyer))
}

// The production reader takes the switches from the settings table, on the
// transaction's own connection when ctx carries one.
func TestRedeemPurchaseIntegration_SettingsReadOnTxConnection(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewRedeemPurchaseRepository(client)
	keys := service.RedeemPurchaseSettingKeys()
	cleanup := func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM settings WHERE key = ANY($1)`, pq.Array(keys))
	}
	cleanup()
	t.Cleanup(cleanup)

	cfg, err := repo.LoadRedeemPurchaseSettings(ctx)
	require.NoError(t, err)
	require.False(t, cfg.Enabled, "missing rows mean off")
	require.True(t, cfg.MaxAmount.Equal(service.DefaultRedeemPurchaseMaxAmount))

	for k, v := range map[string]string{
		service.SettingKeyRedeemPurchaseEnabled:        "true",
		service.SettingKeyRedeemPurchaseMaxAmount:      "19.99",
		service.SettingKeyRedeemPurchaseWindowMaxCount: "3",
	} {
		_, err := client.Setting.Create().SetKey(k).SetValue(v).Save(ctx)
		require.NoError(t, err)
	}
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	// Change a value inside the transaction: only a read on the tx connection sees it.
	_, err = tx.Client().Setting.Update().Where(setting.KeyEQ(service.SettingKeyRedeemPurchaseWindowMaxCount)).SetValue("4").Save(txCtx)
	require.NoError(t, err)
	cfg, err = repo.LoadRedeemPurchaseSettings(txCtx)
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.True(t, cfg.MaxAmount.Equal(decimal.RequireFromString("19.99")))
	require.Equal(t, 4, cfg.WindowMaxCount)
	outside, err := repo.LoadRedeemPurchaseSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, outside.WindowMaxCount)
}
