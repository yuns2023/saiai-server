package service

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// Market (shared_sale) settlement, P3a BE3.
//
// A command admitted on the market buyer path (UsageBillingCommand.MarketPath,
// or a stray admission proof) settles in ONE transaction with the charge:
// dedup claim → effects (the buyer row lock captures the balance before and
// after the deduction) → shared_sale event → accrue ledger entry. Unlike the
// P2b shadow events there is no savepoint and no low-budget skip: any failure
// rolls back the whole charge. A command whose proof is missing or does not
// match the stored admission (request identity, key, buyer, account, priced
// group) is an integrity error and also rolls back the whole charge.
//
// Owner earnings come from the admission row (owner_source='admission'),
// never from the current account owner: an owner cleared or an account
// deleted after the admission still pays the admitted owner. Business
// ineligibility (self purchase, the admission's owner version no longer
// naming that owner, zero charge, cut = 1, earning below 1e-10) commits the
// event with owner_earning = 0 and earning_zero_reason, without an accrue.
//
// Money: charged_amount = the buyer's balance before − after the deduction,
// read as database decimals inside the row lock the deduction holds;
// owner_earning = trunc(charged × (1 − cut), 10); platform_amount = charged −
// owner_earning (the rounding remainder goes to the platform). A buyer that
// ends below zero is charged in full and the owner earns on the full amount:
// the platform bears the in-flight bad debt of every concurrent request the
// same balance admitted.
//
// The billing transaction never locks anything of the seller (no wallet row,
// no users row, no foreign key to users), so two users buying from each
// other's accounts cannot form a lock cycle.

// MarketSettlementRuleVersion is rule_version of shared_sale events.
const MarketSettlementRuleVersion = 2

// marketEarningScale is the decimal places of owner earnings (numeric(20,10)).
const marketEarningScale = 10

var (
	// ErrMarketSettlementIntegrity: a market command whose settlement proof
	// is missing or does not match the stored admission. The whole charge is
	// rolled back.
	ErrMarketSettlementIntegrity = errors.New("market settlement integrity error")
	// ErrMarketSettlementConflict: the admission already settled another
	// identity, or a replay of this identity carries a different admission.
	ErrMarketSettlementConflict = errors.New("market settlement admission conflict")
	// ErrMarketSettlementRequiresLedger: a market command reached a billing
	// path without the transactional repository (non-transactional fallback
	// or Simple mode). It is refused, never billed without its event.
	ErrMarketSettlementRequiresLedger = errors.New("market usage requires the transactional billing repository")
	// ErrUsageBillingCommitUnknown wraps a COMMIT error of a market command:
	// the outcome is unknown (retry only with the same identity and command).
	ErrUsageBillingCommitUnknown = errors.New("usage billing commit outcome unknown")
)

func marketIntegrityError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMarketSettlementIntegrity, fmt.Sprintf(format, args...))
}

// IsMarketSettlement reports a command that must settle as shared_sale: the
// frozen MarketPath flag, or an admission proof (a proof without the flag is
// inconsistent and fails the shape check rather than settling as shadow).
func (c *UsageBillingCommand) IsMarketSettlement() bool {
	if c == nil {
		return false
	}
	return c.MarketPath || (c.Settlement != nil && c.Settlement.Market != nil)
}

// MarketAdmissionID is the proof's admission id (0 without a proof).
func (c *UsageBillingCommand) MarketAdmissionID() int64 {
	if c == nil || c.Settlement == nil || c.Settlement.Market == nil {
		return 0
	}
	return c.Settlement.Market.AdmissionID
}

// MarketBillingRequestID is the billing request id a market admission's
// request_ref maps to (resolveUsageBillingRequestID: "client:" + the trimmed
// server-generated request id).
func MarketBillingRequestID(requestRef string) string {
	return "client:" + strings.TrimSpace(requestRef)
}

