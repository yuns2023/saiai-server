package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/ent/redeemcodepurchase"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

// redeemPurchaseRequestConstraint is UNIQUE (buyer_user_id, request_id) from
// migration 132.
const redeemPurchaseRequestConstraint = "redeem_code_purchases_buyer_request_key"

// redeemCodeNotPurchased excludes codes bought by users with their balance
// (any purchase status): spending balance on a code is not a top-up, whoever
// redeems it.
func redeemCodeNotPurchased() predicate.RedeemCode {
	return predicate.RedeemCode(func(s *entsql.Selector) {
		s.Where(entsql.ExprP("NOT EXISTS (SELECT 1 FROM redeem_code_purchases p WHERE p.redeem_code_id = " + s.C(redeemcode.FieldID) + ")"))
	})
}

type redeemPurchaseRepository struct {
	client *dbent.Client
}

func NewRedeemPurchaseRepository(client *dbent.Client) service.RedeemPurchaseRepository {
	return &redeemPurchaseRepository{client: client}
}

// redeemPurchaseTxClient returns the client of the transaction carried by ctx;
// locking and writing methods refuse to run outside one.
func redeemPurchaseTxClient(ctx context.Context, op string) (*dbent.Client, error) {
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return nil, fmt.Errorf("redeem purchase: %s requires a transaction", op)
	}
	return tx.Client(), nil
}

// classifyRedeemPurchaseWriteErr maps unique violations: the (buyer,
// request_id) constraint → a racing duplicate request; anything else (the
// code column) → a generated-code collision.
func classifyRedeemPurchaseWriteErr(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		if pqErr.Constraint == redeemPurchaseRequestConstraint {
			return fmt.Errorf("%w: %v", service.ErrRedeemPurchaseRequestRace, err)
		}
		return fmt.Errorf("%w: %v", service.ErrRedeemPurchaseCodeCollision, err)
	}
	return err
}

// LoadRedeemPurchaseSettings reads the switches from the settings table on the
// connection carried by ctx (the purchase transaction's own connection when
// there is one), never a second pool connection while the buyer is locked.
func (r *redeemPurchaseRepository) LoadRedeemPurchaseSettings(ctx context.Context) (service.RedeemPurchaseSettings, error) {
	rows, err := clientFromContext(ctx, r.client).Setting.Query().
		Where(setting.KeyIn(service.RedeemPurchaseSettingKeys()...)).
		All(ctx)
	if err != nil {
		return service.RedeemPurchaseSettings{}, err
	}
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		values[row.Key] = row.Value
	}
	return service.ParseRedeemPurchaseSettings(values), nil
}

func (r *redeemPurchaseRepository) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	if err := scanSingleRow(ctx, clientFromContext(ctx, r.client), `SELECT clock_timestamp()`, nil, &now); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

func (r *redeemPurchaseRepository) LockBuyer(ctx context.Context, buyerID int64) (*service.RedeemPurchaseBuyer, error) {
	client, err := redeemPurchaseTxClient(ctx, "LockBuyer")
	if err != nil {
		return nil, err
	}
	var status, balance string
	err = scanSingleRow(ctx, client, `
		SELECT status, balance::text
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE
	`, []any{buyerID}, &status, &balance)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	bal, err := decimal.NewFromString(balance)
	if err != nil {
		return nil, fmt.Errorf("parse balance: %w", err)
	}
	// Read the clock only now: a purchase that waited for the lock must not
	// look older than it is to the rolling window.
	now, err := r.Now(ctx)
	if err != nil {
		return nil, err
	}
	return &service.RedeemPurchaseBuyer{Status: status, Balance: bal, Now: now}, nil
}

func (r *redeemPurchaseRepository) GetByRequestID(ctx context.Context, buyerID int64, requestID string) (*service.RedeemPurchaseRecord, error) {
	client := clientFromContext(ctx, r.client)
	p, err := client.RedeemCodePurchase.Query().
		Where(redeemcodepurchase.BuyerUserIDEQ(buyerID), redeemcodepurchase.RequestIDEQ(requestID)).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r.withCode(ctx, client, p)
}

func (r *redeemPurchaseRepository) WindowUsage(ctx context.Context, buyerID int64, since time.Time) (decimal.Decimal, int, error) {
	var sum string
	var count int
	err := scanSingleRow(ctx, clientFromContext(ctx, r.client), `
		SELECT COALESCE(SUM(amount), 0)::text, COUNT(*)
		FROM redeem_code_purchases
		WHERE buyer_user_id = $1 AND created_at > $2
	`, []any{buyerID, since}, &sum, &count)
	if err != nil {
		return decimal.Zero, 0, err
	}
	total, err := decimal.NewFromString(sum)
	if err != nil {
		return decimal.Zero, 0, fmt.Errorf("parse window sum: %w", err)
	}
	return total, count, nil
}

