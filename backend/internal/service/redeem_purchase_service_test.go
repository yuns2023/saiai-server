//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type purchaseSettingsStub struct {
	cfg   RedeemPurchaseSettings
	err   error
	reads int
}

func (s *purchaseSettingsStub) LoadRedeemPurchaseSettings(context.Context) (RedeemPurchaseSettings, error) {
	s.reads++
	return s.cfg, s.err
}

// purchaseRepoStub records the order of calls; the fake transaction runner
// marks whether each transaction committed.
type purchaseRepoStub struct {
	calls []string

	buyer       *RedeemPurchaseBuyer
	byRequest   *RedeemPurchaseRecord
	windowSum   decimal.Decimal
	windowCount int
	createErrs  []error
	created     []RedeemPurchaseCreateInput

	locked     *RedeemPurchaseRecord
	expireOK   bool
	codeStatus string
	refundErrs []error
	refunds    []string
}

// The service reads settings through the separate reader in these tests.
func (r *purchaseRepoStub) LoadRedeemPurchaseSettings(context.Context) (RedeemPurchaseSettings, error) {
	return RedeemPurchaseSettings{}, errors.New("unexpected settings read through the repository")
}

func (r *purchaseRepoStub) LockBuyer(context.Context, int64) (*RedeemPurchaseBuyer, error) {
	r.calls = append(r.calls, "lock_buyer")
	if r.buyer == nil {
		return nil, ErrUserNotFound
	}
	return r.buyer, nil
}

func (r *purchaseRepoStub) GetByRequestID(context.Context, int64, string) (*RedeemPurchaseRecord, error) {
	r.calls = append(r.calls, "get_by_request")
	return r.byRequest, nil
}

func (r *purchaseRepoStub) WindowUsage(context.Context, int64, time.Time) (decimal.Decimal, int, error) {
	r.calls = append(r.calls, "window")
	return r.windowSum, r.windowCount, nil
}

func (r *purchaseRepoStub) Create(_ context.Context, in RedeemPurchaseCreateInput) (*RedeemPurchaseRecord, error) {
	r.calls = append(r.calls, "create")
	r.created = append(r.created, in)
	if len(r.createErrs) > 0 {
		err := r.createErrs[0]
		r.createErrs = r.createErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &RedeemPurchaseRecord{ID: 9, Amount: in.Amount, Status: RedeemPurchaseStatusActive, Code: in.Code, CodeStatus: StatusUnused, CreatedAt: in.Now}, nil
}

func (r *purchaseRepoStub) LockForRevoke(context.Context, int64, int64) (*RedeemPurchaseRecord, error) {
	r.calls = append(r.calls, "lock_purchase")
	if r.locked == nil {
		return nil, ErrRedeemPurchaseNotFound
	}
	cp := *r.locked
	return &cp, nil
}

func (r *purchaseRepoStub) ExpireUnusedCode(context.Context, int64) (bool, error) {
	r.calls = append(r.calls, "expire_code")
	return r.expireOK, nil
}

func (r *purchaseRepoStub) GetCodeState(context.Context, int64) (string, string, *time.Time, error) {
	r.calls = append(r.calls, "code_state")
	return "c", r.codeStatus, nil, nil
}

func (r *purchaseRepoStub) Refund(_ context.Context, _ *RedeemPurchaseRecord, refundCode string, _ time.Time) error {
	r.calls = append(r.calls, "refund")
	r.refunds = append(r.refunds, refundCode)
	if len(r.refundErrs) > 0 {
		err := r.refundErrs[0]
		r.refundErrs = r.refundErrs[1:]
		return err
	}
	return nil
}

func (r *purchaseRepoStub) Now(context.Context) (time.Time, error) {
	return time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC), nil
}

func (r *purchaseRepoStub) ListByBuyer(_ context.Context, _ int64, p pagination.PaginationParams) ([]RedeemPurchaseRecord, *pagination.PaginationResult, error) {
	return nil, &pagination.PaginationResult{Page: p.Page, PageSize: p.PageSize, Pages: 1}, nil
}

type txLog struct{ commits, rollbacks int }

func (l *txLog) run(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		l.rollbacks++
		return err
	}
	l.commits++
	return nil
}

type authInvalidatorStub struct{ users []int64 }

func (a *authInvalidatorStub) InvalidateAuthCacheByKey(context.Context, string) {}
func (a *authInvalidatorStub) InvalidateAuthCacheByUserID(_ context.Context, id int64) {
	a.users = append(a.users, id)
}
func (a *authInvalidatorStub) InvalidateAuthCacheByGroupID(context.Context, int64) {}

