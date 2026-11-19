package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// Market (shared_sale) settlement inside the billing transaction (P3a BE3).
// See service/market_settlement.go for the rules. Everything runs in the
// billing transaction itself — no savepoint, no budget skip: any error rolls
// back the claim, the effects, the event and the ledger entry together.

// usageSettlementAdmissionConstraint is the partial UNIQUE index on
// usage_settlement_events.admission_id (migration 134).
const usageSettlementAdmissionConstraint = "usage_settlement_events_admission_key"

// marketBeforeCommit runs just before a market Apply commits (tests only).
var marketBeforeCommit func()

// classifyMarketCommitError classifies an error returned by tx.Commit().
// applyMarket checks ctx.Err() itself before calling Commit (a done context
// there is a definite rollback). Inside Commit only a bare sql.ErrTxDone can
// be attributed — applyMarket owns the transaction, so it means database/sql
// had already rolled it back. Every other error, a context error included,
// cannot be proven to have stopped COMMIT and is reported as unknown.
func classifyMarketCommitError(err error) error {
	if err == sql.ErrTxDone {
		return fmt.Errorf("market settlement not committed (rolled back): %w", err)
	}
	return fmt.Errorf("%w: %v", service.ErrUsageBillingCommitUnknown, err)
}

// marketBuyerCharge is the buyer balance read inside the row lock the
// deduction holds (database decimals, never floats).
type marketBuyerCharge struct {
	before   decimal.Decimal
	after    decimal.Decimal
	deducted bool
}

// charged is the actual amount taken from the buyer's balance.
func (c *marketBuyerCharge) charged() decimal.Decimal {
	if c == nil || !c.deducted {
		return decimal.Zero
	}
	return c.before.Sub(c.after)
}

func (r *usageBillingRepository) applyMarket(ctx context.Context, cmd *service.UsageBillingCommand) (_ *service.UsageBillingApplyResult, err error) {
	if err := service.CheckMarketCommandShape(cmd); err != nil {
		service.RecordMarketSettlementIntegrityError(cmd, err)
		return nil, err
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	applied, err := r.claimUsageBillingKey(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}

	// The proof is checked against the stored admission on replays too, so
	// a forged proof is never reported as a successful replay.
	adm, err := loadMarketAdmissionTx(ctx, tx, cmd.MarketAdmissionID())
	if err != nil {
		return nil, err
	}
	if err := service.CheckMarketAdmission(cmd, adm); err != nil {
		service.RecordMarketSettlementIntegrityError(cmd, err)
		return nil, err
	}
	if !applied {
		// Same identity and fingerprint: only the original admission may
		// replay (nothing new is written).
		if err := checkMarketReplayTx(ctx, tx, cmd); err != nil {
			return nil, err
		}
		return &service.UsageBillingApplyResult{Applied: false}, nil
	}

	result := &service.UsageBillingApplyResult{Applied: true}
	charge := &marketBuyerCharge{}
	if err := r.applyUsageBillingEffects(ctx, tx, cmd, result, charge); err != nil {
		return nil, err
	}
	if err := writeSharedSaleTx(ctx, tx, cmd, adm, charge.charged()); err != nil {
		if errors.Is(err, service.ErrMarketSettlementIntegrity) {
			service.RecordMarketSettlementIntegrityError(cmd, err)
		}
		return nil, err
	}

	if marketBeforeCommit != nil {
		marketBeforeCommit()
	}
	if err := ctx.Err(); err != nil {
		// Never send COMMIT on a done context: roll back, a definite outcome.
		_ = tx.Rollback()
		tx = nil
		return nil, fmt.Errorf("market settlement not committed (context done, rolled back): %w", err)
	}
	if err := tx.Commit(); err != nil {
		tx = nil
		return nil, classifyMarketCommitError(err)
	}
	tx = nil
	return result, nil
}

// checkMarketReplayTx: the claimed identity must already carry a shared_sale
// event of the same admission.
func checkMarketReplayTx(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	var admissionID sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT admission_id FROM usage_settlement_events WHERE request_id = $1 AND api_key_id = $2
	`, cmd.RequestID, cmd.APIKeyID).Scan(&admissionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) || !admissionID.Valid || admissionID.Int64 != cmd.MarketAdmissionID() {
		logger.L().Error("market settlement replay does not match the settled admission",
			zap.String("request_id", cmd.RequestID),
			zap.Int64("api_key_id", cmd.APIKeyID),
			zap.Int64("admission_id", cmd.MarketAdmissionID()),
			zap.Int64("settled_admission_id", admissionID.Int64),
		)
		return fmt.Errorf("%w: identity already billed without this admission", service.ErrMarketSettlementConflict)
	}
	return nil
}

func loadMarketAdmissionTx(ctx context.Context, tx *sql.Tx, id int64) (*service.MarketAdmission, error) {
	a := &service.MarketAdmission{}
	err := tx.QueryRowContext(ctx, `
		SELECT id, attempt_id, request_ref, account_id, group_id, owner_user_id, owner_event_id,
			listing_version, buyer_user_id, api_key_id, admitted_at, created_at
		FROM market_admissions WHERE id = $1
	`, id).Scan(&a.ID, &a.AttemptID, &a.RequestRef, &a.AccountID, &a.GroupID, &a.OwnerUserID, &a.OwnerEventID,
		&a.ListingVersion, &a.BuyerUserID, &a.APIKeyID, &a.AdmittedAt, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// deductMarketBuyerBalance is deductUsageBillingBalance that also reads the
// balance before and after as decimals, inside the same row lock.
func deductMarketBuyerBalance(ctx context.Context, tx *sql.Tx, userID int64, amount float64, charge *marketBuyerCharge) (bool, error) {
	var oldLevelID sql.NullInt64
	var before string
	if err := tx.QueryRowContext(ctx, `
		SELECT auto_level_id, balance::text FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE
	`, userID).Scan(&oldLevelID, &before); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, service.ErrUserNotFound
		}
		return false, err
	}
	var newLevelID sql.NullInt64
	var after string
	err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance - $1,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING auto_level_id, balance::text
	`, amount, userID).Scan(&newLevelID, &after)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, service.ErrUserNotFound
		}
		return false, err
	}
	if charge.before, err = decimal.NewFromString(before); err != nil {
		return false, fmt.Errorf("parse balance before: %w", err)
	}
	if charge.after, err = decimal.NewFromString(after); err != nil {
		return false, fmt.Errorf("parse balance after: %w", err)
	}
	charge.deducted = true
	return oldLevelID.Valid != newLevelID.Valid || (oldLevelID.Valid && oldLevelID.Int64 != newLevelID.Int64), nil
}

