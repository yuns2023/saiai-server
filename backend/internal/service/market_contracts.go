package service

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// Contracts for the market selection (BE2) and settlement (BE3) parts. Only
// types, constants and the admission insert/read exist in this part; nothing
// calls them yet.

// MarketAdmission is one immutable market_admissions row: the authoritative
// record that a market (buyer path) candidate attempt was admitted. BE2
// writes it after the last admission check succeeds and before the request
// is sent upstream; BE3 settles by AdmissionID, never by the current owner.
type MarketAdmission struct {
	ID int64
	// AttemptID is server-generated per candidate attempt (unique).
	AttemptID   string
	AccountID   int64
	GroupID     int64
	OwnerUserID int64
	// OwnerEventID is the account_owner_events id that was current at
	// admission (the owner version).
	OwnerEventID   int64
	ListingVersion int64
	BuyerUserID    int64
	APIKeyID       int64
	// AdmittedAt is the database clock_timestamp() of the insert.
	AdmittedAt time.Time
	CreatedAt  time.Time
}

// ErrMarketAdmissionAttemptExists: the attempt id was already recorded.
var ErrMarketAdmissionAttemptExists = errors.New("market admission attempt already recorded")

// MarketAdmissionRepository is the admission store. InsertMarketAdmission
// sets ID / AdmittedAt / CreatedAt from the database and returns
// ErrMarketAdmissionAttemptExists on a duplicate attempt id. It performs no
// eligibility checks: BE2 verifies account, owner version, listing version,
// group and seller state in its own transaction before inserting.
type MarketAdmissionRepository interface {
	InsertMarketAdmission(ctx context.Context, in *MarketAdmission) (*MarketAdmission, error)
	// GetMarketAdmission returns ErrMarketAdmissionNotFound when missing.
	GetMarketAdmission(ctx context.Context, id int64) (*MarketAdmission, error)
}

var ErrMarketAdmissionNotFound = errors.New("market admission not found")

// MarketSettlementProof is frozen on the successful attempt and carried in
// UsageSettlementInput.Market. When MustSettleAsMarket is set, the billing
// transaction settles the sale as shared_sale against AdmissionID; a missing
// or mismatching proof is an integrity error (whole charge rolls back). It is
// NOT part of the billing fingerprint.
type MarketSettlementProof struct {
	AdmissionID        int64
	AttemptID          string
	AccountID          int64
	GroupID            int64
	BuyerUserID        int64
	APIKeyID           int64
	MustSettleAsMarket bool
}

// Settlement category and zero-earning reasons of shared_sale events
// (migration 134 CHECKs).
const (
	SettlementCategorySharedSale   = "shared_sale"
	SettlementOwnerSourceAdmission = "admission"

	SettlementEarningZeroReasonSelfPurchase  = "self_purchase"
	SettlementEarningZeroReasonZeroCharge    = "zero_charge"
	SettlementEarningZeroReasonOwnerMismatch = "owner_mismatch"
	SettlementEarningZeroReasonNotEligible   = "not_eligible"
	SettlementEarningZeroReasonOther         = "other"
)

// Owner earnings ledger entry kinds (owner_earning_entries.kind).
const (
	OwnerEarningEntryAccrue  = "accrue"
	OwnerEarningEntryReverse = "reverse"
	OwnerEarningEntryConvert = "convert"
)

// RedeemTypeMarketEarning is the redeem_codes history type of an earnings →
// balance conversion (status used, used_by the owner, value = +amount).
const RedeemTypeMarketEarning = domain.RedeemTypeMarketEarning

// IsMarketEarningRecordType reports the market_earning history type.
func IsMarketEarningRecordType(codeType string) bool {
	return codeType == RedeemTypeMarketEarning
}
