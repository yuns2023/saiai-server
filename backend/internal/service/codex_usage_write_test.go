package service

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

// codexFenceRepo records Codex usage writes; it is safe for background writers.
type codexFenceRepo struct {
	stubOpenAIAccountRepo
	mu      sync.Mutex
	extras  []map[string]any
	limits  []time.Time
	written chan struct{}
}

func newCodexFenceRepo(accounts ...Account) *codexFenceRepo {
	return &codexFenceRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: accounts}, written: make(chan struct{}, 16)}
}

func (r *codexFenceRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.mu.Lock()
	r.extras = append(r.extras, updates)
	r.mu.Unlock()
	r.written <- struct{}{}
	return nil
}

func (r *codexFenceRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.mu.Lock()
	r.limits = append(r.limits, resetAt)
	r.mu.Unlock()
	r.written <- struct{}{}
	return nil
}

func (r *codexFenceRepo) ListWithFilters(_ context.Context, params pagination.PaginationParams, _, _, _, _ string, _ int64) ([]Account, *pagination.PaginationResult, error) {
	return r.accounts, &pagination.PaginationResult{Total: int64(len(r.accounts)), Page: params.Page, PageSize: params.PageSize}, nil
}

func (r *codexFenceRepo) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.extras), len(r.limits)
}

func (r *codexFenceRepo) awaitWrite(t *testing.T) {
	t.Helper()
	select {
	case <-r.written:
	case <-time.After(2 * time.Second):
		t.Fatal("waiting for a fenced Codex usage write")
	}
}

// requireNoWrite waits for every background write queued through limits.
func (r *codexFenceRepo) requireNoWrite(t *testing.T, limits *RateLimitService) {
	t.Helper()
	limits.codexUsageWrites.pending.Wait()
	select {
	case <-r.written:
		t.Fatal("a stale Codex usage observation was written")
	default:
	}
}

func codexFenceEntries(s *RateLimitService) int {
	s.codexUsageWrites.mu.Lock()
	defer s.codexUsageWrites.mu.Unlock()
	return len(s.codexUsageWrites.accounts)
}

// holdCodexFenceForReset simulates a reset reconciliation in progress and
// returns a function that records its boundary and releases the fence.
func holdCodexFenceForReset(t *testing.T, s *RateLimitService, id int64) func() {
	t.Helper()
	state, release, err := s.lockCodexUsageWrite(context.Background(), id)
	require.NoError(t, err)
	return func() {
		boundary := time.Now()
		state.resetAt.Store(&boundary)
		release()
	}
}

func exhaustedCodex429Headers() http.Header {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "100")
	h.Set("x-codex-primary-reset-after-seconds", "7200")
	h.Set("x-codex-primary-window-minutes", "10080")
	h.Set("x-codex-secondary-used-percent", "3")
	h.Set("x-codex-secondary-reset-after-seconds", "1800")
	h.Set("x-codex-secondary-window-minutes", "300")
	return h
}

