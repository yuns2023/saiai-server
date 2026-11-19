package service

import (
	"context"
	"errors"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// Owner earnings (P3a BE3): the insert-only ledger owner_earning_entries
// (accrue > 0 from shared_sale settlements, reverse / convert < 0) and the
// per-owner wallet row owner_earning_wallets that serializes convert and
// reverse.
//
// At database time t (read after the wallet lock):
//
//	available = Σ accrue(available_at ≤ t) + Σ reverse(available_at ≤ t) + Σ convert
//	frozen    = Σ accrue(available_at > t)  + Σ reverse(available_at > t)
//
// A reverse copies its accrue's available_at, so both always fall in the
// same bucket. Freezing needs no job: an accrue becomes available when the
// clock passes its available_at.
//
// Convert (owner): wallet FOR UPDATE → t → idempotency by (owner,
// request_id) BEFORE any availability check (same amount = the original
// success, different amount = conflict) → minimum (setting at operation
// time) → available → convert entry + users.balance += amount + a
// market_earning history row, one transaction (lock order wallet → users).
// The converted amount is ordinary balance afterwards (the balance trigger
// may raise the user's auto level, like any top-up), and it is not counted
// as a real top-up anywhere (market_earning is not in the top-up types).
//
// Reverse (admin): for accrue entries of the scope (account and/or owner)
// still frozen at t and not yet reversed, insert an equal negative reverse
// entry (same owner, same available_at).
//
// Scope promise: a reverse acts on the earnings of the scope that are
// committed when it runs, recorded at or before as_of, and still frozen and
// unreversed at the database time read after the wallet locks. Every call
// (including a retry) selects its scope again; as_of is a cutoff, not an
// idempotency key, and a retry does not replay the first attempt's exact
// entry set. Scope by owner only affects that owner; scope by account can
// affect several owners the account had over time; give both to touch one
// owner's earnings on one account. Already available earnings are never
// clawed back. Several wallets are locked in user id order.

const (
	// OwnerEarningEntriesMaxLimit bounds one entries page.
	OwnerEarningEntriesMaxLimit     = 100
	OwnerEarningEntriesDefaultLimit = 20
	// ownerEarningNoteMaxChars bounds the admin reverse note.
	ownerEarningNoteMaxChars = 500

	OwnerEarningStatusFrozen    = "frozen"
	OwnerEarningStatusAvailable = "available"
	OwnerEarningStatusReversed  = "reversed"
	OwnerEarningStatusConverted = "converted"

	// MarketEarningCodePrefix + entry id is the redeem_codes code of a
	// convert's history row (never a generated hex code).
	MarketEarningCodePrefix = "MKTEARN-"
)

var (
	ErrOwnerEarningInvalidAmount    = infraerrors.BadRequest("MARKET_EARNING_INVALID_AMOUNT", "amount must be a positive number with at most 2 decimal places")
	ErrOwnerEarningInvalidRequestID = infraerrors.BadRequest("MARKET_EARNING_INVALID_REQUEST_ID", "request_id must be a UUID")
	ErrOwnerEarningBelowMinimum     = infraerrors.BadRequest("MARKET_EARNING_BELOW_MINIMUM", "amount is below the minimum conversion")
	ErrOwnerEarningInsufficient     = infraerrors.Conflict("MARKET_EARNING_INSUFFICIENT", "amount exceeds the available earnings")
	ErrOwnerEarningConvertConflict  = infraerrors.Conflict("MARKET_EARNING_CONVERT_CONFLICT", "request_id was already used with a different amount")
	ErrOwnerEarningReverseScope     = infraerrors.BadRequest("MARKET_EARNING_REVERSE_SCOPE_REQUIRED", "account_id or owner_user_id is required")
	ErrOwnerEarningReverseAsOf      = infraerrors.BadRequest("MARKET_EARNING_REVERSE_AS_OF_INVALID", "as_of is a required RFC3339 time not later than the server time; use the as_of returned by GET /api/v1/admin/market/earnings")
	ErrOwnerEarningInvalidQuery     = infraerrors.BadRequest("MARKET_EARNING_INVALID_QUERY", "invalid cursor, limit or owner")
	ErrOwnerEarningUnavailable      = infraerrors.ServiceUnavailable("MARKET_EARNING_UNAVAILABLE", "owner earnings are unavailable")
)

// OwnerEarningBalances are one owner's ledger sums at AsOf.
type OwnerEarningBalances struct {
	Frozen    decimal.Decimal
	Available decimal.Decimal
	Converted decimal.Decimal // positive total converted to balance
	Reversed  decimal.Decimal // positive total reversed
	Accrued   decimal.Decimal
	AsOf      time.Time
}

// OwnerEarningEntry is one ledger row with its display facts.
type OwnerEarningEntry struct {
	ID                int64
	OwnerUserID       int64
	Kind              string
	Amount            decimal.Decimal
	AvailableAt       *time.Time
	CreatedAt         time.Time
	Status            string
	AccountID         *int64
	Model             *string
	SettlementEventID *int64
	ReversesEntryID   *int64
	ConvertRequestID  *string
	AdminUserID       *int64
	Note              *string
}

// OwnerEarningConvertInput is a validated conversion request.
type OwnerEarningConvertInput struct {
	OwnerUserID int64
	Amount      decimal.Decimal
	RequestID   string
}

// OwnerEarningConvertRecord is the stored conversion (or the original one on
// a replay).
type OwnerEarningConvertRecord struct {
	EntryID   int64
	Amount    decimal.Decimal
	RequestID string
	CreatedAt time.Time
	Replayed  bool
}

// OwnerEarningReverseInput: AccountID and/or OwnerUserID scope the reverse
// (both = that owner's earnings on that account); AsOf is a required cutoff,
// not later than the database time: only accrues recorded at or before it
// are in scope (see the scope promise above — not an idempotency key).
type OwnerEarningReverseInput struct {
	AccountID   *int64
	OwnerUserID *int64
	AsOf        time.Time
	AdminUserID int64
	Note        string
}

type OwnerEarningReverseOwner struct {
	OwnerUserID int64  `json:"owner_user_id"`
	Count       int    `json:"count"`
	Amount      string `json:"amount"`
}

// OwnerEarningReverseResult is the body of the admin reverse.
type OwnerEarningReverseResult struct {
	// Cutoff echoes the request's as_of; AsOf is the database time the
	// frozen check used (null when nothing was in scope).
	Cutoff         time.Time                  `json:"cutoff"`
	AsOf           *time.Time                 `json:"as_of"`
	ReversedCount  int                        `json:"reversed_count"`
	ReversedAmount string                     `json:"reversed_amount"`
	Owners         []OwnerEarningReverseOwner `json:"owners"`
}

// AdminOwnerEarningSummary is one owner row of the admin overview.
type AdminOwnerEarningSummary struct {
	OwnerUserID    int64  `json:"owner_user_id"`
	Email          string `json:"email"`
	Frozen         string `json:"frozen"`
	Available      string `json:"available"`
	ConvertedTotal string `json:"converted_total"`
	ReversedTotal  string `json:"reversed_total"`
	AccruedTotal   string `json:"accrued_total"`
}

// OwnerEarningRepository is the ledger store.
type OwnerEarningRepository interface {
	// Balances reads one owner's sums in one statement at the database time.
	Balances(ctx context.Context, ownerUserID int64) (*OwnerEarningBalances, error)
	// ListEntries pages the ledger newest first (id < beforeID when > 0).
	// ownerUserID 0 = every owner (admin).
	ListEntries(ctx context.Context, ownerUserID, beforeID int64, limit int) ([]OwnerEarningEntry, error)
	// Convert runs the whole conversion in one transaction (see above).
	Convert(ctx context.Context, in OwnerEarningConvertInput) (*OwnerEarningConvertRecord, error)
	// Reverse runs the admin reverse in one transaction (see above).
	Reverse(ctx context.Context, in OwnerEarningReverseInput) (*OwnerEarningReverseResult, error)
	// SaleAudit reads one admission with its settlement (if any):
	// ErrMarketAdmissionNotFound when the admission does not exist.
	SaleAudit(ctx context.Context, admissionID int64) (*MarketSaleAudit, error)
	// AdminSummaries lists owners with ledger rows (ownerUserID filters to
	// one), ordered by owner id.
	AdminSummaries(ctx context.Context, ownerUserID *int64, offset, limit int) ([]AdminOwnerEarningSummary, int64, time.Time, error)
}

// MarketSaleAudit is GET /api/v1/admin/market/admissions/:id: the
// admission, its admission-time price snapshot and the final settlement
// (model, charge input, actual charge, split). A snapshot price different
// from the final price is normal (the charge follows the ordinary recording
// price); only charged_amount ≠ owner_earning + platform_amount is an
// anomaly.
type MarketSaleAudit struct {
	Admission      MarketSaleAuditAdmission   `json:"admission"`
	AdmissionPrice *MarketPriceAudit          `json:"admission_price"`
	Settlement     *MarketSaleAuditSettlement `json:"settlement"`
}

type MarketSaleAuditAdmission struct {
	ID             int64     `json:"id"`
	AttemptID      string    `json:"attempt_id"`
	RequestRef     string    `json:"request_ref"`
	AccountID      int64     `json:"account_id"`
	GroupID        int64     `json:"group_id"`
	OwnerUserID    int64     `json:"owner_user_id"`
	OwnerEventID   int64     `json:"owner_event_id"`
	ListingVersion int64     `json:"listing_version"`
	BuyerUserID    int64     `json:"buyer_user_id"`
	APIKeyID       int64     `json:"api_key_id"`
	AdmittedAt     time.Time `json:"admitted_at"`
}

// MarketSaleAuditSettlement: Model is the usage log's model (null when the
// log is missing or expired). Amounts are exact decimal strings.
type MarketSaleAuditSettlement struct {
	EventID            int64     `json:"event_id"`
	RequestID          string    `json:"request_id"`
	Model              *string   `json:"model"`
	StandardCost       string    `json:"standard_cost"`
	ChargeInput        string    `json:"charge_input"`
	ChargedAmount      string    `json:"charged_amount"`
	OwnerEarning       string    `json:"owner_earning"`
	PlatformAmount     string    `json:"platform_amount"`
	PlatformCutRate    string    `json:"platform_cut_rate"`
	EarningZeroReason  *string   `json:"earning_zero_reason"`
	EarningAvailableAt time.Time `json:"earning_available_at"`
	SplitConserved     bool      `json:"split_conserved"`
	CreatedAt          time.Time `json:"created_at"`
}

// OwnerEarningsView is GET /api/v1/hosted/earnings. Amounts are exact
// decimal strings (USD).
type OwnerEarningsView struct {
	Frozen         string    `json:"frozen"`
	Available      string    `json:"available"`
	ConvertedTotal string    `json:"converted_total"`
	MinConvert     string    `json:"min_convert"`
	AsOf           time.Time `json:"as_of"`
}

// OwnerEarningEntryView is one row of GET /api/v1/hosted/earnings/entries:
// only the owner's own amount, status and time — no buyer, key, request or
// charged amount.
type OwnerEarningEntryView struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Amount      string     `json:"amount"`
	Status      string     `json:"status"`
	AvailableAt *time.Time `json:"available_at"`
	CreatedAt   time.Time  `json:"created_at"`
	AccountID   *int64     `json:"account_id"`
	Model       *string    `json:"model"`
}

