package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type oauthStickyAccountRepoStub struct {
	AccountRepository
	accounts map[int64]*Account

	tempUnschedCalls  int
	tempUnschedID     int64
	tempUnschedUntil  time.Time
	tempUnschedReason string
}

func (s *oauthStickyAccountRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	if s != nil && s.accounts != nil {
		if account, ok := s.accounts[id]; ok {
			return account, nil
		}
	}
	return nil, errors.New("account not found")
}

func (s *oauthStickyAccountRepoStub) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	s.tempUnschedCalls++
	s.tempUnschedID = id
	s.tempUnschedUntil = until
	s.tempUnschedReason = reason
	return nil
}

type oauthStickyGatewayCacheStub struct {
	GatewayCache

	binding        int64
	pendingBinding int64

	setCalls     int
	setAccountID int64
	setTTL       time.Duration

	refreshCalls int
	refreshTTL   time.Duration

	deleteCalls int

	pendingSetCalls    int
	pendingSetAccount  int64
	pendingSetTTL      time.Duration
	pendingDeleteCalls int
}

func (s *oauthStickyGatewayCacheStub) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if s.binding > 0 {
		return s.binding, nil
	}
	return 0, errors.New("not found")
}

func (s *oauthStickyGatewayCacheStub) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	s.setCalls++
	s.setAccountID = accountID
	s.setTTL = ttl
	s.binding = accountID
	return nil
}

func (s *oauthStickyGatewayCacheStub) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	s.refreshCalls++
	s.refreshTTL = ttl
	return nil
}

func (s *oauthStickyGatewayCacheStub) DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	s.deleteCalls++
	s.binding = 0
	return nil
}

func (s *oauthStickyGatewayCacheStub) GetPendingSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if s.pendingBinding > 0 {
		return s.pendingBinding, nil
	}
	return 0, errors.New("not found")
}

func (s *oauthStickyGatewayCacheStub) SetPendingSessionAccountIDNX(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) (bool, error) {
	if s.pendingBinding > 0 {
		return false, nil
	}
	s.pendingSetCalls++
	s.pendingSetAccount = accountID
	s.pendingSetTTL = ttl
	s.pendingBinding = accountID
	return true, nil
}

func (s *oauthStickyGatewayCacheStub) DeletePendingSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	s.pendingDeleteCalls++
	s.pendingBinding = 0
	return nil
}