func enabledPurchaseSettings() RedeemPurchaseSettings {
	return RedeemPurchaseSettings{
		Enabled:         true,
		MinAmount:       decimal.NewFromInt(1),
		MaxAmount:       decimal.NewFromInt(500),
		WindowMaxAmount: decimal.NewFromInt(1000),
		WindowMaxCount:  20,
	}
}

func newPurchaseServiceForTest(repo *purchaseRepoStub, cfg RedeemPurchaseSettings) (*RedeemPurchaseService, *txLog, *authInvalidatorStub) {
	svc, tx, inv, _ := newPurchaseServiceWithSettings(repo, &purchaseSettingsStub{cfg: cfg})
	return svc, tx, inv
}

func newPurchaseServiceWithSettings(repo *purchaseRepoStub, settings *purchaseSettingsStub) (*RedeemPurchaseService, *txLog, *authInvalidatorStub, *purchaseSettingsStub) {
	tx := &txLog{}
	inv := &authInvalidatorStub{}
	svc := NewRedeemPurchaseServiceWithTx(repo, settings, tx.run, inv, nil)
	n := 0
	svc.newCode = func() (string, error) {
		n++
		return fmt.Sprintf("code-%d", n), nil
	}
	return svc, tx, inv, settings
}

func activeBuyer(balance string) *RedeemPurchaseBuyer {
	return &RedeemPurchaseBuyer{Status: StatusActive, Balance: decimal.RequireFromString(balance), Now: time.Now()}
}

const testRequestID = "3f2b8c1e-6a4d-4b7e-9c2a-1d5e8f7a6b3c"

func TestParseRedeemPurchaseAmount(t *testing.T) {
	ok := map[string]string{"1": "1", "10.5": "10.5", "10.50": "10.5", "0.01": "0.01", "007.10": "7.1", " 3 ": "3"}
	for in, want := range ok {
		got, err := ParseRedeemPurchaseAmount(in)
		require.NoError(t, err, in)
		require.True(t, got.Equal(decimal.RequireFromString(want)), "%s → %s", in, got)
	}
	for _, in := range []string{"", "0", "0.00", "-1", "1.001", "10.500", "1.000", "0.009", "1e2", "1E2", "+5", " ", "1.", ".5", "NaN", "Infinity", "1,5", "12345678901234567890123456789012345"} {
		_, err := ParseRedeemPurchaseAmount(in)
		require.ErrorIs(t, err, ErrRedeemPurchaseInvalidAmount, in)
	}
}

func TestNormalizeRedeemPurchaseRequestID(t *testing.T) {
	got, err := NormalizeRedeemPurchaseRequestID("3F2B8C1E-6A4D-4B7E-9C2A-1D5E8F7A6B3C")
	require.NoError(t, err)
	require.Equal(t, testRequestID, got)
	for _, in := range []string{"", "abc", "00000000-0000-0000-0000-000000000000", "{3f2b8c1e-6a4d-4b7e-9c2a-1d5e8f7a6b3c}", "urn:uuid:3f2b8c1e-6a4d-4b7e-9c2a-1d5e8f7a6b3c", "3f2b8c1e6a4d4b7e9c2a1d5e8f7a6b3c"} {
		_, err := NormalizeRedeemPurchaseRequestID(in)
		require.ErrorIs(t, err, ErrRedeemPurchaseInvalidRequestID, in)
	}
}

func TestRedeemPurchase_HappyPathWritesAndInvalidates(t *testing.T) {
	repo := &purchaseRepoStub{buyer: activeBuyer("100")}
	svc, tx, inv := newPurchaseServiceForTest(repo, enabledPurchaseSettings())

	view, replayed, err := svc.Purchase(context.Background(), 7, "12.30", testRequestID)
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, RedeemPurchaseViewUnused, view.Status)
	require.Equal(t, "code-1", view.Code)
	require.True(t, view.Amount.Equal(decimal.RequireFromString("12.3")))
	require.Equal(t, []string{"lock_buyer", "get_by_request", "window", "create"}, repo.calls)
	require.Len(t, repo.created, 1)
	require.Equal(t, "code-2", repo.created[0].DebitCode)
	require.Equal(t, testRequestID, repo.created[0].RequestID)
	require.Equal(t, 1, tx.commits)
	require.Equal(t, []int64{7}, inv.users)
}

