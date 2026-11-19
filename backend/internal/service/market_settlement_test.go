//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func marketTestCommand() (*UsageBillingCommand, *MarketAdmission) {
	group := int64(30)
	cmd := &UsageBillingCommand{
		RequestID: "client:req-1", APIKeyID: 5, UserID: 7, AccountID: 11, BalanceCost: 1.5, MarketPath: true,
		Settlement: &UsageSettlementInput{
			GroupID: &group, StandardCost: 1.25, ChargeInput: 1.5, ChargeSource: SettlementChargeSourceBalance,
			Market: &MarketSettlementProof{AdmissionID: 99, AttemptID: "att-1", RequestRef: "req-1", AccountID: 11, GroupID: 30, BuyerUserID: 7, APIKeyID: 5},
		},
	}
	adm := &MarketAdmission{ID: 99, AttemptID: "att-1", RequestRef: "req-1", AccountID: 11, GroupID: 30, OwnerUserID: 3,
		OwnerEventID: 4, ListingVersion: 2, BuyerUserID: 7, APIKeyID: 5}
	return cmd, adm
}

func TestCheckMarketCommandShape(t *testing.T) {
	cmd, _ := marketTestCommand()
	require.NoError(t, CheckMarketCommandShape(cmd))
	require.True(t, cmd.IsMarketSettlement())
	require.Equal(t, int64(99), cmd.MarketAdmissionID())

	cases := map[string]func(c *UsageBillingCommand){
		"proof without market path": func(c *UsageBillingCommand) { c.MarketPath = false },
		"no settlement":             func(c *UsageBillingCommand) { c.Settlement = nil },
		"no proof":                  func(c *UsageBillingCommand) { c.Settlement.Market = nil },
		"zero admission":            func(c *UsageBillingCommand) { c.Settlement.Market.AdmissionID = 0 },
		"empty attempt":             func(c *UsageBillingCommand) { c.Settlement.Market.AttemptID = " " },
		"empty request ref":         func(c *UsageBillingCommand) { c.Settlement.Market.RequestRef = "" },
		"request id":                func(c *UsageBillingCommand) { c.RequestID = "client:req-2" },
		"local request id":          func(c *UsageBillingCommand) { c.RequestID = "local:req-1" },
		"api key":                   func(c *UsageBillingCommand) { c.APIKeyID = 6 },
		"buyer":                     func(c *UsageBillingCommand) { c.UserID = 8 },
		"account":                   func(c *UsageBillingCommand) { c.AccountID = 12 },
		"priced group":              func(c *UsageBillingCommand) { g := int64(31); c.Settlement.GroupID = &g },
		"no priced group":           func(c *UsageBillingCommand) { c.Settlement.GroupID = nil },
		"subscription cost":         func(c *UsageBillingCommand) { c.SubscriptionCost = 1 },
		"subscription id":           func(c *UsageBillingCommand) { id := int64(1); c.SubscriptionID = &id },
		"subscription source":       func(c *UsageBillingCommand) { c.Settlement.ChargeSource = SettlementChargeSourceSubscription },
		"NaN cost":                  func(c *UsageBillingCommand) { c.BalanceCost = math.NaN() },
		"Inf cost":                  func(c *UsageBillingCommand) { c.BalanceCost = math.Inf(1) },
		"negative cost":             func(c *UsageBillingCommand) { c.BalanceCost = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := marketTestCommand()
			mutate(c)
			require.ErrorIs(t, CheckMarketCommandShape(c), ErrMarketSettlementIntegrity)
		})
	}

	// MarketPath without settlement still selects the market branch.
	c, _ := marketTestCommand()
	c.Settlement = nil
	require.True(t, c.IsMarketSettlement())
	require.Zero(t, c.MarketAdmissionID())
	require.False(t, (&UsageBillingCommand{Settlement: &UsageSettlementInput{}}).IsMarketSettlement())
	require.False(t, (*UsageBillingCommand)(nil).IsMarketSettlement())
}

