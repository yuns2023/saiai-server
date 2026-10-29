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

func hostedUsageGroup() *Group {
	return &Group{ID: 3, Platform: PlatformOpenAI, IsExclusive: true,
		SubscriptionType: SubscriptionTypeStandard, HostedUsageEnabled: true}
}

func TestHostedUsageRequiresExplicitNarrowPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Group)
	}{
		{"paid", func(g *Group) { g.RateMultiplier = 1 }},
		{"public", func(g *Group) { g.IsExclusive = false }},
		{"subscription", func(g *Group) { g.SubscriptionType = SubscriptionTypeSubscription }},
		{"other platform", func(g *Group) { g.Platform = PlatformAnthropic }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := hostedUsageGroup()
			tc.change(g)
			require.False(t, g.UsesHostedUsage())
			require.Error(t, g.ValidateHostedUsage())
		})
	}
	g := hostedUsageGroup()
	require.NoError(t, g.ValidateHostedUsage())
	g.HostedUsageEnabled = false
	require.False(t, g.UsesHostedUsage(), "ordinary free groups must keep their old behavior")
	require.False(t, (*Group)(nil).UsesHostedUsage())
}

func TestHostedOpenAIRecordUsageMetersBothAccountsAndTransports(t *testing.T) {
	// Both hosted accounts can also serve a paid group. Cost isolation belongs
	// to each request, never to a global account multiplier mutation.
	for _, accountID := range []int64{11, 12} {
		for _, ws := range []bool{false, true} {
			for _, hosted := range []bool{false, true} {
				t.Run(fmt.Sprintf("account=%d/ws=%t/hosted=%t", accountID, ws, hosted), func(t *testing.T) {
					logs := &openAIRecordUsageLogRepoStub{inserted: true}
					billing := &openAIRecordUsageBillingRepoStub{}
					user := &openAIRecordUsageUserRepoStub{}
					// An old per-user paid override cannot introduce a charge
					// after an administrator explicitly declares the group hosted.
					rate := 2.0
					svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, user, nil, &openAIUserGroupRateRepoStub{rate: &rate})
					g := hostedUsageGroup()
					g.HostedUsageEnabled = hosted
					if !hosted {
						g.RateMultiplier = .5
					}
					accountRate := 1.0
					account := &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, RateMultiplier: &accountRate}
					quota := &openAIRecordUsageAPIKeyQuotaStub{}
					key := &APIKey{ID: 2, GroupID: &g.ID, Group: g, Quota: 200, RateLimit5h: 200}
					err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
						Result: &OpenAIForwardResult{RequestID: "TEST_ONLY_HOSTED", Model: "gpt-5", OpenAIWSMode: ws,
							Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 100, CacheReadInputTokens: 200}},
						APIKey: key, User: &User{ID: 1}, Account: account, APIKeyService: quota,
					})
					require.NoError(t, err)
					log := logs.lastLog
					require.Positive(t, log.TotalCost)
					if hosted {
						require.NotNil(t, log.MeteredCost)
						require.Equal(t, log.TotalCost, *log.MeteredCost)
						require.Zero(t, log.ActualCost)
						require.Zero(t, *log.AccountRateMultiplier)
						require.Zero(t, billing.lastCmd.BalanceCost)
						require.Equal(t, log.TotalCost, billing.lastCmd.APIKeyQuotaCost)
						require.Equal(t, log.TotalCost, billing.lastCmd.APIKeyRateLimitCost)
					} else {
						require.Nil(t, log.MeteredCost)
						require.Equal(t, log.TotalCost*rate, log.ActualCost)
						require.Equal(t, log.ActualCost, billing.lastCmd.BalanceCost)
						require.Equal(t, log.ActualCost, billing.lastCmd.APIKeyQuotaCost)
						require.Equal(t, 1.0, *log.AccountRateMultiplier)
					}
					require.Equal(t, 1.0, *account.RateMultiplier)
					require.Zero(t, user.deductCalls, "atomic settlement owns any wallet operation")
				})
			}
		}
	}
}

func TestHostedUsageLegacySettlementAndCacheUseSameReference(t *testing.T) {
	cache := &BillingCacheService{cache: &billingCacheWorkerStub{}, cacheWriteChan: make(chan cacheWriteTask, 4)}
	user := &openAIRecordUsageUserRepoStub{}
	quota := &openAIRecordUsageAPIKeyQuotaStub{}
	metered := 10.0
	p := &postUsageBillingParams{Cost: &CostBreakdown{TotalCost: 10}, MeteredCost: &metered,
		User: &User{ID: 1}, APIKey: &APIKey{ID: 2, Quota: 200, RateLimit5h: 200},
		Account: &Account{ID: 3, Type: AccountTypeOAuth}, APIKeyService: quota}
	postUsageBilling(context.Background(), p, &billingDeps{userRepo: user, billingCacheService: cache, deferredService: &DeferredService{}})
	require.Zero(t, user.deductCalls)
	require.Equal(t, 1, quota.quotaCalls)
	require.Equal(t, 1, quota.rateLimitCalls)
	require.Equal(t, metered, quota.lastAmount)
	require.Len(t, cache.cacheWriteChan, 1)
	task := <-cache.cacheWriteChan
	require.Equal(t, cacheWriteUpdateRateLimitUsage, task.kind)
	require.Equal(t, metered, task.amount)
}

