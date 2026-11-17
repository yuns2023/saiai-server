//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type wsTurnBillingUserRepoStub struct {
	service.UserRepository
	balance atomic.Value // float64
	err     error
	reads   atomic.Int32
}

func (s *wsTurnBillingUserRepoStub) GetByID(_ context.Context, id int64) (*service.User, error) {
	s.reads.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	balance, _ := s.balance.Load().(float64)
	return &service.User{ID: id, Status: service.StatusActive, Balance: balance}, nil
}

type wsTurnBillingSubRepoStub struct {
	service.UserSubscriptionRepository
	mu  sync.Mutex
	sub *service.UserSubscription
	err error
}

func (s *wsTurnBillingSubRepoStub) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*service.UserSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if s.sub == nil {
		return nil, service.ErrSubscriptionNotFound
	}
	copy := *s.sub
	return &copy, nil
}

func (s *wsTurnBillingSubRepoStub) update(f func(sub *service.UserSubscription)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s.sub)
}

// wsTurnWindowMaintenance records SubscriptionService.DoWindowMaintenance
// calls and applies the reset to the stub's stored row.
type wsTurnWindowMaintenance struct {
	mu    sync.Mutex
	calls []service.UserSubscription
}

func (m *wsTurnWindowMaintenance) install(h *OpenAIGatewayHandler, subs *wsTurnBillingSubRepoStub, block <-chan struct{}) {
	h.billingCacheService.SetSubscriptionWindowMaintenance(func(sub *service.UserSubscription) {
		if block != nil {
			<-block
		}
		subs.update(func(stored *service.UserSubscription) {
			now := time.Now()
			if sub.NeedsDailyReset() {
				stored.DailyWindowStart, stored.DailyUsageUSD = &now, 0
			}
			if sub.NeedsFiveHourReset() {
				stored.FiveHourWindowStart, stored.FiveHourUsageUSD = &now, 0
			}
		})
		// Published once the reset is applied, so tests that wait for it
		// never race the stored row.
		m.mu.Lock()
		m.calls = append(m.calls, *sub)
		m.mu.Unlock()
	})
}

func (m *wsTurnWindowMaintenance) first() service.UserSubscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[0]
}

func (m *wsTurnWindowMaintenance) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// newWSTurnBillingHandler returns a handler whose billing check reads the
// user's balance from users on every call (no Redis cache), as after a
// cache miss in production. The auth-time key snapshot keeps a positive
// balance so a check that trusted it would pass.
func newWSTurnBillingHandler(t *testing.T, group *service.Group, users *wsTurnBillingUserRepoStub, subs *wsTurnBillingSubRepoStub) (*OpenAIGatewayHandler, *service.APIKey) {
	t.Helper()
	cfg := &config.Config{}
	key := &service.APIKey{
		ID: 3, Key: "sk-test", Status: service.StatusAPIKeyActive, UserID: 2,
		GroupID: &group.ID, Group: group,
		User: &service.User{ID: 2, Status: service.StatusActive, Balance: 10},
	}
	var subRepo service.UserSubscriptionRepository
	if subs != nil {
		subRepo = subs
	}
	billing := service.NewBillingCacheService(nil, users, subRepo, nil, cfg)
	t.Cleanup(billing.Stop)
	return &OpenAIGatewayHandler{
		apiKeyService:       service.NewAPIKeyService(&openAIWSRevalidationAPIKeyRepoStub{apiKey: key}, nil, nil, nil, nil, nil, cfg),
		billingCacheService: billing,
	}, key
}

func wsTurnBillingTestContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	return c
}

const wsTurnBillingPayload = `{"type":"response.create","model":"gpt-5","input":"ok"}`