func (r *redeemPurchaseRepository) Create(ctx context.Context, in service.RedeemPurchaseCreateInput) (*service.RedeemPurchaseRecord, error) {
	client, err := redeemPurchaseTxClient(ctx, "Create")
	if err != nil {
		return nil, err
	}
	amount := in.Amount.StringFixed(2)

	// The buyer row is already locked (LockBuyer); the condition keeps the
	// balance from going below zero whatever the caller checked.
	var balanceAfter string
	err = scanSingleRow(ctx, client, `
		UPDATE users
		SET balance = balance - $2::numeric, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL AND balance >= $2::numeric
		RETURNING balance::text
	`, []any{in.BuyerUserID, amount}, &balanceAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrInsufficientBalance
	}
	if err != nil {
		return nil, err
	}

	var codeID int64
	err = scanSingleRow(ctx, client, `
		INSERT INTO redeem_codes (code, type, value, status, validity_days, created_at)
		VALUES ($1, $2, $3::numeric, $4, 30, $5)
		RETURNING id
	`, []any{in.Code, service.RedeemTypeBalance, amount, service.StatusUnused, in.Now}, &codeID)
	if err != nil {
		return nil, classifyRedeemPurchaseWriteErr(err)
	}

	p, err := client.RedeemCodePurchase.Create().
		SetBuyerUserID(in.BuyerUserID).
		SetRedeemCodeID(codeID).
		SetAmount(in.Amount).
		SetRequestID(in.RequestID).
		SetStatus(service.RedeemPurchaseStatusActive).
		SetCreatedAt(in.Now).
		Save(ctx)
	if err != nil {
		return nil, classifyRedeemPurchaseWriteErr(err)
	}

	notes := fmt.Sprintf("user purchase #%d", p.ID)
	// Only the notes column: never a whole-row write of the code.
	if _, err := client.ExecContext(ctx, `UPDATE redeem_codes SET notes = $2 WHERE id = $1`, codeID, notes); err != nil {
		return nil, err
	}
	if _, err := client.ExecContext(ctx, `
		INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, notes, validity_days, created_at)
		VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, 30, $6)
	`, in.DebitCode, service.RedeemTypePurchaseDebit, in.Amount.Neg().StringFixed(2), service.StatusUsed, in.BuyerUserID, in.Now, notes); err != nil {
		return nil, classifyRedeemPurchaseWriteErr(err)
	}

	return &service.RedeemPurchaseRecord{
		ID:           p.ID,
		BuyerUserID:  p.BuyerUserID,
		RedeemCodeID: codeID,
		Amount:       p.Amount,
		RequestID:    p.RequestID,
		Status:       p.Status,
		CreatedAt:    p.CreatedAt,
		Code:         in.Code,
		CodeStatus:   service.StatusUnused,
	}, nil
}

func (r *redeemPurchaseRepository) LockForRevoke(ctx context.Context, buyerID, purchaseID int64) (*service.RedeemPurchaseRecord, error) {
	client, err := redeemPurchaseTxClient(ctx, "LockForRevoke")
	if err != nil {
		return nil, err
	}
	p, err := client.RedeemCodePurchase.Query().
		Where(redeemcodepurchase.IDEQ(purchaseID), redeemcodepurchase.BuyerUserIDEQ(buyerID)).
		ForUpdate().
		Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, service.ErrRedeemPurchaseNotFound
	}
	if err != nil {
		return nil, err
	}
	// Plain read of the code; ExpireUnusedCode takes its row lock next.
	return r.withCode(ctx, client, p)
}

