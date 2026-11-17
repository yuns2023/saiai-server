//go:build unit

package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func settlementHostedGroup(id int64, rate float64) *Group {
	return &Group{ID: id, Platform: PlatformAnthropic, HostedUsageEnabled: true, IsExclusive: true,
		SubscriptionType: SubscriptionTypeStandard, RateMultiplier: rate}
}

func recordGatewayUsageForSettlement(t *testing.T, key *APIKey, model string, sub *UserSubscription) *UsageBillingCommand {
	t.Helper()
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{}, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{RequestID: "settlement-" + model, Model: model, Duration: time.Second,
			Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 500}},
		APIKey: key, User: &User{ID: 601}, Account: &Account{ID: 701}, Subscription: sub,
		APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
	})
	require.NoError(t, err)
	require.NotNil(t, billingRepo.lastCmd)
	require.NotNil(t, billingRepo.lastCmd.Settlement, "every sale carries settlement input")
	return billingRepo.lastCmd
}

func TestBuildUsageBillingCommandSettlement_HostedCharged(t *testing.T) {
	groupID := int64(30)
	cmd := recordGatewayUsageForSettlement(t, &APIKey{ID: 501, GroupID: &groupID, Group: settlementHostedGroup(groupID, 0.2)}, "claude-sonnet-4", nil)
	in := cmd.Settlement
	require.True(t, in.Hosted)
	require.False(t, in.HostedNotMetered)
	require.NotNil(t, in.FeeRate)
	require.InDelta(t, 0.2, *in.FeeRate, 1e-12)
	require.Equal(t, groupID, *in.GroupID)
	require.Nil(t, in.OriginalGroupID)
	require.Positive(t, in.StandardCost)
	require.Equal(t, SettlementChargeSourceBalance, in.ChargeSource)
	require.Equal(t, cmd.BalanceCost, in.ChargeInput, "charge input is the exact effects amount")
	require.InDelta(t, in.StandardCost*0.2, in.ChargeInput, 1e-12)
	require.False(t, in.FreeGroup)
	require.False(t, in.MissingPrice)
}

func TestBuildUsageBillingCommandSettlement_HostedFree(t *testing.T) {
	groupID := int64(31)
	cmd := recordGatewayUsageForSettlement(t, &APIKey{ID: 501, GroupID: &groupID, Group: settlementHostedGroup(groupID, 0)}, "claude-sonnet-4", nil)
	in := cmd.Settlement
	require.True(t, in.Hosted)
	require.Zero(t, *in.FeeRate)
	require.Positive(t, in.StandardCost)
	require.Zero(t, in.ChargeInput)
	require.Equal(t, SettlementChargeSourceNone, in.ChargeSource)
	require.True(t, in.FreeGroup)
	ev := BuildUsageSettlementEvent(cmd, &cmd.UserID, true)
	require.NotNil(t, ev)
	require.Equal(t, SettlementZeroReasonFreeGroup, *ev.ZeroReason)
	require.False(t, ev.Anomaly)
	require.True(t, ev.SelfPurchase)
}

func TestBuildUsageBillingCommandSettlement_Subscription(t *testing.T) {
	groupID := int64(32)
	group := &Group{ID: groupID, Platform: PlatformAnthropic, SubscriptionType: SubscriptionTypeSubscription, RateMultiplier: 1.5}
	cmd := recordGatewayUsageForSettlement(t, &APIKey{ID: 501, GroupID: &groupID, Group: group}, "claude-sonnet-4", &UserSubscription{ID: 77, GroupID: groupID})
	in := cmd.Settlement
	require.False(t, in.Hosted)
	require.Nil(t, in.FeeRate)
	require.Equal(t, SettlementChargeSourceSubscription, in.ChargeSource)
	require.Positive(t, in.ChargeInput)
	require.Equal(t, cmd.SubscriptionCost, in.ChargeInput)
	require.InDelta(t, in.StandardCost*1.5, in.ChargeInput, 1e-12)
	require.Equal(t, int64(77), *in.SubscriptionID)
	// A platform account without owner gets no event.
	require.Nil(t, BuildUsageSettlementEvent(cmd, nil, true))
	owner := int64(9)
	ev := BuildUsageSettlementEvent(cmd, &owner, true)
	require.Equal(t, SettlementCategoryOwnedOther, ev.Category)
	require.True(t, ev.Anomaly)
	require.Equal(t, int64(77), *ev.SubscriptionID)
}

