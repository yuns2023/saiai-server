//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexGateRepoStub struct {
	SettingRepository
	values map[string]string
	err    error
	calls  int
}

func (s *codexGateRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	s.calls++
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
	codexVersionGateCache.Store(&cachedCodexVersionGate{})
	codexVersionGateSF.Forget("codex_version_gate")
	t.Cleanup(func() {
		codexVersionGateCache.Store(&cachedCodexVersionGate{})
		codexVersionGateSF.Forget("codex_version_gate")
	})
}

func expireCodexVersionGateCache() {
	cached, _ := codexVersionGateCache.Load().(*cachedCodexVersionGate)
	next := *cached
	next.expiresAt = time.Now().Add(-time.Second).UnixNano()
	codexVersionGateCache.Store(&next)
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
	require.Equal(t, 1, repo.calls, "served from the cache")

	// A failed refresh keeps the last known good value.
	repo.err = errors.New("TEST_ONLY_DB_DOWN")
	expireCodexVersionGateCache()
	require.Equal(t, want, svc.GetCodexVersionGate(context.Background()))
	require.Equal(t, 2, repo.calls)
}

func TestGetCodexVersionGateFailsOpenWithoutKnownValue(t *testing.T) {
	resetCodexVersionGateCache(t)
	repo := &codexGateRepoStub{err: errors.New("TEST_ONLY_DB_DOWN")}
	svc := NewSettingService(repo, nil)
	require.Equal(t, CodexVersionGate{}, svc.GetCodexVersionGate(context.Background()))

	// The fail-open placeholder is not a known value: a later success is used
	// and becomes the last known good value.
	repo.err = nil
	repo.values = map[string]string{SettingKeyCodexVersionGateEnabled: "true", SettingKeyCodexMinVersion: "0.164.0"}
	expireCodexVersionGateCache()
	require.Equal(t, CodexVersionGate{Enabled: true, Min: "0.164.0"}, svc.GetCodexVersionGate(context.Background()))
	require.Equal(t, CodexVersionGate{}, (*SettingService)(nil).GetCodexVersionGate(context.Background()))
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