func TestHostedUsageAccountBudgetIsIndependentOfPlatformCostSnapshot(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	billing := &openAIRecordUsageBillingRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, nil, nil)
	g := hostedUsageGroup()
	rate := 2.0
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		RateMultiplier: &rate, Extra: map[string]any{"quota_limit": 100.0}}
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: "TEST_ONLY_HOSTED_BUDGET", Model: "gpt-5", Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 100}},
		APIKey: &APIKey{ID: 2, GroupID: &g.ID, Group: g}, User: &User{ID: 1}, Account: account,
	})
	require.NoError(t, err)
	require.Positive(t, logs.lastLog.TotalCost)
	require.Zero(t, *logs.lastLog.AccountRateMultiplier)
	require.Equal(t, logs.lastLog.TotalCost*rate, billing.lastCmd.AccountQuotaCost)
}

func TestHostedUsageEligibilityNeedsNoWalletButKeepsKeyLimits(t *testing.T) {
	now := time.Now()
	loader := &hostedKeyRepo{data: &APIKeyRateLimitData{Usage5h: 200, Window5hStart: &now}}
	svc := &BillingCacheService{cfg: &config.Config{}, apiKeyRateLimitLoader: loader}
	g := hostedUsageGroup()
	require.NoError(t, svc.CheckBillingEligibility(context.Background(), &User{ID: 1}, &APIKey{ID: 2}, g, nil))
	require.ErrorIs(t, svc.CheckBillingEligibility(context.Background(), &User{ID: 1}, &APIKey{ID: 2, RateLimit5h: 200}, g, nil), ErrAPIKeyRateLimit5hExceeded)
}

type hostedKeyRepo struct {
	APIKeyRepository
	key     *APIKey
	data    *APIKeyRateLimitData
	updated bool
}

func (r *hostedKeyRepo) GetByID(context.Context, int64) (*APIKey, error) { return r.key, nil }
func (r *hostedKeyRepo) GetRateLimitData(context.Context, int64) (*APIKeyRateLimitData, error) {
	return r.data, nil
}
func (r *hostedKeyRepo) ResetRateLimitWindows(context.Context, int64) error { return nil }
func (r *hostedKeyRepo) Update(context.Context, *APIKey) error              { r.updated = true; return nil }

type hostedUserRepo struct {
	UserRepository
	user *User
}

func (r *hostedUserRepo) GetByID(context.Context, int64) (*User, error)               { return r.user, nil }
func (r *hostedUserRepo) AddGroupToAllowedGroups(context.Context, int64, int64) error { return nil }

// Keys move freely between hosted and billed groups. Outstanding quota and
// window usage is kept as recorded; only later requests use the new basis.
func TestHostedUsageGroupSwitchKeepsOutstandingCounters(t *testing.T) {
	hosted := hostedUsageGroup()
	paid := &Group{ID: 4, Platform: PlatformOpenAI, RateMultiplier: 1}
	start := time.Now()
	for _, tc := range []struct {
		name     string
		from, to *Group
	}{
		{"hosted to paid", hosted, paid},
		{"paid to hosted", paid, hosted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := &APIKey{ID: 2, UserID: 1, GroupID: &tc.from.ID, Group: tc.from, QuotaUsed: 5,
				Usage5h: 3, Usage1d: 4, Usage7d: 5, Window5hStart: &start, Window1dStart: &start, Window7dStart: &start}
			repo := &hostedKeyRepo{key: key, data: &APIKeyRateLimitData{Usage5h: 3, Window5hStart: &start}}
			svc := &APIKeyService{apiKeyRepo: repo, groupRepo: &groupRepoStubForAdmin{getByID: tc.to}, userRepo: &hostedUserRepo{user: &User{ID: 1, AllowedGroups: []int64{hosted.ID}}}}
			got, err := svc.Update(context.Background(), 2, 1, UpdateAPIKeyRequest{GroupID: &tc.to.ID})
			require.NoError(t, err)
			require.True(t, repo.updated)
			require.Equal(t, tc.to.ID, *got.GroupID)
			require.Equal(t, 5.0, got.QuotaUsed)
			require.Equal(t, []float64{3, 4, 5}, []float64{got.Usage5h, got.Usage1d, got.Usage7d})
			require.Equal(t, []*time.Time{&start, &start, &start}, []*time.Time{got.Window5hStart, got.Window1dStart, got.Window7dStart})
		})
	}
}

