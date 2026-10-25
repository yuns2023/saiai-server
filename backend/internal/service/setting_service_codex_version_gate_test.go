//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexGateRepoStub struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
	err    error
	block  chan struct{} // when set, reads wait for it or the context
	calls  atomic.Int32
}

func (s *codexGateRepoStub) setErr(err error) { s.mu.Lock(); s.err = err; s.mu.Unlock() }
func (s *codexGateRepoStub) setValues(values map[string]string) {
	s.mu.Lock()
	s.values = values
	s.mu.Unlock()
}

func (s *codexGateRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	s.calls.Add(1)
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func resetCodexVersionGateCache(t *testing.T) {
	t.Helper()
	reset := func() {
		codexVersionGateCache.Store(nil)
		codexVersionGateSF.Forget("codex_version_gate")
	}
	reset()
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return !codexVersionGateRefreshing.Load() }, 5*time.Second, time.Millisecond)
		reset()
	})
}

func expireCodexVersionGateCache() {
	next := *codexVersionGateCache.Load()
	next.expiresAt = time.Now().Add(-time.Second).UnixNano()
	codexVersionGateCache.Store(&next)
}

func waitCodexVersionGateRefresh(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		cached := codexVersionGateCache.Load()
		return !codexVersionGateRefreshing.Load() && cached != nil && time.Now().UnixNano() < cached.expiresAt
	}, 5*time.Second, time.Millisecond)
}

func TestGetCodexVersionGateCachesAndKeepsLastKnownGood(t *testing.T) {
	resetCodexVersionGateCache(t)
	repo := &codexGateRepoStub{values: map[string]string{
		SettingKeyCodexVersionGateEnabled: "true",
		SettingKeyCodexMinVersion:         " 0.164.0 ",
		SettingKeyCodexMaxVersion:         "0.170.0",
	}}
	svc := NewSettingService(repo, nil)
	want := CodexVersionGate{Enabled: true, Min: "0.164.0", Max: "0.170.0"}
	require.Equal(t, want, svc.GetCodexVersionGate(context.Background()))
	require.Equal(t, want, svc.GetCodexVersionGate(context.Background()))
	require.Equal(t, int32(1), repo.calls.Load(), "served from the cache")

	// A failed background refresh keeps the last known good value.
	repo.setErr(errors.New("TEST_ONLY_DB_DOWN"))
	expireCodexVersionGateCache()
	require.Equal(t, want, svc.GetCodexVersionGate(context.Background()), "stale value served")
	waitCodexVersionGateRefresh(t)
	require.Equal(t, int32(2), repo.calls.Load())
	require.Equal(t, want, svc.GetCodexVersionGate(context.Background()))
}

func TestGetCodexVersionGateFailsOpenWithoutKnownValue(t *testing.T) {
	resetCodexVersionGateCache(t)
	repo := &codexGateRepoStub{err: errors.New("TEST_ONLY_DB_DOWN")}
	svc := NewSettingService(repo, nil)
	require.Equal(t, CodexVersionGate{}, svc.GetCodexVersionGate(context.Background()))

	// The fail-open placeholder is not a known value: the next (background)
	// success is used and becomes the last known good value.
	repo.setErr(nil)
	repo.setValues(map[string]string{SettingKeyCodexVersionGateEnabled: "true", SettingKeyCodexMinVersion: "0.164.0"})
	expireCodexVersionGateCache()
	_ = svc.GetCodexVersionGate(context.Background())
	waitCodexVersionGateRefresh(t)
	require.Equal(t, CodexVersionGate{Enabled: true, Min: "0.164.0"}, svc.GetCodexVersionGate(context.Background()))
	require.Equal(t, CodexVersionGate{}, (*SettingService)(nil).GetCodexVersionGate(context.Background()))
}

// A slow store never blocks a request once a value is cached, and the first
// load is bounded.
func TestGetCodexVersionGateDoesNotBlockOnSlowStore(t *testing.T) {
	resetCodexVersionGateCache(t)
	saved := codexVersionGateFirstLoadTimeout
	codexVersionGateFirstLoadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { codexVersionGateFirstLoadTimeout = saved })
	repo := &codexGateRepoStub{block: make(chan struct{})}
	svc := NewSettingService(repo, nil)

	start := time.Now()
	require.Equal(t, CodexVersionGate{}, svc.GetCodexVersionGate(context.Background()), "first load fails open")
	require.Less(t, time.Since(start), time.Second)

	expireCodexVersionGateCache()
	start = time.Now()
	for i := 0; i < 5; i++ {
		require.Equal(t, CodexVersionGate{}, svc.GetCodexVersionGate(context.Background()))
	}
	// A blocking refresh would hold each call for the 5 s store timeout.
	require.Less(t, time.Since(start), time.Second, "refresh runs in the background")
	require.Eventually(t, func() bool { return repo.calls.Load() == 2 }, time.Second, time.Millisecond, "one background refresh")
	close(repo.block)
}

func TestSettingService_UpdateSettings_PublishesCodexVersionGate(t *testing.T) {
	resetCodexVersionGateCache(t)
	repo := &settingUpdateRepoStub{}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.UpdateSettings(context.Background(), &SystemSettings{
		CodexVersionGateEnabled: true, CodexMinVersion: "0.164.0", CodexMaxVersion: " 0.170.0-rc.1 ",
	}))
	require.Equal(t, "true", repo.updates[SettingKeyCodexVersionGateEnabled])
	require.Equal(t, "0.164.0", repo.updates[SettingKeyCodexMinVersion])
	require.Equal(t, "0.170.0-rc.1", repo.updates[SettingKeyCodexMaxVersion])
	// The new value is published at once, without a repository read.
	require.Equal(t, CodexVersionGate{Enabled: true, Min: "0.164.0", Max: "0.170.0-rc.1"}, svc.GetCodexVersionGate(context.Background()))
}

type codexGateBlockingRepo struct {
	settingUpdateRepoStub
	started chan struct{}
	release chan struct{}
}

func (s *codexGateBlockingRepo) GetMultiple(_ context.Context, _ []string) (map[string]string, error) {
	close(s.started)
	<-s.release
	return map[string]string{SettingKeyCodexVersionGateEnabled: "false"}, nil
}

// A refresh that read the old value before an admin save must not overwrite
// the saved value when it finishes.
func TestGetCodexVersionGateRefreshDoesNotOverwriteSave(t *testing.T) {
	resetCodexVersionGateCache(t)
	repo := &codexGateBlockingRepo{started: make(chan struct{}), release: make(chan struct{})}
	svc := NewSettingService(repo, &config.Config{})
	done := make(chan CodexVersionGate, 1)
	go func() { done <- svc.GetCodexVersionGate(context.Background()) }()
	<-repo.started
	require.NoError(t, svc.UpdateSettings(context.Background(), &SystemSettings{CodexVersionGateEnabled: true, CodexMinVersion: "0.164.0"}))
	close(repo.release)
	saved := CodexVersionGate{Enabled: true, Min: "0.164.0"}
	require.Equal(t, saved, <-done)
	require.Equal(t, saved, svc.GetCodexVersionGate(context.Background()))
}