func TestCheckMarketAdmission(t *testing.T) {
	cmd, adm := marketTestCommand()
	require.NoError(t, CheckMarketAdmission(cmd, adm))
	require.ErrorIs(t, CheckMarketAdmission(cmd, nil), ErrMarketSettlementIntegrity)
	cases := map[string]func(a *MarketAdmission){
		"id":          func(a *MarketAdmission) { a.ID = 98 },
		"attempt":     func(a *MarketAdmission) { a.AttemptID = "att-2" },
		"request ref": func(a *MarketAdmission) { a.RequestRef = "req-2" },
		"api key":     func(a *MarketAdmission) { a.APIKeyID = 6 },
		"buyer":       func(a *MarketAdmission) { a.BuyerUserID = 8 },
		"account":     func(a *MarketAdmission) { a.AccountID = 12 },
		"group":       func(a *MarketAdmission) { a.GroupID = 31 },
		"no owner":    func(a *MarketAdmission) { a.OwnerUserID = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, a := marketTestCommand()
			mutate(a)
			require.ErrorIs(t, CheckMarketAdmission(c, a), ErrMarketSettlementIntegrity)
		})
	}
}

func TestComputeSharedSaleSplit(t *testing.T) {
	d := decimal.RequireFromString
	cases := []struct {
		name              string
		charged, cut      string
		self, ownerOK     bool
		earning, platform string
		reason            string
	}{
		{"20 percent", "1.5", "0.2", false, true, "1.2", "0.3", ""},
		{"cut 0 honored", "1.5", "0", false, true, "1.5", "0", ""},
		{"cut 1", "1.5", "1", false, true, "0", "1.5", SettlementEarningZeroReasonOther},
		{"self purchase", "1.5", "0.2", true, true, "0", "1.5", SettlementEarningZeroReasonSelfPurchase},
		{"owner version mismatch", "1.5", "0.2", false, false, "0", "1.5", SettlementEarningZeroReasonOwnerMismatch},
		{"zero charge", "0", "0.2", false, true, "0", "0", SettlementEarningZeroReasonZeroCharge},
		// trunc to 10 dp, remainder to the platform.
		{"truncation", "0.00000003", "0.3333", false, true, "0.00000002", "0.00000001", ""},
		{"truncation remainder", "0.12345679", "0.1235", false, true, "0.1082098764", "0.0152469136", ""},
		{"below precision", "0.00000001", "0.9999", false, true, "0", "0.00000001", SettlementEarningZeroReasonOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputeSharedSaleSplit(d(tc.charged), d(tc.cut), tc.self, tc.ownerOK)
			require.NoError(t, err)
			require.True(t, got.OwnerEarning.Equal(d(tc.earning)), "earning %s", got.OwnerEarning)
			require.True(t, got.PlatformAmount.Equal(d(tc.platform)), "platform %s", got.PlatformAmount)
			require.True(t, got.Charged.Equal(got.OwnerEarning.Add(got.PlatformAmount)), "conservation")
			require.Equal(t, tc.reason, got.ZeroReason)
			require.Equal(t, tc.self, got.SelfPurchase)
			require.True(t, got.OwnerEarning.Equal(got.OwnerEarning.Truncate(10)), "at most 10 decimals")
		})
	}
	_, err := ComputeSharedSaleSplit(d("-0.01"), d("0.2"), false, true)
	require.ErrorIs(t, err, ErrMarketSettlementIntegrity)
	_, err = ComputeSharedSaleSplit(d("1"), d("1.0001"), false, true)
	require.ErrorIs(t, err, ErrMarketSettlementIntegrity)
	_, err = ComputeSharedSaleSplit(d("1"), d("0.12345"), false, true)
	require.ErrorIs(t, err, ErrMarketSettlementIntegrity)
}

func TestBuildSharedSaleEvent(t *testing.T) {
	cmd, adm := marketTestCommand()
	orig := int64(29)
	cmd.Settlement.OriginalGroupID = &orig
	split, err := ComputeSharedSaleSplit(decimal.RequireFromString("1.5"), decimal.RequireFromString("0.2"), false, true)
	require.NoError(t, err)
	ev := BuildSharedSaleEvent(cmd, adm, split, 7)
	require.Equal(t, SettlementCategorySharedSale, ev.Category)
	require.Equal(t, int16(MarketSettlementRuleVersion), ev.RuleVersion)
	require.Equal(t, SettlementOwnerSourceAdmission, ev.OwnerSource)
	require.Equal(t, int64(3), *ev.OwnerUserID, "owner from the admission")
	require.Equal(t, int64(99), ev.AdmissionID)
	require.Equal(t, int64(30), *ev.GroupID)
	require.Equal(t, int64(29), *ev.OriginalGroupID)
	require.Nil(t, ev.FeeRate)
	require.Nil(t, ev.EarningZeroReason)
	require.Nil(t, ev.ZeroReason)
	require.False(t, ev.Anomaly)
	require.Equal(t, 1.5, ev.ChargeInput)
	require.Equal(t, 1.25, ev.StandardCost)
	require.Equal(t, SettlementChargeSourceBalance, ev.ChargeSource)
	require.Equal(t, 7, ev.FreezeDays)

	// Zero charge with an invalid charge input: reasons and anomaly.
	cmd.Settlement.ChargeInput = math.NaN()
	cmd.Settlement.ChargeSource = SettlementChargeSourceNone
	split, err = ComputeSharedSaleSplit(decimal.Zero, decimal.RequireFromString("0.2"), false, false)
	require.NoError(t, err)
	ev = BuildSharedSaleEvent(cmd, adm, split, 0)
	require.Equal(t, SettlementEarningZeroReasonOwnerMismatch, *ev.EarningZeroReason)
	require.True(t, ev.Anomaly)
	require.Equal(t, []string{"charge_input"}, ev.InvalidFields)
	require.Equal(t, SettlementZeroReasonInvalidAmount, *ev.ZeroReason)
	require.Equal(t, SettlementChargeSourceNone, ev.ChargeSource)
}

