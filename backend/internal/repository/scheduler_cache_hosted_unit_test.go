//go:build unit

package repository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The scheduler cache stores whole accounts as JSON: the owner pause,
// withdraw request, self-onboarded marker and identity must survive the
// round trip, or a cached copy would schedule a paused / proxy-less account.
func TestSchedulerCacheRoundTripKeepsHostedFields(t *testing.T) {
	paused := time.Date(2026, 11, 18, 1, 2, 3, 0, time.UTC)
	withdraw := paused.Add(time.Hour)
	upstream := "0b6b8f86-0d9b-4f4e-9f55-6a1f5d7c0a11"
	in := service.Account{ID: 7, Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, Status: service.StatusActive,
		Schedulable: true, SelfOnboarded: true, OwnerPausedAt: &paused, OwnerWithdrawRequestedAt: &withdraw, UpstreamAccountUUID: &upstream}
	payload, err := json.Marshal(in)
	require.NoError(t, err)
	out, err := decodeCachedAccount(string(payload))
	require.NoError(t, err)
	require.True(t, out.SelfOnboarded)
	require.NotNil(t, out.OwnerPausedAt)
	require.True(t, paused.Equal(*out.OwnerPausedAt))
	require.NotNil(t, out.OwnerWithdrawRequestedAt)
	require.True(t, withdraw.Equal(*out.OwnerWithdrawRequestedAt))
	require.Equal(t, upstream, *out.UpstreamAccountUUID)
	require.False(t, out.IsSchedulable())
}