type OwnerEarningEntriesPage struct {
	Items      []OwnerEarningEntryView `json:"items"`
	NextCursor *int64                  `json:"next_cursor"`
}

// AdminOwnerEarningEntryView adds the ledger references for administrators.
type AdminOwnerEarningEntryView struct {
	OwnerEarningEntryView
	OwnerUserID       int64   `json:"owner_user_id"`
	SettlementEventID *int64  `json:"settlement_event_id"`
	ReversesEntryID   *int64  `json:"reverses_entry_id"`
	ConvertRequestID  *string `json:"convert_request_id"`
	AdminUserID       *int64  `json:"admin_user_id"`
	Note              *string `json:"note"`
}

type AdminOwnerEarningEntriesPage struct {
	Items      []AdminOwnerEarningEntryView `json:"items"`
	NextCursor *int64                       `json:"next_cursor"`
}

type AdminOwnerEarningsPage struct {
	Items    []AdminOwnerEarningSummary `json:"items"`
	Total    int64                      `json:"total"`
	Page     int                        `json:"page"`
	PageSize int                        `json:"page_size"`
	AsOf     time.Time                  `json:"as_of"`
}

// OwnerEarningConvertView is POST /api/v1/hosted/earnings/convert.
type OwnerEarningConvertView struct {
	EntryID   int64     `json:"entry_id"`
	RequestID string    `json:"request_id"`
	Amount    string    `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
	Replayed  bool      `json:"replayed"`
}

type OwnerEarningService struct {
	repo                 OwnerEarningRepository
	settingRepo          SettingRepository
	authCacheInvalidator APIKeyAuthCacheInvalidator
	billingCacheService  *BillingCacheService
}

func NewOwnerEarningService(repo OwnerEarningRepository, settingRepo SettingRepository, authCacheInvalidator APIKeyAuthCacheInvalidator, billingCacheService *BillingCacheService) *OwnerEarningService {
	return &OwnerEarningService{repo: repo, settingRepo: settingRepo, authCacheInvalidator: authCacheInvalidator, billingCacheService: billingCacheService}
}

// ParseOwnerEarningConvertAmount accepts a plain positive decimal with at
// most two decimal places; more digits are rejected, never rounded.
func ParseOwnerEarningConvertAmount(raw string) (decimal.Decimal, error) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > maxRedeemPurchaseAmountChars || !redeemPurchaseAmountRe.MatchString(s) {
		return decimal.Decimal{}, ErrOwnerEarningInvalidAmount
	}
	d, err := decimal.NewFromString(s)
	if err != nil || !d.IsPositive() {
		return decimal.Decimal{}, ErrOwnerEarningInvalidAmount
	}
	return d, nil
}

// NormalizeOwnerEarningRequestID accepts a canonical UUID, lower-cased.
func NormalizeOwnerEarningRequestID(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if len(s) != 36 {
		return "", ErrOwnerEarningInvalidRequestID
	}
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		return "", ErrOwnerEarningInvalidRequestID
	}
	return id.String(), nil
}

func (s *OwnerEarningService) ready() error {
	if s == nil || s.repo == nil {
		return ErrOwnerEarningUnavailable
	}
	return nil
}

func (s *OwnerEarningService) minConvert(ctx context.Context) (decimal.Decimal, error) {
	if s.settingRepo == nil {
		return DefaultMarketMinConvertUSD, nil
	}
	values, err := s.settingRepo.GetMultiple(ctx, MarketSettingKeys())
	if err != nil {
		return decimal.Zero, err
	}
	return ParseMarketSettings(values).MinConvertUSD, nil
}

// Summary is the owner's earnings card.
func (s *OwnerEarningService) Summary(ctx context.Context, ownerUserID int64) (*OwnerEarningsView, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	b, err := s.repo.Balances(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	minConvert, err := s.minConvert(ctx)
	if err != nil {
		return nil, err
	}
	return &OwnerEarningsView{
		Frozen:         b.Frozen.String(),
		Available:      b.Available.String(),
		ConvertedTotal: b.Converted.String(),
		MinConvert:     minConvert.String(),
		AsOf:           b.AsOf,
	}, nil
}

func normalizeOwnerEarningPage(cursor int64, limit int) (int, error) {
	if cursor < 0 || limit < 0 || limit > OwnerEarningEntriesMaxLimit {
		return 0, ErrOwnerEarningInvalidQuery
	}
	if limit == 0 {
		limit = OwnerEarningEntriesDefaultLimit
	}
	return limit, nil
}

func ownerEarningEntryView(e OwnerEarningEntry) OwnerEarningEntryView {
	return OwnerEarningEntryView{
		ID:          e.ID,
		Kind:        e.Kind,
		Amount:      e.Amount.String(),
		Status:      e.Status,
		AvailableAt: e.AvailableAt,
		CreatedAt:   e.CreatedAt,
		AccountID:   e.AccountID,
		Model:       e.Model,
	}
}

func ownerEarningNextCursor(n, limit int, lastID int64) *int64 {
	if n < limit || n == 0 {
		return nil
	}
	return &lastID
}

// Entries pages the owner's own ledger (keyset by id, newest first).
func (s *OwnerEarningService) Entries(ctx context.Context, ownerUserID, cursor int64, limit int) (*OwnerEarningEntriesPage, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if ownerUserID <= 0 {
		return nil, ErrOwnerEarningInvalidQuery
	}
	limit, err := normalizeOwnerEarningPage(cursor, limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListEntries(ctx, ownerUserID, cursor, limit)
	if err != nil {
		return nil, err
	}
	out := &OwnerEarningEntriesPage{Items: make([]OwnerEarningEntryView, 0, len(rows))}
	for _, e := range rows {
		out.Items = append(out.Items, ownerEarningEntryView(e))
	}
	if len(rows) > 0 {
		out.NextCursor = ownerEarningNextCursor(len(rows), limit, rows[len(rows)-1].ID)
	}
	return out, nil
}

// Convert moves available earnings into the owner's balance.
func (s *OwnerEarningService) Convert(ctx context.Context, ownerUserID int64, rawAmount, rawRequestID string) (*OwnerEarningConvertView, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	amount, err := ParseOwnerEarningConvertAmount(rawAmount)
	if err != nil {
		return nil, err
	}
	requestID, err := NormalizeOwnerEarningRequestID(rawRequestID)
	if err != nil {
		return nil, err
	}
	rec, err := s.repo.Convert(ctx, OwnerEarningConvertInput{OwnerUserID: ownerUserID, Amount: amount, RequestID: requestID})
	if err != nil {
		return nil, err
	}
	// Also on a replay: the first call may have committed and lost its reply
	// before invalidating.
	s.invalidateBalanceCaches(ctx, ownerUserID)
	if !rec.Replayed {
		logger.L().Info("market earnings converted to balance",
			zap.Int64("owner_user_id", ownerUserID),
			zap.Int64("entry_id", rec.EntryID),
			zap.String("amount", rec.Amount.String()),
		)
	}
	return &OwnerEarningConvertView{
		EntryID:   rec.EntryID,
		RequestID: rec.RequestID,
		Amount:    rec.Amount.StringFixed(2),
		CreatedAt: rec.CreatedAt,
		Replayed:  rec.Replayed,
	}, nil
}

// invalidateBalanceCaches runs after the commit (as the stablecoin credit):
// the auth cache with its own deadline, the balance cache in the background.
func (s *OwnerEarningService) invalidateBalanceCaches(ctx context.Context, userID int64) {
	if s.authCacheInvalidator != nil {
		authCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), redeemAuthCacheInvalidateTimeout)
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(authCtx, userID)
		cancel()
	}
	if s.billingCacheService == nil {
		return
	}
	go func() {
		cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.billingCacheService.InvalidateUserBalance(cacheCtx, userID); err != nil {
			logger.L().Warn("invalidate balance cache after earnings conversion failed", zap.Int64("user_id", userID), zap.Error(err))
		}
	}()
}

// AdminOwners is the per-owner overview (ownerUserID filters to one owner).
func (s *OwnerEarningService) AdminOwners(ctx context.Context, ownerUserID *int64, page, pageSize int) (*AdminOwnerEarningsPage, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if ownerUserID != nil && *ownerUserID <= 0 {
		return nil, ErrOwnerEarningInvalidQuery
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > OwnerEarningEntriesMaxLimit {
		pageSize = OwnerEarningEntriesDefaultLimit
	}
	items, total, asOf, err := s.repo.AdminSummaries(ctx, ownerUserID, (page-1)*pageSize, pageSize)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []AdminOwnerEarningSummary{}
	}
	return &AdminOwnerEarningsPage{Items: items, Total: total, Page: page, PageSize: pageSize, AsOf: asOf}, nil
}

// AdminEntries pages the ledger (ownerUserID 0 = every owner).
func (s *OwnerEarningService) AdminEntries(ctx context.Context, ownerUserID, cursor int64, limit int) (*AdminOwnerEarningEntriesPage, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if ownerUserID < 0 {
		return nil, ErrOwnerEarningInvalidQuery
	}
	limit, err := normalizeOwnerEarningPage(cursor, limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListEntries(ctx, ownerUserID, cursor, limit)
	if err != nil {
		return nil, err
	}
	out := &AdminOwnerEarningEntriesPage{Items: make([]AdminOwnerEarningEntryView, 0, len(rows))}
	for _, e := range rows {
		out.Items = append(out.Items, AdminOwnerEarningEntryView{
			OwnerEarningEntryView: ownerEarningEntryView(e),
			OwnerUserID:           e.OwnerUserID,
			SettlementEventID:     e.SettlementEventID,
			ReversesEntryID:       e.ReversesEntryID,
			ConvertRequestID:      e.ConvertRequestID,
			AdminUserID:           e.AdminUserID,
			Note:                  e.Note,
		})
	}
	if len(rows) > 0 {
		out.NextCursor = ownerEarningNextCursor(len(rows), limit, rows[len(rows)-1].ID)
	}
	return out, nil
}

// AdminReverse reverses the frozen earnings of an account and/or owner.
func (s *OwnerEarningService) AdminReverse(ctx context.Context, in OwnerEarningReverseInput) (*OwnerEarningReverseResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if in.AccountID == nil && in.OwnerUserID == nil {
		return nil, ErrOwnerEarningReverseScope
	}
	if (in.AccountID != nil && *in.AccountID <= 0) || (in.OwnerUserID != nil && *in.OwnerUserID <= 0) {
		return nil, ErrOwnerEarningReverseScope
	}
	if in.AsOf.IsZero() {
		return nil, ErrOwnerEarningReverseAsOf
	}
	in.Note = strings.TrimSpace(in.Note)
	if len([]rune(in.Note)) > ownerEarningNoteMaxChars {
		return nil, infraerrors.BadRequest("MARKET_EARNING_NOTE_TOO_LONG", "note is too long")
	}
	res, err := s.repo.Reverse(ctx, in)
	if err != nil {
		return nil, err
	}
	if res.Owners == nil {
		res.Owners = []OwnerEarningReverseOwner{}
	}
	logger.L().Info("market earnings reversed",
		zap.Int64("admin_user_id", in.AdminUserID),
		zap.Any("account_id", in.AccountID),
		zap.Any("owner_user_id", in.OwnerUserID),
		zap.Int("count", res.ReversedCount),
		zap.String("amount", res.ReversedAmount),
	)
	return res, nil
}

// AdminSaleAudit reads one market admission with its settlement.
func (s *OwnerEarningService) AdminSaleAudit(ctx context.Context, admissionID int64) (*MarketSaleAudit, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if admissionID <= 0 {
		return nil, ErrOwnerEarningInvalidQuery
	}
	out, err := s.repo.SaleAudit(ctx, admissionID)
	if errors.Is(err, ErrMarketAdmissionNotFound) {
		return nil, ErrMarketSaleAuditNotFound
	}
	return out, err
}

// ErrMarketSaleAuditNotFound: no admission with that id.
var ErrMarketSaleAuditNotFound = infraerrors.NotFound("MARKET_ADMISSION_NOT_FOUND", "market admission not found")
