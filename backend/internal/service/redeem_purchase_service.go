package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// User-bought balance redeem codes ("buy a code for someone").
//
// A signed-in user spends balance (any balance, paid or not) on an ordinary
// unused 'balance' redeem code and hands it to anyone; the code is redeemed
// through the unchanged /redeem flow, by the buyer or by someone else.
//
// Settings (admin; the switch defaults off):
//   - redeem_purchase_enabled: new purchases. Listing and revoking keep working
//     when it is off, and replaying a request that already succeeded returns
//     the original purchase.
//   - redeem_purchase_min_amount / _max_amount: per purchase (default 1 / 500).
//   - redeem_purchase_window_max_amount / _window_max_count: rolling 24h caps
//     over all of the buyer's purchases, revoked ones included (1000 / 20).
//
// Money: a purchase is one transaction holding the buyer's user row lock:
// balance deduction, the code, the redeem_code_purchases row and a
// purchase_debit record in redeem_codes. A revoke is one transaction: expire
// the still-unused code, refund the buyer, mark the purchase revoked and write
// a purchase_refund record. Lock order: purchase = users row, then only new
// rows (never a lock on existing purchases or codes); revoke = purchase row,
// code, users row; redeem = code, users row. No cycle.
const (
	SettingKeyRedeemPurchaseEnabled         = "redeem_purchase_enabled"
	SettingKeyRedeemPurchaseMinAmount       = "redeem_purchase_min_amount"
	SettingKeyRedeemPurchaseMaxAmount       = "redeem_purchase_max_amount"
	SettingKeyRedeemPurchaseWindowMaxAmount = "redeem_purchase_window_max_amount"
	SettingKeyRedeemPurchaseWindowMaxCount  = "redeem_purchase_window_max_count"

	RedeemTypePurchaseDebit  = domain.RedeemTypePurchaseDebit
	RedeemTypePurchaseRefund = domain.RedeemTypePurchaseRefund

	// redeem_code_purchases.status
	RedeemPurchaseStatusActive  = "active"
	RedeemPurchaseStatusRevoked = "revoked"

	// Status the buyer sees for one purchase.
	RedeemPurchaseViewUnused  = "unused"
	RedeemPurchaseViewUsed    = "used"
	RedeemPurchaseViewRevoked = "revoked"
	// RedeemPurchaseViewUnknown: an active purchase whose code is neither
	// unused nor used (expired by hand in the database). Shown as is, never
	// disguised as one of the normal states.
	RedeemPurchaseViewUnknown = "unknown"

	// RedeemPurchaseWindow is the rolling window of the amount/count caps.
	RedeemPurchaseWindow = 24 * time.Hour

	DefaultRedeemPurchaseWindowMaxCount = 20
	MaxRedeemPurchaseWindowMaxCount     = 10000
	// maxRedeemPurchaseAmountChars bounds the amount text before parsing.
	maxRedeemPurchaseAmountChars = 32
)

var (
	DefaultRedeemPurchaseMinAmount       = decimal.NewFromInt(1)
	DefaultRedeemPurchaseMaxAmount       = decimal.NewFromInt(500)
	DefaultRedeemPurchaseWindowMaxAmount = decimal.NewFromInt(1000)
	// MaxRedeemPurchaseAmountSetting caps every amount setting.
	MaxRedeemPurchaseAmountSetting = decimal.NewFromInt(1000000)
)