// The connection opened with a positive balance; a later turn sent after it
// reached 0 must not be forwarded. Before the fix only the handshake checked
// the wallet, so turn 3 here was admitted.
func TestOpenAIWSLaterTurnRejectedAfterBalanceRunsOut(t *testing.T) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard}
	users := &wsTurnBillingUserRepoStub{}
	users.balance.Store(float64(10))
	h, key := newWSTurnBillingHandler(t, group, users, nil)

	type result struct {
		turn2, turn3 error
		closed       bool
		opsFailure   opsLocalFailure
	}
	results := make(chan result, 1)
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		conn, err := coderws.Accept(c.Writer, c.Request, nil)
		if err != nil {
			results <- result{turn2: err}
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx := c.Request.Context()
		var sessionGroup atomic.Pointer[service.Group]
		sessionGroup.Store(group)
		payload := []byte(wsTurnBillingPayload)
		turn2 := h.admitOpenAIWSLaterTurn(ctx, c, conn, key, &sessionGroup, nil, key.UserID, 2, payload)
		users.balance.Store(float64(0))
		turn3 := h.admitOpenAIWSLaterTurn(ctx, c, conn, key, &sessionGroup, nil, key.UserID, 3, payload)
		// The relay wraps hook errors; the handler's close path must still
		// recognize the rejection.
		closed := closeOpenAIWSRejectedSession(c, conn, fmt.Errorf("relay: %w", turn3))
		raw, _ := c.Get(opsLocalFailureKey)
		failure, _ := raw.(opsLocalFailure)
		results <- result{turn2: turn2, turn3: turn3, closed: closed, opsFailure: failure}
	})
	conn, ctx := dialTZGuardWS(t, router)

	_, frame, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "error", gjson.GetBytes(frame, "type").String(), string(frame))
	require.Equal(t, int64(http.StatusForbidden), gjson.GetBytes(frame, "status").Int())
	require.Equal(t, "billing_error", gjson.GetBytes(frame, "error.code").String())
	require.Equal(t, "insufficient balance", gjson.GetBytes(frame, "error.message").String())
	_, _, err = conn.Read(ctx)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))

	got := <-results
	require.NoError(t, got.turn2)
	require.Error(t, got.turn3, "a later turn after the balance reached 0 must be rejected")
	var closeErr *service.OpenAIWSClientCloseError
	require.ErrorAs(t, got.turn3, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	require.True(t, got.closed)
	require.Equal(t, http.StatusForbidden, got.opsFailure.status)
	require.Equal(t, "billing_error", got.opsFailure.code)
	require.Equal(t, int(coderws.StatusPolicyViolation), got.opsFailure.wsCloseCode)
	require.GreaterOrEqual(t, users.reads.Load(), int32(2), "each later turn must read the balance afresh")
}

func TestOpenAIWSLaterTurnBillingCases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	daily := 5.0
	future := time.Now().Add(24 * time.Hour)
	hostedGroup := func(rate float64) *service.Group {
		return &service.Group{
			ID: 9, Platform: service.PlatformOpenAI, Status: service.StatusActive,
			SubscriptionType: service.SubscriptionTypeStandard, IsExclusive: true,
			HostedUsageEnabled: true, RateMultiplier: rate,
		}
	}
	subGroup := &service.Group{
		ID: 11, Platform: service.PlatformOpenAI, Status: service.StatusActive,
		SubscriptionType: service.SubscriptionTypeSubscription, DailyLimitUSD: &daily,
	}
	today := time.Now().Add(-time.Hour)
	yesterday := time.Now().Add(-25 * time.Hour)
	activeSub := func(dailyUsage float64) *service.UserSubscription {
		return &service.UserSubscription{ID: 21, UserID: 2, GroupID: 11, Status: service.SubscriptionStatusActive, ExpiresAt: future, DailyUsageUSD: dailyUsage, DailyWindowStart: &today}
	}
	endedWindowSub := activeSub(6)
	endedWindowSub.DailyWindowStart = &yesterday
	cases := []struct {
		name     string
		group    *service.Group
		balance  float64
		repoErr  error
		sub      *service.UserSubscription
		subRepo  *wsTurnBillingSubRepoStub
		rejected bool
	}{
		{name: "balance positive", group: &service.Group{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive}, balance: 1},
		{name: "balance zero", group: &service.Group{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive}, balance: 0, rejected: true},
		{name: "free hosted exempt", group: hostedGroup(0), balance: 0},
		{name: "charged hosted needs wallet", group: hostedGroup(0.1), balance: 0, rejected: true},
		{name: "subscription within limit ignores wallet", group: subGroup, balance: 0, sub: activeSub(1)},
		{name: "subscription over daily limit", group: subGroup, balance: 100, sub: activeSub(6), rejected: true},
		// The session crossed the daily boundary: yesterday's exhausted
		// usage is still stored, but the window has ended.
		{name: "subscription ended window is not a rejection", group: subGroup, sub: activeSub(6), subRepo: &wsTurnBillingSubRepoStub{sub: endedWindowSub}},
		{name: "subscription revoked mid-session", group: subGroup, balance: 100, sub: activeSub(0), subRepo: &wsTurnBillingSubRepoStub{}, rejected: true},
		{name: "subscription lookup failure fails open", group: subGroup, sub: activeSub(0), subRepo: &wsTurnBillingSubRepoStub{err: errors.New("TEST_ONLY_DB_DOWN")}},
		{name: "balance lookup failure fails open", group: &service.Group{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive}, repoErr: errors.New("TEST_ONLY_DB_DOWN")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users := &wsTurnBillingUserRepoStub{err: tc.repoErr}
			users.balance.Store(tc.balance)
			subs := tc.subRepo
			if subs == nil && tc.sub != nil {
				subs = &wsTurnBillingSubRepoStub{sub: tc.sub}
			}
			h, key := newWSTurnBillingHandler(t, tc.group, users, subs)
			c := wsTurnBillingTestContext()
			var sessionGroup atomic.Pointer[service.Group]
			sessionGroup.Store(tc.group)
			err := h.admitOpenAIWSLaterTurn(c.Request.Context(), c, nil, key, &sessionGroup, tc.sub, key.UserID, 2, []byte(wsTurnBillingPayload))
			if !tc.rejected {
				require.NoError(t, err)
				return
			}
			var rejection *openAIWSSessionRejection
			require.ErrorAs(t, err, &rejection)
			require.Equal(t, http.StatusForbidden, rejection.status)
			require.Equal(t, "billing_error", rejection.code)
		})
	}
}