// The replay check runs before the switch and every limit: a retried request
// that already succeeded gets the original purchase back, never a second charge.
func TestRedeemPurchase_ReplayBeforeSwitchAndLimits(t *testing.T) {
	existing := &RedeemPurchaseRecord{ID: 3, Amount: decimal.RequireFromString("10.00000000"), Status: RedeemPurchaseStatusRevoked, Code: "orig", CodeStatus: StatusExpired}
	off := enabledPurchaseSettings()
	off.Enabled = false
	off.MaxAmount = decimal.NewFromInt(5)
	off.WindowMaxCount = 0
	repo := &purchaseRepoStub{buyer: activeBuyer("0"), byRequest: existing}
	svc, _, inv, settings := newPurchaseServiceWithSettings(repo, &purchaseSettingsStub{cfg: off})

	view, replayed, err := svc.Purchase(context.Background(), 7, "10", testRequestID)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, int64(3), view.ID)
	require.Equal(t, "orig", view.Code)
	require.Equal(t, RedeemPurchaseViewRevoked, view.Status)
	require.Equal(t, []string{"lock_buyer", "get_by_request"}, repo.calls)
	require.Empty(t, repo.created)
	require.Empty(t, inv.users, "a replay changes nothing")
	require.Zero(t, settings.reads, "a replay does not even read the switches")
}

func TestRedeemPurchase_ReplayWithDifferentAmountConflicts(t *testing.T) {
	existing := &RedeemPurchaseRecord{ID: 3, Amount: decimal.RequireFromString("10"), Status: RedeemPurchaseStatusActive, CodeStatus: StatusUnused}
	off := enabledPurchaseSettings()
	off.Enabled = false
	repo := &purchaseRepoStub{buyer: activeBuyer("100"), byRequest: existing}
	svc, _, _ := newPurchaseServiceForTest(repo, off)

	_, _, err := svc.Purchase(context.Background(), 7, "10.01", testRequestID)
	require.ErrorIs(t, err, ErrRedeemPurchaseRequestConflict)
	require.Empty(t, repo.created)
}

func TestRedeemPurchase_InactiveBuyerRefusedBeforeReplay(t *testing.T) {
	repo := &purchaseRepoStub{buyer: &RedeemPurchaseBuyer{Status: StatusDisabled, Balance: decimal.NewFromInt(100)}}
	svc, _, _ := newPurchaseServiceForTest(repo, enabledPurchaseSettings())
	_, _, err := svc.Purchase(context.Background(), 7, "10", testRequestID)
	require.ErrorIs(t, err, ErrUserNotActive)
	require.Equal(t, []string{"lock_buyer"}, repo.calls)
}

func TestRedeemPurchase_Refusals(t *testing.T) {
	cases := []struct {
		name    string
		amount  string
		balance string
		sum     string
		count   int
		mutate  func(*RedeemPurchaseSettings)
		want    error
	}{
		{name: "switch off", amount: "10", balance: "100", mutate: func(c *RedeemPurchaseSettings) { c.Enabled = false }, want: ErrRedeemPurchaseDisabled},
		{name: "below min", amount: "0.99", balance: "100", want: ErrRedeemPurchaseAmountBelowMin},
		{name: "above max", amount: "500.01", balance: "1000", want: ErrRedeemPurchaseAmountAboveMax},
		{name: "count cap", amount: "1", balance: "100", count: 20, want: ErrRedeemPurchaseWindowCountLimit},
		{name: "amount cap", amount: "100", balance: "1000", sum: "900.01", want: ErrRedeemPurchaseWindowAmountLimit},
		{name: "insufficient balance", amount: "10", balance: "9.99", want: ErrInsufficientBalance},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := enabledPurchaseSettings()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			sum := decimal.Zero
			if tc.sum != "" {
				sum = decimal.RequireFromString(tc.sum)
			}
			repo := &purchaseRepoStub{buyer: activeBuyer(tc.balance), windowSum: sum, windowCount: tc.count}
			svc, tx, inv := newPurchaseServiceForTest(repo, cfg)
			_, _, err := svc.Purchase(context.Background(), 7, tc.amount, testRequestID)
			require.ErrorIs(t, err, tc.want)
			require.Empty(t, repo.created)
			require.Equal(t, 1, tx.rollbacks)
			require.Empty(t, inv.users)
		})
	}

	t.Run("exactly at the caps and balance is allowed", func(t *testing.T) {
		cfg := enabledPurchaseSettings()
		repo := &purchaseRepoStub{buyer: activeBuyer("100"), windowSum: decimal.RequireFromString("900"), windowCount: 19}
		svc, _, _ := newPurchaseServiceForTest(repo, cfg)
		_, _, err := svc.Purchase(context.Background(), 7, "100", testRequestID)
		require.NoError(t, err)
	})
}

