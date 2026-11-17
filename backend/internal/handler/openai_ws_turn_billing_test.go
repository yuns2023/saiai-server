//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	sub *service.UserSubscription
}

func (s *wsTurnBillingSubRepoStub) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*service.UserSubscription, error) {
	if s.sub == nil {
		return nil, service.ErrSubscriptionNotFound
	}
	copy := *s.sub
	return &copy, nil
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
	activeSub := func(dailyUsage float64) *service.UserSubscription {
		return &service.UserSubscription{ID: 21, UserID: 2, GroupID: 11, Status: service.SubscriptionStatusActive, ExpiresAt: future, DailyUsageUSD: dailyUsage}
	}
	cases := []struct {
		name     string
		group    *service.Group
		balance  float64
		repoErr  error
		sub      *service.UserSubscription
		rejected bool
	}{
		{name: "balance positive", group: &service.Group{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive}, balance: 1},
		{name: "balance zero", group: &service.Group{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive}, balance: 0, rejected: true},
		{name: "free hosted exempt", group: hostedGroup(0), balance: 0},
		{name: "charged hosted needs wallet", group: hostedGroup(0.1), balance: 0, rejected: true},
		{name: "subscription within limit ignores wallet", group: subGroup, balance: 0, sub: activeSub(1)},
		{name: "subscription over daily limit", group: subGroup, balance: 100, sub: activeSub(6), rejected: true},
		{name: "balance lookup failure fails open", group: &service.Group{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive}, repoErr: errors.New("TEST_ONLY_DB_DOWN")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users := &wsTurnBillingUserRepoStub{err: tc.repoErr}
			users.balance.Store(tc.balance)
			var subs *wsTurnBillingSubRepoStub
			if tc.sub != nil {
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
