//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newAdjustBalanceUser(t *testing.T, repo *userRepository, email string, balance float64) *service.User {
	t.Helper()
	_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE email = $1", email)
	u := &service.User{Email: email, PasswordHash: "x", Role: service.RoleUser, Status: service.StatusActive, Concurrency: 5, Balance: balance}
	require.NoError(t, repo.Create(context.Background(), u))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", u.ID)
	})
	return u
}

func storedBalance(t *testing.T, id int64) float64 {
	t.Helper()
	var b float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT balance::float8 FROM users WHERE id = $1", id).Scan(&b))
	return b
}

func TestUserRepositoryAdjustBalance_Operations(t *testing.T) {
	ctx := context.Background()
	repo := newUserRepositoryWithSQL(testEntClient(t), integrationDB)
	u := newAdjustBalanceUser(t, repo, "adjust-ops@test.com", 10)

	adj, err := repo.AdjustBalance(ctx, u.ID, service.BalanceOpAdd, 0.1)
	require.NoError(t, err)
	require.Equal(t, service.BalanceAdjustment{OldBalance: 10, NewBalance: 10.1, Delta: 0.1, Changed: true}, adj)

	adj, err = repo.AdjustBalance(ctx, u.ID, service.BalanceOpSubtract, 0.2)
	require.NoError(t, err)
	require.Equal(t, service.BalanceAdjustment{OldBalance: 10.1, NewBalance: 9.9, Delta: -0.2, Changed: true}, adj, "numeric arithmetic, no float drift")

	adj, err = repo.AdjustBalance(ctx, u.ID, service.BalanceOpSet, 3)
	require.NoError(t, err)
	require.Equal(t, service.BalanceAdjustment{OldBalance: 9.9, NewBalance: 3, Delta: -6.9, Changed: true}, adj)

	adj, err = repo.AdjustBalance(ctx, u.ID, service.BalanceOpSet, 3)
	require.NoError(t, err)
	require.False(t, adj.Changed, "setting the same balance is no change")
	require.Zero(t, adj.Delta)

	adj, err = repo.AdjustBalance(ctx, u.ID, service.BalanceOpSubtract, 3)
	require.NoError(t, err, "exactly zero is allowed")
	require.Equal(t, 0.0, adj.NewBalance)
	require.Equal(t, 0.0, storedBalance(t, u.ID))
}

func TestUserRepositoryAdjustBalance_RejectsNegativeAndKeepsBalance(t *testing.T) {
	ctx := context.Background()
	repo := newUserRepositoryWithSQL(testEntClient(t), integrationDB)
	u := newAdjustBalanceUser(t, repo, "adjust-neg@test.com", 5)

	_, err := repo.AdjustBalance(ctx, u.ID, service.BalanceOpSubtract, 5.01)
	require.Error(t, err)
	require.Equal(t, 400, infraerrors.Code(err))
	require.Equal(t, 5.0, storedBalance(t, u.ID))

	// Billing may overdraw; a top-up that still leaves it negative is rejected.
	require.NoError(t, repo.DeductBalance(ctx, u.ID, 15))
	_, err = repo.AdjustBalance(ctx, u.ID, service.BalanceOpAdd, 4)
	require.Error(t, err)
	require.Equal(t, 400, infraerrors.Code(err))
	require.Equal(t, -10.0, storedBalance(t, u.ID))
}

func TestUserRepositoryAdjustBalance_DeletedOrMissingUser(t *testing.T) {
	ctx := context.Background()
	repo := newUserRepositoryWithSQL(testEntClient(t), integrationDB)
	u := newAdjustBalanceUser(t, repo, "adjust-del@test.com", 5)
	require.NoError(t, repo.Delete(ctx, u.ID))

	_, err := repo.AdjustBalance(ctx, u.ID, service.BalanceOpAdd, 1)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	_, err = repo.AdjustBalance(ctx, 1<<50, service.BalanceOpAdd, 1)
	require.ErrorIs(t, err, service.ErrUserNotFound)
}

func TestUserRepositoryAdjustBalance_UsesCallerTransaction(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUserRepositoryWithSQL(client, integrationDB)
	u := newAdjustBalanceUser(t, repo, "adjust-tx@test.com", 5)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)
	_, err = repo.AdjustBalance(txCtx, u.ID, service.BalanceOpAdd, 1)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.Equal(t, 5.0, storedBalance(t, u.ID), "rolled back with the caller's transaction")
}

