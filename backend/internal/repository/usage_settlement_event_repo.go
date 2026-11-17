package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"go.uber.org/zap"
)

// Shadow settlement event write inside the billing transaction (P2b).
//
// The segment runs under SAVEPOINT settlement_evt with one shared deadline
// (2s, earlier if needed to keep a reserve of the billing context for
// ROLLBACK TO and COMMIT). Before each owner read / INSERT the local
// statement_timeout is set to the remaining budget and lock_timeout to the
// smaller of 1s and that budget, which bounds the cumulative wait of the
// segment's queries; an exhausted budget skips straight to ROLLBACK TO. This
// is a wait budget, not a wall-clock guarantee: if ROLLBACK TO still fails
// (dead connection, cancelled context) billing returns an error. Any failure rolls back to the savepoint and billing still commits
// (error log + counter); only a failed ROLLBACK TO (the transaction is no
// longer usable) is returned. The one fatal outcome is an existing event for
// an identity the dedup claim just accepted: that means the dedup/archive
// record is missing, so the whole transaction is rolled back rather than
// charging the sale a second time.

const (
	settlementEventMaxLockTimeout = time.Second
	// settlementEventSegmentBudget bounds the whole segment.
	settlementEventSegmentBudget = 2 * time.Second
	// settlementEventReserve is billing context time kept for ROLLBACK TO
	// and COMMIT after the segment.
	settlementEventReserve = 1500 * time.Millisecond
	// settlementEventMinStatementBudget is the smallest budget worth running
	// another statement with.
	settlementEventMinStatementBudget = 50 * time.Millisecond
	// settlementEventMinBudget is the minimum remaining billing context time
	// needed to attempt the event write.
	settlementEventMinBudget = 3 * time.Second
)

// settlementNow is the segment clock (replaced in tests).
var settlementNow = time.Now

var errSettlementSegmentBudgetExhausted = errors.New("settlement event segment budget exhausted")

// settlementSegmentBudget is the absolute deadline of one event segment.
type settlementSegmentBudget struct {
	deadline time.Time
}

func newSettlementSegmentBudget(ctx context.Context) settlementSegmentBudget {
	deadline := settlementNow().Add(settlementEventSegmentBudget)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		if reserved := ctxDeadline.Add(-settlementEventReserve); reserved.Before(deadline) {
			deadline = reserved
		}
	}
	return settlementSegmentBudget{deadline: deadline}
}

// bound sets the local timeouts for the next segment statement to the
// remaining budget, or reports the budget as exhausted.
func (b settlementSegmentBudget) bound(ctx context.Context, tx *sql.Tx) error {
	remaining := b.deadline.Sub(settlementNow())
	if remaining < settlementEventMinStatementBudget {
		return errSettlementSegmentBudgetExhausted
	}
	lockTimeout := remaining
	if lockTimeout > settlementEventMaxLockTimeout {
		lockTimeout = settlementEventMaxLockTimeout
	}
	return setSettlementTimeouts(ctx, tx, strconv.FormatInt(lockTimeout.Milliseconds(), 10), strconv.FormatInt(remaining.Milliseconds(), 10))
}

