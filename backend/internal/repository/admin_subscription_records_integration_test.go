//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbgroup "github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// These tests commit through the shared client (not the per-test rollback
// transaction) because they exercise the service's own transactions and row
// locks across concurrent transactions. Names are unique per test.

type adminSubFixture struct {
	client *dbent.Client
	repo   *userSubscriptionRepository
	svc    *service.SubscriptionService
	userID int64
	group  int64
	subID  int64
}

func newAdminSubFixture(t *testing.T, expiresAt time.Time, status string) *adminSubFixture {
	t.Helper()
	ctx := context.Background()
	client := testEntClient(t)
	suffix := fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())

	u, err := client.User.Create().
		SetEmail("admin-sub-" + suffix + "@example.com").
		SetPasswordHash("test").
		SetStatus(service.StatusActive).
		SetRole(service.RoleUser).
		Save(ctx)
	require.NoError(t, err)
	g, err := client.Group.Create().SetName("admin-sub-" + suffix).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	window := time.Now().Add(-2 * time.Hour)
	sub, err := client.UserSubscription.Create().
		SetUserID(u.ID).
		SetGroupID(g.ID).
		SetStartsAt(time.Now().Add(-40 * 24 * time.Hour)).
		SetExpiresAt(expiresAt).
		SetStatus(status).
		SetAssignedAt(time.Now()).
		SetNotes("").
		SetFiveHourWindowStart(window).SetFiveHourUsageUsd(1).
		SetDailyWindowStart(window).SetDailyUsageUsd(2).
		SetWeeklyWindowStart(window).SetWeeklyUsageUsd(3).
		SetMonthlyWindowStart(window).SetMonthlyUsageUsd(4).
		Save(ctx)
	require.NoError(t, err)

	// Other suites assume empty tables: hard-delete everything committed here.
	t.Cleanup(func() {
		hard := mixins.SkipSoftDelete(context.Background())
		_, _ = client.RedeemCode.Delete().Where(redeemcode.UsedByEQ(u.ID)).Exec(hard)
		_, _ = client.UserSubscription.Delete().Where(usersubscription.UserIDEQ(u.ID)).Exec(hard)
		_, _ = client.Group.Delete().Where(dbgroup.IDEQ(g.ID)).Exec(hard)
		_, _ = client.User.Delete().Where(dbuser.IDEQ(u.ID)).Exec(hard)
	})

	repo := NewUserSubscriptionRepository(client).(*userSubscriptionRepository)
	svc := service.NewSubscriptionService(nil, repo, nil, client, nil)
	svc.SetAdminRecordRepository(NewRedeemCodeRepository(client))
	return &adminSubFixture{client: client, repo: repo, svc: svc, userID: u.ID, group: g.ID, subID: sub.ID}
}

func (f *adminSubFixture) records(t *testing.T) []*dbent.RedeemCode {
	t.Helper()
	recs, err := f.client.RedeemCode.Query().
		Where(redeemcode.UsedByEQ(f.userID)).
		Order(dbent.Asc(redeemcode.FieldID)).
		All(context.Background())
	require.NoError(t, err)
	return recs
}

func TestAdminSubscriptionRecords_ExtendActive(t *testing.T) {
	expires := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Microsecond)
	f := newAdminSubFixture(t, expires, service.SubscriptionStatusActive)

	got, err := f.svc.ExtendSubscription(context.Background(), f.subID, 30, "  续费补偿  ")
	require.NoError(t, err)
	require.WithinDuration(t, expires.AddDate(0, 0, 30), got.ExpiresAt, time.Millisecond)

	recs := f.records(t)
	require.Len(t, recs, 1)
	require.Equal(t, service.AdjustmentTypeAdminSubAdjust, recs[0].Type)
	require.Equal(t, service.StatusUsed, recs[0].Status)
	require.InDelta(t, 30, recs[0].Value, 1e-9)
	require.Equal(t, 30, recs[0].ValidityDays)
	require.NotNil(t, recs[0].Notes)
	require.Equal(t, "续费补偿", *recs[0].Notes)
	require.NotNil(t, recs[0].GroupID)
	require.Equal(t, f.group, *recs[0].GroupID)
	require.NotNil(t, recs[0].UsedAt)
}

