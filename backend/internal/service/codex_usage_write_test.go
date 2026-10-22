package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
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

func codexExhaustedExtraFormat(observedAt time.Time, layout string) map[string]any {
	extra := map[string]any{
		"codex_7d_used_percent": 100.0,
		"codex_7d_reset_at":     time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
	}
	if !observedAt.IsZero() {
		extra["codex_usage_updated_at"] = observedAt.UTC().Format(layout)
	}
	return extra
}

func codexExhaustedExtra(observedAt time.Time) map[string]any {
	return codexExhaustedExtraFormat(observedAt, time.RFC3339Nano)
}

// setCodexResetBoundary records a completed reset at a chosen instant.
func setCodexResetBoundary(t *testing.T, s *RateLimitService, id int64, boundary time.Time) {
	t.Helper()
	state, release, err := s.lockCodexUsageWrite(context.Background(), id)
	require.NoError(t, err)
	state.resetAt.Store(&boundary)
	release()
}

func TestCodexExtraRateLimitSyncRespectsResetBoundary(t *testing.T) {
	limits := &RateLimitService{}
	// The reset completed half way through a second.
	boundary := time.Now().Add(-time.Hour).Truncate(time.Second).Add(500 * time.Millisecond)
	setCodexResetBoundary(t, limits, 10, boundary)
	second := boundary.Truncate(time.Second)

	for name, tc := range map[string]struct {
		observedAt time.Time
		layout     string
		applied    bool
	}{
		"before reset":             {boundary.Add(-time.Minute), time.RFC3339Nano, false},
		"without observation time": {time.Time{}, time.RFC3339Nano, false},
		// 12:00:00.100 observed, reset at 12:00:00.500.
		"same second before reset": {second.Add(100 * time.Millisecond), time.RFC3339Nano, false},
		"same second after reset":  {second.Add(700 * time.Millisecond), time.RFC3339Nano, true},
		// A legacy whole-second value is the start of its second: a pre-reset
		// snapshot from the reset's second is never accepted, and a newer
		// snapshot from that second waits for the next observation.
		"legacy same second as reset": {second.Add(100 * time.Millisecond), time.RFC3339, false},
		"legacy later second":         {second.Add(time.Second), time.RFC3339, true},
		"after reset":                 {boundary.Add(time.Minute), time.RFC3339Nano, true},
	} {
		t.Run(name, func(t *testing.T) {
			account := Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: codexExhaustedExtraFormat(tc.observedAt, tc.layout)}
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

func TestCodexUsageExtraKeepsSubSecondObservationTime(t *testing.T) {
	observedAt := time.Date(2026, 10, 22, 12, 0, 0, 123456789, time.UTC)
	used := 100.0
	updates := buildCodexUsageExtraUpdates(&OpenAICodexUsageSnapshot{PrimaryUsedPercent: &used, UpdatedAt: observedAt.Format(time.RFC3339), ObservedAt: observedAt}, time.Now())
	require.Equal(t, "2026-10-22T12:00:00.123456789Z", updates["codex_usage_updated_at"])
	require.True(t, codexExtraObservedAt(updates).Equal(observedAt))
	// Existing readers accept the fractional form.
	parsed, err := parseTime(updates["codex_usage_updated_at"].(string))
	require.NoError(t, err)
	require.True(t, parsed.Equal(observedAt))
	at, ok := extraTime(updates, "codex_usage_updated_at")
	require.True(t, ok)
	require.True(t, at.Equal(observedAt))
	require.NotNil(t, hostedResetTime(updates["codex_usage_updated_at"]))
	// Header snapshots keep the receipt time's sub-second part.
	headers := exhaustedCodex429Headers()
	snapshot := ParseCodexRateLimitHeaders(headers)
	require.NotNil(t, snapshot)
	require.False(t, snapshot.ObservedAt.IsZero())
	require.Contains(t, buildCodexUsageExtraUpdates(snapshot, time.Now())["codex_usage_updated_at"], ".")
}

func TestCodexBusyLockObservationsAreCoalesced(t *testing.T) {
	repo := newCodexFenceRepo()
	limits := &RateLimitService{accountRepo: repo}
	_, release, err := limits.lockCodexUsageWrite(context.Background(), 13)
	require.NoError(t, err)

	before := runtime.NumGoroutine()
	base := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resetAt := base.Add(time.Duration(i+1) * time.Minute)
			queued, err := limits.writeCodexUsageFromRequest(context.Background(), 13, codexUsageWrite429Limit, base.Add(time.Duration(i)*time.Millisecond), func(ctx context.Context) error {
				return repo.SetRateLimited(ctx, 13, resetAt)
			})
			require.NoError(t, err)
			require.True(t, queued)
		}(i)
	}
	wg.Wait()
	require.LessOrEqual(t, runtime.NumGoroutine()-before, 5, "one background worker per account, not one per observation")
	limits.codexUsageWrites.queueMu.Lock()
	require.Len(t, limits.codexUsageWrites.queues, 1)
	require.LessOrEqual(t, len(limits.codexUsageWrites.queues[13].pending), 1, "one pending slot per kind")
	limits.codexUsageWrites.queueMu.Unlock()

	// At most the write the worker already took plus the latest pending one.
	release()
	limits.codexUsageWrites.pending.Wait()
	_, writes := repo.counts()
	require.GreaterOrEqual(t, writes, 1)
	require.LessOrEqual(t, writes, 2)
	repo.mu.Lock()
	require.True(t, repo.limits[len(repo.limits)-1].Equal(base.Add(200*time.Minute)), "the final write uses the latest observation")
	repo.mu.Unlock()
	limits.codexUsageWrites.queueMu.Lock()
	require.Empty(t, limits.codexUsageWrites.queues)
	limits.codexUsageWrites.queueMu.Unlock()
}

func TestCodexStaleNoReset429DoesNotEscalateAfterReset(t *testing.T) {
	repo := newCodexFenceRepo()
	limits := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 14, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	generic := []byte(`{"error":{"type":"rate_limit_exceeded"}}`)
	// These 429s were received before the reset but are handled after it
	// (for example after a slow body read). A 429 handled while the reset is
	// still running is counted and then cleared by the reset itself.
	staleAt := time.Now()
	time.Sleep(time.Millisecond)
	holdCodexFenceForReset(t, limits, account.ID)()
	for i := 0; i < openAINoReset429EscalatedAfter+2; i++ {
		limits.HandleUpstreamError(withCodexObservedAt(context.Background(), staleAt), account, http.StatusTooManyRequests, http.Header{}, generic)
	}
	repo.requireNoWrite(t, limits)
	limits.openAINoReset429Mu.Lock()
	require.Zero(t, limits.openAINoReset429[account.ID].count, "stale 429s must not count")
	limits.openAINoReset429Mu.Unlock()

	observedAt := time.Now()
	limits.HandleUpstreamError(withCodexObservedAt(context.Background(), observedAt), account, http.StatusTooManyRequests, http.Header{}, generic)
	_, writes := repo.counts()
	require.Equal(t, 1, writes)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.True(t, repo.limits[0].Equal(observedAt.Add(openAINoReset429Cooldown)), "the first post-reset 429 keeps the short backoff")
}

// The handshake 429's headers arrive before a reset; coder/websocket reads
// the delayed body after the reset. The 429 must still be stale.
func TestCodexWSHandshake429UsesHeaderReceiptTime(t *testing.T) {
	headersSent := make(chan struct{})
	releaseBody := make(chan struct{})
	headersReceived := make(chan struct{}, 1)
	openAIWSHandshakeHeadersHook = func() { headersReceived <- struct{}{} }
	t.Cleanup(func() { openAIWSHandshakeHeadersHook = nil })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range exhaustedCodex429Headers() {
			w.Header()[k] = v
		}
		w.WriteHeader(http.StatusTooManyRequests)
		w.(http.Flusher).Flush()
		close(headersSent)
		<-releaseBody
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_exceeded"}}`))
	}))
	defer server.Close()

	repo := newCodexFenceRepo()
	limits := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 15, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	dialer := newDefaultOpenAIWSClientDialer()
	type dialResult struct {
		status  int
		headers http.Header
		err     error
	}
	done := make(chan dialResult, 1)
	go func() {
		_, status, headers, err := dialer.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), http.Header{}, "")
		done <- dialResult{status, headers, err}
	}()
	<-headersSent
	<-headersReceived
	holdCodexFenceForReset(t, limits, account.ID)()
	close(releaseBody)
	result := <-done
	require.Error(t, result.err)
	require.Equal(t, http.StatusTooManyRequests, result.status)

	// Through the pool's dial error, as the forwarder sees it.
	poolErr := &openAIWSDialError{StatusCode: result.status, ResponseHeaders: result.headers, Err: result.err}
	limits.HandleUpstreamError(withOpenAIWSHandshakeObservedAt(context.Background(), poolErr), account, result.status, result.headers, nil)
	repo.requireNoWrite(t, limits)

	// Without the recorded time the same 429 would have passed the fence.
	limits.HandleUpstreamError(context.Background(), account, result.status, result.headers, nil)
	_, writes := repo.counts()
	require.Equal(t, 1, writes)
}