func TestMarketUsageCounters(t *testing.T) {
	cmd, _ := marketTestCommand()
	before := GetUsageSettlementMetricsSnapshot()
	recordMarketApplyFailure(cmd, fmt.Errorf("%w: boom", ErrUsageBillingCommitUnknown))
	recordMarketApplyFailure(cmd, errors.New("deadlock"))
	recordMarketApplyFailure(cmd, ErrMarketSettlementIntegrity)
	RecordMarketSettlementIntegrityError(cmd, ErrMarketSettlementIntegrity)
	ObserveMarketUsageSubmit(UsageRecordSubmitModeEnqueued, 99)
	ObserveMarketUsageSubmit(UsageRecordSubmitModeSync, 99)
	ObserveMarketUsageSubmit(UsageRecordSubmitModeDropped, 99)
	after := GetUsageSettlementMetricsSnapshot()
	require.Equal(t, before.MarketCommitUnknown+1, after.MarketCommitUnknown)
	require.Equal(t, before.MarketApplyRolledBack+2, after.MarketApplyRolledBack)
	require.Equal(t, before.MarketIntegrityErrors+1, after.MarketIntegrityErrors)
	require.Equal(t, before.MarketNotEnqueued+1, after.MarketNotEnqueued)
}

// A market command never falls back to the non-transactional billing.
func TestApplyUsageBillingRefusesMarketWithoutRepository(t *testing.T) {
	cmd, _ := marketTestCommand()
	before := GetUsageSettlementMetricsSnapshot()
	require.True(t, cmd.IsMarketSettlement())
	err := refuseMarketWithoutLedger(cmd, nil)
	require.ErrorIs(t, err, ErrMarketSettlementRequiresLedger)
	require.Equal(t, before.MarketApplyRolledBack+1, GetUsageSettlementMetricsSnapshot().MarketApplyRolledBack)
	require.NoError(t, refuseMarketWithoutLedger(&UsageBillingCommand{RequestID: "x"}, nil), "non-market keeps its fallback")
	require.NoError(t, refuseMarketWithoutLedger(cmd, stubUsageBillingRepo{}))
}

type stubUsageBillingRepo struct{ UsageBillingRepository }

func TestOwnerEarningInputParsing(t *testing.T) {
	for _, ok := range []string{"1", "1.5", "1.50", "0.01", "10000"} {
		_, err := ParseOwnerEarningConvertAmount(ok)
		require.NoError(t, err, ok)
	}
	for _, bad := range []string{"", "0", "0.00", "1.234", "-1", "1e2", "abc", " 1. ", "NaN"} {
		_, err := ParseOwnerEarningConvertAmount(bad)
		require.ErrorIs(t, err, ErrOwnerEarningInvalidAmount, bad)
	}
	id, err := NormalizeOwnerEarningRequestID("9F1C2B9E-6F00-4E43-9C5A-111111111111")
	require.NoError(t, err)
	require.Equal(t, "9f1c2b9e-6f00-4e43-9c5a-111111111111", id)
	for _, bad := range []string{"", "x", "00000000-0000-0000-0000-000000000000", "9f1c2b9e6f004e439c5a111111111111"} {
		_, err := NormalizeOwnerEarningRequestID(bad)
		require.ErrorIs(t, err, ErrOwnerEarningInvalidRequestID, bad)
	}
}

