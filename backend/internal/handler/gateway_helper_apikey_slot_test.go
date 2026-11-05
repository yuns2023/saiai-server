package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// pairSlotCacheMock models the user and API key ZSETs in memory.
type pairSlotCacheMock struct {
	concurrencyCacheMock
	mu       sync.Mutex
	user     map[string]struct{}
	apiKey   map[string]struct{}
	keyMaxes []int
}

func newPairSlotCacheMock() *pairSlotCacheMock {
	return &pairSlotCacheMock{user: map[string]struct{}{}, apiKey: map[string]struct{}{}}
}

func (m *pairSlotCacheMock) AcquireUserAPIKeySlot(_ context.Context, _ int64, userMax int, _ int64, keyMax int, requestID string) (service.SlotAcquireOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keyMaxes = append(m.keyMaxes, keyMax)
	if len(m.apiKey) >= keyMax {
		return service.SlotAPIKeyFull, nil
	}
	if userMax > 0 && len(m.user) >= userMax {
		return service.SlotUserFull, nil
	}
	m.user[requestID] = struct{}{}
	m.apiKey[requestID] = struct{}{}
	return service.SlotAcquired, nil
}

func (m *pairSlotCacheMock) ReleaseUserAPIKeySlot(_ context.Context, _ int64, _ int64, requestID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.user, requestID)
	delete(m.apiKey, requestID)
	return nil
}

func (m *pairSlotCacheMock) counts() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.user), len(m.apiKey)
}

func TestTryAcquireUserSlot_APIKeyFullTakesNoUserSlot(t *testing.T) {
	cache := newPairSlotCacheMock()
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)
	slot := UserSlot{UserID: 1, Concurrency: 10, APIKeyID: 7, APIKeyConcurrency: 2}

	var releases []func()
	for i := 0; i < 2; i++ {
		release, acquired, apiKeyFull, err := helper.TryAcquireUserSlot(context.Background(), slot)
		require.NoError(t, err)
		require.True(t, acquired)
		require.False(t, apiKeyFull)
		releases = append(releases, release)
	}

	release, acquired, apiKeyFull, err := helper.TryAcquireUserSlot(context.Background(), slot)
	require.NoError(t, err)
	require.False(t, acquired)
	require.True(t, apiKeyFull)
	require.Nil(t, release)
	users, keys := cache.counts()
	require.Equal(t, 2, users)
	require.Equal(t, 2, keys)

	for _, r := range releases {
		r()
	}
	users, keys = cache.counts()
	require.Zero(t, users)
	require.Zero(t, keys)
}

func TestTryAcquireUserSlot_UserFullTakesNoKeySlot(t *testing.T) {
	cache := newPairSlotCacheMock()
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)

	_, acquired, _, err := helper.TryAcquireUserSlot(context.Background(), UserSlot{UserID: 1, Concurrency: 1, APIKeyID: 7, APIKeyConcurrency: 5})
	require.NoError(t, err)
	require.True(t, acquired)

	// Another key of the same user: the user total (1) is spent.
	_, acquired, apiKeyFull, err := helper.TryAcquireUserSlot(context.Background(), UserSlot{UserID: 1, Concurrency: 1, APIKeyID: 8, APIKeyConcurrency: 5})
	require.NoError(t, err)
	require.False(t, acquired)
	require.False(t, apiKeyFull)
	users, keys := cache.counts()
	require.Equal(t, 1, users)
	require.Equal(t, 1, keys)
}

func TestTryAcquireUserSlot_MissingKeyLimitUsesDefaultNotUnlimited(t *testing.T) {
	cache := newPairSlotCacheMock()
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)

	_, acquired, _, err := helper.TryAcquireUserSlot(context.Background(), UserSlot{UserID: 1, Concurrency: 0, APIKeyID: 7})
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, []int{service.DefaultAPIKeyMaxConcurrency}, cache.keyMaxes)
}

func TestTryAcquireUserSlot_NoAPIKeyUsesUserSlotOnly(t *testing.T) {
	cache := newPairSlotCacheMock()
	cache.acquireUserSlotFn = func(context.Context, int64, int, string) (bool, error) { return true, nil }
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)

	release, acquired, _, err := helper.TryAcquireUserSlot(context.Background(), UserSlot{UserID: 1, Concurrency: 3})
	require.NoError(t, err)
	require.True(t, acquired)
	release()
	require.Empty(t, cache.keyMaxes)
	require.Equal(t, int32(1), cache.releaseUserCalled)
}

func TestAcquireUserSlotWithWait_TimeoutNamesAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cache := newPairSlotCacheMock()
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)
	slot := UserSlot{UserID: 1, Concurrency: 10, APIKeyID: 7, APIKeyConcurrency: 1}

	_, acquired, _, err := helper.TryAcquireUserSlot(context.Background(), slot)
	require.NoError(t, err)
	require.True(t, acquired)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	streamStarted := false

	release, err := helper.AcquireUserSlotWithWait(c, slot, false, &streamStarted)
	require.Nil(t, release)
	var concurrencyErr *ConcurrencyError
	require.True(t, errors.As(err, &concurrencyErr))
	require.True(t, concurrencyErr.IsTimeout)
	require.Equal(t, slotTypeAPIKey, concurrencyErr.SlotType)
	require.Equal(t, "api key", concurrencyLimitSlotType(err, "user"))
}

func TestAcquireUserSlotWithWait_AcquiresAfterRelease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cache := newPairSlotCacheMock()
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)
	slot := UserSlot{UserID: 1, Concurrency: 10, APIKeyID: 7, APIKeyConcurrency: 1}

	first, acquired, _, err := helper.TryAcquireUserSlot(context.Background(), slot)
	require.NoError(t, err)
	require.True(t, acquired)
	time.AfterFunc(150*time.Millisecond, first)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	streamStarted := false
	release, err := helper.AcquireUserSlotWithWait(c, slot, false, &streamStarted)
	require.NoError(t, err)
	require.NotNil(t, release)
	release()
	users, keys := cache.counts()
	require.Zero(t, users)
	require.Zero(t, keys)
}
