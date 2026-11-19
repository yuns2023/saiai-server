package service

import (
	"errors"
	"math"
	"sync/atomic"
)

// Shadow settlement events (P2b). The usage billing repository tries to write
// one event per completed sale that it identifies at settlement time as hosted
// or as served by an owned account. Events never carry money (owner earning is
// always 0) and are never a source of owner earnings; they freeze the
// classification, fee rate and charge input for reconciliation and for P3.
// The write is best effort: an event failure never blocks the charge, so a
// sale may lack an event. The non-transactional postUsageBilling fallback
// writes no events.

const UsageSettlementRuleVersion = 1

const (
	SettlementCategoryHostedSelf = "hosted_self"
	SettlementCategoryOwnedOther = "owned_other"

	SettlementOwnerSourceSettlement = "settlement"

	SettlementChargeSourceBalance      = "balance"
	SettlementChargeSourceSubscription = "subscription"
	SettlementChargeSourceNone         = "none"

	SettlementZeroReasonFreeGroup     = "free_group"
	SettlementZeroReasonMissingPrice  = "missing_price"
	SettlementZeroReasonZeroUsage     = "zero_usage"
	SettlementZeroReasonInvalidAmount = "invalid_amount"
	SettlementZeroReasonOther         = "other"
)

// settlementAmountLimit is the first magnitude numeric(20,10) cannot store.
const settlementAmountLimit = 1e10

// ErrUsageSettlementEventConflict means the billing claim succeeded (no dedup
// or archive row) but a settlement event already exists for the identity. The
// whole billing transaction is rolled back so the sale is never charged twice.
var ErrUsageSettlementEventConflict = errors.New("usage settlement event exists without billing dedup record")

// UsageSettlementInput is built for every completed sale (it does not depend
// on cached account ownership); the repository decides whether to write an
// event. It is not part of the billing fingerprint.
type UsageSettlementInput struct {
	// GroupID is the group the sale was priced in (the smart switch target).
	GroupID *int64
	// OriginalGroupID is the smart switch source group (attribution only).
	OriginalGroupID *int64
	// Hosted reports that the priced group uses hosted metering.
	Hosted bool
	// HostedNotMetered reports a hosted group reaching a recording path that
	// does not apply hosted pricing (for example the long-context entry).
	HostedNotMetered bool
	// FeeRate is Group.HostedFeeRate() for hosted groups.
	FeeRate *float64
	// StandardCost is CostBreakdown.TotalCost.
	StandardCost float64
	// ChargeInput is the exact amount handed to the effects
	// (BalanceCost or SubscriptionCost); ChargeSource names it.
	ChargeInput    float64
	ChargeSource   string
	SubscriptionID *int64

	// Hints for zero_reason, set where the cost was built.
	FreeGroup    bool
	MissingPrice bool
	ZeroUsage    bool

	// Market is the admission proof of a market (shared_sale) attempt, set by
	// the market selection path (BE2) and consumed by the settlement (BE3).
	// nil for every other sale. Not part of the billing fingerprint. Whether
	// the sale MUST settle as market is UsageBillingCommand.MarketPath.
	Market *MarketSettlementProof
}

// UsageSettlementEvent is one row of usage_settlement_events.
type UsageSettlementEvent struct {
	RequestID       string
	APIKeyID        int64
	Category        string
	RuleVersion     int16
	Anomaly         bool
	BuyerUserID     int64
	AccountID       int64
	OwnerUserID     *int64
	OwnerSource     string
	SelfPurchase    bool
	GroupID         *int64
	OriginalGroupID *int64
	FeeRate         *float64
	StandardCost    float64
	ChargeInput     float64
	ChargeSource    string
	SubscriptionID  *int64
	ZeroReason      *string
	InvalidFields   []string
}

// classifySettlement decides the event category from the priced group and
// the account owner read at settlement. Platform sales without an owner get
// no event. P3 adds shared_sale here.
func classifySettlement(hosted bool, owner *int64, buyer int64) (category string, anomaly bool, write bool) {
	if hosted {
		return SettlementCategoryHostedSelf, owner == nil || *owner != buyer, true
	}
	if owner != nil {
		// An owned account in a non-hosted group: an administrator mixed it in.
		return SettlementCategoryOwnedOther, true, true
	}
	return "", false, false
}