func TestRedeemPurchase_CodeCollisionRetriesOnceWithNewCodes(t *testing.T) {
	repo := &purchaseRepoStub{buyer: activeBuyer("100"), createErrs: []error{fmt.Errorf("%w: dup", ErrRedeemPurchaseCodeCollision)}}
	svc, tx, _ := newPurchaseServiceForTest(repo, enabledPurchaseSettings())
	view, _, err := svc.Purchase(context.Background(), 7, "10", testRequestID)
	require.NoError(t, err)
	require.Len(t, repo.created, 2)
	require.NotEqual(t, repo.created[0].Code, repo.created[1].Code)
	require.NotEqual(t, repo.created[0].DebitCode, repo.created[1].DebitCode)
	require.Equal(t, repo.created[1].Code, view.Code)
	require.Equal(t, 1, tx.rollbacks)
	require.Equal(t, 1, tx.commits)

	repo = &purchaseRepoStub{buyer: activeBuyer("100"), createErrs: []error{ErrRedeemPurchaseCodeCollision, ErrRedeemPurchaseCodeCollision}}
	svc, _, _ = newPurchaseServiceForTest(repo, enabledPurchaseSettings())
	_, _, err = svc.Purchase(context.Background(), 7, "10", testRequestID)
	require.ErrorIs(t, err, ErrRedeemPurchaseCodeCollision)
	require.Len(t, repo.created, 2, "only one retry")
}

func TestRedeemPurchase_RequestRaceAnswersLikeReplay(t *testing.T) {
	repo := &purchaseRepoStub{buyer: activeBuyer("100"), createErrs: []error{ErrRedeemPurchaseRequestRace}}
	svc, _, inv := newPurchaseServiceForTest(repo, enabledPurchaseSettings())
	// The racing request committed between our replay check and our insert.
	winner := &RedeemPurchaseRecord{ID: 11, Amount: decimal.NewFromInt(10), Status: RedeemPurchaseStatusActive, Code: "w", CodeStatus: StatusUnused}
	svc.repo = &raceRepo{purchaseRepoStub: repo, after: winner}
	view, replayed, err := svc.Purchase(context.Background(), 7, "10", testRequestID)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, int64(11), view.ID)
	require.Empty(t, inv.users)

	svc.repo = &raceRepo{purchaseRepoStub: &purchaseRepoStub{buyer: activeBuyer("100"), createErrs: []error{ErrRedeemPurchaseRequestRace}}, after: winner}
	_, _, err = svc.Purchase(context.Background(), 7, "9", testRequestID)
	require.ErrorIs(t, err, ErrRedeemPurchaseRequestConflict)
}

// raceRepo returns no purchase for the in-transaction replay check and the
// winner afterwards.
type raceRepo struct {
	*purchaseRepoStub
	after *RedeemPurchaseRecord
	reads int
}

func (r *raceRepo) GetByRequestID(context.Context, int64, string) (*RedeemPurchaseRecord, error) {
	r.reads++
	if r.reads == 1 {
		return nil, nil
	}
	return r.after, nil
}