// A charge committed while the adjustment waits on the row lock is kept, and
// the reported old balance is the one after that charge.
func TestUserRepositoryAdjustBalance_WaitsForConcurrentCharge(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUserRepositoryWithSQL(client, integrationDB)
	u := newAdjustBalanceUser(t, repo, "adjust-race@test.com", 10)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	require.NoError(t, repo.DeductBalance(dbent.NewTxContext(ctx, tx), u.ID, 3))
	var holderPID int
	rows, err := tx.Client().QueryContext(ctx, "SELECT pg_backend_pid()")
	require.NoError(t, err)
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&holderPID))
	require.NoError(t, rows.Close())

	var wg sync.WaitGroup
	var adj service.BalanceAdjustment
	var adjErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		adj, adjErr = repo.AdjustBalance(ctx, u.ID, service.BalanceOpAdd, 5)
	}()
	waitForLockWaiter(t, holderPID)
	require.NoError(t, tx.Commit())
	wg.Wait()

	require.NoError(t, adjErr)
	require.Equal(t, service.BalanceAdjustment{OldBalance: 7, NewBalance: 12, Delta: 5, Changed: true}, adj)
	require.Equal(t, 12.0, storedBalance(t, u.ID))
}

func TestUserRepositoryAdjustBalance_ConcurrentChargesAndAdjustmentsSum(t *testing.T) {
	ctx := context.Background()
	repo := newUserRepositoryWithSQL(testEntClient(t), integrationDB)
	u := newAdjustBalanceUser(t, repo, "adjust-sum@test.com", 100)

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); errs <- repo.DeductBalance(ctx, u.ID, 1) }()
		go func() {
			defer wg.Done()
			_, err := repo.AdjustBalance(ctx, u.ID, service.BalanceOpAdd, 2)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 120.0, storedBalance(t, u.ID))
}

// waitForLockWaiter blocks until a backend is waiting on a lock held by
// holderPID, so the adjustment demonstrably reads the row after that commit.
func waitForLockWaiter(t *testing.T, holderPID int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		var waiting int
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, holderPID).Scan(&waiting))
		if waiting > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("adjustment never waited on the row lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func storedBalanceText(t *testing.T, id int64) string {
	t.Helper()
	var b string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT balance::text FROM users WHERE id = $1", id).Scan(&b))
	return b
}

// float64(new)-float64(old) is 0 here; the numeric delta is not.
func TestUserRepositoryAdjustBalance_TinyChangeOnLargeBalance(t *testing.T) {
	ctx := context.Background()
	repo := newUserRepositoryWithSQL(testEntClient(t), integrationDB)
	u := newAdjustBalanceUser(t, repo, "adjust-tiny@test.com", 1000000000)

	adj, err := repo.AdjustBalance(ctx, u.ID, service.BalanceOpAdd, 0.00000001)
	require.NoError(t, err)
	require.True(t, adj.Changed)
	require.Equal(t, 1e-8, adj.Delta)
	require.Equal(t, "1000000000.00000001", storedBalanceText(t, u.ID))
}

func TestUserRepositoryAdjustBalance_RoundsToColumnScale(t *testing.T) {
	ctx := context.Background()
	repo := newUserRepositoryWithSQL(testEntClient(t), integrationDB)
	u := newAdjustBalanceUser(t, repo, "adjust-scale@test.com", 0)

	adj, err := repo.AdjustBalance(ctx, u.ID, service.BalanceOpSet, 0.000000005)
	require.NoError(t, err)
	require.Equal(t, "0.00000001", storedBalanceText(t, u.ID))
	require.Equal(t, 1e-8, adj.NewBalance, "reports the stored, rounded value")
	require.Equal(t, 1e-8, adj.Delta)
}

func TestUserRepositoryAdjustBalance_OutOfRange(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUserRepositoryWithSQL(client, integrationDB)
	u := newAdjustBalanceUser(t, repo, "adjust-range@test.com", 5)

	_, err := repo.AdjustBalance(ctx, u.ID, service.BalanceOpSet, 1e12)
	require.ErrorIs(t, err, service.ErrBalanceOutOfRange)
	require.Equal(t, 400, infraerrors.Code(err))
	require.Equal(t, 5.0, storedBalance(t, u.ID))

	// Inside a caller's transaction the overflow fails that transaction.
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)
	_, err = repo.AdjustBalance(txCtx, u.ID, service.BalanceOpAdd, 1e12)
	require.ErrorIs(t, err, service.ErrBalanceOutOfRange)
	_, err = repo.AdjustBalance(txCtx, u.ID, service.BalanceOpAdd, 1)
	require.Error(t, err, "aborted transaction rejects further statements")
	require.NoError(t, tx.Rollback())
	require.Equal(t, 5.0, storedBalance(t, u.ID))
}

func TestUserRepositoryAdjustBalance_NegativeInCallerTransactionKeepsItUsable(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUserRepositoryWithSQL(client, integrationDB)
	u := newAdjustBalanceUser(t, repo, "adjust-negtx@test.com", 5)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)
	_, err = repo.AdjustBalance(txCtx, u.ID, service.BalanceOpSubtract, 6)
	require.Equal(t, 400, infraerrors.Code(err))
	_, err = repo.AdjustBalance(txCtx, u.ID, service.BalanceOpSubtract, 1)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.Equal(t, 4.0, storedBalance(t, u.ID))
}