var (
	ErrRedeemPurchaseDisabled          = infraerrors.Forbidden("REDEEM_PURCHASE_DISABLED", "buying redeem codes is not enabled on this site")
	ErrRedeemPurchaseInvalidRequestID  = infraerrors.BadRequest("REDEEM_PURCHASE_INVALID_REQUEST_ID", "request_id must be a UUID")
	ErrRedeemPurchaseInvalidAmount     = infraerrors.BadRequest("REDEEM_PURCHASE_INVALID_AMOUNT", "amount must be a positive number with at most 2 decimal places")
	ErrRedeemPurchaseAmountBelowMin    = infraerrors.BadRequest("REDEEM_PURCHASE_AMOUNT_BELOW_MIN", "amount is below the minimum")
	ErrRedeemPurchaseAmountAboveMax    = infraerrors.BadRequest("REDEEM_PURCHASE_AMOUNT_ABOVE_MAX", "amount is above the maximum")
	ErrRedeemPurchaseWindowAmountLimit = infraerrors.TooManyRequests("REDEEM_PURCHASE_WINDOW_AMOUNT_LIMIT", "24-hour purchase amount limit reached")
	ErrRedeemPurchaseWindowCountLimit  = infraerrors.TooManyRequests("REDEEM_PURCHASE_WINDOW_COUNT_LIMIT", "24-hour purchase count limit reached")
	ErrRedeemPurchaseRequestConflict   = infraerrors.Conflict("REDEEM_PURCHASE_REQUEST_CONFLICT", "request_id was already used with a different amount")
	ErrRedeemPurchaseNotFound          = infraerrors.NotFound("REDEEM_PURCHASE_NOT_FOUND", "purchase not found")
	ErrRedeemPurchaseCodeUsed          = infraerrors.Conflict("REDEEM_PURCHASE_CODE_USED", "the code has already been redeemed and cannot be revoked")
	ErrRedeemPurchaseInconsistent      = infraerrors.InternalServer("REDEEM_PURCHASE_INCONSISTENT", "the purchase is in an unexpected state, please contact support")
	// ErrRedeemPurchaseRecordImmutable rejects admin delete/expire of a
	// purchased code (active or revoked) or a purchase_debit/purchase_refund record.
	ErrRedeemPurchaseRecordImmutable = infraerrors.BadRequest("REDEEM_PURCHASE_RECORD_IMMUTABLE", "purchased codes and purchase records cannot be deleted or expired")

	// ErrRedeemPurchaseRequestRace: the (buyer, request_id) insert hit the
	// unique constraint (a concurrent duplicate committed first). Repository → service only.
	ErrRedeemPurchaseRequestRace = errors.New("redeem purchase: request_id already recorded")
	// ErrRedeemPurchaseCodeCollision: any other unique violation while
	// writing (a generated code already exists). Repository → service only.
	ErrRedeemPurchaseCodeCollision = errors.New("redeem purchase: generated code collision")
)

// IsRedeemPurchaseRecordType reports the purchase_debit / purchase_refund history types.
func IsRedeemPurchaseRecordType(codeType string) bool {
	return codeType == RedeemTypePurchaseDebit || codeType == RedeemTypePurchaseRefund
}

// RedeemPurchaseSettings are the admin switches (see the package comment above).
type RedeemPurchaseSettings struct {
	Enabled         bool
	MinAmount       decimal.Decimal
	MaxAmount       decimal.Decimal
	WindowMaxAmount decimal.Decimal
	WindowMaxCount  int
}

// RedeemPurchaseRecord is one purchase with its code, as stored.
type RedeemPurchaseRecord struct {
	ID           int64
	BuyerUserID  int64
	RedeemCodeID int64
	Amount       decimal.Decimal
	RequestID    string
	Status       string // active | revoked
	CreatedAt    time.Time
	RevokedAt    *time.Time

	Code       string
	CodeStatus string
	CodeUsedAt *time.Time
}

// RedeemPurchaseView is what the buyer sees for one purchase. It never
// carries who redeemed the code.
type RedeemPurchaseView struct {
	ID        int64
	Code      string
	Amount    decimal.Decimal
	Status    string // unused | used | revoked | unknown
	UsedAt    *time.Time
	CreatedAt time.Time
	RevokedAt *time.Time
}

// RedeemPurchaseConfigView is the buyer's purchase form state. The remaining
// values are a display snapshot; the purchase re-checks under the row lock.
type RedeemPurchaseConfigView struct {
	Enabled            bool
	MinAmount          decimal.Decimal
	MaxAmount          decimal.Decimal
	RemainingAmount24h decimal.Decimal
	RemainingCount24h  int
}