// loadMarketSettingsTx reads the market settings on the billing transaction
// (snapshot at settlement time; no cache).
func loadMarketSettingsTx(ctx context.Context, q sqlQueryer) (service.MarketSettings, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM settings WHERE key = ANY($1)`, pq.Array(service.MarketSettingKeys()))
	if err != nil {
		return service.MarketSettings{}, err
	}
	defer func() { _ = rows.Close() }()
	values := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return service.MarketSettings{}, err
		}
		values[k] = v
	}
	if err := rows.Err(); err != nil {
		return service.MarketSettings{}, err
	}
	return service.ParseMarketSettings(values), nil
}

// marketOwnerVersionMatchesTx: the admission's owner version (an
// account_owner_events row of the account) names the admission owner.
func marketOwnerVersionMatchesTx(ctx context.Context, tx *sql.Tx, adm *service.MarketAdmission) (bool, error) {
	var owner sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT new_owner_user_id FROM account_owner_events WHERE id = $1 AND account_id = $2
	`, adm.OwnerEventID, adm.AccountID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return owner.Valid && owner.Int64 == adm.OwnerUserID, nil
}

func writeSharedSaleTx(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, adm *service.MarketAdmission, charged decimal.Decimal) error {
	settings, err := loadMarketSettingsTx(ctx, tx)
	if err != nil {
		return err
	}
	ownerOK, err := marketOwnerVersionMatchesTx(ctx, tx, adm)
	if err != nil {
		return err
	}
	split, err := service.ComputeSharedSaleSplit(charged, settings.PlatformCutRate, cmd.UserID == adm.OwnerUserID, ownerOK)
	if err != nil {
		return err
	}
	ev := service.BuildSharedSaleEvent(cmd, adm, split, settings.EarningFreezeDays)
	eventID, availableAt, err := insertSharedSaleEventTx(ctx, tx, ev)
	if err != nil {
		return err
	}
	if !ev.OwnerEarning.IsPositive() {
		return nil
	}
	_, err = insertAccrueEntryTx(ctx, tx, adm.OwnerUserID, ev.OwnerEarning, eventID, availableAt)
	return err
}

// insertSharedSaleEventTx writes the event; earning_available_at = the
// event time (transaction NOW(), same as created_at) + freeze days × 24 h
// (fixed hours, independent of the session time zone's DST).
func insertSharedSaleEventTx(ctx context.Context, tx *sql.Tx, ev *service.SharedSaleEvent) (int64, time.Time, error) {
	var invalid any
	if len(ev.InvalidFields) > 0 {
		invalid = pq.Array(ev.InvalidFields)
	}
	var id int64
	var availableAt time.Time
	err := tx.QueryRowContext(ctx, `
		INSERT INTO usage_settlement_events (
			request_id, api_key_id, category, rule_version, anomaly,
			buyer_user_id, account_id, owner_user_id, owner_source, self_purchase,
			group_id, original_group_id, fee_rate, standard_cost, charge_input,
			charge_source, subscription_id, zero_reason, invalid_fields,
			owner_earning, admission_id, charged_amount, platform_amount, platform_cut_rate,
			earning_zero_reason, earning_available_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULL, $13, $14, $15, NULL, $16, $17,
			$18::numeric, $19, $20::numeric, $21::numeric, $22::numeric, $23, NOW() + make_interval(hours => $24::int * 24))
		ON CONFLICT (request_id, api_key_id) DO NOTHING
		RETURNING id, earning_available_at
	`,
		ev.RequestID, ev.APIKeyID, ev.Category, ev.RuleVersion, ev.Anomaly,
		ev.BuyerUserID, ev.AccountID, nullableInt64(ev.OwnerUserID), ev.OwnerSource, ev.SelfPurchase,
		nullableInt64(ev.GroupID), nullableInt64(ev.OriginalGroupID), ev.StandardCost, ev.ChargeInput,
		ev.ChargeSource, nullableString(ev.ZeroReason), invalid,
		ev.OwnerEarning.String(), ev.AdmissionID, ev.ChargedAmount.String(), ev.PlatformAmount.String(), ev.PlatformCutRate.String(),
		nullableString(ev.EarningZeroReason), ev.FreezeDays,
	).Scan(&id, &availableAt)
	if errors.Is(err, sql.ErrNoRows) {
		// The claim accepted the identity, so its dedup/archive record is
		// missing: never settle the sale a second time.
		return 0, availableAt, service.ErrUsageSettlementEventConflict
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == usageSettlementAdmissionConstraint {
		return 0, availableAt, fmt.Errorf("%w: admission %d already settled another identity", service.ErrMarketSettlementConflict, ev.AdmissionID)
	}
	if err != nil {
		return 0, availableAt, err
	}
	return id, availableAt, nil
}