// CheckMarketCommandShape validates a market command without the database:
// MarketPath, Settlement and proof present, the proof matching the command,
// a balance (never subscription) charge with a finite cost.
func CheckMarketCommandShape(cmd *UsageBillingCommand) error {
	if cmd == nil {
		return marketIntegrityError("nil command")
	}
	if !cmd.MarketPath {
		return marketIntegrityError("admission proof without market path")
	}
	if cmd.Settlement == nil {
		return marketIntegrityError("market path without settlement input")
	}
	p := cmd.Settlement.Market
	if p == nil {
		return marketIntegrityError("market path without admission proof")
	}
	switch {
	case p.AdmissionID <= 0:
		return marketIntegrityError("admission id %d", p.AdmissionID)
	case strings.TrimSpace(p.AttemptID) == "" || strings.TrimSpace(p.RequestRef) == "":
		return marketIntegrityError("proof without attempt or request ref")
	case cmd.RequestID != MarketBillingRequestID(p.RequestRef):
		return marketIntegrityError("request id does not match the proof request ref")
	case p.APIKeyID != cmd.APIKeyID:
		return marketIntegrityError("proof api key %d, command %d", p.APIKeyID, cmd.APIKeyID)
	case p.BuyerUserID != cmd.UserID:
		return marketIntegrityError("proof buyer %d, command %d", p.BuyerUserID, cmd.UserID)
	case p.AccountID != cmd.AccountID:
		return marketIntegrityError("proof account %d, command %d", p.AccountID, cmd.AccountID)
	case cmd.Settlement.GroupID == nil || *cmd.Settlement.GroupID != p.GroupID:
		return marketIntegrityError("priced group does not match the proof group %d", p.GroupID)
	}
	// Market tiers are standard (balance) groups (groups_market_tier_check):
	// a subscription charge cannot be split.
	if cmd.SubscriptionCost != 0 || cmd.SubscriptionID != nil || cmd.Settlement.ChargeSource == SettlementChargeSourceSubscription {
		return marketIntegrityError("subscription charge on a market sale")
	}
	if math.IsNaN(cmd.BalanceCost) || math.IsInf(cmd.BalanceCost, 0) || cmd.BalanceCost < 0 {
		return marketIntegrityError("invalid balance cost")
	}
	return nil
}

// CheckMarketAdmission compares the proof and the command with the stored
// admission row (every field the proof carries).
func CheckMarketAdmission(cmd *UsageBillingCommand, adm *MarketAdmission) error {
	if adm == nil {
		return marketIntegrityError("admission not found")
	}
	p := cmd.Settlement.Market
	switch {
	case adm.ID != p.AdmissionID:
		return marketIntegrityError("admission id")
	case adm.AttemptID != p.AttemptID:
		return marketIntegrityError("attempt id does not match the admission")
	case adm.RequestRef != p.RequestRef || cmd.RequestID != MarketBillingRequestID(adm.RequestRef):
		return marketIntegrityError("request ref does not match the admission")
	case adm.APIKeyID != p.APIKeyID || adm.APIKeyID != cmd.APIKeyID:
		return marketIntegrityError("api key does not match the admission")
	case adm.BuyerUserID != p.BuyerUserID || adm.BuyerUserID != cmd.UserID:
		return marketIntegrityError("buyer does not match the admission")
	case adm.AccountID != p.AccountID || adm.AccountID != cmd.AccountID:
		return marketIntegrityError("account does not match the admission")
	case adm.GroupID != p.GroupID:
		return marketIntegrityError("group does not match the admission")
	case adm.OwnerUserID <= 0:
		return marketIntegrityError("admission without owner")
	}
	return nil
}

// SharedSaleSplit is the money split of one shared_sale event.
type SharedSaleSplit struct {
	Charged        decimal.Decimal
	OwnerEarning   decimal.Decimal
	PlatformAmount decimal.Decimal
	CutRate        decimal.Decimal
	SelfPurchase   bool
	// ZeroReason is set whenever OwnerEarning is 0.
	ZeroReason string
}

