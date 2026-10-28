package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type openAISnapshotCacheStub struct {
	SchedulerCache
	snapshotAccounts []*Account
	accountsByID     map[int64]*Account
}

func (s *openAISnapshotCacheStub) GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	if len(s.snapshotAccounts) == 0 {
		return nil, false, nil
	}
	out := make([]*Account, 0, len(s.snapshotAccounts))
	for _, account := range s.snapshotAccounts {
		if account == nil {
			continue
		}
		cloned := *account
		out = append(out, &cloned)
	}
	return out, true, nil
}

func (s *openAISnapshotCacheStub) GetAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s.accountsByID == nil {
		return nil, nil
	}
	account := s.accountsByID[accountID]
	if account == nil {
		return nil, nil
	}
	cloned := *account
	return &cloned, nil
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionStickyRateLimitedAccountFallsBackToFreshCandidate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10101)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	staleSticky := &Account{ID: 31001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0}
	staleBackup := &Account{ID: 31002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	freshSticky := &Account{ID: 31001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, RateLimitResetAt: &rateLimitedUntil}
	freshBackup := &Account{ID: 31002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	cache := &stubGatewayCache{sessionBindings: map[string]int64{"openai:session_hash_rate_limited": 31001}}
	snapshotCache := &openAISnapshotCacheStub{snapshotAccounts: []*Account{staleSticky, staleBackup}, accountsByID: map[int64]*Account{31001: freshSticky, 31002: freshBackup}}
	snapshotService := &SchedulerSnapshotService{cache: snapshotCache}
	svc := &OpenAIGatewayService{accountRepo: stubOpenAIAccountRepo{accounts: []Account{*freshSticky, *freshBackup}}, cache: cache, cfg: &config.Config{}, schedulerSnapshot: snapshotService, concurrencyService: NewConcurrencyService(stubConcurrencyCache{})}

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session_hash_rate_limited", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(31002), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_FreshFiveHourGateUsesHealthyPeer(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10103)
	resetAt := time.Now().Add(2 * time.Hour)
	stalePrimary := &Account{
		ID: 31101, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
		Extra: map[string]any{"codex_5h_used_percent": 10.0, "codex_5h_reset_at": resetAt.Format(time.RFC3339)},
	}
	staleBackup := &Account{
		ID: 31102, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
		Extra: map[string]any{"codex_5h_used_percent": 10.0, "codex_5h_reset_at": resetAt.Format(time.RFC3339)},
	}
	freshPrimary := *stalePrimary
	freshPrimary.Extra = map[string]any{"codex_5h_used_percent": 95.0, "codex_5h_reset_at": resetAt.Format(time.RFC3339)}
	freshBackup := *staleBackup
	snapshotCache := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{stalePrimary, staleBackup},
		accountsByID:     map[int64]*Account{freshPrimary.ID: &freshPrimary, freshBackup.ID: &freshBackup},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{freshPrimary, freshBackup}},
		cache:              &stubGatewayCache{},
		cfg:                &config.Config{},
		schedulerSnapshot:  &SchedulerSnapshotService{cache: snapshotCache},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"fresh_5h_gate_fallback",
		"gpt-5.5",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, freshBackup.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountForModelWithExclusions_SkipsFreshlyRateLimitedSnapshotCandidate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10102)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	stalePrimary := &Account{ID: 32001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0}
	staleSecondary := &Account{ID: 32002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	freshPrimary := &Account{ID: 32001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, RateLimitResetAt: &rateLimitedUntil}
	freshSecondary := &Account{ID: 32002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}
	snapshotCache := &openAISnapshotCacheStub{snapshotAccounts: []*Account{stalePrimary, staleSecondary}, accountsByID: map[int64]*Account{32001: freshPrimary, 32002: freshSecondary}}
	snapshotService := &SchedulerSnapshotService{cache: snapshotCache}
	svc := &OpenAIGatewayService{accountRepo: stubOpenAIAccountRepo{accounts: []Account{*freshPrimary, *freshSecondary}}, cfg: &config.Config{}, schedulerSnapshot: snapshotService}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, &groupID, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(32002), account.ID)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_PreviousResponseSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(9)
	resetAt := time.Now().Add(2 * time.Hour)
	account := Account{
		ID:          1001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 2,
		Extra: map[string]any{
			"codex_5h_used_percent":                        95.0,
			"codex_5h_reset_at":                            resetAt.Format(time.RFC3339),
			"openai_oauth_responses_websockets_v2_enabled": true,
		},
	}
	cache := &stubGatewayCache{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_001", account.ID, time.Hour))

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"resp_prev_001",
		"session_hash_001",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	require.True(t, decision.StickyPreviousHit)
	require.Equal(t, account.ID, cache.sessionBindings["openai:session_hash_001"])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10)
	resetAt := time.Now().Add(2 * time.Hour)
	account := Account{
		ID:          2001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"codex_5h_used_percent": 95.0,
			"codex_5h_reset_at":     resetAt.Format(time.RFC3339),
		},
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_abc": account.ID,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_abc",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionStickyBusyKeepsSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10100)
	accounts := []Account{
		{
			ID:          21001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
		{
			ID:          21002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    9,
		},
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_sticky_busy": 21001,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true

	concurrencyCache := stubConcurrencyCache{
		acquireResults: map[int64]bool{
			21001: false, // sticky 账号已满
			21002: true,  // 若回退负载均衡会命中该账号（本测试要求不能切换）
		},
		waitCounts: map[int64]int{
			21001: 999,
		},
		loadMap: map[int64]*AccountLoadInfo{
			21001: {AccountID: 21001, LoadRate: 90, WaitingCount: 9},
			21002: {AccountID: 21002, LoadRate: 1, WaitingCount: 0},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_sticky_busy",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(21001), selection.Account.ID, "busy sticky account should remain selected")
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(21001), selection.WaitPlan.AccountID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionStickyRemovedFromGroupClearsSession(t *testing.T) {
	ctx := context.Background()
	groupID := int64(45)
	sessionHash := "scheduler-removed-from-group"
	oldAccount := Account{
		ID: 2101, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		AccountGroups: []AccountGroup{{GroupID: 99}},
	}
	currentAccount := Account{
		ID: 2102, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		AccountGroups: []AccountGroup{{GroupID: groupID}},
	}
	cache := &stubGatewayCache{sessionBindings: map[string]int64{"openai:" + sessionHash: oldAccount.ID}}
	svc := &OpenAIGatewayService{
		accountRepo:        groupFilteredOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{oldAccount, currentAccount}}},
		cache:              cache,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", sessionHash, "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, currentAccount.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickySessionHit)
	require.Equal(t, 1, cache.deletedSessions["openai:"+sessionHash])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionSticky_ForceHTTP(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1010)
	account := Account{
		ID:          2101,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"openai_ws_force_http": true,
		},
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_force_http": account.ID,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_force_http",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_RequiredWSV2_SkipsStickyHTTPAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1011)
	accounts := []Account{
		{
			ID:          2201,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
		{
			ID:          2202,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_ws_only": 2201,
		},
	}
	cfg := newOpenAIWSV2TestConfig()

	// 构造“HTTP-only 账号负载更低”的场景，验证 required transport 会强制过滤。
	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			2201: {AccountID: 2201, LoadRate: 0, WaitingCount: 0},
			2202: {AccountID: 2202, LoadRate: 90, WaitingCount: 5},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_ws_only",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportResponsesWebsocketV2,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(2202), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickySessionHit)
	require.Equal(t, 1, decision.CandidateCount)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_RequiredWSV2_NoAvailableAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1012)
	accounts := []Account{
		{
			ID:          2301,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{},
		cfg:                newOpenAIWSV2TestConfig(),
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportResponsesWebsocketV2,
	)
	require.Error(t, err)
	require.Nil(t, selection)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 0, decision.CandidateCount)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceTriesEveryCandidateByLoad(t *testing.T) {
	ctx := context.Background()
	groupID := int64(11)
	newAccount := func(id int64) Account {
		return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	}
	accounts := make([]Account, 0, 9)
	loadMap := map[int64]*AccountLoadInfo{}
	acquire := map[int64]bool{}
	for i := int64(0); i < 9; i++ {
		id := 3001 + i
		accounts = append(accounts, newAccount(id))
		loadMap[id] = &AccountLoadInfo{AccountID: id, LoadRate: int(10 * i)}
		acquire[id] = false
	}
	// Only the most loaded account (LoadRate 80, ninth in order) has a free
	// slot: no top-K cut, every candidate of the tier is tried.
	acquire[3009] = true

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2 // legacy setting, ignored
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{loadMap: loadMap, acquireResults: acquire}),
	}
	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(3009), selection.Account.ID)
	require.True(t, selection.Acquired)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 9, decision.TopK, "legacy TopK reports the tier's eligible candidates")
	require.Greater(t, decision.LoadSkew, 0.0)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	// The lowest load rate is always preferred when its slot is free.
	acquire[3001] = true
	for i := 0; i < 20; i++ {
		selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", fmt.Sprintf("session_%d", i), "gpt-5.1", nil, OpenAIUpstreamTransportAny)
		require.NoError(t, err)
		require.Equal(t, int64(3001), selection.Account.ID)
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceKeepsFullLoadRateCandidates(t *testing.T) {
	ctx := context.Background()
	groupID := int64(16)
	accounts := []Account{
		{ID: 5201, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 3, LoadFactor: intPtrForTest(1)},
		{ID: 5202, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 3},
	}
	svc := &OpenAIGatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: accounts},
		cache:       &stubGatewayCache{},
		cfg:         &config.Config{},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{
			// 5201's rate counts a waiting request against LoadFactor 1, but its
			// real slot (Concurrency 3) is free; 5202 is busy.
			loadMap:        map[int64]*AccountLoadInfo{5201: {AccountID: 5201, LoadRate: 200, WaitingCount: 1}, 5202: {AccountID: 5202, LoadRate: 30}},
			acquireResults: map[int64]bool{5201: true, 5202: false},
		}),
	}
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.Equal(t, int64(5201), selection.Account.ID)
	require.True(t, selection.Acquired)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceDoesNotSoftSortBySevenDayQuota(t *testing.T) {
	ctx := context.Background()
	groupID := int64(12)
	now := time.Now().UTC()
	accounts := []Account{
		{
			ID:          3051,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_7d_used_percent": 80.0,
				"codex_7d_reset_at":     now.Add(10 * time.Minute).Format(time.RFC3339),
			},
		},
		{
			ID:          3052,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_7d_used_percent": 60.0,
				"codex_7d_reset_at":     now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
			},
		},
	}

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			3051: {AccountID: 3051, LoadRate: 40, WaitingCount: 0},
			3052: {AccountID: 3052, LoadRate: 40, WaitingCount: 0},
		},
		acquireResults: map[int64]bool{
			3051: true,
			3052: true,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{},
		cfg:                &config.Config{Gateway: config.GatewayConfig{OpenAIWS: config.GatewayOpenAIWSConfig{LBTopK: 2}}},
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.2-codex",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Contains(t, []int64{3051, 3052}, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 2, decision.TopK, "7d remaining quota and reset time must not remove a healthy account")
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceDoesNotSoftSortBelowFiveHourGate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(100)
	now := time.Now().UTC()
	accounts := []Account{
		{
			ID:          1001,
			Name:        "quota-high",
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    1,
			Extra: map[string]any{
				"codex_5h_used_percent":   60.0,
				"codex_5h_reset_at":       now.Add(2 * time.Hour).Format(time.RFC3339),
				"codex_7d_used_percent":   5.0,
				"codex_7d_reset_at":       now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at":  now.Add(-time.Minute).Format(time.RFC3339),
				"codex_5h_window_minutes": 300.0,
				"codex_7d_window_minutes": 10080.0,
			},
		},
		{
			ID:          1002,
			Name:        "quota-low",
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    1,
			Extra: map[string]any{
				"codex_5h_used_percent":   10.0,
				"codex_5h_reset_at":       now.Add(time.Hour).Format(time.RFC3339),
				"codex_7d_used_percent":   20.0,
				"codex_7d_reset_at":       now.Add(2 * 24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at":  now.Add(-time.Minute).Format(time.RFC3339),
				"codex_5h_window_minutes": 300.0,
				"codex_7d_window_minutes": 10080.0,
			},
		},
	}

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			1001: {AccountID: 1001, LoadRate: 0, WaitingCount: 0},
			1002: {AccountID: 1002, LoadRate: 0, WaitingCount: 0},
		},
		acquireResults: map[int64]bool{
			1001: true,
			1002: true,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                &config.Config{Gateway: config.GatewayConfig{OpenAIWS: config.GatewayOpenAIWSConfig{LBTopK: 2}}},
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"00000000-0000-7000-8000-000000000003",
		"gpt-5.5",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Contains(t, []int64{1001, 1002}, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 2, decision.CandidateCount)
	require.Equal(t, 2, decision.TopK, "5h usage below the admission gate must not become a soft ranking layer")
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceRejectsNewSessionAboveFiveHourGate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(13)
	resetAt := time.Now().Add(2 * time.Hour)
	accounts := []Account{
		{
			ID:          3061,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent": 80.0001,
				"codex_5h_reset_at":     resetAt.Format(time.RFC3339),
			},
		},
		{
			ID:          3062,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent": 80.0,
				"codex_5h_reset_at":     resetAt.Format(time.RFC3339),
			},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{},
		cfg:                &config.Config{Gateway: config.GatewayConfig{OpenAIWS: config.GatewayOpenAIWSConfig{LBTopK: 2}}},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"new_session_above_5h_gate",
		"gpt-5.5",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(3062), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 1, decision.TopK)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceUsesHighestPriorityTier(t *testing.T) {
	ctx := context.Background()
	groupID := int64(16001)
	accounts := []Account{
		{
			ID:          6101,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    1,
		},
		{
			ID:          6102,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    2,
		},
	}
	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			6101: {AccountID: 6101, LoadRate: 99, WaitingCount: 9},
			6102: {AccountID: 6102, LoadRate: 0, WaitingCount: 0},
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_priority_tier",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(6101), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 2, decision.CandidateCount)
	require.Equal(t, 1, decision.TopK)
	require.Equal(t, int64(6101), cache.sessionBindings["openai:session_hash_priority_tier"])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceFallsToLowerPriorityWhenHigherTierUnavailable(t *testing.T) {
	ctx := context.Background()
	groupID := int64(16002)
	accounts := []Account{
		{
			ID:          6201,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusDisabled,
			Schedulable: true,
			Concurrency: 1,
			Priority:    1,
		},
		{
			ID:          6202,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    2,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_priority_lower_fallback",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(6202), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 1, decision.CandidateCount)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceWaitsOnBusyHigherPriorityTier(t *testing.T) {
	ctx := context.Background()
	groupID := int64(16003)
	accounts := []Account{
		{
			ID:          6301,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    1,
		},
		{
			ID:          6302,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    2,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 2
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	concurrencyCache := stubConcurrencyCache{
		acquireResults: map[int64]bool{
			6301: false,
			6302: true,
		},
		loadMap: map[int64]*AccountLoadInfo{
			6301: {AccountID: 6301, LoadRate: 100, WaitingCount: 3},
			6302: {AccountID: 6302, LoadRate: 0, WaitingCount: 0},
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_priority_wait",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(6301), selection.Account.ID)
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(6301), selection.WaitPlan.AccountID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 2, decision.CandidateCount)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionStickyKeepsBoundAccountWhenHigherPriorityAvailable(t *testing.T) {
	ctx := context.Background()
	groupID := int64(16004)
	accounts := []Account{
		{
			ID:          6401,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    1,
		},
		{
			ID:          6402,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    2,
		},
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_reclaim": 6402,
		},
	}
	concurrencyCache := stubConcurrencyCache{
		acquireResults: map[int64]bool{
			6401: true,
			6402: true,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_reclaim",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(6402), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	require.Equal(t, int64(6402), cache.sessionBindings["openai:session_hash_reclaim"])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionStickyKeepsLowerPriorityWhenHigherPriorityBusy(t *testing.T) {
	ctx := context.Background()
	groupID := int64(16005)
	accounts := []Account{
		{
			ID:          6501,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    1,
		},
		{
			ID:          6502,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    2,
		},
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_reclaim_busy": 6502,
		},
	}
	concurrencyCache := stubConcurrencyCache{
		acquireResults: map[int64]bool{
			6501: false,
			6502: true,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_reclaim_busy",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(6502), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	require.Equal(t, int64(6502), cache.sessionBindings["openai:session_hash_reclaim_busy"])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_PreviousResponseStickyDoesNotReclaimHigherPriority(t *testing.T) {
	ctx := context.Background()
	groupID := int64(16006)
	accounts := []Account{
		{
			ID:          6601,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    1,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
		{
			ID:          6602,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    2,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	cfg := newOpenAIWSV2TestConfig()
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_low_priority", 6602, time.Hour))

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"resp_prev_low_priority",
		"session_hash_previous_response_low",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(6602), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	require.True(t, decision.StickyPreviousHit)
	require.Equal(t, int64(6602), cache.sessionBindings["openai:session_hash_previous_response_low"])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_OpenAIAccountSchedulerMetrics(t *testing.T) {
	ctx := context.Background()
	groupID := int64(12)
	account := Account{
		ID:          4001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_metrics": account.ID,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session_hash_metrics", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	svc.RecordOpenAIAccountSwitch()

	snapshot := svc.SnapshotOpenAIAccountSchedulerMetrics()
	require.GreaterOrEqual(t, snapshot.SelectTotal, int64(1))
	require.GreaterOrEqual(t, snapshot.StickySessionHitTotal, int64(1))
	require.GreaterOrEqual(t, snapshot.AccountSwitchTotal, int64(1))
	require.GreaterOrEqual(t, snapshot.SchedulerLatencyMsAvg, float64(0))
	require.GreaterOrEqual(t, snapshot.StickyHitRatio, 0.0)
}

func intPtrForTest(v int) *int {
	return &v
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceDistributesAcrossSessions(t *testing.T) {
	ctx := context.Background()
	groupID := int64(15)
	accounts := []Account{
		{
			ID:          5101,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 3,
			Priority:    0,
		},
		{
			ID:          5102,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 3,
			Priority:    0,
		},
		{
			ID:          5103,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 3,
			Priority:    0,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 3
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 1

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			5101: {AccountID: 5101, LoadRate: 20, WaitingCount: 1},
			5102: {AccountID: 5102, LoadRate: 20, WaitingCount: 1},
			5103: {AccountID: 5103, LoadRate: 20, WaitingCount: 1},
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selected := make(map[int64]int, len(accounts))
	for i := 0; i < 300; i++ {
		sessionHash := fmt.Sprintf("session_hash_lb_%d", i)
		selection, decision, err := svc.SelectAccountWithScheduler(
			ctx,
			&groupID,
			"",
			sessionHash,
			"gpt-5.1",
			nil,
			OpenAIUpstreamTransportAny,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
		selected[selection.Account.ID]++
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}

	// Equal load: uniform random choice (expected 100 each; legacy score
	// weights in the config no longer influence it).
	require.Len(t, selected, len(accounts))
	for accountID, count := range selected {
		require.Greater(t, count, 60, "account %d", accountID)
	}
}

func TestCalcLoadSkewByMoments_Branches(t *testing.T) {
	require.Equal(t, 0.0, calcLoadSkewByMoments(1, 1, 1))
	// variance < 0 分支：sumSquares/count - mean^2 为负值时应钳制为 0。
	require.Equal(t, 0.0, calcLoadSkewByMoments(1, 0, 2))
	require.GreaterOrEqual(t, calcLoadSkewByMoments(6, 20, 3), 0.0)
}

func TestDefaultOpenAIAccountScheduler_ReportSwitchAndSnapshot(t *testing.T) {
	schedulerAny := newDefaultOpenAIAccountScheduler(&OpenAIGatewayService{})
	scheduler, ok := schedulerAny.(*defaultOpenAIAccountScheduler)
	require.True(t, ok)

	scheduler.ReportSwitch()
	scheduler.metrics.recordSelect(OpenAIAccountScheduleDecision{
		Layer:             openAIAccountScheduleLayerLoadBalance,
		LatencyMs:         8,
		LoadSkew:          0.5,
		StickyPreviousHit: true,
	})
	scheduler.metrics.recordSelect(OpenAIAccountScheduleDecision{
		Layer:            openAIAccountScheduleLayerSessionSticky,
		LatencyMs:        6,
		LoadSkew:         0.2,
		StickySessionHit: true,
	})

	snapshot := scheduler.SnapshotMetrics()
	require.Equal(t, int64(2), snapshot.SelectTotal)
	require.Equal(t, int64(1), snapshot.StickyPreviousHitTotal)
	require.Equal(t, int64(1), snapshot.StickySessionHitTotal)
	require.Equal(t, int64(1), snapshot.LoadBalanceSelectTotal)
	require.Equal(t, int64(1), snapshot.AccountSwitchTotal)
	require.Greater(t, snapshot.SchedulerLatencyMsAvg, 0.0)
	require.Greater(t, snapshot.StickyHitRatio, 0.0)
	require.Greater(t, snapshot.LoadSkewAvg, 0.0)
}

func TestOpenAIGatewayService_SchedulerWrappersAndDefaults(t *testing.T) {
	svc := &OpenAIGatewayService{}
	svc.RecordOpenAIAccountSwitch()
	snapshot := svc.SnapshotOpenAIAccountSchedulerMetrics()
	require.GreaterOrEqual(t, snapshot.AccountSwitchTotal, int64(1))
	require.Equal(t, openaiStickySessionTTL, svc.openAIWSSessionStickyTTL())

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 180
	svcWithCfg := &OpenAIGatewayService{cfg: cfg}
	require.Equal(t, 180*time.Second, svcWithCfg.openAIWSSessionStickyTTL())

	cfgCap := &config.Config{}
	cfgCap.Gateway.OpenAIWS.StickySessionTTLSeconds = 7200
	svcWithCfgCap := &OpenAIGatewayService{cfg: cfgCap}
	require.Equal(t, time.Hour, svcWithCfgCap.openAIWSSessionStickyTTL())
}

func TestDefaultOpenAIAccountScheduler_IsAccountTransportCompatible_Branches(t *testing.T) {
	scheduler := &defaultOpenAIAccountScheduler{}
	require.True(t, scheduler.isAccountTransportCompatible(nil, OpenAIUpstreamTransportAny))
	require.True(t, scheduler.isAccountTransportCompatible(nil, OpenAIUpstreamTransportHTTPSSE))
	require.False(t, scheduler.isAccountTransportCompatible(nil, OpenAIUpstreamTransportResponsesWebsocketV2))

	cfg := newOpenAIWSV2TestConfig()
	scheduler.service = &OpenAIGatewayService{cfg: cfg}
	account := &Account{
		ID:          8801,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	require.True(t, scheduler.isAccountTransportCompatible(account, OpenAIUpstreamTransportResponsesWebsocketV2))
}

// recordingConcurrencyCache records slot-acquisition order and can delay
// the load read or fail an acquisition.
type recordingConcurrencyCache struct {
	stubConcurrencyCache
	mu         sync.Mutex
	attempts   []int64
	loadDelay  time.Duration
	acquireErr map[int64]error
}

func (c *recordingConcurrencyCache) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	c.mu.Lock()
	c.attempts = append(c.attempts, accountID)
	c.mu.Unlock()
	if err := c.acquireErr[accountID]; err != nil {
		return false, err
	}
	return c.stubConcurrencyCache.AcquireAccountSlot(ctx, accountID, maxConcurrency, requestID)
}

func (c *recordingConcurrencyCache) GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	time.Sleep(c.loadDelay)
	return c.stubConcurrencyCache.GetAccountsLoadBatch(ctx, accounts)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceAttemptOrder(t *testing.T) {
	ctx := context.Background()
	groupID := int64(17)
	loads := map[int64]int{6001: 50, 6002: 10, 6003: 30, 6004: 40, 6005: 20, 6006: 60}
	accounts := make([]Account, 0, len(loads))
	loadMap := map[int64]*AccountLoadInfo{}
	acquire := map[int64]bool{}
	for id := int64(6001); id <= 6006; id++ {
		accounts = append(accounts, Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1})
		loadMap[id] = &AccountLoadInfo{AccountID: id, LoadRate: loads[id]}
		acquire[id] = id == 6004
	}
	cache := &recordingConcurrencyCache{stubConcurrencyCache: stubConcurrencyCache{loadMap: loadMap, acquireResults: acquire}}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{},
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(cache),
	}
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.Equal(t, int64(6004), selection.Account.ID)
	// Strictly ascending load, each account once, stop at the first success.
	require.Equal(t, []int64{6002, 6005, 6003, 6004}, cache.attempts)

	// A slot-acquisition error is returned immediately.
	cache.attempts = nil
	cache.acquireErr = map[int64]error{6002: errors.New("redis down")}
	_, _, err = svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.Error(t, err)
	require.Equal(t, []int64{6002}, cache.attempts)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_ReturnsLatency(t *testing.T) {
	ctx := context.Background()
	groupID := int64(18)
	account := Account{ID: 6101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	cache := &recordingConcurrencyCache{loadDelay: 5 * time.Millisecond}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              &stubGatewayCache{},
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(cache),
	}
	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.GreaterOrEqual(t, decision.LatencyMs, int64(5))
	snapshot := svc.SnapshotOpenAIAccountSchedulerMetrics()
	require.Equal(t, int64(1), snapshot.SelectTotal)
	require.Equal(t, decision.LatencyMs, snapshot.SchedulerLatencyMsTotal)
}
