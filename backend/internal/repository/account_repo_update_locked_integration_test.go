//go:build integration

package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newLockedTestAccount(t *testing.T, name string) (*accountRepository, *service.Account) {
	t.Helper()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	acc := mustCreateAccount(t, client, &service.Account{
		Name:        name,
		Credentials: map[string]any{"access_token": "t"},
		Extra:       map[string]any{"passive_usage_7d_utilization": 10.0},
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", acc.ID)
	})
	return repo, acc
}

// The admin edit used to read early and write the whole row late; state
// committed in between was lost. UpdateLocked applies to the current row.
func TestUpdateLocked_KeepsStateCommittedBeforeTheLock(t *testing.T) {
	ctx := context.Background()
	repo, acc := newLockedTestAccount(t, "locked-before")

	resetAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, repo.SetRateLimited(ctx, acc.ID, resetAt))
	require.NoError(t, repo.UpdateExtra(ctx, acc.ID, map[string]any{"passive_usage_7d_utilization": 55.0}))

	_, err := repo.UpdateLocked(ctx, acc.ID, func(a *service.Account) error {
		a.Name = "renamed"
		return nil
	})
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, acc.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Name)
	require.NotNil(t, got.RateLimitResetAt)
	require.True(t, got.RateLimitResetAt.Equal(resetAt))
	require.Equal(t, 55.0, got.Extra["passive_usage_7d_utilization"])
}

// A runtime write arriving while the edit holds the lock waits for the commit
// and is applied on top of it.
func TestUpdateLocked_ConcurrentWriterWaitsAndIsKept(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo, acc := newLockedTestAccount(t, "locked-concurrent")

	inside := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseEdit := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseEdit() // never leave the row locked on a failed assertion

	editDone := make(chan error, 1)
	go func() {
		_, err := repo.UpdateLocked(ctx, acc.ID, func(a *service.Account) error {
			close(inside)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			a.Name = "edited"
			return nil
		})
		editDone <- err
	}()
	select {
	case <-inside:
	case err := <-editDone:
		t.Fatalf("edit finished before apply ran: %v", err)
	case <-ctx.Done():
		t.Fatal("edit never reached apply")
	}

	writerDone := make(chan error, 1)
	go func() {
		writerDone <- repo.UpdateExtra(ctx, acc.ID, map[string]any{"passive_usage_7d_utilization": 77.0})
	}()
	waitForExtraWriterBlocked(ctx, t)
	select {
	case err := <-writerDone:
		t.Fatalf("runtime write did not wait for the edit's lock: %v", err)
	default:
	}
	releaseEdit()
	require.NoError(t, <-editDone)
	require.NoError(t, <-writerDone)

	got, err := repo.GetByID(context.Background(), acc.ID)
	require.NoError(t, err)
	require.Equal(t, "edited", got.Name)
	require.Equal(t, 77.0, got.Extra["passive_usage_7d_utilization"])
}

// Associations loaded and rewritten inside the transaction are preserved.
func TestUpdateLocked_KeepsProxyAndGroups(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo, acc := newLockedTestAccount(t, "locked-assoc")
	proxy := mustCreateProxy(t, client, &service.Proxy{Name: "locked-assoc-proxy"})
	group := mustCreateGroup(t, client, &service.Group{Name: "locked-assoc-group"})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id = $1", acc.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id = $1", group.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM proxies WHERE id = $1", proxy.ID)
	})
	_, err := repo.UpdateLocked(ctx, acc.ID, func(a *service.Account) error {
		a.ProxyID = &proxy.ID
		return nil
	})
	require.NoError(t, err)
	mustBindAccountToGroup(t, client, acc.ID, group.ID, 3)

	_, err = repo.UpdateLocked(ctx, acc.ID, func(a *service.Account) error {
		// The locked row is loaded with its associations through the tx.
		require.NotNil(t, a.Proxy)
		require.Equal(t, proxy.ID, a.Proxy.ID)
		require.Equal(t, []int64{group.ID}, a.GroupIDs)
		require.Len(t, a.AccountGroups, 1)
		require.Equal(t, 3, a.AccountGroups[0].Priority)
		a.Name = "assoc-renamed"
		return nil
	})
	require.NoError(t, err)
	got, err := repo.GetByID(ctx, acc.ID)
	require.NoError(t, err)
	require.Equal(t, "assoc-renamed", got.Name)
	require.NotNil(t, got.ProxyID)
	require.Equal(t, proxy.ID, *got.ProxyID)
	require.Equal(t, []int64{group.ID}, got.GroupIDs)
}