// BuildUsageSettlementEvent classifies the sale and returns the event to
// insert, or nil when the sale needs no event. accountFound=false means the
// account row was missing at settlement (owner unknown, anomaly).
func BuildUsageSettlementEvent(cmd *UsageBillingCommand, owner *int64, accountFound bool) *UsageSettlementEvent {
	if cmd == nil || cmd.Settlement == nil {
		return nil
	}
	in := cmd.Settlement
	if !accountFound {
		owner = nil
	}
	category, anomaly, write := classifySettlement(in.Hosted, owner, cmd.UserID)
	if !write {
		return nil
	}
	ev := &UsageSettlementEvent{
		RequestID:       cmd.RequestID,
		APIKeyID:        cmd.APIKeyID,
		Category:        category,
		RuleVersion:     UsageSettlementRuleVersion,
		Anomaly:         anomaly || !accountFound || in.HostedNotMetered,
		BuyerUserID:     cmd.UserID,
		AccountID:       cmd.AccountID,
		OwnerUserID:     owner,
		OwnerSource:     SettlementOwnerSourceSettlement,
		SelfPurchase:    owner != nil && *owner == cmd.UserID,
		GroupID:         in.GroupID,
		OriginalGroupID: in.OriginalGroupID,
		ChargeSource:    in.ChargeSource,
		SubscriptionID:  in.SubscriptionID,
	}
	switch ev.ChargeSource {
	case SettlementChargeSourceBalance, SettlementChargeSourceSubscription, SettlementChargeSourceNone:
	default:
		ev.ChargeSource = SettlementChargeSourceNone
		ev.Anomaly = true
	}
	// Normalize the event copy only; the effects and fingerprint keep the
	// original values.
	var standardOK, chargeOK bool
	ev.StandardCost, standardOK = normalizeSettlementAmount(in.StandardCost)
	if !standardOK {
		ev.InvalidFields = append(ev.InvalidFields, "standard_cost")
	}
	ev.ChargeInput, chargeOK = normalizeSettlementAmount(in.ChargeInput)
	if !chargeOK {
		ev.InvalidFields = append(ev.InvalidFields, "charge_input")
	}
	if in.FeeRate != nil {
		rate, ok := normalizeSettlementAmount(*in.FeeRate)
		if !ok {
			ev.InvalidFields = append(ev.InvalidFields, "fee_rate")
		}
		ev.FeeRate = &rate
	}
	if len(ev.InvalidFields) > 0 {
		ev.Anomaly = true
	}
	if ev.ChargeInput == 0 {
		reason := settlementZeroReason(in, chargeOK)
		ev.ZeroReason = &reason
	}
	return ev
}

func normalizeSettlementAmount(v float64) (float64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= settlementAmountLimit {
		return 0, false
	}
	return v, true
}

func settlementZeroReason(in *UsageSettlementInput, chargeOK bool) string {
	switch {
	case !chargeOK:
		return SettlementZeroReasonInvalidAmount
	case in.FreeGroup:
		return SettlementZeroReasonFreeGroup
	case in.MissingPrice:
		return SettlementZeroReasonMissingPrice
	case in.ZeroUsage:
		return SettlementZeroReasonZeroUsage
	default:
		return SettlementZeroReasonOther
	}
}

// UsageSettlementMetricsSnapshot are process-local shadow event counters.
type UsageSettlementMetricsSnapshot struct {
	WriteFailures    uint64 `json:"write_failures"`
	SkippedLowBudget uint64 `json:"skipped_low_budget"`
}

var (
	usageSettlementWriteFailures    atomic.Uint64
	usageSettlementSkippedLowBudget atomic.Uint64
)

// RecordUsageSettlementWriteFailure counts a shadow event write that was
// rolled back to its savepoint while billing committed.
func RecordUsageSettlementWriteFailure() { usageSettlementWriteFailures.Add(1) }

// RecordUsageSettlementSkippedLowBudget counts an event skipped because the
// billing context had too little time left.
func RecordUsageSettlementSkippedLowBudget() { usageSettlementSkippedLowBudget.Add(1) }

func GetUsageSettlementMetricsSnapshot() UsageSettlementMetricsSnapshot {
	return UsageSettlementMetricsSnapshot{
		WriteFailures:    usageSettlementWriteFailures.Load(),
		SkippedLowBudget: usageSettlementSkippedLowBudget.Load(),
	}
}
