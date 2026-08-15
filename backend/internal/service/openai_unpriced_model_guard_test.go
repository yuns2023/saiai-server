//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIUnpricedModelGuard_AllowsUntilThresholdAndResets(t *testing.T) {
	now := time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC)
	guard := newOpenAIUnpricedModelGuard(&config.Config{Gateway: config.GatewayConfig{
		OpenAIUnpricedModelMaxSuccesses:  2,
		OpenAIUnpricedModelWindowSeconds: 60,
	}}, nil)
	guard.now = func() time.Time { return now }

	require.False(t, guard.isOpen(context.Background(), " GPT-NEW "))
	guard.recordSuccess(context.Background(), "gpt-new")
	require.False(t, guard.isOpen(context.Background(), "gpt-new"))
	guard.recordSuccess(context.Background(), "GPT-NEW")
	require.True(t, guard.isOpen(context.Background(), "gpt-new"))

	now = now.Add(61 * time.Second)
	require.False(t, guard.isOpen(context.Background(), "gpt-new"))
}

func TestOpenAIUnpricedModelGuard_ZeroThresholdDisablesProtection(t *testing.T) {
	guard := newOpenAIUnpricedModelGuard(&config.Config{}, nil)
	for range 10 {
		guard.recordSuccess(context.Background(), "gpt-new")
	}
	require.False(t, guard.isOpen(context.Background(), "gpt-new"))
}

func TestOpenAIUnpricedModelGuard_BoundsLocalCardinality(t *testing.T) {
	guard := newOpenAIUnpricedModelGuard(&config.Config{Gateway: config.GatewayConfig{
		OpenAIUnpricedModelMaxSuccesses:  2,
		OpenAIUnpricedModelWindowSeconds: 60,
	}}, nil)
	for i := range openAIUnpricedModelLocalMaxEntries + 50 {
		guard.incrementLocal(unpricedModelCounterKey(fmt.Sprintf("gpt-new-%d", i)))
	}

	guard.mu.Lock()
	defer guard.mu.Unlock()
	require.LessOrEqual(t, len(guard.local), openAIUnpricedModelLocalMaxEntries)
}