func TestClaudeOAuthStickyIsSuccessOnlyAndMaxOneHour(t *testing.T) {
	ctx := context.Background()
	claudeCodeCtx := SetClaudeCodeClient(context.Background(), true)
	groupID := int64(7)
	sessionHash := "session-a"
	oauthAccount := &Account{ID: 101, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	apiKeyAccount := &Account{ID: 202, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	cache := &oauthStickyGatewayCacheStub{}
	svc := &GatewayService{
		cache: cache,
		accountRepo: &oauthStickyAccountRepoStub{accounts: map[int64]*Account{
			oauthAccount.ID:  oauthAccount,
			apiKeyAccount.ID: apiKeyAccount,
		}},
		cfg: &config.Config{Gateway: config.GatewayConfig{StickySessionTTLSeconds: 7200}},
	}

	require.NoError(t, svc.BindStickySession(ctx, &groupID, sessionHash, oauthAccount.ID))
	require.Zero(t, cache.setCalls, "OAuth sticky must not bind before upstream success")

	cache.binding = oauthAccount.ID
	svc.refreshStickySessionTTLForAccount(ctx, &groupID, sessionHash, oauthAccount)
	require.Zero(t, cache.refreshCalls, "OAuth sticky must not refresh during selection")

	require.NoError(t, svc.BindStickySessionForAccount(ctx, &groupID, sessionHash, oauthAccount))
	require.Equal(t, 1, cache.setCalls)
	require.Equal(t, oauthAccount.ID, cache.setAccountID)
	require.Equal(t, time.Hour, cache.setTTL)

	require.NoError(t, svc.ClearStickySession(ctx, &groupID, sessionHash))
	require.Equal(t, 1, cache.deleteCalls)
	require.Zero(t, cache.binding)

	require.NoError(t, svc.BindStickySession(ctx, &groupID, sessionHash, apiKeyAccount.ID))
	require.Equal(t, 2, cache.setCalls)
	require.Equal(t, apiKeyAccount.ID, cache.setAccountID)
	require.Equal(t, 2*time.Hour, cache.setTTL)

	require.NoError(t, svc.BindStickySession(claudeCodeCtx, &groupID, sessionHash, apiKeyAccount.ID))
	require.Equal(t, 2, cache.setCalls, "Claude Code Anthropic API key sticky must not bind before upstream success")

	require.NoError(t, svc.BindStickySessionForAccount(claudeCodeCtx, &groupID, sessionHash, apiKeyAccount))
	require.Equal(t, 3, cache.setCalls)
	require.Equal(t, apiKeyAccount.ID, cache.setAccountID)
	require.Equal(t, time.Hour, cache.setTTL)

	svc.refreshStickySessionTTL(ctx, &groupID, sessionHash)
	require.Equal(t, 1, cache.refreshCalls)
	require.Equal(t, 2*time.Hour, cache.refreshTTL)

	svc.refreshStickySessionTTLForAccount(claudeCodeCtx, &groupID, sessionHash, apiKeyAccount)
	require.Equal(t, 1, cache.refreshCalls, "Claude Code Anthropic API key sticky must refresh only after success")
}

func TestClaudeOAuthPendingStickyCoordinatesBeforeSuccess(t *testing.T) {
	ctx := context.Background()
	groupID := int64(7)
	sessionHash := "session-pending"
	oauthAccount := &Account{ID: 101, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	cache := &oauthStickyGatewayCacheStub{}
	svc := &GatewayService{
		cache: cache,
		accountRepo: &oauthStickyAccountRepoStub{accounts: map[int64]*Account{
			oauthAccount.ID: oauthAccount,
		}},
		cfg: &config.Config{Gateway: config.GatewayConfig{StickySessionTTLSeconds: 7200}},
	}

	svc.setStickySessionAccount(ctx, &groupID, sessionHash, oauthAccount.ID, 0)
	require.Zero(t, cache.setCalls, "pending selection must not create a confirmed sticky binding")
	require.Equal(t, 1, cache.pendingSetCalls)
	require.Equal(t, oauthAccount.ID, cache.pendingSetAccount)
	require.Equal(t, pendingStickySessionTTL, cache.pendingSetTTL)

	accountID, err := svc.GetCachedSessionAccountID(ctx, &groupID, sessionHash)
	require.NoError(t, err)
	require.Equal(t, oauthAccount.ID, accountID, "pending sticky should be visible to concurrent selection")

	require.NoError(t, svc.BindStickySessionForAccount(ctx, &groupID, sessionHash, oauthAccount))
	require.Equal(t, 1, cache.setCalls)
	require.Equal(t, oauthAccount.ID, cache.setAccountID)
	require.Equal(t, time.Hour, cache.setTTL)

	require.NoError(t, svc.ClearStickySessionForAccount(ctx, &groupID, sessionHash, oauthAccount))
	require.Equal(t, 1, cache.pendingDeleteCalls)
	require.Zero(t, cache.pendingBinding)
	require.Equal(t, oauthAccount.ID, cache.binding, "failure cleanup with pending present must not delete confirmed sticky")

	require.NoError(t, svc.ClearStickySessionForAccount(ctx, &groupID, sessionHash, oauthAccount))
	require.Equal(t, 1, cache.deleteCalls)
	require.Zero(t, cache.binding)
}

func TestAnthropicLongContextCreditsTempUnschedule(t *testing.T) {
	ctx := context.Background()
	account := &Account{ID: 303, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	repo := &oauthStickyAccountRepoStub{}
	svc := &GatewayService{accountRepo: repo}

	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests."}}`)
	require.True(t, svc.tempUnscheduleAnthropicLongContextCredits(ctx, account, http.StatusTooManyRequests, body))
	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, account.ID, repo.tempUnschedID)
	require.WithinDuration(t, time.Now().Add(anthropicLongContextCreditsCooldown), repo.tempUnschedUntil, 2*time.Second)
	require.Contains(t, repo.tempUnschedReason, "usage credits are required for long context requests")

	repo.tempUnschedCalls = 0
	require.False(t, svc.tempUnscheduleAnthropicLongContextCredits(ctx, account, http.StatusTooManyRequests, []byte(`{"error":{"message":"This request would exceed your account's rate limit."}}`)))
	require.Zero(t, repo.tempUnschedCalls)
}
