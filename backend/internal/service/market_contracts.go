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
	AttemptID string
	// RequestRef binds the admission to one client request: the
	// server-generated request id set before any upstream call
	// (ctxkey.ClientRequestID). Settlement compares it with the request
	// being billed.
	RequestRef  string
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

// MarketAdmissionState is the authoritative state AdmitMarket reads inside
// its transaction, after share-locking the listing row (every listing change
// — owner list/unlist/switch, admin configure/revoke, owner cleared/paused/
// withdrawn, account delete, tier group removal — updates that row, so it is
// stable until commit).
type MarketAdmissionState struct {
	// Listing is nil when the account has no listing row.
	Listing *MarketListing
	// Account is nil when the account is missing or deleted. Same facts as
	// the listing checks (owner, pause/withdraw, status, schedulable, carpool
	// extra, concurrency, self-onboarded proxy).
	Account *Account
	// OwnerEventID / OwnerEventUserID: the latest account_owner_events row
	// (0 / nil when the account has no ownership history).
	OwnerEventID     int64
	OwnerEventUserID *int64
	// OwnerUserStatus is users.status of Account.OwnerUserID ("" = no owner
	// or the user is deleted).
	OwnerUserStatus string
	// Group facts of MarketAdmission.GroupID (GroupFound=false: missing or
	// deleted).
	GroupFound      bool
	GroupMarketTier bool
	GroupStatus     string
	GroupPlatform   string
	// GroupRateMultiplier / GroupModelRateMultipliers: rate_multiplier and
	// model_rate_multipliers of the group in the same snapshot (the final
	// price positivity check).
	GroupRateMultiplier       float64
	GroupModelRateMultipliers map[string]float64
	// Membership rows of (account, group).
	AdminMember  bool
	MarketMember bool
	// DBNow is clock_timestamp() after the locks.
	DBNow time.Time
	// MarketEnabled is the market_enabled setting in the same snapshot.
	MarketEnabled bool
}

// MarketAdmissionRepository is the admission store.
//
// AdmitMarket is the authoritative "check then insert" unit of work, one
// REPEATABLE READ transaction (all facts from one snapshot): share-lock the
// listing row → read MarketAdmissionState →
// check(state, in) (the caller's rules; it may fill in OwnerUserID,
// OwnerEventID, ListingVersion from the state; an error aborts and is
// returned unchanged) → insert. The database sets ID / AdmittedAt
// (clock_timestamp()) / CreatedAt. A duplicate attempt id returns
// ErrMarketAdmissionAttemptExists. A commit error leaves the outcome
// unknown: confirm with GetMarketAdmissionByAttempt or do not dispatch. A
// listing row changed after the snapshot (40001) reruns the transaction once,
// so check may run twice (each time on a fresh copy of in, judged on the new
// snapshot); it must not have side effects.
//
// Promise scope: eligibility is decided on the consistent snapshot of the
// admission transaction that finally commits; a disable committed after that
// snapshot counts as after the admission (the request continues as in
// flight and settles normally). AdmittedAt is the insert's database time and
// can be later than such a disable, so audits must not judge by timestamp
// order alone. The window has no hard bound: every concurrent request that
// obtained an eligible snapshot and commits may continue.
type MarketAdmissionRepository interface {
	AdmitMarket(ctx context.Context, in *MarketAdmission, check func(state *MarketAdmissionState, in *MarketAdmission) error) (*MarketAdmission, error)
	// GetMarketAdmission / GetMarketAdmissionByAttempt return
	// ErrMarketAdmissionNotFound when missing (by attempt: to confirm an
	// insert whose commit result is unknown).
	GetMarketAdmission(ctx context.Context, id int64) (*MarketAdmission, error)
	GetMarketAdmissionByAttempt(ctx context.Context, attemptID string) (*MarketAdmission, error)
}

var ErrMarketAdmissionNotFound = errors.New("market admission not found")

// MarketSettlementProof is frozen on the successful attempt and carried in
// UsageSettlementInput.Market; UsageBillingCommand.MarketPath (top level,
// outside the nullable Settlement) says the sale must settle as shared_sale.
// MarketPath with a missing Settlement or proof, or a proof whose fields differ from the stored admission (including
// RequestRef = the server-generated request id of the request being
// settled), is an integrity error: the whole charge rolls back. Not part of
// the billing fingerprint.
type MarketSettlementProof struct {
	AdmissionID int64
	AttemptID   string
	RequestRef  string
	AccountID   int64
	GroupID     int64
	BuyerUserID int64
	APIKeyID    int64
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