// RedeemPurchaseOverview is GET /redeem/purchases.
type RedeemPurchaseOverview struct {
	Config     RedeemPurchaseConfigView
	Items      []RedeemPurchaseView
	Pagination pagination.PaginationResult
}

// RedeemPurchaseBuyer is the buyer row read under its row lock.
type RedeemPurchaseBuyer struct {
	Status  string
	Balance decimal.Decimal
	// Now is the database clock read after the lock was granted.
	Now time.Time
}

// RedeemPurchaseCreateInput is everything the purchase writes.
type RedeemPurchaseCreateInput struct {
	BuyerUserID int64
	RequestID   string
	Amount      decimal.Decimal
	Code        string // the purchased (unused) balance code
	DebitCode   string // code column of the purchase_debit record
	Now         time.Time
}

type RedeemPurchaseRepository interface {
	RedeemPurchaseSettingsReader
	// LockBuyer: SELECT ... FROM users WHERE id = ? FOR UPDATE inside the
	// transaction carried by ctx (required). ErrUserNotFound when missing or
	// soft-deleted.
	LockBuyer(ctx context.Context, buyerID int64) (*RedeemPurchaseBuyer, error)
	// GetByRequestID is a plain read (no lock); nil, nil when absent.
	GetByRequestID(ctx context.Context, buyerID int64, requestID string) (*RedeemPurchaseRecord, error)
	// WindowUsage sums and counts the buyer's purchases created after since,
	// any status, with a plain read.
	WindowUsage(ctx context.Context, buyerID int64, since time.Time) (decimal.Decimal, int, error)
	// Create writes the purchase inside the transaction carried by ctx:
	// deducts the balance (never below zero → ErrInsufficientBalance), inserts
	// the code, the purchase row, the code's notes and the purchase_debit
	// record. Unique violations come back as ErrRedeemPurchaseRequestRace or
	// ErrRedeemPurchaseCodeCollision; the transaction must then be rolled back.
	Create(ctx context.Context, in RedeemPurchaseCreateInput) (*RedeemPurchaseRecord, error)
	// LockForRevoke: SELECT purchase ... FOR UPDATE (with the buyer check),
	// inside the transaction carried by ctx. ErrRedeemPurchaseNotFound.
	LockForRevoke(ctx context.Context, buyerID, purchaseID int64) (*RedeemPurchaseRecord, error)
	// ExpireUnusedCode: UPDATE redeem_codes SET status='expired' WHERE id=? AND
	// status='unused'; reports whether a row changed.
	ExpireUnusedCode(ctx context.Context, codeID int64) (bool, error)
	// GetCodeState is a plain read of the code's status and used_at.
	GetCodeState(ctx context.Context, codeID int64) (code, status string, usedAt *time.Time, err error)
	// Refund adds the amount back to the buyer, marks the purchase revoked at
	// now and inserts the purchase_refund record, inside the transaction
	// carried by ctx. ErrRedeemPurchaseCodeCollision on a code collision.
	Refund(ctx context.Context, rec *RedeemPurchaseRecord, refundCode string, now time.Time) error
	// Now reads the database clock (inside the transaction when there is one).
	Now(ctx context.Context) (time.Time, error)
	// ListByBuyer lists the buyer's purchases, newest first, with their codes.
	ListByBuyer(ctx context.Context, buyerID int64, params pagination.PaginationParams) ([]RedeemPurchaseRecord, *pagination.PaginationResult, error)
}

// RedeemPurchaseSettingsReader reads the admin switches. Inside a purchase it
// runs on the transaction's own connection (ctx carries the transaction):
// while the buyer row is locked no second pool connection is taken, so a
// burst of requests from one user queued on that lock cannot starve the pool
// the lock holder needs. A read error fails the request (fail closed).
type RedeemPurchaseSettingsReader interface {
	LoadRedeemPurchaseSettings(ctx context.Context) (RedeemPurchaseSettings, error)
}

