package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func cacheFixture() (*moderationContentCache, context.Context, config.GatewayInputModerationConfig, func() (*service.InputModerationClassification, error), *atomic.Int32) {
	c := newModerationContentCache()
	ctx := service.WithInputModerationCacheScope(context.Background(), 1, 2)
	settings := config.GatewayInputModerationConfig{Engine: "jev", Endpoint: "https://fixture.invalid", APIKey: "TEST_ONLY"}
	calls := &atomic.Int32{}
	call := func() (*service.InputModerationClassification, error) {
		calls.Add(1)
		rules, _ := service.JevRulesForPolicy(nil)
		scores := map[string]float64{}
		for category := range rules {
			scores[category] = .1
		}
		return &service.InputModerationClassification{Safety: "safe", ModelVersion: jevModerationModel, Scores: scores}, nil
	}
	return c, ctx, settings, call, calls
}
func TestModerationCacheReuseAndIsolation(t *testing.T) {
	c, ctx, settings, call, calls := cacheFixture()
	first, err := c.classify(ctx, settings, "content", nil, call)
	require.NoError(t, err)
	first.Scores["PII"] = .99
	again, err := c.classify(ctx, settings, "content", nil, call)
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.NotEqual(t, .99, again.Scores["PII"])
	for _, scoped := range []context.Context{service.WithInputModerationCacheScope(context.Background(), 3, 2), service.WithInputModerationCacheScope(context.Background(), 1, 4)} {
		_, err = c.classify(scoped, settings, "content", nil, call)
		require.NoError(t, err)
	}
	require.Equal(t, int32(3), calls.Load())
	_, err = c.classify(ctx, settings, "edited content", nil, call)
	require.NoError(t, err)
	settings.APIKey = "TEST_ONLY_REPLACEMENT"
	_, err = c.classify(ctx, settings, "content", nil, call)
	require.NoError(t, err)
	settings.Endpoint = "https://replacement.invalid"
	_, err = c.classify(ctx, settings, "content", nil, call)
	require.NoError(t, err)
	require.Equal(t, int32(6), calls.Load())
}
func TestModerationCacheTTLFailureAndCapacity(t *testing.T) {
	c, ctx, settings, call, calls := cacheFixture()
	c.capacity = 2
	now := time.Now()
	c.now = func() time.Time { return now }
	_, err := c.classify(ctx, settings, "a", nil, call)
	require.NoError(t, err)
	now = now.Add(25 * time.Hour)
	_, err = c.classify(ctx, settings, "a", nil, call)
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
	for _, text := range []string{"b", "c"} {
		now = now.Add(time.Second)
		_, err = c.classify(ctx, settings, text, nil, call)
		require.NoError(t, err)
	}
	require.Len(t, c.entries, 2)
	fail := func() (*service.InputModerationClassification, error) { return nil, errors.New("fixture failure") }
	_, err = c.classify(ctx, settings, "failed", nil, fail)
	require.Error(t, err)
	_, err = c.classify(ctx, settings, "failed", nil, call)
	require.NoError(t, err)
	require.Equal(t, int32(5), calls.Load())
}
func TestModerationCacheConcurrentCoalescing(t *testing.T) {
	c, ctx, settings, call, calls := cacheFixture()
	entered := make(chan struct{})
	release := make(chan struct{})
	slow := func() (*service.InputModerationClassification, error) { close(entered); <-release; return call() }
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := c.classify(ctx, settings, "same", nil, slow)
		require.NoError(t, err)
	}()
	<-entered
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := c.classify(cancelled, settings, "same", nil, call)
	require.ErrorIs(t, err, context.Canceled)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.classify(ctx, settings, "same", nil, call)
			require.NoError(t, err)
		}()
	}
	close(release)
	wg.Wait()
	require.Equal(t, int32(1), calls.Load())
}
func TestModerationCacheRulesAndThresholds(t *testing.T) {
	c, ctx, settings, _, calls := cacheFixture()
	policy := &service.InputModerationPolicy{RulesVersion: service.JevExpandedRulesVersion, AuditCategories: []string{"PII"}, Thresholds: map[string]int{"PII": 90}}
	call := func() (*service.InputModerationClassification, error) {
		calls.Add(1)
		return &service.InputModerationClassification{Safety: "safe", ModelVersion: jevModerationModel, Scores: map[string]float64{"PII": .7}}, nil
	}
	_, err := c.classify(ctx, settings, "same", policy, call)
	require.NoError(t, err)
	policy.Thresholds["PII"] = 60
	result, err := c.classify(ctx, settings, "same", policy, call)
	require.NoError(t, err)
	require.Equal(t, .7, result.Scores["PII"])
	require.Equal(t, int32(1), calls.Load())
	policy.RulesVersion = service.JevLegacyRulesVersion
	_, err = c.classify(ctx, settings, "same", policy, call)
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
	// Invalid/incomplete results never populate the cache.
	invalid := func() (*service.InputModerationClassification, error) {
		calls.Add(1)
		return &service.InputModerationClassification{ModelVersion: jevModerationModel}, nil
	}
	_, _ = c.classify(ctx, settings, "invalid", policy, invalid)
	_, _ = c.classify(ctx, settings, "invalid", policy, invalid)
	require.Equal(t, int32(4), calls.Load())
}

func TestModerationCacheMockTLSCalls(t *testing.T) {
	cache, ctx, settings, _, _ := cacheFixture()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Len(t, payload, 3) // No local user/group identity reaches the provider.
		require.Equal(t, "stable instructions", payload["state"])
		rules, _ := service.JevRulesForPolicy(nil)
		answers := map[string]any{}
		for category := range rules {
			answers[category] = map[string]any{"type": "noul", "noul": .1}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"model": jevModerationModel, "answers": answers}))
	}))
	defer server.Close()
	settings.Endpoint = server.URL
	cfg := &config.Config{}
	cfg.Gateway.InputModeration = settings
	detector := newJevModerationClient(cfg)
	detector.client.Transport = server.Client().Transport
	call := func() (*service.InputModerationClassification, error) {
		return detector.Classify(ctx, "stable instructions")
	}
	for i := 0; i < 3; i++ {
		_, err := cache.classify(ctx, settings, "stable instructions", nil, call)
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), calls.Load())
}