func TestAdminSubscriptionRecords_ExtendExpiredCountsFromNow(t *testing.T) {
	f := newAdminSubFixture(t, time.Now().Add(-10*24*time.Hour), service.SubscriptionStatusExpired)

	got, err := f.svc.ExtendSubscription(context.Background(), f.subID, 30, "")
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().AddDate(0, 0, 30), got.ExpiresAt, time.Minute)
	require.Equal(t, service.SubscriptionStatusActive, got.Status)

	recs := f.records(t)
	require.Len(t, recs, 1)
	require.Equal(t, 30, recs[0].ValidityDays, "records the usable period granted, not new-old expiry (40 days)")
}

func TestAdminSubscriptionRecords_ShortenRecordsNegativeDays(t *testing.T) {
	expires := time.Now().Add(20 * 24 * time.Hour)
	f := newAdminSubFixture(t, expires, service.SubscriptionStatusActive)

	_, err := f.svc.ExtendSubscription(context.Background(), f.subID, -5, "")
	require.NoError(t, err)
	recs := f.records(t)
	require.Len(t, recs, 1)
	require.InDelta(t, -5, recs[0].Value, 1e-9)
	require.Equal(t, -5, recs[0].ValidityDays)
}

// A second admin change waits for the first one's row lock and then extends
// from the committed expiry, so neither update is lost.
func TestAdminSubscriptionRecords_ConcurrentAdjustNotLost(t *testing.T) {
	expires := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Microsecond)
	f := newAdminSubFixture(t, expires, service.SubscriptionStatusActive)
	ctx := context.Background()

	tx, err := f.client.Tx(ctx)
	require.NoError(t, err)
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	txCtx := dbent.NewTxContext(ctx, tx)
	require.NoError(t, f.repo.LockByID(txCtx, f.subID))

	done := make(chan error, 1)
	go func() {
		_, err := f.svc.ExtendSubscription(ctx, f.subID, 30, "")
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("extend did not wait for the row lock (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, f.repo.ExtendExpiry(txCtx, f.subID, expires.AddDate(0, 0, 10)))
	require.NoError(t, tx.Commit())
	committed = true
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("extend did not finish after the lock was released")
	}

	got, err := f.repo.GetByID(ctx, f.subID)
	require.NoError(t, err)
	require.WithinDuration(t, expires.AddDate(0, 0, 40), got.ExpiresAt, time.Millisecond)
}

func TestAdminSubscriptionRecords_Revoke(t *testing.T) {
	f := newAdminSubFixture(t, time.Now().Add(36*time.Hour), service.SubscriptionStatusActive)

	require.NoError(t, f.svc.RevokeSubscription(context.Background(), f.subID, "退款"))
	_, err := f.repo.GetByID(context.Background(), f.subID)
	require.ErrorIs(t, err, service.ErrSubscriptionNotFound)

	recs := f.records(t)
	require.Len(t, recs, 1)
	require.Equal(t, service.AdjustmentTypeAdminSubRevoke, recs[0].Type)
	require.InDelta(t, -2, recs[0].Value, 1e-9, "36h left rounds up to 2 days")
	require.NotNil(t, recs[0].Notes)
	require.Equal(t, "退款", *recs[0].Notes)
}

func TestAdminSubscriptionRecords_ResetSelectedWindows(t *testing.T) {
	f := newAdminSubFixture(t, time.Now().Add(5*24*time.Hour), service.SubscriptionStatusActive)

	got, err := f.svc.AdminResetQuota(context.Background(), f.subID, false, true, true, false, "")
	require.NoError(t, err)
	require.InDelta(t, 1, got.FiveHourUsageUSD, 1e-9)
	require.InDelta(t, 0, got.DailyUsageUSD, 1e-9)
	require.InDelta(t, 0, got.WeeklyUsageUSD, 1e-9)
	require.InDelta(t, 4, got.MonthlyUsageUSD, 1e-9)

	recs := f.records(t)
	require.Len(t, recs, 1)
	require.Equal(t, service.AdjustmentTypeAdminSubReset, recs[0].Type)
	require.InDelta(t, float64(service.SubscriptionResetDaily|service.SubscriptionResetWeekly), recs[0].Value, 1e-9)
}