// ComputeSharedSaleSplit splits the actual charge. ownerVersionOK=false means
// the admission's owner version no longer names the admission owner.
func ComputeSharedSaleSplit(charged, cut decimal.Decimal, selfPurchase, ownerVersionOK bool) (SharedSaleSplit, error) {
	if charged.IsNegative() {
		return SharedSaleSplit{}, marketIntegrityError("negative charged amount %s", charged.String())
	}
	if !ValidMarketPlatformCutRate(cut) {
		return SharedSaleSplit{}, marketIntegrityError("invalid cut rate %s", cut.String())
	}
	out := SharedSaleSplit{Charged: charged, CutRate: cut, SelfPurchase: selfPurchase, OwnerEarning: decimal.Zero}
	switch {
	case selfPurchase:
		out.ZeroReason = SettlementEarningZeroReasonSelfPurchase
	case !ownerVersionOK:
		out.ZeroReason = SettlementEarningZeroReasonOwnerMismatch
	case charged.IsZero():
		out.ZeroReason = SettlementEarningZeroReasonZeroCharge
	default:
		out.OwnerEarning = charged.Mul(decimal.NewFromInt(1).Sub(cut)).Truncate(marketEarningScale)
		if !out.OwnerEarning.IsPositive() {
			// cut = 1, or an earning below the ledger precision.
			out.OwnerEarning = decimal.Zero
			out.ZeroReason = SettlementEarningZeroReasonOther
		}
	}
	out.PlatformAmount = charged.Sub(out.OwnerEarning)
	return out, nil
}

// SharedSaleEvent is a shared_sale usage_settlement_events row: the common
// columns plus the market money columns.
type SharedSaleEvent struct {
	UsageSettlementEvent
	AdmissionID       int64
	ChargedAmount     decimal.Decimal
	PlatformAmount    decimal.Decimal
	PlatformCutRate   decimal.Decimal
	OwnerEarning      decimal.Decimal
	EarningZeroReason *string
	// FreezeDays is the freeze setting snapshot: earning_available_at =
	// event time + FreezeDays.
	FreezeDays int
}

// BuildSharedSaleEvent builds the event of a validated market command. The
// owner is the admission's; charge_input keeps the command's charge as a
// diagnostic next to the actual charged_amount.
func BuildSharedSaleEvent(cmd *UsageBillingCommand, adm *MarketAdmission, split SharedSaleSplit, freezeDays int) *SharedSaleEvent {
	in := cmd.Settlement
	owner := adm.OwnerUserID
	ev := &SharedSaleEvent{
		UsageSettlementEvent: UsageSettlementEvent{
			RequestID:       cmd.RequestID,
			APIKeyID:        cmd.APIKeyID,
			Category:        SettlementCategorySharedSale,
			RuleVersion:     MarketSettlementRuleVersion,
			BuyerUserID:     cmd.UserID,
			AccountID:       cmd.AccountID,
			OwnerUserID:     &owner,
			OwnerSource:     SettlementOwnerSourceAdmission,
			SelfPurchase:    split.SelfPurchase,
			GroupID:         cloneInt64Ptr(in.GroupID),
			OriginalGroupID: cloneInt64Ptr(in.OriginalGroupID),
			ChargeSource:    in.ChargeSource,
		},
		AdmissionID:     adm.ID,
		ChargedAmount:   split.Charged,
		PlatformAmount:  split.PlatformAmount,
		PlatformCutRate: split.CutRate,
		OwnerEarning:    split.OwnerEarning,
		FreezeDays:      freezeDays,
	}
	if split.ZeroReason != "" {
		reason := split.ZeroReason
		ev.EarningZeroReason = &reason
	}
	if ev.ChargeSource != SettlementChargeSourceBalance && ev.ChargeSource != SettlementChargeSourceNone {
		// Subscription sources were refused by the shape check.
		ev.ChargeSource = SettlementChargeSourceNone
		ev.InvalidFields = append(ev.InvalidFields, "charge_source")
	}
	var standardOK, chargeOK bool
	ev.StandardCost, standardOK = normalizeSettlementAmount(in.StandardCost)
	if !standardOK {
		ev.InvalidFields = append(ev.InvalidFields, "standard_cost")
	}
	ev.ChargeInput, chargeOK = normalizeSettlementAmount(in.ChargeInput)
	if !chargeOK {
		ev.InvalidFields = append(ev.InvalidFields, "charge_input")
	}
	ev.Anomaly = len(ev.InvalidFields) > 0 || split.ZeroReason == SettlementEarningZeroReasonOwnerMismatch || in.HostedNotMetered
	if ev.ChargeInput == 0 {
		reason := settlementZeroReason(in, chargeOK)
		ev.ZeroReason = &reason
	}
	return ev
}

