package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type sessionInitialSerialStub struct {
	results       []service.SessionInitialSerialDecision
	err           error
	calls         int
	releases      int
	forwarded     bool
	cleanupCtxErr error
	generation    string
	owner         string
	releaseDone   chan struct{}
	onAcquire     func()
}

func (s *sessionInitialSerialStub) TryAcquireSessionInitialSerial(_ context.Context, _ int64, _ string, _ int64, generation, owner string, _ time.Duration) (*service.SessionInitialSerialResult, error) {
	s.calls++
	s.generation = generation
	s.owner = owner
	if s.err != nil {
		return nil, s.err
	}
	i := s.calls - 1
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	if s.results[i] == service.SessionInitialSerialAcquired && s.onAcquire != nil {
		s.onAcquire()
	}
	return &service.SessionInitialSerialResult{Decision: s.results[i], Generation: "generation", Ordinal: 3}, nil
}

func (s *sessionInitialSerialStub) ReleaseSessionInitialSerial(ctx context.Context, _ int64, _ string, _ int64, _, _ string, forwarded bool) error {
	s.releases++
	s.forwarded = forwarded
	s.cleanupCtxErr = ctx.Err()
	if s.releaseDone != nil {
		close(s.releaseDone)
	}
	return nil
}

func sessionInitialSerialTestContext(ctx context.Context) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
	return c
}

func TestSessionInitialSerialWaitReleasesAccountSlotAndFencesGeneration(t *testing.T) {
	stub := &sessionInitialSerialStub{results: []service.SessionInitialSerialDecision{
		service.SessionInitialSerialWait, service.SessionInitialSerialAcquired,
	}}
	h := &GatewayHandler{cfg: &config.Config{}}
	slotReleases := 0
	selection := &service.AccountSelectionResult{
		Account: &service.Account{ID: 11, Concurrency: 5}, Acquired: true,
		ReleaseFunc: func() { slotReleases++ },
	}
	started := false
	permit, err := acquireSessionInitialSerial(sessionInitialSerialTestContext(context.Background()), stub, 1, "session", 11,
		time.Now().Add(time.Second), false, &started, time.Second,
		func() { h.releaseSelectedSlotForSessionWait(selection) }, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, permit)
	require.Equal(t, 1, slotReleases)
	require.False(t, selection.Acquired)
	require.Nil(t, selection.ReleaseFunc)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, 5, selection.WaitPlan.MaxConcurrency)
	require.Equal(t, "generation", stub.generation, "retry must retain generation from the first wait")
	require.NotEmpty(t, stub.owner)
	permit.MarkForwarded()
	permit.Release()
	permit.Release()
	require.Equal(t, 1, stub.releases)
	require.True(t, stub.forwarded)
}

func TestSessionInitialSerialCancellationRefundsUnsentReservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stub := &sessionInitialSerialStub{results: []service.SessionInitialSerialDecision{service.SessionInitialSerialAcquired}}
	started := false
	permit, err := acquireSessionInitialSerial(sessionInitialSerialTestContext(ctx), stub, 1, "session", 11,
		time.Now().Add(time.Second), false, &started, time.Second, func() {}, zap.NewNop())
	require.NoError(t, err)
	// Explicit release wins once and unregisters the context callback. The
	// background cleanup context is usable after the request is canceled.
	permit.Release()
	cancel()
	permit.Release()
	require.Equal(t, 1, stub.releases)
	require.False(t, stub.forwarded)
	require.NoError(t, stub.cleanupCtxErr)
}

func TestSessionInitialSerialClientCancellationReleasesLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := &sessionInitialSerialStub{results: []service.SessionInitialSerialDecision{service.SessionInitialSerialAcquired}, releaseDone: make(chan struct{})}
	started := false
	permit, err := acquireSessionInitialSerial(sessionInitialSerialTestContext(ctx), stub, 1, "session", 11,
		time.Now().Add(time.Second), false, &started, time.Second, func() {}, zap.NewNop())
	require.NoError(t, err)
	permit.MarkForwarded()
	cancel()
	select {
	case <-stub.releaseDone:
	case <-time.After(time.Second):
		t.Fatal("client cancellation must release the lease")
	}
	permit.Release()
	require.Equal(t, 1, stub.releases)
	require.True(t, stub.forwarded)
	require.NoError(t, stub.cleanupCtxErr)
}

func TestSessionInitialSerialCancellationDuringLeaseRegistration(t *testing.T) {
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		stub := &sessionInitialSerialStub{
			results:     []service.SessionInitialSerialDecision{service.SessionInitialSerialAcquired},
			releaseDone: make(chan struct{}), onAcquire: cancel,
		}
		started := false
		permit, err := acquireSessionInitialSerial(sessionInitialSerialTestContext(ctx), stub, 1, "session", 11,
			time.Now().Add(time.Second), false, &started, time.Second, func() {}, zap.NewNop())
		require.NoError(t, err)
		select {
		case <-stub.releaseDone:
		case <-time.After(time.Second):
			t.Fatal("an already-canceled request must release its newly acquired lease")
		}
		permit.Release()
		require.Equal(t, 1, stub.releases)
		require.False(t, stub.forwarded)
		require.NoError(t, stub.cleanupCtxErr)
	}
}