func (r *redeemPurchaseRepository) ExpireUnusedCode(ctx context.Context, codeID int64) (bool, error) {
	client, err := redeemPurchaseTxClient(ctx, "ExpireUnusedCode")
	if err != nil {
		return false, err
	}
	res, err := client.ExecContext(ctx,
		`UPDATE redeem_codes SET status = $2 WHERE id = $1 AND status = $3`,
		codeID, service.StatusExpired, service.StatusUnused)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (r *redeemPurchaseRepository) GetCodeState(ctx context.Context, codeID int64) (string, string, *time.Time, error) {
	c, err := clientFromContext(ctx, r.client).RedeemCode.Query().
		Where(redeemcode.IDEQ(codeID)).
		Select(redeemcode.FieldID, redeemcode.FieldCode, redeemcode.FieldStatus, redeemcode.FieldUsedAt).
		Only(ctx)
	if err != nil {
		return "", "", nil, err
	}
	return c.Code, c.Status, c.UsedAt, nil
}

func (r *redeemPurchaseRepository) Refund(ctx context.Context, rec *service.RedeemPurchaseRecord, refundCode string, now time.Time) error {
	client, err := redeemPurchaseTxClient(ctx, "Refund")
	if err != nil {
		return err
	}
	amount := rec.Amount.StringFixed(2)
	// A refund is never refused for an overdrawn balance.
	res, err := client.ExecContext(ctx, `
		UPDATE users SET balance = balance + $2::numeric, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`, rec.BuyerUserID, amount)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return service.ErrUserNotFound
	}
	n, err := client.RedeemCodePurchase.Update().
		Where(redeemcodepurchase.IDEQ(rec.ID), redeemcodepurchase.StatusEQ(service.RedeemPurchaseStatusActive)).
		SetStatus(service.RedeemPurchaseStatusRevoked).
		SetRevokedAt(now).
		Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("redeem purchase %d is no longer active", rec.ID)
	}
	if _, err := client.ExecContext(ctx, `
		INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, notes, validity_days, created_at)
		VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, 30, $6)
	`, refundCode, service.RedeemTypePurchaseRefund, amount, service.StatusUsed, rec.BuyerUserID, now,
		fmt.Sprintf("user purchase #%d revoked", rec.ID)); err != nil {
		return classifyRedeemPurchaseWriteErr(err)
	}
	return nil
}

func (r *redeemPurchaseRepository) ListByBuyer(ctx context.Context, buyerID int64, params pagination.PaginationParams) ([]service.RedeemPurchaseRecord, *pagination.PaginationResult, error) {
	client := clientFromContext(ctx, r.client)
	q := client.RedeemCodePurchase.Query().Where(redeemcodepurchase.BuyerUserIDEQ(buyerID))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := q.
		Order(dbent.Desc(redeemcodepurchase.FieldCreatedAt), dbent.Desc(redeemcodepurchase.FieldID)).
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	codes := map[int64]*dbent.RedeemCode{}
	if len(rows) > 0 {
		ids := make([]int64, 0, len(rows))
		for _, p := range rows {
			ids = append(ids, p.RedeemCodeID)
		}
		list, err := client.RedeemCode.Query().
			Where(redeemcode.IDIn(ids...)).
			Select(redeemcode.FieldID, redeemcode.FieldCode, redeemcode.FieldStatus, redeemcode.FieldUsedAt).
			All(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, c := range list {
			codes[c.ID] = c
		}
	}
	out := make([]service.RedeemPurchaseRecord, 0, len(rows))
	for _, p := range rows {
		out = append(out, redeemPurchaseRecord(p, codes[p.RedeemCodeID]))
	}
	return out, paginationResultFromTotal(int64(total), params), nil
}

func (r *redeemPurchaseRepository) withCode(ctx context.Context, client *dbent.Client, p *dbent.RedeemCodePurchase) (*service.RedeemPurchaseRecord, error) {
	c, err := client.RedeemCode.Query().
		Where(redeemcode.IDEQ(p.RedeemCodeID)).
		Select(redeemcode.FieldID, redeemcode.FieldCode, redeemcode.FieldStatus, redeemcode.FieldUsedAt).
		Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return nil, err
	}
	rec := redeemPurchaseRecord(p, c)
	return &rec, nil
}

// redeemPurchaseRecord joins a purchase with its code; a missing code (which
// the foreign key prevents) leaves the code fields empty, shown as "unknown".
func redeemPurchaseRecord(p *dbent.RedeemCodePurchase, c *dbent.RedeemCode) service.RedeemPurchaseRecord {
	rec := service.RedeemPurchaseRecord{
		ID:           p.ID,
		BuyerUserID:  p.BuyerUserID,
		RedeemCodeID: p.RedeemCodeID,
		Amount:       p.Amount,
		RequestID:    p.RequestID,
		Status:       p.Status,
		CreatedAt:    p.CreatedAt,
		RevokedAt:    p.RevokedAt,
	}
	if c != nil {
		rec.Code = c.Code
		rec.CodeStatus = c.Status
		rec.CodeUsedAt = c.UsedAt
	}
	return rec
}