func TestCodex429DuringResetIsQueuedAndRejected(t *testing.T) {
	openAI := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for name, observe := range map[string]func(*RateLimitService){
		"codex headers": func(s *RateLimitService) {
			s.handle429(context.Background(), openAI, exhaustedCodex429Headers(), nil)
		},
		"usage_limit_reached body": func(s *RateLimitService) {
			body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + `}}`)
			s.handle429(context.Background(), openAI, http.Header{}, body)
		},
		"no reset metadata": func(s *RateLimitService) {
			s.handle429(context.Background(), openAI, http.Header{}, []byte(`{"error":{"type":"rate_limit_exceeded"}}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newCodexFenceRepo()
			s := &RateLimitService{accountRepo: repo}
			finishReset := holdCodexFenceForReset(t, s, openAI.ID)

			returned := make(chan struct{})
			go func() { observe(s); close(returned) }()
			select {
			case <-returned:
			case <-time.After(time.Second):
				t.Fatal("the request path must not wait behind a reset reconciliation")
			}
			// The 429 was received before the reset completed.
			time.Sleep(time.Millisecond)
			finishReset()
			repo.requireNoWrite(t, s)

			// A 429 observed after the reset is written on the request path.
			observe(s)
			repo.awaitWrite(t)
			_, limits := repo.counts()
			require.Equal(t, 1, limits)
		})
	}
}

func TestCodex429WithoutResetWritesSynchronously(t *testing.T) {
	repo := newCodexFenceRepo()
	s := &RateLimitService{accountRepo: repo}
	s.handle429(context.Background(), &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, exhaustedCodex429Headers(), nil)
	extras, limits := repo.counts()
	require.Equal(t, 1, extras)
	require.Equal(t, 1, limits)
	require.Zero(t, codexFenceEntries(s))
}

func TestCodexProbeSnapshotRespectsResetBoundary(t *testing.T) {
	repo := newCodexFenceRepo()
	limits := &RateLimitService{}
	svc := &AccountUsageService{accountRepo: repo}
	svc.SetRateLimitService(limits)
	resetAt := time.Now().Add(time.Hour)
	updates := map[string]any{"codex_5h_used_percent": 100.0}

	finishReset := holdCodexFenceForReset(t, limits, 9)
	svc.persistOpenAICodexProbeSnapshot(9, time.Now(), updates, &resetAt)
	time.Sleep(time.Millisecond)
	finishReset()
	repo.requireNoWrite(t, limits)

	svc.persistOpenAICodexProbeSnapshot(9, time.Now(), updates, &resetAt)
	repo.awaitWrite(t)
	repo.awaitWrite(t)
	extras, rateLimits := repo.counts()
	require.Equal(t, 1, extras)
	require.Equal(t, 1, rateLimits)
}

func codexExhaustedExtra(observedAt time.Time) map[string]any {
	extra := map[string]any{
		"codex_7d_used_percent": 100.0,
		"codex_7d_reset_at":     time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
	}
	if !observedAt.IsZero() {
		extra["codex_usage_updated_at"] = observedAt.UTC().Format(time.RFC3339)
	}
	return extra
}

func TestCodexExtraRateLimitSyncRespectsResetBoundary(t *testing.T) {
	limits := &RateLimitService{}
	finishReset := holdCodexFenceForReset(t, limits, 10)
	finishReset()
	boundary := *limits.codexUsageWrites.boundary(10)

	for name, tc := range map[string]struct {
		observedAt time.Time
		applied    bool
	}{
		"before reset":             {boundary.Add(-time.Minute), false},
		"without observation time": {time.Time{}, false},
		// One-second precision: the reconciled snapshot shares the boundary's second.
		"reconciled snapshot": {boundary, true},
		"after reset":         {boundary.Add(time.Minute), true},
	} {
		t.Run(name, func(t *testing.T) {
			account := Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: codexExhaustedExtra(tc.observedAt)}
			repo := newCodexFenceRepo(account)
			syncOpenAICodexRateLimitFromExtra(limits, repo, &account, time.Now())
			if !tc.applied {
				require.Nil(t, account.RateLimitResetAt, "a pre-reset snapshot must not affect scheduling")
				repo.requireNoWrite(t, limits)
				return
			}
			require.NotNil(t, account.RateLimitResetAt)
			repo.awaitWrite(t)
		})
	}

	// Callers in the Gateway and administrator listing share the same fence.
	stale := Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: codexExhaustedExtra(boundary.Add(-time.Minute))}
	repo := newCodexFenceRepo(stale)
	gateway := &OpenAIGatewayService{accountRepo: repo, rateLimitService: limits}
	fresh, err := gateway.getSchedulableAccount(context.Background(), stale.ID)
	require.NoError(t, err)
	require.Nil(t, fresh.RateLimitResetAt)
	admin := &adminServiceImpl{accountRepo: repo, rateLimits: limits}
	accounts, _, err := admin.ListAccounts(context.Background(), 1, 20, PlatformOpenAI, "", "", "", 0)
	require.NoError(t, err)
	require.Nil(t, accounts[0].RateLimitResetAt)
	repo.requireNoWrite(t, limits)
}

func TestCodexExtraRateLimitPersistQueuedBehindResetIsRejected(t *testing.T) {
	limits := &RateLimitService{}
	account := Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: codexExhaustedExtra(time.Now().Add(-time.Minute))}
	repo := newCodexFenceRepo(account)
	finishReset := holdCodexFenceForReset(t, limits, account.ID)
	syncOpenAICodexRateLimitFromExtra(limits, repo, &account, time.Now())
	require.NotNil(t, account.RateLimitResetAt, "no reset has completed yet")
	finishReset()
	repo.requireNoWrite(t, limits)
}

func TestCodexUsageFenceReclaimsUnusedAccountStates(t *testing.T) {
	s := &RateLimitService{}
	var inside [2]atomic.Int32
	var overlaps atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_ = s.codexUsageWrite(context.Background(), id, time.Now(), func() error {
				if inside[id].Add(1) > 1 {
					overlaps.Add(1)
				}
				time.Sleep(100 * time.Microsecond)
				inside[id].Add(-1)
				return nil
			})
		}(int64(i % 2))
	}
	wg.Wait()
	require.Zero(t, overlaps.Load(), "writes to one account must never overlap")
	require.Zero(t, codexFenceEntries(s), "unlocked states without a reset boundary are reclaimed")

	// A waiter keeps the state alive so both writers share one lock.
	_, release, err := s.lockCodexUsageWrite(context.Background(), 3)
	require.NoError(t, err)
	acquired := make(chan struct{})
	go func() {
		_, releaseWaiter, err := s.lockCodexUsageWrite(context.Background(), 3)
		require.NoError(t, err)
		close(acquired)
		releaseWaiter()
	}()
	require.Eventually(t, func() bool {
		s.codexUsageWrites.mu.Lock()
		defer s.codexUsageWrites.mu.Unlock()
		return s.codexUsageWrites.accounts[3] != nil && s.codexUsageWrites.accounts[3].refs == 2
	}, time.Second, time.Millisecond)
	_, _, ok := s.tryLockCodexUsageWrite(3)
	require.False(t, ok)
	release()
	<-acquired
	require.Eventually(t, func() bool { return codexFenceEntries(s) == 0 }, time.Second, time.Millisecond)

	// Canceled waiters release their reference.
	_, release, err = s.lockCodexUsageWrite(context.Background(), 4)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = s.lockCodexUsageWrite(ctx, 4)
	require.ErrorIs(t, err, context.Canceled)
	release()
	release() // idempotent
	require.Zero(t, codexFenceEntries(s))

	// A reset boundary is retained so later stale observations are rejected.
	holdCodexFenceForReset(t, s, 5)()
	require.Equal(t, 1, codexFenceEntries(s))
	require.ErrorIs(t, s.codexUsageWrite(context.Background(), 5, time.Now().Add(-time.Hour), func() error { return nil }), errCodexUsageSuperseded)
	require.Equal(t, 1, codexFenceEntries(s))
}

// A 429 whose headers arrived before a reset stays stale even when its body is
// read and handled after the reset completed.
func TestCodex429UsesHeaderReceiptTime(t *testing.T) {
	repo := newCodexFenceRepo()
	limits := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	ctx := withCodexObservedAt(context.Background(), time.Now())
	time.Sleep(time.Millisecond)
	holdCodexFenceForReset(t, limits, account.ID)()
	limits.HandleUpstreamError(ctx, account, http.StatusTooManyRequests, exhaustedCodex429Headers(), nil)
	repo.requireNoWrite(t, limits)

	limits.HandleUpstreamError(withCodexObservedAt(context.Background(), time.Now()), account, http.StatusTooManyRequests, exhaustedCodex429Headers(), nil)
	extras, rateLimits := repo.counts()
	require.Equal(t, 1, extras)
	require.Equal(t, 1, rateLimits)
}