// Turn 1 is checked at the handshake; the per-turn check only runs for
// later turns, and failover replays (checkOpenAIWSSessionPayload alone) do
// not re-check billing for an already admitted turn.
func TestOpenAIWSFailoverReplaySkipsBillingCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive}
	users := &wsTurnBillingUserRepoStub{}
	users.balance.Store(float64(0))
	h, key := newWSTurnBillingHandler(t, group, users, nil)
	c := wsTurnBillingTestContext()
	var sessionGroup atomic.Pointer[service.Group]
	sessionGroup.Store(group)
	fresh, err := h.checkOpenAIWSSessionPayload(c.Request.Context(), c, nil, key, &sessionGroup, key.UserID, 2, []byte(wsTurnBillingPayload))
	require.NoError(t, err)
	require.NotNil(t, fresh)
	require.Zero(t, users.reads.Load())
}

// Turns are settled with the connect-time group. A later turn whose group
// switched billing mode in place (same group ID) must reconnect instead of
// being admitted under rules that differ from its settlement.
func TestOpenAIWSLaterTurnBillingModeChangeRequiresReconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	connectGroup := &service.Group{
		ID: 9, Platform: service.PlatformOpenAI, Status: service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard, IsExclusive: true,
		HostedUsageEnabled: true, RateMultiplier: 0.1,
	}
	freeNow := *connectGroup
	freeNow.RateMultiplier = 0
	users := &wsTurnBillingUserRepoStub{}
	users.balance.Store(float64(0))
	h, key := newWSTurnBillingHandler(t, &freeNow, users, nil)
	connectKey := *key
	connectKey.Group = connectGroup
	c := wsTurnBillingTestContext()
	var sessionGroup atomic.Pointer[service.Group]
	sessionGroup.Store(connectGroup)
	err := h.admitOpenAIWSLaterTurn(c.Request.Context(), c, nil, &connectKey, &sessionGroup, nil, key.UserID, 2, []byte(wsTurnBillingPayload))
	var rejection *openAIWSSessionRejection
	require.ErrorAs(t, err, &rejection)
	require.Equal(t, "api_key_group_changed", rejection.code)
	require.Zero(t, users.reads.Load())
}

type wsTurnWindowFixture struct {
	h           *OpenAIGatewayHandler
	key         *service.APIKey
	group       *service.Group
	subs        *wsTurnBillingSubRepoStub
	maintenance *wsTurnWindowMaintenance
	sessionSub  service.UserSubscription
}

func newWSTurnWindowFixture(t *testing.T, group *service.Group, stored *service.UserSubscription, block <-chan struct{}) *wsTurnWindowFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	subs := &wsTurnBillingSubRepoStub{sub: stored}
	users := &wsTurnBillingUserRepoStub{}
	users.balance.Store(float64(0))
	h, key := newWSTurnBillingHandler(t, group, users, subs)
	maintenance := &wsTurnWindowMaintenance{}
	maintenance.install(h, subs, block)
	return &wsTurnWindowFixture{h: h, key: key, group: group, subs: subs, maintenance: maintenance, sessionSub: *stored}
}

func (f *wsTurnWindowFixture) admit(turn int) error {
	c := wsTurnBillingTestContext()
	var sessionGroup atomic.Pointer[service.Group]
	sessionGroup.Store(f.group)
	return f.h.admitOpenAIWSLaterTurn(c.Request.Context(), c, nil, f.key, &sessionGroup, &f.sessionSub, f.key.UserID, turn, []byte(wsTurnBillingPayload))
}

func wsTurnDailyGroup() *service.Group {
	daily := 5.0
	return &service.Group{
		ID: 11, Platform: service.PlatformOpenAI, Status: service.StatusActive,
		SubscriptionType: service.SubscriptionTypeSubscription, DailyLimitUSD: &daily,
	}
}

func wsTurnStoredSub(dailyUsage float64, dailyStart time.Time) *service.UserSubscription {
	return &service.UserSubscription{
		ID: 21, UserID: 2, GroupID: 11, Status: service.SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(48 * time.Hour), DailyUsageUSD: dailyUsage, DailyWindowStart: &dailyStart,
	}
}

