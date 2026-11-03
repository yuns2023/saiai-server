//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func hostedAnthropicGroup() *Group {
	g := hostedUsageGroup()
	g.Platform = PlatformAnthropic
	return g
}

func recordHostedAnthropicUsage(t *testing.T, svc *GatewayService, g *Group, account *Account, model string, forceCache bool, quota APIKeyQuotaUpdater) {
	t.Helper()
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{RequestID: "TEST_ONLY_HOSTED_ANTHROPIC", Model: model, Duration: time.Second,
			Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 100, CacheReadInputTokens: 200, CacheCreationInputTokens: 300}},
		APIKey:            &APIKey{ID: 2, GroupID: &g.ID, Group: g, Quota: 200, RateLimit5h: 200},
		User:              &User{ID: 1},
		Account:           account,
		ForceCacheBilling: forceCache,
		APIKeyService:     quota,
	})
	require.NoError(t, err)
}

func TestHostedAnthropicRecordUsageMetersStandardCost(t *testing.T) {
	for _, hosted := range []bool{false, true} {
		for _, forceCache := range []bool{false, true} {
			t.Run(fmt.Sprintf("hosted=%t/force_cache=%t", hosted, forceCache), func(t *testing.T) {
				logs := &openAIRecordUsageLogRepoStub{inserted: true}
				billing := &openAIRecordUsageBillingRepoStub{}
				svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, nil)
				// A per-user paid override must not charge once the group is hosted.
				rate := 2.0
				svc.userGroupRateResolver = newUserGroupRateResolver(&userGroupRateResolverRepoStub{rate: &rate}, nil, time.Minute, nil, "service.test")
				g := hostedAnthropicGroup()
				g.HostedUsageEnabled = hosted
				if !hosted {
					g.RateMultiplier = .5
				}
				accountRate := 1.5
				account := &Account{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, RateMultiplier: &accountRate}
				recordHostedAnthropicUsage(t, svc, g, account, "claude-sonnet-4", forceCache, &openAIRecordUsageAPIKeyQuotaStub{})

				log := logs.lastLog
				input := 1000
				cacheRead := 200
				if forceCache {
					input, cacheRead = 0, 1200
				}
				want, err := svc.billingService.CalculateCost("claude-sonnet-4", UsageTokens{InputTokens: input, OutputTokens: 100,
					CacheReadTokens: cacheRead, CacheCreationTokens: 300}, 1)
				require.NoError(t, err)
				require.Positive(t, want.TotalCost)
				require.InDelta(t, want.TotalCost, log.TotalCost, 1e-12)
				if hosted {
					require.NotNil(t, log.MeteredCost)
					require.Equal(t, log.TotalCost, *log.MeteredCost)
					require.Zero(t, log.ActualCost)
					require.Zero(t, log.RateMultiplier)
					require.Zero(t, *log.AccountRateMultiplier)
					require.Zero(t, billing.lastCmd.BalanceCost)
					require.Equal(t, log.TotalCost, billing.lastCmd.APIKeyQuotaCost)
					require.Equal(t, log.TotalCost, billing.lastCmd.APIKeyRateLimitCost)
				} else {
					require.Nil(t, log.MeteredCost)
					require.InDelta(t, log.TotalCost*rate, log.ActualCost, 1e-12)
					require.Equal(t, log.ActualCost, billing.lastCmd.BalanceCost)
					require.Equal(t, log.ActualCost, billing.lastCmd.APIKeyQuotaCost)
					require.Equal(t, accountRate, *log.AccountRateMultiplier)
				}
				// Setup-token accounts have no local dollar quota either way.
				require.Zero(t, billing.lastCmd.AccountQuotaCost)
				require.Equal(t, 1.5, *account.RateMultiplier)
			})
		}
	}
}

func TestHostedAnthropicAccountBudgetKeepsAccountRate(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	billing := &openAIRecordUsageBillingRepoStub{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, nil)
	rate := 2.0
	account := &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		RateMultiplier: &rate, Extra: map[string]any{"quota_limit": 100.0}}
	recordHostedAnthropicUsage(t, svc, hostedAnthropicGroup(), account, "claude-sonnet-4", false, nil)
	require.Positive(t, logs.lastLog.TotalCost)
	require.Zero(t, *logs.lastLog.AccountRateMultiplier)
	require.InDelta(t, logs.lastLog.TotalCost*rate, billing.lastCmd.AccountQuotaCost, 1e-12)
}