// RedeemPurchaseTxRunner runs fn in one database transaction carried by
// txCtx; fn's error rolls it back, nil commits.
type RedeemPurchaseTxRunner func(ctx context.Context, fn func(txCtx context.Context) error) error

type RedeemPurchaseService struct {
	repo                 RedeemPurchaseRepository
	settings             RedeemPurchaseSettingsReader
	runTx                RedeemPurchaseTxRunner
	authCacheInvalidator APIKeyAuthCacheInvalidator
	billingCacheService  *BillingCacheService
	newCode              func() (string, error)
}

func NewRedeemPurchaseService(
	repo RedeemPurchaseRepository,
	entClient *dbent.Client,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
	billingCacheService *BillingCacheService,
) *RedeemPurchaseService {
	return NewRedeemPurchaseServiceWithTx(repo, repo, EntRedeemPurchaseTxRunner(entClient), authCacheInvalidator, billingCacheService)
}

// NewRedeemPurchaseServiceWithTx takes the settings reader and the
// transaction runner explicitly (tests run the flow against stubs).
func NewRedeemPurchaseServiceWithTx(
	repo RedeemPurchaseRepository,
	settings RedeemPurchaseSettingsReader,
	runTx RedeemPurchaseTxRunner,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
	billingCacheService *BillingCacheService,
) *RedeemPurchaseService {
	return &RedeemPurchaseService{
		repo:                 repo,
		settings:             settings,
		runTx:                runTx,
		authCacheInvalidator: authCacheInvalidator,
		billingCacheService:  billingCacheService,
		newCode:              GenerateRedeemCode,
	}
}

// EntRedeemPurchaseTxRunner runs fn in one ent transaction carried by txCtx.
func EntRedeemPurchaseTxRunner(client *dbent.Client) RedeemPurchaseTxRunner {
	return func(ctx context.Context, fn func(txCtx context.Context) error) error {
		if client == nil {
			return errors.New("redeem purchase: database client is not configured")
		}
		tx, err := client.Tx(ctx)
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		if err := fn(dbent.NewTxContext(ctx, tx)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit transaction: %w", err)
		}
		return nil
	}
}