func TestHostedUsageAdminRebindingAndUnbindingKeepCounters(t *testing.T) {
	for _, targetID := range []int64{0, 4} {
		t.Run(fmt.Sprintf("target=%d", targetID), func(t *testing.T) {
			g := hostedUsageGroup()
			repo := &hostedKeyRepo{key: &APIKey{ID: 2, GroupID: &g.ID, Group: g, QuotaUsed: 1}}
			svc := &adminServiceImpl{apiKeyRepo: repo, groupRepo: &groupRepoStubForAdmin{getByID: &Group{ID: 4, Status: StatusActive}}}
			got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 2, &targetID)
			require.NoError(t, err)
			require.True(t, repo.updated)
			require.Equal(t, 1.0, got.APIKey.QuotaUsed)
			if targetID == 0 {
				require.Nil(t, got.APIKey.GroupID)
			} else {
				require.Equal(t, targetID, *got.APIKey.GroupID)
			}
		})
	}
	g := hostedUsageGroup()
	g.Status = StatusActive
	repo := &hostedKeyRepo{key: &APIKey{ID: 2, QuotaUsed: 1}}
	svc := &adminServiceImpl{apiKeyRepo: repo, groupRepo: &groupRepoStubForAdmin{getByID: g}, userRepo: &hostedUserRepo{}}
	_, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 2, &g.ID)
	require.NoError(t, err, "binding an unbound Key to a hosted group")
	require.True(t, repo.updated)
}

type hostedGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *hostedGroupRepo) GetByID(_ context.Context, id int64) (*Group, error) {
	return r.groups[id], nil
}
func (r *hostedGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	return r.groups[id], nil
}

func TestHostedUsageBulkReplacementPassesValidationAcrossMeteringBases(t *testing.T) {
	hosted, paid := hostedUsageGroup(), &Group{ID: 4, IsExclusive: true, Status: StatusActive}
	hosted.Status = StatusActive
	svc := &adminServiceImpl{groupRepo: &hostedGroupRepo{groups: map[int64]*Group{hosted.ID: hosted, paid.ID: paid}}}
	for _, pair := range [][2]int64{{hosted.ID, paid.ID}, {paid.ID, hosted.ID}} {
		_, err := svc.ReplaceUserGroup(context.Background(), 1, pair[0], pair[1])
		require.ErrorContains(t, err, "entClient is nil", "no metering-basis rejection before the transaction")
	}
}

func TestHostedUsageAdminPolicyPersistsAndRejectsPartialPaidChange(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}
	g, err := svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "hosted", Platform: PlatformOpenAI, IsExclusive: true, HostedUsageEnabled: true})
	require.NoError(t, err)
	require.True(t, repo.created.UsesHostedUsage())
	repo.getByID = g
	_, err = svc.UpdateGroup(context.Background(), g.ID, &UpdateGroupInput{Name: "renamed"})
	require.NoError(t, err)
	require.True(t, repo.updated.UsesHostedUsage())
	repo.updated = nil
	rate := 1.0
	_, err = svc.UpdateGroup(context.Background(), g.ID, &UpdateGroupInput{RateMultiplier: &rate})
	require.Error(t, err)
	require.Nil(t, repo.updated, "disable hosted policy explicitly before restoring paid behavior")
}

func TestHostedUsageAuthCacheRoundTrip(t *testing.T) {
	svc := &APIKeyService{}
	g := hostedUsageGroup()
	snapshot := svc.snapshotFromAPIKey(&APIKey{ID: 2, User: &User{ID: 1}, Group: g})
	restored := svc.snapshotToAPIKey("TEST_ONLY_HOSTED", snapshot)
	require.True(t, restored.Group.UsesHostedUsage())
	snapshot.Group.HostedUsageEnabled = false
	require.False(t, svc.snapshotToAPIKey("TEST_ONLY_HOSTED", snapshot).Group.UsesHostedUsage())
}

func TestHostedNativeChatMetersWithoutCharging(t *testing.T) {
	logs := &chatGPTFixedUsageLogRepo{}
	billing := &chatGPTFixedBillingRepo{}
	cache := NewBillingCacheService(nil, nil, nil, nil, &config.Config{})
	t.Cleanup(cache.Stop)
	svc := NewOpenAIGatewayService(nil, logs, billing, nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, cache, nil, &DeferredService{}, nil)
	g := hostedUsageGroup()
	err := svc.RecordChatGPTTurnUsage(context.Background(), &OpenAIChatGPTTurnUsageInput{
		RequestID: "TEST_ONLY_HOSTED_CHAT", BasePriceUSD: .05,
		APIKey: &APIKey{ID: 2, GroupID: &g.ID, Group: g, Quota: 200, RateLimit5h: 200},
		User:   &User{ID: 1}, Account: &Account{ID: 3, Type: AccountTypeOAuth}, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
	})
	require.NoError(t, err)
	require.Len(t, logs.logs, 1)
	require.Equal(t, .05, *logs.logs[0].MeteredCost)
	require.Zero(t, logs.logs[0].ActualCost)
	require.Equal(t, .05, billing.commands[0].APIKeyRateLimitCost)
	require.Zero(t, billing.commands[0].BalanceCost)
}