type failingRedeemCreate struct {
	service.RedeemCodeRepository
}

func (failingRedeemCreate) Create(context.Context, *service.RedeemCode) error {
	return errors.New("record insert failed")
}

// When the record cannot be written, the change itself is rolled back.
func TestAdminSubscriptionRecords_RecordFailureRollsBack(t *testing.T) {
	expires := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Microsecond)
	f := newAdminSubFixture(t, expires, service.SubscriptionStatusActive)
	f.svc.SetAdminRecordRepository(failingRedeemCreate{NewRedeemCodeRepository(f.client)})
	ctx := context.Background()

	_, err := f.svc.ExtendSubscription(ctx, f.subID, 30, "")
	require.Error(t, err)
	_, err = f.svc.AdminResetQuota(ctx, f.subID, true, true, true, true, "")
	require.Error(t, err)
	require.Error(t, f.svc.RevokeSubscription(ctx, f.subID, ""))

	got, err := f.repo.GetByID(ctx, f.subID)
	require.NoError(t, err, "revoke rolled back")
	require.WithinDuration(t, expires, got.ExpiresAt, time.Millisecond, "extend rolled back")
	require.InDelta(t, 2, got.DailyUsageUSD, 1e-9, "reset rolled back")
	require.InDelta(t, 4, got.MonthlyUsageUSD, 1e-9, "reset rolled back")
	require.Empty(t, f.records(t))
}

// The redeem repository joins the caller's transaction, so an admin record
// rolls back with the change it describes.
func TestRedeemCodeCreate_JoinsCallerTransaction(t *testing.T) {
	f := newAdminSubFixture(t, time.Now().Add(24*time.Hour), service.SubscriptionStatusActive)
	ctx := context.Background()
	tx, err := f.client.Tx(ctx)
	require.NoError(t, err)

	userID := f.userID
	now := time.Now()
	rec := &service.RedeemCode{Code: fmt.Sprintf("tx-join-%d", now.UnixNano()), Type: service.AdjustmentTypeAdminSubAdjust, Value: 1, Status: service.StatusUsed, UsedBy: &userID, UsedAt: &now}
	require.NoError(t, NewRedeemCodeRepository(f.client).Create(dbent.NewTxContext(ctx, tx), rec))
	require.NoError(t, tx.Rollback())

	require.Empty(t, f.records(t))
}

// Near MaxExpiresAt the record holds the exact (capped) change; a change with
// no effect is refused and leaves no record.
func TestAdminSubscriptionRecords_CappedAtMaxExpiresAt(t *testing.T) {
	f := newAdminSubFixture(t, service.MaxExpiresAt.Add(-6*time.Hour), service.SubscriptionStatusActive)
	ctx := context.Background()

	got, err := f.svc.ExtendSubscription(ctx, f.subID, 30, "")
	require.NoError(t, err)
	require.True(t, got.ExpiresAt.Equal(service.MaxExpiresAt))
	recs := f.records(t)
	require.Len(t, recs, 1)
	require.InDelta(t, 0.25, recs[0].Value, 1e-9)

	_, err = f.svc.ExtendSubscription(ctx, f.subID, 30, "")
	require.ErrorIs(t, err, service.ErrAdjustNoChange)
	require.Len(t, f.records(t), 1)
}

// Cache invalidation runs after the service's own commit, so a caller's
// transaction is refused rather than joined.
func TestAdminSubscriptionRecords_RefusesCallerTransaction(t *testing.T) {
	f := newAdminSubFixture(t, time.Now().Add(24*time.Hour), service.SubscriptionStatusActive)
	ctx := context.Background()
	tx, err := f.client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	_, err = f.svc.ExtendSubscription(dbent.NewTxContext(ctx, tx), f.subID, 30, "")
	require.Error(t, err)
	require.Empty(t, f.records(t))
}