// Market settlement and market usage-task counters (process local).
var (
	marketSettlementIntegrityErrors atomic.Uint64
	marketUsageNotEnqueued          atomic.Uint64
	marketUsageApplyRolledBack      atomic.Uint64
	marketUsageCommitUnknown        atomic.Uint64
)

// RecordMarketSettlementIntegrityError logs and counts a market command
// rolled back for an integrity error (alert: it should never happen).
func RecordMarketSettlementIntegrityError(cmd *UsageBillingCommand, err error) {
	marketSettlementIntegrityErrors.Add(1)
	logger.L().Error("ALERT market settlement integrity error; charge rolled back",
		zap.String("request_id", requestIDOf(cmd)),
		zap.Int64("api_key_id", apiKeyIDOf(cmd)),
		zap.Int64("admission_id", cmd.MarketAdmissionID()),
		zap.Error(err),
	)
}

// ObserveMarketUsageSubmit counts a market usage task that was not enqueued
// (pool stopped or full). The handler calls it with the submit result of a
// task whose attempt was admitted on the market path.
func ObserveMarketUsageSubmit(mode UsageRecordSubmitMode, admissionID int64) {
	if mode != UsageRecordSubmitModeDropped {
		return
	}
	marketUsageNotEnqueued.Add(1)
	logger.L().Error("market usage task not enqueued; the sale is not billed",
		zap.Int64("admission_id", admissionID),
		zap.String("mode", string(mode)),
	)
}

// recordMarketApplyFailure classifies a failed Apply of a market command:
// a COMMIT error leaves the outcome unknown, anything else was rolled back.
func recordMarketApplyFailure(cmd *UsageBillingCommand, err error) {
	if errors.Is(err, ErrUsageBillingCommitUnknown) {
		marketUsageCommitUnknown.Add(1)
		logger.L().Error("market usage billing commit outcome unknown",
			zap.String("request_id", requestIDOf(cmd)),
			zap.Int64("api_key_id", apiKeyIDOf(cmd)),
			zap.Int64("admission_id", cmd.MarketAdmissionID()),
			zap.Error(err),
		)
		return
	}
	marketUsageApplyRolledBack.Add(1)
	logger.L().Error("market usage billing rolled back; the sale is not billed",
		zap.String("request_id", requestIDOf(cmd)),
		zap.Int64("api_key_id", apiKeyIDOf(cmd)),
		zap.Int64("admission_id", cmd.MarketAdmissionID()),
		zap.Error(err),
	)
}

func requestIDOf(cmd *UsageBillingCommand) string {
	if cmd == nil {
		return ""
	}
	return cmd.RequestID
}

func apiKeyIDOf(cmd *UsageBillingCommand) int64 {
	if cmd == nil {
		return 0
	}
	return cmd.APIKeyID
}

// refuseMarketWithoutLedger: a market sale is billed only together with its
// settlement event and ledger entry, never through the non-transactional
// fallback (no repository or no request id).
func refuseMarketWithoutLedger(cmd *UsageBillingCommand, repo UsageBillingRepository) error {
	if !cmd.IsMarketSettlement() || (cmd.RequestID != "" && repo != nil) {
		return nil
	}
	err := fmt.Errorf("%w: request_id_present=%t repository_present=%t", ErrMarketSettlementRequiresLedger, cmd.RequestID != "", repo != nil)
	recordMarketApplyFailure(cmd, err)
	return err
}