func recordUsageSettlementEvent(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	if cmd == nil || cmd.Settlement == nil {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < settlementEventMinBudget {
		service.RecordUsageSettlementSkippedLowBudget()
		logger.L().Error("usage settlement event skipped: billing context budget too low",
			zap.String("request_id", cmd.RequestID),
			zap.Int64("api_key_id", cmd.APIKeyID),
			zap.Duration("remaining", time.Until(deadline)),
		)
		return nil
	}

	if _, err := tx.ExecContext(ctx, `SAVEPOINT settlement_evt`); err != nil {
		// Without a savepoint nothing can be undone selectively; same as an
		// effect failure.
		return fmt.Errorf("settlement savepoint: %w", err)
	}

	segErr := writeUsageSettlementEventSegment(ctx, tx, cmd, newSettlementSegmentBudget(ctx))
	if errors.Is(segErr, service.ErrUsageSettlementEventConflict) {
		logger.L().Error("usage settlement event exists without billing dedup record; rolling back billing",
			zap.String("request_id", cmd.RequestID),
			zap.Int64("api_key_id", cmd.APIKeyID),
		)
		return segErr
	}
	if segErr == nil {
		// The segment restored the pre-segment timeouts before returning, so
		// RELEASE carries the original values into the outer transaction.
		_, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT settlement_evt`)
		if err == nil {
			return nil
		}
		segErr = fmt.Errorf("release settlement savepoint: %w", err)
	}
	// ROLLBACK TO undoes the event and the segment's SET LOCAL values, so the
	// pre-segment timeouts are in effect again without another statement.
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT settlement_evt`); err != nil {
		return fmt.Errorf("rollback to settlement savepoint after %v: %w", segErr, err)
	}
	if errors.Is(segErr, errSettlementSegmentBudgetExhausted) {
		service.RecordUsageSettlementSkippedLowBudget()
		logger.L().Error("usage settlement event skipped: segment time budget exhausted; billing continues",
			zap.String("request_id", cmd.RequestID),
			zap.Int64("api_key_id", cmd.APIKeyID),
		)
		return nil
	}
	recordShadowSettlementFailure(cmd, segErr)
	return nil
}

// recordShadowSettlementFailure logs and counts an event write that was rolled
// back to its savepoint while billing continues.
func recordShadowSettlementFailure(cmd *service.UsageBillingCommand, err error) {
	service.RecordUsageSettlementWriteFailure()
	logger.L().Error("usage settlement event not recorded; billing continues",
		zap.String("request_id", cmd.RequestID),
		zap.Int64("api_key_id", cmd.APIKeyID),
		zap.Error(err),
	)
}

// writeUsageSettlementEventSegment runs inside the savepoint. Every statement,
// including reading and restoring the session timeouts, is undone by
// ROLLBACK TO on failure. On success it restores the pre-segment timeouts.
func writeUsageSettlementEventSegment(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, budget settlementSegmentBudget) error {
	var prevLockTimeout, prevStatementTimeout string
	if err := tx.QueryRowContext(ctx, `SELECT current_setting('lock_timeout'), current_setting('statement_timeout')`).Scan(&prevLockTimeout, &prevStatementTimeout); err != nil {
		return err
	}
	if err := insertUsageSettlementEvent(ctx, tx, cmd, budget); err != nil {
		return err
	}
	return setSettlementTimeouts(ctx, tx, prevLockTimeout, prevStatementTimeout)
}

func setSettlementTimeouts(ctx context.Context, tx *sql.Tx, lockTimeout, statementTimeout string) error {
	_, err := tx.ExecContext(ctx, `SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`, lockTimeout, statementTimeout)
	return err
}

func insertUsageSettlementEvent(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, budget settlementSegmentBudget) error {
	if err := budget.bound(ctx, tx); err != nil {
		return err
	}
	var owner sql.NullInt64
	accountFound := true
	err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM accounts WHERE id = $1`, cmd.AccountID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		accountFound = false
	} else if err != nil {
		return err
	}
	var ownerPtr *int64
	if owner.Valid {
		v := owner.Int64
		ownerPtr = &v
	}
	ev := service.BuildUsageSettlementEvent(cmd, ownerPtr, accountFound)
	if ev == nil {
		return nil
	}
	var invalid any
	if len(ev.InvalidFields) > 0 {
		invalid = pq.Array(ev.InvalidFields)
	}
	if err := budget.bound(ctx, tx); err != nil {
		return err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO usage_settlement_events (
			request_id, api_key_id, category, rule_version, anomaly,
			buyer_user_id, account_id, owner_user_id, owner_source, self_purchase,
			group_id, original_group_id, fee_rate, standard_cost, charge_input,
			charge_source, subscription_id, zero_reason, invalid_fields
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		ON CONFLICT (request_id, api_key_id) DO NOTHING
		RETURNING id
	`,
		ev.RequestID, ev.APIKeyID, ev.Category, ev.RuleVersion, ev.Anomaly,
		ev.BuyerUserID, ev.AccountID, nullableInt64(ev.OwnerUserID), ev.OwnerSource, ev.SelfPurchase,
		nullableInt64(ev.GroupID), nullableInt64(ev.OriginalGroupID), nullableFloat64(ev.FeeRate), ev.StandardCost, ev.ChargeInput,
		ev.ChargeSource, nullableInt64(ev.SubscriptionID), nullableString(ev.ZeroReason), invalid,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageSettlementEventConflict
	}
	return err
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableFloat64(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