func TestSessionInitialSerialWaitingStreamsOnlyKeepalives(t *testing.T) {
	stub := &sessionInitialSerialStub{results: []service.SessionInitialSerialDecision{service.SessionInitialSerialWait}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	started := false
	permit, err := acquireSessionInitialSerial(c, stub, 1, "session", 11,
		time.Now().Add(30*time.Millisecond), true, &started, 5*time.Millisecond, func() {}, zap.NewNop())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, permit)
	require.True(t, started)
	require.Equal(t, "text/event-stream", c.Writer.Header().Get("Content-Type"))
	require.Contains(t, recorder.Body.String(), string(SSEPingFormatClaude))
	require.NotContains(t, recorder.Body.String(), "message_start")
	require.Zero(t, stub.releases)
}

func TestSessionInitialSerialWaitFailures(t *testing.T) {
	t.Run("total wait timeout", func(t *testing.T) {
		stub := &sessionInitialSerialStub{results: []service.SessionInitialSerialDecision{service.SessionInitialSerialWait}}
		started, waiting := false, 0
		permit, err := acquireSessionInitialSerial(sessionInitialSerialTestContext(context.Background()), stub, 1, "session", 11,
			time.Now().Add(20*time.Millisecond), false, &started, time.Second, func() { waiting++ }, zap.NewNop())
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Nil(t, permit)
		require.Equal(t, 1, waiting)
		require.Zero(t, stub.releases)
	})
	t.Run("client cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stub := &sessionInitialSerialStub{results: []service.SessionInitialSerialDecision{service.SessionInitialSerialWait}}
		started := false
		permit, err := acquireSessionInitialSerial(sessionInitialSerialTestContext(ctx), stub, 1, "session", 11,
			time.Now().Add(time.Second), false, &started, time.Second, cancel, zap.NewNop())
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, permit)
		require.Equal(t, 1, stub.calls)
	})
	t.Run("account changed while waiting", func(t *testing.T) {
		stub := &sessionInitialSerialStub{results: []service.SessionInitialSerialDecision{
			service.SessionInitialSerialWait, service.SessionInitialSerialStale,
		}}
		started := false
		permit, err := acquireSessionInitialSerial(sessionInitialSerialTestContext(context.Background()), stub, 1, "session", 11,
			time.Now().Add(time.Second), false, &started, time.Second, func() {}, zap.NewNop())
		require.ErrorIs(t, err, errSessionInitialSerialStale)
		require.Nil(t, permit)
		require.Zero(t, stub.releases)
	})
	t.Run("redis failure", func(t *testing.T) {
		stub := &sessionInitialSerialStub{err: errors.New("redis unavailable")}
		started := false
		permit, err := acquireSessionInitialSerial(sessionInitialSerialTestContext(context.Background()), stub, 1, "session", 11,
			time.Now().Add(time.Second), false, &started, time.Second, func() {}, zap.NewNop())
		require.EqualError(t, err, "redis unavailable")
		require.Nil(t, permit)
	})
	t.Run("three finished bypasses queue", func(t *testing.T) {
		stub := &sessionInitialSerialStub{results: []service.SessionInitialSerialDecision{service.SessionInitialSerialBypass}}
		started := false
		permit, err := acquireSessionInitialSerial(sessionInitialSerialTestContext(context.Background()), stub, 1, "session", 11,
			time.Now().Add(time.Second), false, &started, time.Second, func() { t.Fatal("must not wait") }, zap.NewNop())
		require.NoError(t, err)
		require.Nil(t, permit)
	})
}

func TestUseSessionInitialSerialScope(t *testing.T) {
	h := &GatewayHandler{cfg: &config.Config{Gateway: config.GatewayConfig{
		ClaudeSessionInitialSerial: config.ClaudeSessionInitialSerialConfig{Enabled: true},
	}}}
	account := &service.Account{Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken}
	parsed := &service.ParsedRequest{MetadataUserID: `{"session_id":"session","device_id":"device","account_uuid":""}`}
	require.True(t, h.useSessionInitialSerial(true, "session", account, parsed))
	require.False(t, h.useSessionInitialSerial(false, "session", account, parsed))
	require.False(t, h.useSessionInitialSerial(true, "", account, parsed))
	require.False(t, h.useSessionInitialSerial(true, "session", account, &service.ParsedRequest{}))
	account.Type = service.AccountTypeAPIKey
	require.False(t, h.useSessionInitialSerial(true, "session", account, parsed))
	account.Type = service.AccountTypeOAuth
	require.True(t, h.useSessionInitialSerial(true, "session", account, parsed))
	h.cfg.Gateway.ClaudeSessionInitialSerial.Enabled = false
	require.False(t, h.useSessionInitialSerial(true, "session", account, parsed))
}