func TestBuildUsageBillingCommandSettlement_SmartSwitchActualAndOriginalGroup(t *testing.T) {
	actual, original := int64(40), int64(41)
	key := &APIKey{ID: 501, GroupID: &actual, Group: settlementHostedGroup(actual, 0.5), SmartSwitchOriginalGroupID: &original}
	cmd := recordGatewayUsageForSettlement(t, key, "claude-sonnet-4", nil)
	require.Equal(t, actual, *cmd.Settlement.GroupID)
	require.Equal(t, original, *cmd.Settlement.OriginalGroupID)
	require.True(t, cmd.Settlement.Hosted)
	// The settlement copy must not alias the key's pointers.
	*key.GroupID = 99
	require.Equal(t, int64(40), *cmd.Settlement.GroupID)
}

func TestBuildUsageBillingCommandSettlement_MissingPriceMarked(t *testing.T) {
	groupID := int64(33)
	cmd := recordGatewayUsageForSettlement(t, &APIKey{ID: 501, GroupID: &groupID, Group: settlementHostedGroup(groupID, 0.2)}, "TEST_ONLY_unpriced-model", nil)
	in := cmd.Settlement
	require.True(t, in.MissingPrice)
	require.Zero(t, in.ChargeInput)
	ev := BuildUsageSettlementEvent(cmd, &cmd.UserID, true)
	require.Equal(t, SettlementZeroReasonMissingPrice, *ev.ZeroReason)
}

func TestBuildUsageBillingCommandSettlement_LongContextHostedIsAnomaly(t *testing.T) {
	groupID := int64(34)
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{}, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	err := svc.RecordUsageWithLongContext(context.Background(), &RecordUsageLongContextInput{
		Result: &ForwardResult{RequestID: "settlement-long-context", Model: "claude-sonnet-4", Duration: time.Second,
			Usage: ClaudeUsage{InputTokens: 12, OutputTokens: 8}},
		APIKey: &APIKey{ID: 502, GroupID: &groupID, Group: settlementHostedGroup(groupID, 0.2)},
		User:   &User{ID: 602}, Account: &Account{ID: 702},
		LongContextThreshold: 200000, LongContextMultiplier: 2,
		APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
	})
	require.NoError(t, err)
	cmd := billingRepo.lastCmd
	require.NotNil(t, cmd.Settlement)
	require.True(t, cmd.Settlement.Hosted)
	require.True(t, cmd.Settlement.HostedNotMetered)
	ev := BuildUsageSettlementEvent(cmd, &cmd.UserID, true)
	require.Equal(t, SettlementCategoryHostedSelf, ev.Category)
	require.True(t, ev.Anomaly, "hosted group on the long-context path is not claimed as hosted-priced")
}

func TestBuildUsageBillingCommandSettlement_DoesNotChangeFingerprint(t *testing.T) {
	groupID := int64(35)
	cmd := recordGatewayUsageForSettlement(t, &APIKey{ID: 501, GroupID: &groupID, Group: settlementHostedGroup(groupID, 0.2)}, "claude-sonnet-4", nil)
	clone := *cmd
	clone.Settlement = nil
	require.Equal(t, cmd.RequestFingerprint, buildUsageBillingFingerprint(&clone))
	clone.Settlement = &UsageSettlementInput{StandardCost: 123, ChargeInput: 456, Hosted: true}
	require.Equal(t, cmd.RequestFingerprint, buildUsageBillingFingerprint(&clone))
}

func TestClassifySettlement(t *testing.T) {
	buyer, other := int64(1), int64(2)
	cases := []struct {
		name     string
		hosted   bool
		owner    *int64
		category string
		anomaly  bool
		write    bool
	}{
		{"hosted self", true, &buyer, SettlementCategoryHostedSelf, false, true},
		{"hosted other owner", true, &other, SettlementCategoryHostedSelf, true, true},
		{"hosted no owner", true, nil, SettlementCategoryHostedSelf, true, true},
		{"owned in platform group", false, &other, SettlementCategoryOwnedOther, true, true},
		{"platform", false, nil, "", false, false},
	}
	for _, tc := range cases {
		category, anomaly, write := classifySettlement(tc.hosted, tc.owner, buyer)
		require.Equal(t, tc.category, category, tc.name)
		require.Equal(t, tc.anomaly, anomaly, tc.name)
		require.Equal(t, tc.write, write, tc.name)
	}
}