// A session that crosses a daily boundary with yesterday's window already
// exhausted: the turn after the boundary passes and persists the window
// reset (as the auth middleware would), so usage of the new window is
// enforced on the following turns instead of being zeroed in memory forever.
func TestOpenAIWSLaterTurnPersistsEndedSubscriptionWindow(t *testing.T) {
	f := newWSTurnWindowFixture(t, wsTurnDailyGroup(), wsTurnStoredSub(6, time.Now().Add(-25*time.Hour)), nil)

	require.NoError(t, f.admit(2))
	require.Eventually(t, func() bool { return f.maintenance.count() == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, int64(21), f.maintenance.first().ID)

	f.subs.update(func(stored *service.UserSubscription) { stored.DailyUsageUSD = 6 }) // spent in the new window
	var rejection *openAIWSSessionRejection
	require.ErrorAs(t, f.admit(3), &rejection)
	require.Equal(t, "billing_error", rejection.code)
	require.Equal(t, 1, f.maintenance.count())
}

// Crossing a boundary while the ended window is still under its limit must
// also reset the window, before new-window usage is added to the old one.
func TestOpenAIWSLaterTurnMaintainsEndedWindowUnderLimit(t *testing.T) {
	f := newWSTurnWindowFixture(t, wsTurnDailyGroup(), wsTurnStoredSub(4, time.Now().Add(-25*time.Hour)), nil)

	require.NoError(t, f.admit(2))
	require.Eventually(t, func() bool { return f.maintenance.count() == 1 }, 2*time.Second, 5*time.Millisecond)
	// Later turns within the interval do not schedule it again.
	require.NoError(t, f.admit(3))
	require.NoError(t, f.admit(4))
	require.Never(t, func() bool { return f.maintenance.count() > 1 }, 100*time.Millisecond, 10*time.Millisecond)
}

func TestOpenAIWSLaterTurnMaintainsEndedFiveHourWindow(t *testing.T) {
	fiveHour := 3.0
	group := &service.Group{
		ID: 11, Platform: service.PlatformOpenAI, Status: service.StatusActive,
		SubscriptionType: service.SubscriptionTypeSubscription, FiveHourLimitUSD: &fiveHour,
	}
	started := time.Now().Add(-6 * time.Hour)
	stored := &service.UserSubscription{
		ID: 21, UserID: 2, GroupID: 11, Status: service.SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(48 * time.Hour), FiveHourUsageUSD: 1, FiveHourWindowStart: &started,
	}
	f := newWSTurnWindowFixture(t, group, stored, nil)

	require.NoError(t, f.admit(2))
	require.Eventually(t, func() bool { return f.maintenance.count() == 1 }, 2*time.Second, 5*time.Millisecond)
	maintained := f.maintenance.first()
	require.True(t, maintained.NeedsFiveHourReset())
}

// No maintenance when the windows are current.
func TestOpenAIWSLaterTurnSkipsMaintenanceForCurrentWindows(t *testing.T) {
	f := newWSTurnWindowFixture(t, wsTurnDailyGroup(), wsTurnStoredSub(1, time.Now().Add(-time.Hour)), nil)
	require.NoError(t, f.admit(2))
	require.Never(t, func() bool { return f.maintenance.count() > 0 }, 100*time.Millisecond, 10*time.Millisecond)
}

// Window maintenance may run synchronously (no queue) with a 5 s timeout;
// the turn must not wait for it.
func TestOpenAIWSLaterTurnDoesNotBlockOnWindowMaintenance(t *testing.T) {
	block := make(chan struct{})
	f := newWSTurnWindowFixture(t, wsTurnDailyGroup(), wsTurnStoredSub(4, time.Now().Add(-25*time.Hour)), block)
	admitted := make(chan error, 1)
	go func() { admitted <- f.admit(2) }()
	select {
	case err := <-admitted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("turn admission waited for window maintenance")
	}
	close(block)
	require.Eventually(t, func() bool { return f.maintenance.count() == 1 }, 2*time.Second, 5*time.Millisecond)
}

// Concurrent sessions of one subscription schedule its maintenance once.
func TestOpenAIWSLaterTurnWindowMaintenanceDedupedAcrossSessions(t *testing.T) {
	f := newWSTurnWindowFixture(t, wsTurnDailyGroup(), wsTurnStoredSub(4, time.Now().Add(-25*time.Hour)), nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = f.admit(2)
		}()
	}
	wg.Wait()
	require.Eventually(t, func() bool { return f.maintenance.count() == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return f.maintenance.count() > 1 }, 100*time.Millisecond, 10*time.Millisecond)
}