func TestRedeemPurchase_RevokeFlow(t *testing.T) {
	active := &RedeemPurchaseRecord{ID: 5, RedeemCodeID: 50, Amount: decimal.NewFromInt(10), Status: RedeemPurchaseStatusActive, Code: "c", CodeStatus: StatusUnused}

	t.Run("refunds once", func(t *testing.T) {
		repo := &purchaseRepoStub{locked: active, expireOK: true}
		svc, tx, inv := newPurchaseServiceForTest(repo, enabledPurchaseSettings())
		view, err := svc.Revoke(context.Background(), 7, 5)
		require.NoError(t, err)
		require.Equal(t, RedeemPurchaseViewRevoked, view.Status)
		require.NotNil(t, view.RevokedAt)
		require.Nil(t, view.UsedAt)
		require.Equal(t, []string{"lock_purchase", "expire_code", "refund"}, repo.calls)
		require.Equal(t, 1, tx.commits)
		require.Equal(t, []int64{7}, inv.users)
	})

	t.Run("already revoked is returned without a second refund", func(t *testing.T) {
		revokedAt := time.Now()
		repo := &purchaseRepoStub{locked: &RedeemPurchaseRecord{ID: 5, Amount: decimal.NewFromInt(10), Status: RedeemPurchaseStatusRevoked, RevokedAt: &revokedAt, CodeStatus: StatusExpired}}
		svc, _, inv := newPurchaseServiceForTest(repo, enabledPurchaseSettings())
		view, err := svc.Revoke(context.Background(), 7, 5)
		require.NoError(t, err)
		require.Equal(t, RedeemPurchaseViewRevoked, view.Status)
		require.Equal(t, []string{"lock_purchase"}, repo.calls)
		require.Empty(t, inv.users)
	})

	t.Run("used code cannot be revoked", func(t *testing.T) {
		repo := &purchaseRepoStub{locked: active, expireOK: false, codeStatus: StatusUsed}
		svc, tx, _ := newPurchaseServiceForTest(repo, enabledPurchaseSettings())
		_, err := svc.Revoke(context.Background(), 7, 5)
		require.ErrorIs(t, err, ErrRedeemPurchaseCodeUsed)
		require.Empty(t, repo.refunds)
		require.Equal(t, 1, tx.rollbacks)
	})

	t.Run("expired code on an active purchase is an anomaly, not a refund", func(t *testing.T) {
		repo := &purchaseRepoStub{locked: active, expireOK: false, codeStatus: StatusExpired}
		svc, _, _ := newPurchaseServiceForTest(repo, enabledPurchaseSettings())
		_, err := svc.Revoke(context.Background(), 7, 5)
		require.ErrorIs(t, err, ErrRedeemPurchaseInconsistent)
		require.Empty(t, repo.refunds)
	})

	t.Run("not found", func(t *testing.T) {
		svc, _, _ := newPurchaseServiceForTest(&purchaseRepoStub{}, enabledPurchaseSettings())
		_, err := svc.Revoke(context.Background(), 7, 5)
		require.ErrorIs(t, err, ErrRedeemPurchaseNotFound)
		_, err = svc.Revoke(context.Background(), 7, 0)
		require.ErrorIs(t, err, ErrRedeemPurchaseNotFound)
	})

	t.Run("refund record collision retries once", func(t *testing.T) {
		repo := &purchaseRepoStub{locked: active, expireOK: true, refundErrs: []error{ErrRedeemPurchaseCodeCollision}}
		svc, tx, _ := newPurchaseServiceForTest(repo, enabledPurchaseSettings())
		_, err := svc.Revoke(context.Background(), 7, 5)
		require.NoError(t, err)
		require.Len(t, repo.refunds, 2)
		require.NotEqual(t, repo.refunds[0], repo.refunds[1])
		require.Equal(t, 1, tx.rollbacks)
		require.Equal(t, 1, tx.commits)
	})
}

func TestRedeemPurchaseViewStatus(t *testing.T) {
	usedAt := time.Now()
	cases := []struct {
		rec  RedeemPurchaseRecord
		want string
	}{
		{RedeemPurchaseRecord{Status: RedeemPurchaseStatusActive, CodeStatus: StatusUnused}, RedeemPurchaseViewUnused},
		{RedeemPurchaseRecord{Status: RedeemPurchaseStatusActive, CodeStatus: StatusUsed, CodeUsedAt: &usedAt}, RedeemPurchaseViewUsed},
		{RedeemPurchaseRecord{Status: RedeemPurchaseStatusRevoked, CodeStatus: StatusExpired}, RedeemPurchaseViewRevoked},
		{RedeemPurchaseRecord{Status: RedeemPurchaseStatusActive, CodeStatus: StatusExpired}, RedeemPurchaseViewUnknown},
		{RedeemPurchaseRecord{Status: RedeemPurchaseStatusActive, CodeStatus: ""}, RedeemPurchaseViewUnknown},
		{RedeemPurchaseRecord{Status: "weird", CodeStatus: StatusUnused}, RedeemPurchaseViewUnknown},
	}
	for _, tc := range cases {
		v := redeemPurchaseView(&tc.rec)
		require.Equal(t, tc.want, v.Status, "%+v", tc.rec)
		if tc.want == RedeemPurchaseViewUsed {
			require.Equal(t, &usedAt, v.UsedAt)
		} else {
			require.Nil(t, v.UsedAt)
		}
	}
}

