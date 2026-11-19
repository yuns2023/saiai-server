package handler

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// buyerSlotCacheStub adds the market buyer slot set to the helper stub.
type buyerSlotCacheStub struct {
	helperConcurrencyCacheStub
	bmu           sync.Mutex
	buyerSeq      []bool
	buyerCalls    int
	buyerReleases int
	lastTotal     int
	lastBuyerMax  int
}

func (s *buyerSlotCacheStub) AcquireAccountBuyerSlot(_ context.Context, _ int64, totalMax, buyerMax int, _ string) (bool, error) {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	s.buyerCalls++
	s.lastTotal, s.lastBuyerMax = totalMax, buyerMax
	if len(s.buyerSeq) == 0 {
		return false, nil
	}
	v := s.buyerSeq[0]
	s.buyerSeq = s.buyerSeq[1:]
	return v, nil
}

func (s *buyerSlotCacheStub) ReleaseAccountBuyerSlot(context.Context, int64, string) error {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	s.buyerReleases++
	return nil
}

func (s *buyerSlotCacheStub) RenewAccountBuyerSlot(context.Context, int64, string) (bool, error) {
	return true, nil
}

func (s *buyerSlotCacheStub) GetAccountBuyerConcurrencyBatch(context.Context, []int64) (map[int64]int, error) {
	return map[int64]int{}, nil
}

func (s *buyerSlotCacheStub) AccountBuyerLeaseHeld(context.Context, int64, string) (bool, error) {
	return true, nil
}

func (s *buyerSlotCacheStub) SlotTTLSeconds() int { return 900 }

func marketWaitContext(ctx context.Context) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
	return c
}

func buyerSelection() *service.MarketSelection {
	return &service.MarketSelection{Class: service.MarketClassBuyer, AccountID: 11, GroupID: 30, Concurrency: 4, BuyerMaxConcurrency: 3}
}

func TestMarketBuyerWaitReacquiresBuyerLease(t *testing.T) {
	cache := &buyerSlotCacheStub{buyerSeq: []bool{false, false, true}}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)
	plan := &service.AccountWaitPlan{AccountID: 11, MaxConcurrency: 4, Timeout: 5 * time.Second, MaxWaiting: 3, Market: buyerSelection()}
	started := false
	release, err := helper.AcquireAccountSlotForWaitPlan(marketWaitContext(context.Background()), plan, false, &started)
	require.NoError(t, err)
	require.NotNil(t, release)
	require.Equal(t, 3, cache.buyerCalls)
	require.Zero(t, cache.accountAcquireCalls, "a buyer never waits for an owner/platform lease")
	require.Equal(t, 4, cache.lastTotal)
	require.Equal(t, 3, cache.lastBuyerMax)
	require.NotEmpty(t, plan.Market.LeaseID, "the re-acquired lease is what the final admission checks")
	release()
	require.Equal(t, 1, cache.buyerReleases)

	// A non-market plan keeps the old wait.
	plain := &buyerSlotCacheStub{helperConcurrencyCacheStub: helperConcurrencyCacheStub{accountSeq: []bool{true}}}
	helper = NewConcurrencyHelper(service.NewConcurrencyService(plain), SSEPingFormatNone, time.Second)
	release, err = helper.AcquireAccountSlotForWaitPlan(marketWaitContext(context.Background()),
		&service.AccountWaitPlan{AccountID: 11, MaxConcurrency: 4, Timeout: time.Second}, false, &started)
	require.NoError(t, err)
	release()
	require.Zero(t, plain.buyerCalls)
	require.Equal(t, 1, plain.accountAcquireCalls)
}

func TestMarketBuyerWaitCancelledLeavesNoLease(t *testing.T) {
	cache := &buyerSlotCacheStub{}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	started := false
	_, err := helper.AcquireAccountSlotForWaitPlan(marketWaitContext(ctx),
		&service.AccountWaitPlan{AccountID: 11, MaxConcurrency: 4, Timeout: 5 * time.Second, Market: buyerSelection()}, false, &started)
	require.Error(t, err)
	require.Positive(t, cache.buyerCalls)
	require.Zero(t, cache.buyerReleases)
	require.Zero(t, cache.accountAcquireCalls)
}

func TestMarketInitialSerialReleaseKeepsBuyerClass(t *testing.T) {
	h := &GatewayHandler{cfg: &config.Config{}}
	released := 0
	selection := &service.AccountSelectionResult{
		Account: &service.Account{ID: 11, Concurrency: 4}, Acquired: true,
		ReleaseFunc: func() { released++ }, Market: buyerSelection(),
	}
	h.releaseSelectedSlotForSessionWait(selection)
	require.Equal(t, 1, released)
	require.NotNil(t, selection.WaitPlan)
	require.True(t, selection.WaitPlan.Market.IsBuyer(), "the rebuilt wait plan inherits the admission class")
	require.Equal(t, 4, selection.WaitPlan.MaxConcurrency)

	cache := &buyerSlotCacheStub{buyerSeq: []bool{true}}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)
	started := false
	release, err := helper.AcquireAccountSlotForWaitPlan(marketWaitContext(context.Background()), selection.WaitPlan, false, &started)
	require.NoError(t, err)
	release()
	require.Equal(t, 1, cache.buyerCalls)
	require.Zero(t, cache.accountAcquireCalls)
}