func TestBuildUsageSettlementEvent_NormalizesEventCopyOnly(t *testing.T) {
	buyer := int64(5)
	rate := 3e10
	cmd := &UsageBillingCommand{RequestID: "r", APIKeyID: 7, UserID: buyer, AccountID: 8, BalanceCost: math.Inf(1),
		Settlement: &UsageSettlementInput{Hosted: true, FeeRate: &rate, StandardCost: math.NaN(), ChargeInput: math.Inf(1), ChargeSource: SettlementChargeSourceBalance}}
	ev := BuildUsageSettlementEvent(cmd, &buyer, true)
	require.Zero(t, ev.StandardCost)
	require.Zero(t, ev.ChargeInput)
	require.Zero(t, *ev.FeeRate)
	require.Equal(t, []string{"standard_cost", "charge_input", "fee_rate"}, ev.InvalidFields)
	require.True(t, ev.Anomaly)
	require.Equal(t, SettlementZeroReasonInvalidAmount, *ev.ZeroReason)
	require.True(t, math.IsInf(cmd.BalanceCost, 1), "effects amount untouched")
	require.True(t, math.IsInf(cmd.Settlement.ChargeInput, 1))

	// Negative and >= 1e10 are invalid; a valid charge with an invalid
	// standard cost keeps the charge and has no zero reason.
	cmd.Settlement = &UsageSettlementInput{Hosted: true, StandardCost: -1, ChargeInput: 2, ChargeSource: SettlementChargeSourceBalance}
	ev = BuildUsageSettlementEvent(cmd, &buyer, true)
	require.Equal(t, []string{"standard_cost"}, ev.InvalidFields)
	require.Equal(t, 2.0, ev.ChargeInput)
	require.Nil(t, ev.ZeroReason)

	cmd.Settlement = &UsageSettlementInput{Hosted: true, StandardCost: 1, ChargeInput: 1e10, ChargeSource: SettlementChargeSourceBalance}
	ev = BuildUsageSettlementEvent(cmd, &buyer, true)
	require.Equal(t, []string{"charge_input"}, ev.InvalidFields)
	require.Equal(t, SettlementZeroReasonInvalidAmount, *ev.ZeroReason)

	cmd.Settlement = &UsageSettlementInput{Hosted: true, StandardCost: 9999999999.9999999999 - 1, ChargeInput: 0, ChargeSource: SettlementChargeSourceNone, ZeroUsage: true}
	ev = BuildUsageSettlementEvent(cmd, &buyer, true)
	require.Empty(t, ev.InvalidFields)
	require.Equal(t, SettlementZeroReasonZeroUsage, *ev.ZeroReason)

	cmd.Settlement = &UsageSettlementInput{Hosted: true, StandardCost: 1, ChargeSource: SettlementChargeSourceNone}
	ev = BuildUsageSettlementEvent(cmd, &buyer, true)
	require.Equal(t, SettlementZeroReasonOther, *ev.ZeroReason)
}

func TestBuildUsageSettlementEvent_MissingAccountIsAnomaly(t *testing.T) {
	owner := int64(5)
	cmd := &UsageBillingCommand{RequestID: "r", APIKeyID: 7, UserID: owner, AccountID: 8,
		Settlement: &UsageSettlementInput{Hosted: true, StandardCost: 1, ChargeInput: 1, ChargeSource: SettlementChargeSourceBalance}}
	ev := BuildUsageSettlementEvent(cmd, &owner, false)
	require.Nil(t, ev.OwnerUserID)
	require.False(t, ev.SelfPurchase)
	require.True(t, ev.Anomaly)
	require.Equal(t, SettlementOwnerSourceSettlement, ev.OwnerSource)
	require.Equal(t, int16(UsageSettlementRuleVersion), ev.RuleVersion)
	// A non-hosted sale on a missing account has no owner: no event.
	cmd.Settlement.Hosted = false
	require.Nil(t, BuildUsageSettlementEvent(cmd, &owner, false))
}

func TestSubscriptionHostedGroupIsRejected(t *testing.T) {
	g := settlementHostedGroup(1, 0.2)
	g.SubscriptionType = SubscriptionTypeSubscription
	require.False(t, g.UsesHostedUsage())
	require.Error(t, g.ValidateHostedUsage())
}