// A failure after the account row was already updated (the carpool link to
// a missing user fails in a later statement) rolls the whole save back.
func TestUpdateLocked_WriteErrorRollsBack(t *testing.T) {
	ctx := context.Background()
	repo, acc := newLockedTestAccount(t, "locked-write-error")
	_, err := repo.UpdateLocked(ctx, acc.ID, func(a *service.Account) error {
		a.Name = "should-not-persist"
		a.CarpoolUserIDs = []int64{1 << 50}
		return nil
	})
	require.Error(t, err)
	got, err := repo.GetByID(ctx, acc.ID)
	require.NoError(t, err)
	require.Equal(t, "locked-write-error", got.Name, "the earlier account UPDATE was rolled back")
	require.Empty(t, got.CarpoolUserIDs)
}

func TestUpdateLocked_RejectsCallerTransaction(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo, acc := newLockedTestAccount(t, "locked-caller-tx")
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	applied := false
	_, err = repo.UpdateLocked(dbent.NewTxContext(ctx, tx), acc.ID, func(*service.Account) error { applied = true; return nil })
	require.Error(t, err)

	txRepo := newAccountRepositoryWithSQL(tx.Client(), tx.Client(), nil)
	_, err = txRepo.UpdateLocked(ctx, acc.ID, func(*service.Account) error { applied = true; return nil })
	require.Error(t, err)
	require.False(t, applied)
}

func TestUpdateLocked_ApplyErrorRollsBack(t *testing.T) {
	ctx := context.Background()
	repo, acc := newLockedTestAccount(t, "locked-rollback")
	boom := errors.New("invalid input")
	_, err := repo.UpdateLocked(ctx, acc.ID, func(a *service.Account) error {
		a.Name = "should-not-persist"
		return boom
	})
	require.ErrorIs(t, err, boom)
	got, err := repo.GetByID(ctx, acc.ID)
	require.NoError(t, err)
	require.Equal(t, "locked-rollback", got.Name)
}

func TestUpdateLocked_DeletedAccount(t *testing.T) {
	ctx := context.Background()
	repo, acc := newLockedTestAccount(t, "locked-deleted")
	require.NoError(t, repo.Delete(ctx, acc.ID))
	_, err := repo.UpdateLocked(ctx, acc.ID, func(*service.Account) error { return nil })
	require.ErrorIs(t, err, service.ErrAccountNotFound)
}

// waitForExtraWriterBlocked waits until an UpdateExtra statement is blocked
// by another session. Tests in this package run sequentially, so in practice
// that is this test's writer.
func waitForExtraWriterBlocked(ctx context.Context, t *testing.T) {
	t.Helper()
	for {
		var waiting int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND cardinality(pg_blocking_pids(pid)) > 0
			  AND query ILIKE 'UPDATE accounts SET extra = COALESCE(extra%'`).Scan(&waiting))
		if waiting > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("the runtime write never waited on the account row lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type snapshotRecorder struct {
	service.SchedulerCache
	published []string
}

func (r *snapshotRecorder) SetAccount(_ context.Context, a *service.Account) error {
	r.published = append(r.published, a.Name)
	return nil
}

// The scheduler snapshot is published only after a successful commit, with
// the committed values.
func TestUpdateLocked_PublishesSnapshotOnlyAfterCommit(t *testing.T) {
	ctx := context.Background()
	_, acc := newLockedTestAccount(t, "locked-snapshot")
	cache := &snapshotRecorder{}
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, cache)

	_, err := repo.UpdateLocked(ctx, acc.ID, func(a *service.Account) error {
		a.Name = "never"
		return errors.New("rejected")
	})
	require.Error(t, err)
	require.Empty(t, cache.published, "nothing is published for a rolled-back save")

	_, err = repo.UpdateLocked(ctx, acc.ID, func(a *service.Account) error {
		a.Name = "committed"
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"committed"}, cache.published)
}