// Every current generic 429 counts toward escalation even when the writes are
// coalesced behind a busy fence.
func TestCodexNoReset429CountsCoalescedObservations(t *testing.T) {
	repo := newCodexFenceRepo()
	limits := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 16, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	_, release, err := limits.lockCodexUsageWrite(context.Background(), account.ID)
	require.NoError(t, err)
	generic := []byte(`{"error":{"type":"rate_limit_exceeded"}}`)
	var last time.Time
	for i := 0; i < openAINoReset429EscalatedAfter; i++ {
		last = time.Now()
		limits.HandleUpstreamError(withCodexObservedAt(context.Background(), last), account, http.StatusTooManyRequests, http.Header{}, generic)
	}
	release()
	limits.codexUsageWrites.pending.Wait()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.NotEmpty(t, repo.limits)
	require.True(t, repo.limits[len(repo.limits)-1].Equal(last.Add(openAINoReset429EscalatedFor)), "the fifth 429 escalates")
}

// A request observation never lands ahead of an older queued one.
func TestCodexRequestWriteStaysBehindQueuedObservations(t *testing.T) {
	repo := newCodexFenceRepo()
	limits := &RateLimitService{accountRepo: repo}
	_, release, err := limits.lockCodexUsageWrite(context.Background(), 17)
	require.NoError(t, err)
	older, newer := time.Now(), time.Now().Add(time.Millisecond)
	write := func(resetAt time.Time) func(context.Context) error {
		return func(ctx context.Context) error { return repo.SetRateLimited(ctx, 17, resetAt) }
	}
	queued, err := limits.writeCodexUsageFromRequest(context.Background(), 17, codexUsageWrite429Limit, older, write(older))
	require.NoError(t, err)
	require.True(t, queued)
	release()
	// The fence is free, but the older observation is still queued.
	_, err = limits.writeCodexUsageFromRequest(context.Background(), 17, codexUsageWrite429Limit, newer, write(newer))
	require.NoError(t, err)
	limits.codexUsageWrites.pending.Wait()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.True(t, repo.limits[len(repo.limits)-1].Equal(newer))
}