var redeemPurchaseAmountRe = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,2})?$`)

// ParseRedeemPurchaseAmount accepts a plain positive decimal written with at
// most two decimal places ("10", "10.5", "10.50"). More digits ("10.500",
// "10.001"), exponents and signs are rejected, never rounded. The result is
// the one value used for every column and comparison.
func ParseRedeemPurchaseAmount(raw string) (decimal.Decimal, error) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > maxRedeemPurchaseAmountChars || !redeemPurchaseAmountRe.MatchString(s) {
		return decimal.Decimal{}, ErrRedeemPurchaseInvalidAmount
	}
	d, err := decimal.NewFromString(s)
	if err != nil || !d.IsPositive() {
		return decimal.Decimal{}, ErrRedeemPurchaseInvalidAmount
	}
	return d, nil
}

// NormalizeRedeemPurchaseRequestID accepts a canonical 36-character UUID
// (any case) and returns it lower-cased.
func NormalizeRedeemPurchaseRequestID(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if len(s) != 36 {
		return "", ErrRedeemPurchaseInvalidRequestID
	}
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		return "", ErrRedeemPurchaseInvalidRequestID
	}
	return id.String(), nil
}

// redeemPurchaseViewStatus maps a stored purchase to the buyer's status. An
// active purchase whose code is neither unused nor used is an anomaly: it is
// logged and shown as "unknown".
func redeemPurchaseViewStatus(rec *RedeemPurchaseRecord) string {
	if rec.Status == RedeemPurchaseStatusRevoked {
		return RedeemPurchaseViewRevoked
	}
	if rec.Status == RedeemPurchaseStatusActive {
		switch rec.CodeStatus {
		case StatusUnused:
			return RedeemPurchaseViewUnused
		case StatusUsed:
			return RedeemPurchaseViewUsed
		}
	}
	logger.LegacyPrintf("service.redeem_purchase", "[RedeemPurchase] anomaly: purchase_id=%d status=%q code_id=%d code_status=%q",
		rec.ID, rec.Status, rec.RedeemCodeID, rec.CodeStatus)
	return RedeemPurchaseViewUnknown
}

func redeemPurchaseView(rec *RedeemPurchaseRecord) RedeemPurchaseView {
	v := RedeemPurchaseView{
		ID:        rec.ID,
		Code:      rec.Code,
		Amount:    rec.Amount,
		Status:    redeemPurchaseViewStatus(rec),
		CreatedAt: rec.CreatedAt,
		RevokedAt: rec.RevokedAt,
	}
	if v.Status == RedeemPurchaseViewUsed {
		v.UsedAt = rec.CodeUsedAt
	}
	return v
}

// Overview lists the buyer's purchases and the form state. It works with the
// switch off: bought codes stay visible and revocable.
func (s *RedeemPurchaseService) Overview(ctx context.Context, buyerID int64, params pagination.PaginationParams) (*RedeemPurchaseOverview, error) {
	cfg, err := s.settings.LoadRedeemPurchaseSettings(ctx)
	if err != nil {
		return nil, err
	}
	now, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	sum, count, err := s.repo.WindowUsage(ctx, buyerID, now.Add(-RedeemPurchaseWindow))
	if err != nil {
		return nil, err
	}
	remainingAmount := cfg.WindowMaxAmount.Sub(sum)
	if remainingAmount.IsNegative() {
		remainingAmount = decimal.Zero
	}
	remainingCount := cfg.WindowMaxCount - count
	if remainingCount < 0 {
		remainingCount = 0
	}
	records, page, err := s.repo.ListByBuyer(ctx, buyerID, params)
	if err != nil {
		return nil, err
	}
	out := &RedeemPurchaseOverview{
		Config: RedeemPurchaseConfigView{
			Enabled:            cfg.Enabled,
			MinAmount:          cfg.MinAmount,
			MaxAmount:          cfg.MaxAmount,
			RemainingAmount24h: remainingAmount,
			RemainingCount24h:  remainingCount,
		},
		Items: make([]RedeemPurchaseView, 0, len(records)),
	}
	for i := range records {
		out.Items = append(out.Items, redeemPurchaseView(&records[i]))
	}
	if page != nil {
		out.Pagination = *page
	}
	return out, nil
}

// Purchase buys one balance code. The checks run in this order, all under
// the buyer's row lock: buyer active → replay of request_id (same amount →
// the original purchase, never charged again; different amount → conflict)
// → switch → per-purchase range → rolling 24h count and amount → balance.
// It returns the purchase and whether it was a replay.
func (s *RedeemPurchaseService) Purchase(ctx context.Context, buyerID int64, rawAmount, rawRequestID string) (*RedeemPurchaseView, bool, error) {
	requestID, err := NormalizeRedeemPurchaseRequestID(rawRequestID)
	if err != nil {
		return nil, false, err
	}
	amount, err := ParseRedeemPurchaseAmount(rawAmount)
	if err != nil {
		return nil, false, err
	}

	// A random 32-hex collision is practically impossible; retry the whole
	// transaction once with new codes anyway.
	for attempt := 0; ; attempt++ {
		view, replayed, err := s.purchaseOnce(ctx, buyerID, requestID, amount)
		switch {
		case err == nil:
			if !replayed {
				s.invalidateBuyerCaches(ctx, buyerID)
			}
			return view, replayed, nil
		case errors.Is(err, ErrRedeemPurchaseRequestRace):
			// A concurrent request with the same request_id committed first;
			// this transaction was rolled back. Answer like a replay.
			return s.replayAfterRace(ctx, buyerID, requestID, amount)
		case errors.Is(err, ErrRedeemPurchaseCodeCollision) && attempt == 0:
			continue
		default:
			if !isRedeemPurchaseExpectedError(err) {
				logger.LegacyPrintf("service.redeem_purchase", "[RedeemPurchase] purchase failed: user_id=%d err=%v", buyerID, err)
			}
			return nil, false, err
		}
	}
}

func (s *RedeemPurchaseService) purchaseOnce(ctx context.Context, buyerID int64, requestID string, amount decimal.Decimal) (*RedeemPurchaseView, bool, error) {
	var view RedeemPurchaseView
	replayed := false
	err := s.runTx(ctx, func(txCtx context.Context) error {
		buyer, err := s.repo.LockBuyer(txCtx, buyerID)
		if err != nil {
			return err
		}
		if buyer.Status != StatusActive {
			return ErrUserNotActive
		}
		// Replay first, before the switch and the limits: a retried request
		// that already succeeded gets its purchase back, whatever changed since.
		existing, err := s.repo.GetByRequestID(txCtx, buyerID, requestID)
		if err != nil {
			return err
		}
		if existing != nil {
			if !existing.Amount.Equal(amount) {
				return ErrRedeemPurchaseRequestConflict
			}
			view, replayed = redeemPurchaseView(existing), true
			return nil
		}
		// Read the switches only now, under the lock: a request that queued
		// behind another one sees a switch-off or a lowered cap made meanwhile.
		cfg, err := s.settings.LoadRedeemPurchaseSettings(txCtx)
		if err != nil {
			return err
		}
		if !cfg.Enabled {
			return ErrRedeemPurchaseDisabled
		}
		if amount.LessThan(cfg.MinAmount) {
			return ErrRedeemPurchaseAmountBelowMin
		}
		if amount.GreaterThan(cfg.MaxAmount) {
			return ErrRedeemPurchaseAmountAboveMax
		}
		sum, count, err := s.repo.WindowUsage(txCtx, buyerID, buyer.Now.Add(-RedeemPurchaseWindow))
		if err != nil {
			return err
		}
		if count+1 > cfg.WindowMaxCount {
			return ErrRedeemPurchaseWindowCountLimit
		}
		if sum.Add(amount).GreaterThan(cfg.WindowMaxAmount) {
			return ErrRedeemPurchaseWindowAmountLimit
		}
		if buyer.Balance.LessThan(amount) {
			return ErrInsufficientBalance
		}
		code, err := s.newCode()
		if err != nil {
			return fmt.Errorf("generate code: %w", err)
		}
		debitCode, err := s.newCode()
		if err != nil {
			return fmt.Errorf("generate record code: %w", err)
		}
		rec, err := s.repo.Create(txCtx, RedeemPurchaseCreateInput{
			BuyerUserID: buyerID,
			RequestID:   requestID,
			Amount:      amount,
			Code:        code,
			DebitCode:   debitCode,
			Now:         buyer.Now,
		})
		if err != nil {
			return err
		}
		view = redeemPurchaseView(rec)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &view, replayed, nil
}

func (s *RedeemPurchaseService) replayAfterRace(ctx context.Context, buyerID int64, requestID string, amount decimal.Decimal) (*RedeemPurchaseView, bool, error) {
	existing, err := s.repo.GetByRequestID(ctx, buyerID, requestID)
	if err != nil {
		return nil, false, err
	}
	if existing == nil {
		return nil, false, fmt.Errorf("redeem purchase: request_id %s conflicted but no purchase was found", requestID)
	}
	if !existing.Amount.Equal(amount) {
		return nil, false, ErrRedeemPurchaseRequestConflict
	}
	view := redeemPurchaseView(existing)
	return &view, true, nil
}

// Revoke expires the buyer's still-unused code and refunds the amount, in one
// transaction (purchase row → code → user row). Revoking a revoked purchase
// returns it unchanged (no second refund). Works with the switch off.
func (s *RedeemPurchaseService) Revoke(ctx context.Context, buyerID, purchaseID int64) (*RedeemPurchaseView, error) {
	if purchaseID <= 0 {
		return nil, ErrRedeemPurchaseNotFound
	}
	for attempt := 0; ; attempt++ {
		view, refunded, err := s.revokeOnce(ctx, buyerID, purchaseID)
		switch {
		case err == nil:
			if refunded {
				s.invalidateBuyerCaches(ctx, buyerID)
			}
			return view, nil
		case errors.Is(err, ErrRedeemPurchaseCodeCollision) && attempt == 0:
			continue
		default:
			if !isRedeemPurchaseExpectedError(err) {
				logger.LegacyPrintf("service.redeem_purchase", "[RedeemPurchase] revoke failed: user_id=%d purchase_id=%d err=%v", buyerID, purchaseID, err)
			}
			return nil, err
		}
	}
}

func (s *RedeemPurchaseService) revokeOnce(ctx context.Context, buyerID, purchaseID int64) (*RedeemPurchaseView, bool, error) {
	var view RedeemPurchaseView
	refunded := false
	err := s.runTx(ctx, func(txCtx context.Context) error {
		rec, err := s.repo.LockForRevoke(txCtx, buyerID, purchaseID)
		if err != nil {
			return err
		}
		if rec.Status == RedeemPurchaseStatusRevoked {
			view = redeemPurchaseView(rec)
			return nil
		}
		if rec.Status != RedeemPurchaseStatusActive {
			logger.LegacyPrintf("service.redeem_purchase", "[RedeemPurchase] anomaly: purchase_id=%d status=%q", rec.ID, rec.Status)
			return ErrRedeemPurchaseInconsistent
		}
		expired, err := s.repo.ExpireUnusedCode(txCtx, rec.RedeemCodeID)
		if err != nil {
			return err
		}
		if !expired {
			_, codeStatus, _, err := s.repo.GetCodeState(txCtx, rec.RedeemCodeID)
			if err != nil {
				return err
			}
			if codeStatus == StatusUsed {
				return ErrRedeemPurchaseCodeUsed
			}
			logger.LegacyPrintf("service.redeem_purchase", "[RedeemPurchase] anomaly: active purchase_id=%d code_id=%d code_status=%q",
				rec.ID, rec.RedeemCodeID, codeStatus)
			return ErrRedeemPurchaseInconsistent
		}
		now, err := s.repo.Now(txCtx)
		if err != nil {
			return err
		}
		refundCode, err := s.newCode()
		if err != nil {
			return fmt.Errorf("generate record code: %w", err)
		}
		if err := s.repo.Refund(txCtx, rec, refundCode, now); err != nil {
			return err
		}
		rec.Status = RedeemPurchaseStatusRevoked
		rec.RevokedAt = &now
		rec.CodeStatus = StatusExpired
		view = redeemPurchaseView(rec)
		refunded = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &view, refunded, nil
}

// invalidateBuyerCaches runs after commit with its own short timeout, so a
// cancelled request cannot skip it; failures are only logged and never turn
// a committed purchase or refund into an error.
func (s *RedeemPurchaseService) invalidateBuyerCaches(ctx context.Context, buyerID int64) {
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(cacheCtx, buyerID)
	}
	if s.billingCacheService != nil {
		if err := s.billingCacheService.InvalidateUserBalance(cacheCtx, buyerID); err != nil {
			logger.LegacyPrintf("service.redeem_purchase", "[RedeemPurchase] balance cache invalidation failed: user_id=%d err=%v", buyerID, err)
		}
	}
}

func isRedeemPurchaseExpectedError(err error) bool {
	for _, target := range []error{
		ErrRedeemPurchaseDisabled, ErrRedeemPurchaseInvalidRequestID, ErrRedeemPurchaseInvalidAmount,
		ErrRedeemPurchaseAmountBelowMin, ErrRedeemPurchaseAmountAboveMax,
		ErrRedeemPurchaseWindowAmountLimit, ErrRedeemPurchaseWindowCountLimit,
		ErrRedeemPurchaseRequestConflict, ErrRedeemPurchaseNotFound, ErrRedeemPurchaseCodeUsed,
		ErrInsufficientBalance, ErrUserNotActive, ErrUserNotFound,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