func TestHostedAnthropicLegacySettlementUsesMeteredCost(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	user := &openAIRecordUsageUserRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(logs, user, nil)
	quota := &openAIRecordUsageAPIKeyQuotaStub{}
	account := &Account{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}
	recordHostedAnthropicUsage(t, svc, hostedAnthropicGroup(), account, "claude-sonnet-4", false, quota)
	require.Zero(t, user.deductCalls)
	require.Equal(t, 1, quota.quotaCalls)
	require.Equal(t, 1, quota.rateLimitCalls)
	require.Equal(t, logs.lastLog.TotalCost, quota.lastAmount)
}

func TestHostedAnthropicMissingPriceStaysHosted(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	billing := &openAIRecordUsageBillingRepoStub{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, nil)
	account := &Account{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}
	recordHostedAnthropicUsage(t, svc, hostedAnthropicGroup(), account, "TEST_ONLY_unpriced_model", false, nil)
	// The row is still classified as hosted (metered_cost IS NOT NULL).
	require.NotNil(t, logs.lastLog.MeteredCost)
	require.Zero(t, *logs.lastLog.MeteredCost)
	require.Zero(t, billing.lastCmd.BalanceCost)
}

func TestHostedUsageAllowsAnthropicButNotFallbacks(t *testing.T) {
	g := hostedAnthropicGroup()
	require.True(t, g.UsesHostedUsage())
	require.NoError(t, g.ValidateHostedUsage())
	id := int64(9)
	g.FallbackGroupID = &id
	require.Error(t, g.ValidateHostedUsage())
	g.FallbackGroupID = nil
	g.FallbackGroupIDOnInvalidRequest = &id
	require.Error(t, g.ValidateHostedUsage())
	g.HostedUsageEnabled = false
	require.NoError(t, g.ValidateHostedUsage(), "non-hosted groups keep their fallbacks")
}

func TestResolveGatewayGroupNeverCrossesHostedBoundary(t *testing.T) {
	sourceID, fallbackID := int64(10), int64(11)
	for _, tc := range []struct {
		name                     string
		sourceHosted, fallHosted bool
		wantErr                  bool
	}{
		{"paid to paid", false, false, false},
		{"hosted to paid", true, false, true},
		{"paid to hosted", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := hostedAnthropicGroup()
			source.ID, source.Status, source.ClaudeCodeOnly, source.FallbackGroupID = sourceID, StatusActive, true, &fallbackID
			source.HostedUsageEnabled = tc.sourceHosted
			fallback := hostedAnthropicGroup()
			fallback.ID, fallback.Status = fallbackID, StatusActive
			fallback.HostedUsageEnabled = tc.fallHosted
			svc := &GatewayService{groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{sourceID: source, fallbackID: fallback}}}

			got, gotID, err := svc.resolveGatewayGroup(context.Background(), &sourceID)
			if tc.wantErr {
				require.True(t, errors.Is(err, ErrClaudeCodeOnly), "got %v", err)
				require.Nil(t, got)
				require.Nil(t, gotID)
				return
			}
			require.NoError(t, err)
			require.Equal(t, fallbackID, *gotID)
		})
	}
}

func TestValidateFallbackGroupsRejectHostedTargets(t *testing.T) {
	fallbackID := int64(11)
	fallback := hostedAnthropicGroup()
	fallback.ID, fallback.Status = fallbackID, StatusActive
	svc := &adminServiceImpl{groupRepo: &groupRepoStubForFallbackCycle{groups: map[int64]*Group{fallbackID: fallback}}}
	require.ErrorContains(t, svc.validateFallbackGroup(context.Background(), 10, fallbackID), "Hosted")
	require.ErrorContains(t, svc.validateFallbackGroupOnInvalidRequest(context.Background(), 10, PlatformAnthropic, SubscriptionTypeStandard, fallbackID), "Hosted")
	fallback.HostedUsageEnabled = false
	require.NoError(t, svc.validateFallbackGroup(context.Background(), 10, fallbackID))
	require.NoError(t, svc.validateFallbackGroupOnInvalidRequest(context.Background(), 10, PlatformAnthropic, SubscriptionTypeStandard, fallbackID))
}