// The billing identity frozen from the original request is used only for a
// market sale (MarketPath); every other request keeps the identity resolved
// as before, on both recording paths.
func TestRecordUsageUsesFrozenBillingRequestIDOnlyForMarket(t *testing.T) {
	for _, long := range []bool{false, true} {
		for _, market := range []bool{false, true} {
			usageRepo := &openAIRecordUsageLogRepoStub{}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			result := &ForwardResult{RequestID: "upstream-volatile", Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 6}, Model: "claude-sonnet-4", Duration: time.Second}
			var err error
			if long {
				err = svc.RecordUsageWithLongContext(context.Background(), &RecordUsageLongContextInput{Result: result, APIKey: &APIKey{ID: 1}, User: &User{ID: 2},
					Account: &Account{ID: 3}, LongContextThreshold: 200000, LongContextMultiplier: 2, MarketPath: market, BillingRequestID: " client:frozen-1 "})
			} else {
				err = svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: &APIKey{ID: 1}, User: &User{ID: 2},
					Account: &Account{ID: 3}, MarketPath: market, BillingRequestID: "client:frozen-1"})
			}
			require.NoError(t, err)
			want := "upstream-volatile"
			if market {
				want = "client:frozen-1"
			}
			require.Equal(t, want, billingRepo.lastCmd.RequestID, "long=%v market=%v", long, market)
			require.Equal(t, want, usageRepo.lastLog.RequestID)
			require.Equal(t, market, billingRepo.lastCmd.MarketPath)
		}
	}
}

// The long-context entry carries the whole market contract into the
// command (path, proof, price snapshot, frozen id); a non-market call is
// unchanged.
func TestRecordUsageWithLongContextCarriesMarketContract(t *testing.T) {
	run := func(in *RecordUsageLongContextInput) *UsageBillingCommand {
		usageRepo := &openAIRecordUsageLogRepoStub{}
		billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
		svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
		in.Result = &ForwardResult{RequestID: "upstream-volatile", Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 6}, Model: "claude-sonnet-4", Duration: time.Second}
		group := int64(30) // the proof's group
		in.APIKey, in.User, in.Account = &APIKey{ID: 5, GroupID: &group, Group: &Group{ID: 30}}, &User{ID: 7}, &Account{ID: 11}
		in.LongContextThreshold, in.LongContextMultiplier = 200000, 2
		require.NoError(t, svc.RecordUsageWithLongContext(context.Background(), in))
		require.NotNil(t, billingRepo.lastCmd)
		require.Equal(t, billingRepo.lastCmd.RequestID, usageRepo.lastLog.RequestID)
		return billingRepo.lastCmd
	}
	proof := &MarketSettlementProof{AdmissionID: 99, AttemptID: "att-1", RequestRef: "req-1", AccountID: 11, GroupID: 30, BuyerUserID: 7, APIKeyID: 5}
	price := &MarketPriceSnapshot{Model: "claude-sonnet-4", PriceVersion: "v1", GroupRate: 1.2, ModelRate: 1, UserDiscount: 1}
	cmd := run(&RecordUsageLongContextInput{MarketPath: true, Market: proof, MarketPrice: price, BillingRequestID: "client:req-1"})
	require.True(t, cmd.MarketPath)
	require.Equal(t, "client:req-1", cmd.RequestID)
	require.NotNil(t, cmd.Settlement)
	require.Equal(t, *proof, *cmd.Settlement.Market)
	require.NotSame(t, proof, cmd.Settlement.Market, "copied, not shared")
	require.Equal(t, *price, *cmd.Settlement.MarketPrice)

	plain := run(&RecordUsageLongContextInput{BillingRequestID: "client:ignored"})
	require.False(t, plain.MarketPath)
	require.Equal(t, "upstream-volatile", plain.RequestID)
	require.Nil(t, plain.Settlement.Market)
	require.Nil(t, plain.Settlement.MarketPrice)
}

// Audit strings can never break the JSONB column (NUL, invalid UTF-8, size).
func TestMarketPriceAuditFromSanitizes(t *testing.T) {
	long := strings.Repeat("é", 300)
	got := MarketPriceAuditFrom(&MarketPriceSnapshot{Model: "claude\x00-sonnet\xff", PriceVersion: long, GroupRate: math.NaN(), InputPerToken: math.Inf(1), ModelRate: 1.5})
	require.Equal(t, "claude-sonnet", got.Model)
	require.Equal(t, 200, len([]rune(got.PriceVersion)))
	require.Zero(t, got.GroupRate)
	require.Zero(t, got.InputPerToken)
	require.Equal(t, 1.5, got.ModelRate)
	require.Nil(t, MarketPriceAuditFrom(nil))
}