// A generic 429 received during a reset's usage fetch, after earlier 429s,
// must not write the escalated backoff once the reset clears the counter.
func TestCodexNoReset429BackoffIsRedecidedAfterResetClear(t *testing.T) {
	repo := newCodexFenceRepo()
	limits := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 18, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	generic := []byte(`{"error":{"type":"rate_limit_exceeded"}}`)
	for i := 0; i < openAINoReset429EscalatedAfter-1; i++ {
		limits.HandleUpstreamError(withCodexObservedAt(context.Background(), time.Now()), account, http.StatusTooManyRequests, http.Header{}, generic)
	}
	_, writes := repo.counts()
	require.Equal(t, openAINoReset429EscalatedAfter-1, writes)
	for range openAINoReset429EscalatedAfter - 1 {
		<-repo.written
	}

	// Reset in progress: boundary first, then the counter is cleared.
	state, release, err := limits.lockCodexUsageWrite(context.Background(), account.ID)
	require.NoError(t, err)
	boundary := time.Now()
	time.Sleep(time.Millisecond)
	observedAt := time.Now() // received after the boundary, during the fetch
	limits.HandleUpstreamError(withCodexObservedAt(context.Background(), observedAt), account, http.StatusTooManyRequests, http.Header{}, generic)
	state.resetAt.Store(&boundary)
	limits.clearOpenAINoReset429Through(account.ID, boundary)
	release()
	limits.codexUsageWrites.pending.Wait()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.True(t, repo.limits[len(repo.limits)-1].Equal(observedAt.Add(openAINoReset429Cooldown)), "short backoff after the reset cleared the count")
}

// Five current 429s during a reset's fetch still escalate after the reset
// drops the earlier ones, even though their writes were coalesced.
func TestCodexNoReset429KeepsCoalescedPostBoundaryCounts(t *testing.T) {
	repo := newCodexFenceRepo()
	limits := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	generic := []byte(`{"error":{"type":"rate_limit_exceeded"}}`)
	for i := 0; i < 3; i++ {
		limits.HandleUpstreamError(withCodexObservedAt(context.Background(), time.Now()), account, http.StatusTooManyRequests, http.Header{}, generic)
	}
	state, release, err := limits.lockCodexUsageWrite(context.Background(), account.ID)
	require.NoError(t, err)
	boundary := time.Now()
	var last time.Time
	for i := 0; i < openAINoReset429EscalatedAfter; i++ {
		time.Sleep(time.Millisecond)
		last = time.Now()
		limits.HandleUpstreamError(withCodexObservedAt(context.Background(), last), account, http.StatusTooManyRequests, http.Header{}, generic)
	}
	state.resetAt.Store(&boundary)
	limits.clearOpenAINoReset429Through(account.ID, boundary)
	release()
	limits.codexUsageWrites.pending.Wait()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.True(t, repo.limits[len(repo.limits)-1].Equal(last.Add(openAINoReset429EscalatedFor)))
}

// Delayed older 429s never evict newer ones from the escalation window.
func TestCodexNoReset429RecentKeepsNewestObservations(t *testing.T) {
	limits := &RateLimitService{}
	base := time.Now()
	for i := 0; i < openAINoReset429EscalatedAfter; i++ {
		limits.recordOpenAINoReset429(20, base.Add(time.Duration(i+100)*time.Millisecond))
	}
	for i := 0; i < 2*openAINoReset429RecentLimit; i++ {
		limits.recordOpenAINoReset429(20, base.Add(time.Duration(i)*time.Microsecond))
	}
	limits.clearOpenAINoReset429Through(20, base.Add(50*time.Millisecond))
	require.Equal(t, openAINoReset429EscalatedFor, limits.openAINoReset429CooldownAt(20, base.Add(104*time.Millisecond)))
}