func TestRedeemPurchase_OverviewFloorsRemaining(t *testing.T) {
	cfg := enabledPurchaseSettings()
	cfg.Enabled = false
	repo := &purchaseRepoStub{windowSum: decimal.RequireFromString("1200"), windowCount: 25}
	svc, _, _ := newPurchaseServiceForTest(repo, cfg)
	out, err := svc.Overview(context.Background(), 7, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.False(t, out.Config.Enabled)
	require.True(t, out.Config.RemainingAmount24h.IsZero())
	require.Zero(t, out.Config.RemainingCount24h)
	require.NotNil(t, out.Items)
}

func TestParseRedeemPurchaseSettings(t *testing.T) {
	def := parseRedeemPurchaseSettings(nil)
	require.False(t, def.Enabled)
	require.True(t, def.MinAmount.Equal(DefaultRedeemPurchaseMinAmount))
	require.True(t, def.MaxAmount.Equal(DefaultRedeemPurchaseMaxAmount))
	require.True(t, def.WindowMaxAmount.Equal(DefaultRedeemPurchaseWindowMaxAmount))
	require.Equal(t, DefaultRedeemPurchaseWindowMaxCount, def.WindowMaxCount)

	got := parseRedeemPurchaseSettings(map[string]string{
		SettingKeyRedeemPurchaseEnabled:         "true",
		SettingKeyRedeemPurchaseMinAmount:       "0.5",
		SettingKeyRedeemPurchaseMaxAmount:       "-1",
		SettingKeyRedeemPurchaseWindowMaxAmount: "2000000",
		SettingKeyRedeemPurchaseWindowMaxCount:  "0",
	})
	require.True(t, got.Enabled)
	require.True(t, got.MinAmount.Equal(decimal.RequireFromString("0.5")))
	require.True(t, got.MaxAmount.Equal(DefaultRedeemPurchaseMaxAmount), "negative → default")
	require.True(t, got.WindowMaxAmount.Equal(DefaultRedeemPurchaseWindowMaxAmount), "above the cap → default")
	require.Equal(t, 0, got.WindowMaxCount, "0 is a real limit")

	for _, bad := range []string{"1.001", "abc", "NaN"} {
		require.True(t, parseRedeemPurchaseAmountSetting(bad, DefaultRedeemPurchaseMinAmount).Equal(DefaultRedeemPurchaseMinAmount), bad)
	}
	require.Equal(t, DefaultRedeemPurchaseWindowMaxCount, parseRedeemPurchaseWindowMaxCount("10001"))
}

func TestRedeemPurchaseSettingUpdatesRoundTrip(t *testing.T) {
	updates := map[string]string{}
	redeemPurchaseSettingUpdates(&SystemSettings{
		RedeemPurchaseEnabled:         true,
		RedeemPurchaseMinAmount:       0.1,
		RedeemPurchaseMaxAmount:       19.99,
		RedeemPurchaseWindowMaxAmount: 1000,
		RedeemPurchaseWindowMaxCount:  3,
	}, updates)
	require.Equal(t, map[string]string{
		SettingKeyRedeemPurchaseEnabled:         "true",
		SettingKeyRedeemPurchaseMinAmount:       "0.1",
		SettingKeyRedeemPurchaseMaxAmount:       "19.99",
		SettingKeyRedeemPurchaseWindowMaxAmount: "1000",
		SettingKeyRedeemPurchaseWindowMaxCount:  "3",
	}, updates)

	var out SystemSettings
	applyRedeemPurchaseSystemSettings(&out, updates)
	require.InDelta(t, 19.99, out.RedeemPurchaseMaxAmount, 0)
	require.Equal(t, 3, out.RedeemPurchaseWindowMaxCount)
}

func TestRedeemPurchaseSettingsReadErrorFailsClosed(t *testing.T) {
	readErr := errors.New("db down")
	repo := &purchaseRepoStub{buyer: activeBuyer("100")}
	svc, tx, _, settings := newPurchaseServiceWithSettings(repo, &purchaseSettingsStub{cfg: enabledPurchaseSettings(), err: readErr})
	_, _, err := svc.Purchase(context.Background(), 7, "10", testRequestID)
	require.ErrorIs(t, err, readErr)
	require.Empty(t, repo.created)
	require.Equal(t, 1, tx.rollbacks)
	require.Equal(t, 1, settings.reads, "read once, under the lock, after the replay check")
	require.Equal(t, []string{"lock_buyer", "get_by_request"}, repo.calls)

	_, err = svc.Overview(context.Background(), 7, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.ErrorIs(t, err, readErr)
}
