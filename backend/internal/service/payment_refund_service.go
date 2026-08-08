package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbpaymentorder "github.com/Wei-Shaw/sub2api/ent/paymentorder"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	dbusersubscription "github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	refundReasonMaxLength            = 1000
	refundExternalReferenceMaxLength = 200
	refundRecoveryStaleAfter         = 5 * time.Minute
)

type RequestPaymentRefundInput struct {
	Mode              string `json:"mode"`
	Reason            string `json:"reason"`
	ExternalReference string `json:"external_reference"`
	Force             bool   `json:"force"`
	Operator          string `json:"-"`
}

type ResolvePaymentRefundInput struct {
	Outcome           string `json:"outcome"`
	Reason            string `json:"reason"`
	ExternalReference string `json:"external_reference"`
	Operator          string `json:"-"`
}

type refundEntitlementSnapshot struct {
	Kind                string `json:"kind"`
	SubscriptionID      int64  `json:"subscription_id,omitempty"`
	DeductedNanoseconds int64  `json:"deducted_nanoseconds,omitempty"`
}

// RequestRefund starts either an adapter-driven refund or a fully manual
// refund. Refunds are intentionally full-order only: provider settlement and
// the corresponding balance/subscription entitlement always move together.
func (s *PaymentService) RequestRefund(ctx context.Context, orderID int64, input RequestPaymentRefundInput) (*PaymentOrderView, error) {
	input.Mode = strings.ToLower(strings.TrimSpace(input.Mode))
	input.Reason = strings.TrimSpace(input.Reason)
	input.ExternalReference = strings.TrimSpace(input.ExternalReference)
	input.Operator = normalizeRefundOperator(input.Operator)
	if input.Mode != payment.RefundModeAutomatic && input.Mode != payment.RefundModeManual {
		return nil, infraerrors.BadRequest("PAYMENT_REFUND_MODE_INVALID", "refund mode must be automatic or manual")
	}
	if input.Reason == "" || len(input.Reason) > refundReasonMaxLength {
		return nil, infraerrors.BadRequest("PAYMENT_REFUND_REASON_INVALID", "refund reason is required and must not exceed 1000 characters")
	}
	if len(input.ExternalReference) > refundExternalReferenceMaxLength {
		return nil, infraerrors.BadRequest("PAYMENT_REFUND_REFERENCE_INVALID", "refund external reference must not exceed 200 characters")
	}
	if input.Mode == payment.RefundModeManual && input.ExternalReference == "" {
		return nil, infraerrors.BadRequest("PAYMENT_REFUND_REFERENCE_REQUIRED", "manual refunds require an external reference")
	}

	order, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return nil, paymentOrderLookupError(err)
	}
	if order.Status == payment.OrderStatusRefunded {
		if sameRefundRequest(order, input) {
			view := paymentOrderView(order)
			return &view, nil
		}
		return nil, infraerrors.Conflict("PAYMENT_ALREADY_REFUNDED", "payment order has already been refunded")
	}
	if input.Mode == payment.RefundModeAutomatic {
		if _, err := s.refundProviderForOrder(order); err != nil {
			return nil, err
		}
	}

	prepared, created, err := s.prepareRefund(ctx, orderID, input)
	if err != nil {
		return nil, err
	}
	if created {
		s.invalidateRefundEntitlementCaches(ctx, prepared)
	}
	if input.Mode == payment.RefundModeAutomatic && prepared.Status == payment.OrderStatusRefundRequested {
		if err := s.executeAutomaticRefund(ctx, prepared.ID); err != nil {
			return nil, err
		}
		prepared, err = s.entClient.PaymentOrder.Get(ctx, prepared.ID)
		if err != nil {
			return nil, err
		}
	}
	view := paymentOrderView(prepared)
	return &view, nil
}

func (s *PaymentService) prepareRefund(ctx context.Context, orderID int64, input RequestPaymentRefundInput) (*dbent.PaymentOrder, bool, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin refund transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	candidate, err := tx.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return nil, false, paymentOrderLookupError(err)
	}

	now := time.Now()
	refundableStatuses := []string{payment.OrderStatusCompleted}
	// A failed automatic attempt may be completed only through the reviewed
	// manual path. Repeating the automatic endpoint after a response loss must
	// never create an implicit second provider refund attempt.
	if input.Mode == payment.RefundModeManual {
		refundableStatuses = append(refundableStatuses, payment.OrderStatusRefundFailed)
	}
	claimUpdate := tx.PaymentOrder.Update().Where(
		dbpaymentorder.IDEQ(orderID),
		dbpaymentorder.StatusIn(refundableStatuses...),
		dbpaymentorder.RefundEntitlementReversedEQ(false),
	).SetStatus(payment.OrderStatusRefundRequested).
		SetRefundMode(input.Mode).
		SetRefundAmount(candidate.PayAmount).
		SetRefundReason(input.Reason).
		SetRefundRequestedBy(input.Operator).
		SetRefundRequestedAt(now).
		SetRefundForce(input.Force).
		SetRefundID("").
		ClearRefundProviderCallStartedAt().
		ClearRefundedAt().
		ClearRefundError()
	if input.ExternalReference == "" {
		claimUpdate.ClearRefundExternalReference()
	} else {
		claimUpdate.SetRefundExternalReference(input.ExternalReference)
	}
	claimed, err := claimUpdate.Save(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("claim refund order: %w", err)
	}
	if claimed == 0 {
		current, getErr := tx.PaymentOrder.Get(ctx, orderID)
		if getErr != nil {
			return nil, false, paymentOrderLookupError(getErr)
		}
		if sameRefundRequest(current, input) && (current.Status == payment.OrderStatusRefundRequested || current.Status == payment.OrderStatusRefunding || current.Status == payment.OrderStatusRefundPending || current.Status == payment.OrderStatusRefunded) {
			if err := tx.Commit(); err != nil {
				return nil, false, err
			}
			return current, false, nil
		}
		return nil, false, infraerrors.Conflict("PAYMENT_ORDER_NOT_REFUNDABLE", "payment order is not in a refundable state")
	}

	order, err := tx.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return nil, false, err
	}
	snapshot, err := reverseRefundEntitlement(ctx, tx, order, input.Force)
	if err != nil {
		return nil, false, err
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, false, fmt.Errorf("encode refund entitlement snapshot: %w", err)
	}
	update := tx.PaymentOrder.UpdateOneID(orderID).
		SetRefundEntitlementReversed(true).
		SetRefundEntitlementSnapshot(string(snapshotJSON))
	if input.Mode == payment.RefundModeManual {
		update.SetStatus(payment.OrderStatusRefunded).SetRefundedAt(now).SetRefundID(input.ExternalReference)
	}
	prepared, err := update.Save(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("save refund entitlement state: %w", err)
	}
	action := "REFUND_REQUESTED"
	if input.Mode == payment.RefundModeManual {
		action = "MANUAL_REFUND_COMPLETED"
	}
	if err := createPaymentAuditLog(ctx, tx.Client(), orderID, action, input.Operator, map[string]any{
		"mode": input.Mode, "amount": order.PayAmount, "currency": order.Currency,
		"reason": input.Reason, "external_reference": input.ExternalReference, "force": input.Force,
	}); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit refund transaction: %w", err)
	}
	return prepared, true, nil
}

func (s *PaymentService) executeAutomaticRefund(ctx context.Context, orderID int64) error {
	order, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return paymentOrderLookupError(err)
	}
	refundProvider, err := s.refundProviderForOrder(order)
	if err != nil {
		return err
	}
	now := time.Now()
	claimed, err := s.entClient.PaymentOrder.Update().Where(
		dbpaymentorder.IDEQ(orderID),
		dbpaymentorder.StatusEQ(payment.OrderStatusRefundRequested),
		dbpaymentorder.RefundEntitlementReversedEQ(true),
		dbpaymentorder.RefundProviderCallStartedAtIsNil(),
	).SetStatus(payment.OrderStatusRefunding).SetRefundProviderCallStartedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("claim provider refund call: %w", err)
	}
	if claimed == 0 {
		return nil
	}
	s.writeAuditLog(ctx, orderID, "REFUND_PROVIDER_CALL_STARTED", "system", nil)

	response, callErr := refundProvider.Refund(ctx, payment.RefundRequest{
		TradeNo: order.PaymentTradeNo, OrderID: order.OutTradeNo,
		Amount: payment.FormatAmountForCurrency(order.PayAmount, order.Currency), Reason: orderRefundReason(order),
	})
	if callErr != nil {
		if payment.IsRefundRejectedError(callErr) {
			return s.failRefundAndCompensate(ctx, orderID, safePaymentFailureReason(callErr), "provider", "")
		}
		// A transport error is ambiguous: the provider may have accepted the
		// refund before the response was lost. Never compensate or retry blindly.
		return s.markRefundPending(ctx, orderID, "provider refund result is uncertain: "+safePaymentFailureReason(callErr), "")
	}
	if response == nil {
		return s.markRefundPending(ctx, orderID, "provider returned an empty refund response", "")
	}
	switch response.Status {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
		return s.finalizeRefund(ctx, orderID, response.RefundID, "provider")
	case payment.ProviderStatusFailed:
		return s.failRefundAndCompensate(ctx, orderID, "provider rejected the refund", "provider", "")
	default:
		return s.markRefundPending(ctx, orderID, "provider refund is pending", response.RefundID)
	}
}

func (s *PaymentService) ResolveRefund(ctx context.Context, orderID int64, input ResolvePaymentRefundInput) (*PaymentOrderView, error) {
	input.Outcome = strings.ToLower(strings.TrimSpace(input.Outcome))
	input.Reason = strings.TrimSpace(input.Reason)
	input.ExternalReference = strings.TrimSpace(input.ExternalReference)
	input.Operator = normalizeRefundOperator(input.Operator)
	if input.Outcome != "refunded" && input.Outcome != "not_refunded" {
		return nil, infraerrors.BadRequest("PAYMENT_REFUND_OUTCOME_INVALID", "refund outcome must be refunded or not_refunded")
	}
	if input.Reason == "" || len(input.Reason) > refundReasonMaxLength {
		return nil, infraerrors.BadRequest("PAYMENT_REFUND_REASON_INVALID", "resolution reason is required and must not exceed 1000 characters")
	}
	if input.ExternalReference == "" || len(input.ExternalReference) > refundExternalReferenceMaxLength {
		return nil, infraerrors.BadRequest("PAYMENT_REFUND_REFERENCE_REQUIRED", "manual refund resolution requires an external reference")
	}
	if input.Outcome == "refunded" {
		if err := s.resolveRefundAsRefunded(ctx, orderID, input); err != nil {
			return nil, err
		}
	} else if err := s.failRefundAndCompensate(ctx, orderID, input.Reason, input.Operator, input.ExternalReference); err != nil {
		return nil, err
	}
	order, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return nil, paymentOrderLookupError(err)
	}
	view := paymentOrderView(order)
	return &view, nil
}

func (s *PaymentService) RecoverIncompleteRefunds(ctx context.Context) (int, error) {
	staleBefore := time.Now().Add(-refundRecoveryStaleAfter)
	orders, err := s.entClient.PaymentOrder.Query().Where(
		dbpaymentorder.Or(
			dbpaymentorder.StatusEQ(payment.OrderStatusRefundRequested),
			dbpaymentorder.And(dbpaymentorder.StatusIn(payment.OrderStatusRefunding, payment.OrderStatusRefundPending), dbpaymentorder.UpdatedAtLTE(staleBefore)),
		),
		dbpaymentorder.RefundModeEQ(payment.RefundModeAutomatic),
	).Order(dbent.Asc(dbpaymentorder.FieldUpdatedAt)).Limit(paymentExpiryBatchSize).All(ctx)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, order := range orders {
		if order.Status == payment.OrderStatusRefundRequested {
			if err := s.executeAutomaticRefund(ctx, order.ID); err == nil {
				recovered++
			}
			continue
		}
		if err := s.reconcileRefund(ctx, order); err == nil {
			recovered++
		}
	}
	return recovered, nil
}

func (s *PaymentService) reconcileRefund(ctx context.Context, order *dbent.PaymentOrder) error {
	baseProvider, err := s.refundProviderForOrder(order)
	if err != nil {
		return s.markRefundPending(ctx, order.ID, safePaymentFailureReason(err), order.RefundID)
	}
	queryProvider, ok := baseProvider.(payment.RefundQueryProvider)
	if !ok {
		return s.markRefundPending(ctx, order.ID, "provider has no safe refund query; manual resolution is required", order.RefundID)
	}
	response, err := queryProvider.QueryRefund(ctx, payment.RefundQueryRequest{
		TradeNo: order.PaymentTradeNo, OrderID: order.OutTradeNo, RefundID: order.RefundID,
		Amount: payment.FormatAmountForCurrency(order.PayAmount, order.Currency),
	})
	if err != nil || response == nil {
		return s.markRefundPending(ctx, order.ID, "refund query failed: "+safePaymentFailureReason(err), order.RefundID)
	}
	switch response.Status {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
		return s.finalizeRefund(ctx, order.ID, response.RefundID, "system:refund-query")
	case payment.ProviderStatusFailed:
		return s.failRefundAndCompensate(ctx, order.ID, "provider confirmed refund failure", "system:refund-query", response.RefundID)
	default:
		return s.markRefundPending(ctx, order.ID, "provider refund remains pending", response.RefundID)
	}
}

func (s *PaymentService) markRefundPending(ctx context.Context, orderID int64, reason, refundID string) error {
	reason = safePaymentFailureReason(errors.New(reason))
	refundID = strings.TrimSpace(refundID)
	before, getErr := s.entClient.PaymentOrder.Get(ctx, orderID)
	if getErr != nil {
		return paymentOrderLookupError(getErr)
	}
	changed := before.Status != payment.OrderStatusRefundPending || before.RefundError == nil || *before.RefundError != reason || before.RefundID != refundID
	updated, err := s.entClient.PaymentOrder.Update().Where(
		dbpaymentorder.IDEQ(orderID),
		dbpaymentorder.StatusIn(payment.OrderStatusRefunding, payment.OrderStatusRefundPending),
	).SetStatus(payment.OrderStatusRefundPending).SetRefundError(reason).SetRefundID(refundID).Save(ctx)
	if err != nil {
		return err
	}
	if updated > 0 && changed {
		s.writeAuditLog(ctx, orderID, "REFUND_PENDING", "system", map[string]any{"reason": reason, "refund_id": refundID})
	}
	return nil
}

func (s *PaymentService) finalizeRefund(ctx context.Context, orderID int64, refundID, operator string) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	order, err := tx.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return paymentOrderLookupError(err)
	}
	if order.Status == payment.OrderStatusRefunded {
		return tx.Commit()
	}
	if !order.RefundEntitlementReversed {
		return errors.New("refund entitlement state is inconsistent")
	}
	now := time.Now()
	updated, err := tx.PaymentOrder.Update().Where(
		dbpaymentorder.IDEQ(orderID),
		dbpaymentorder.StatusIn(payment.OrderStatusRefundRequested, payment.OrderStatusRefunding, payment.OrderStatusRefundPending),
		dbpaymentorder.RefundEntitlementReversedEQ(true),
	).SetStatus(payment.OrderStatusRefunded).SetRefundedAt(now).SetRefundID(strings.TrimSpace(refundID)).ClearRefundError().Save(ctx)
	if err != nil {
		return err
	}
	if updated == 0 {
		return infraerrors.Conflict("PAYMENT_REFUND_NOT_RESOLVABLE", "payment refund is not awaiting resolution")
	}
	if err := createPaymentAuditLog(ctx, tx.Client(), orderID, "REFUND_COMPLETED", normalizeRefundOperator(operator), map[string]any{"refund_id": refundID}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PaymentService) resolveRefundAsRefunded(ctx context.Context, orderID int64, input ResolvePaymentRefundInput) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	order, err := tx.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return paymentOrderLookupError(err)
	}
	if order.Status == payment.OrderStatusRefunded {
		if order.RefundExternalReference != nil && *order.RefundExternalReference == input.ExternalReference {
			return tx.Commit()
		}
		return infraerrors.Conflict("PAYMENT_ALREADY_REFUNDED", "payment order has already been refunded with different evidence")
	}
	if !order.RefundEntitlementReversed {
		return errors.New("refund entitlement state is inconsistent")
	}
	now := time.Now()
	updated, err := tx.PaymentOrder.Update().Where(
		dbpaymentorder.IDEQ(orderID),
		dbpaymentorder.StatusIn(payment.OrderStatusRefunding, payment.OrderStatusRefundPending),
		dbpaymentorder.RefundEntitlementReversedEQ(true),
	).SetStatus(payment.OrderStatusRefunded).
		SetRefundedAt(now).
		SetRefundID(input.ExternalReference).
		SetRefundExternalReference(input.ExternalReference).
		SetRefundReason(input.Reason).
		ClearRefundError().
		Save(ctx)
	if err != nil {
		return err
	}
	if updated == 0 {
		return infraerrors.Conflict("PAYMENT_REFUND_NOT_RESOLVABLE", "payment refund is not awaiting resolution")
	}
	if err := createPaymentAuditLog(ctx, tx.Client(), orderID, "REFUND_MANUALLY_CONFIRMED", input.Operator, map[string]any{
		"outcome": input.Outcome, "reason": input.Reason, "external_reference": input.ExternalReference,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PaymentService) failRefundAndCompensate(ctx context.Context, orderID int64, reason, operator, externalReference string) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	order, err := tx.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return paymentOrderLookupError(err)
	}
	if order.Status == payment.OrderStatusRefundFailed && !order.RefundEntitlementReversed {
		return tx.Commit()
	}
	if order.Status != payment.OrderStatusRefunding && order.Status != payment.OrderStatusRefundPending && order.Status != payment.OrderStatusRefundRequested {
		return infraerrors.Conflict("PAYMENT_REFUND_NOT_RESOLVABLE", "payment refund is not awaiting resolution")
	}
	if !order.RefundEntitlementReversed {
		return errors.New("refund entitlement state is inconsistent")
	}
	if err := compensateRefundEntitlement(ctx, tx, order); err != nil {
		return fmt.Errorf("compensate refund entitlement: %w", err)
	}
	reason = safePaymentFailureReason(errors.New(reason))
	update := tx.PaymentOrder.UpdateOneID(orderID).
		SetStatus(payment.OrderStatusRefundFailed).
		SetRefundEntitlementReversed(false).
		SetRefundError(reason)
	if externalReference = strings.TrimSpace(externalReference); externalReference != "" {
		update.SetRefundExternalReference(externalReference)
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return err
	}
	if err := createPaymentAuditLog(ctx, tx.Client(), orderID, "REFUND_FAILED_COMPENSATED", normalizeRefundOperator(operator), map[string]any{
		"reason": reason, "external_reference": externalReference,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateRefundEntitlementCaches(ctx, updated)
	return nil
}

func reverseRefundEntitlement(ctx context.Context, tx *dbent.Tx, order *dbent.PaymentOrder, force bool) (refundEntitlementSnapshot, error) {
	switch order.OrderType {
	case payment.OrderTypeBalance:
		update := tx.User.Update().Where(dbuser.IDEQ(order.UserID))
		if !force {
			update.Where(dbuser.BalanceGTE(order.Amount))
		}
		updated, err := update.AddBalance(-order.Amount).Save(ctx)
		if err != nil {
			return refundEntitlementSnapshot{}, err
		}
		if updated == 0 {
			return refundEntitlementSnapshot{}, infraerrors.Conflict("PAYMENT_REFUND_BALANCE_INSUFFICIENT", "user balance is lower than the purchased credit; use force only after reviewing the account")
		}
		return refundEntitlementSnapshot{Kind: payment.DeductionTypeBalance}, nil

	case payment.OrderTypeSubscription:
		if order.SubscriptionGroupID == nil || order.SubscriptionDays == nil || *order.SubscriptionDays <= 0 {
			return refundEntitlementSnapshot{}, errors.New("subscription refund is missing its entitlement snapshot")
		}
		now := time.Now()
		sub, err := tx.UserSubscription.Query().Where(
			dbusersubscription.UserIDEQ(order.UserID),
			dbusersubscription.GroupIDEQ(*order.SubscriptionGroupID),
			dbusersubscription.StatusEQ(SubscriptionStatusActive),
			dbusersubscription.ExpiresAtGT(now),
		).Only(ctx)
		if err != nil {
			if dbent.IsNotFound(err) && force {
				return refundEntitlementSnapshot{Kind: payment.DeductionTypeSubscription}, nil
			}
			if dbent.IsNotFound(err) {
				return refundEntitlementSnapshot{}, infraerrors.Conflict("PAYMENT_REFUND_SUBSCRIPTION_UNAVAILABLE", "the purchased subscription is no longer active; use force only after reviewing consumed entitlement")
			}
			return refundEntitlementSnapshot{}, err
		}
		target := sub.ExpiresAt.AddDate(0, 0, -*order.SubscriptionDays)
		if !target.After(now) && !force {
			return refundEntitlementSnapshot{}, infraerrors.Conflict("PAYMENT_REFUND_SUBSCRIPTION_CONSUMED", "remaining subscription entitlement is less than this order; use force only after reviewing consumed entitlement")
		}
		if target.Before(now) {
			target = now
		}
		update := tx.UserSubscription.Update().Where(
			dbusersubscription.IDEQ(sub.ID),
			dbusersubscription.StatusEQ(SubscriptionStatusActive),
			dbusersubscription.ExpiresAtEQ(sub.ExpiresAt),
		).SetExpiresAt(target)
		if !target.After(now) {
			update.SetStatus(SubscriptionStatusExpired)
		}
		updated, err := update.Save(ctx)
		if err != nil {
			return refundEntitlementSnapshot{}, err
		}
		if updated == 0 {
			return refundEntitlementSnapshot{}, infraerrors.Conflict("PAYMENT_REFUND_ENTITLEMENT_CHANGED", "subscription changed while preparing the refund; retry after reviewing the current entitlement")
		}
		return refundEntitlementSnapshot{
			Kind: payment.DeductionTypeSubscription, SubscriptionID: sub.ID,
			DeductedNanoseconds: sub.ExpiresAt.Sub(target).Nanoseconds(),
		}, nil
	default:
		return refundEntitlementSnapshot{}, errors.New("unsupported payment order type for refund")
	}
}

func compensateRefundEntitlement(ctx context.Context, tx *dbent.Tx, order *dbent.PaymentOrder) error {
	var snapshot refundEntitlementSnapshot
	if err := json.Unmarshal([]byte(order.RefundEntitlementSnapshot), &snapshot); err != nil {
		return fmt.Errorf("decode refund entitlement snapshot: %w", err)
	}
	switch snapshot.Kind {
	case payment.DeductionTypeBalance:
		updated, err := tx.User.Update().Where(dbuser.IDEQ(order.UserID)).AddBalance(order.Amount).Save(ctx)
		if err != nil {
			return err
		}
		if updated == 0 {
			return ErrUserNotFound
		}
		return nil
	case payment.DeductionTypeSubscription:
		if snapshot.SubscriptionID == 0 || snapshot.DeductedNanoseconds <= 0 {
			return nil
		}
		sub, err := tx.UserSubscription.Get(ctx, snapshot.SubscriptionID)
		if err != nil {
			return err
		}
		base := sub.ExpiresAt
		if now := time.Now(); base.Before(now) {
			base = now
		}
		newExpiry := base.Add(time.Duration(snapshot.DeductedNanoseconds))
		updated, err := tx.UserSubscription.Update().Where(
			dbusersubscription.IDEQ(sub.ID), dbusersubscription.ExpiresAtEQ(sub.ExpiresAt),
		).SetExpiresAt(newExpiry).SetStatus(SubscriptionStatusActive).Save(ctx)
		if err != nil {
			return err
		}
		if updated == 0 {
			return errors.New("subscription changed while compensating refund")
		}
		return nil
	default:
		return errors.New("unsupported refund entitlement snapshot")
	}
}

func (s *PaymentService) refundProviderForOrder(order *dbent.PaymentOrder) (payment.RefundProvider, error) {
	config, err := s.configService.decryptProviderConfig(order.ProviderSnapshotEncrypted)
	if err != nil {
		return nil, err
	}
	baseProvider, err := s.providerFactory(order.ProviderKey, strconv.FormatInt(order.ProviderInstanceID, 10), config)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("PAYMENT_REFUND_PROVIDER_UNAVAILABLE", "the original payment adapter is unavailable")
	}
	refundProvider, ok := baseProvider.(payment.RefundProvider)
	if !ok {
		return nil, infraerrors.Conflict("PAYMENT_REFUND_UNSUPPORTED", "the original payment adapter does not support automatic refunds; use a manual refund")
	}
	return refundProvider, nil
}

func (s *PaymentService) invalidateRefundEntitlementCaches(ctx context.Context, order *dbent.PaymentOrder) {
	if s.refundCache == nil || order == nil {
		return
	}
	s.refundCache.InvalidatePaymentEntitlementCaches(ctx, order.UserID, order.OrderType, order.SubscriptionGroupID)
}

// InvalidatePaymentEntitlementCaches is shared by refund reversals and
// compensations so balance, auth and subscription caches observe the committed
// transaction together.
func (s *RedeemService) InvalidatePaymentEntitlementCaches(ctx context.Context, userID int64, orderType string, groupID *int64) {
	code := &RedeemCode{Type: RedeemTypeBalance}
	if orderType == payment.OrderTypeSubscription && groupID != nil {
		code.Type = RedeemTypeSubscription
		code.GroupID = groupID
		if s.subscriptionService != nil {
			s.subscriptionService.InvalidateSubCache(userID, *groupID)
		}
	}
	s.invalidateRedeemCaches(ctx, userID, code)
}

func createPaymentAuditLog(ctx context.Context, client *dbent.Client, orderID int64, action, operator string, detail any) error {
	detailText := ""
	if detail != nil {
		encoded, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		detailText = string(encoded)
	}
	_, err := client.PaymentAuditLog.Create().SetOrderID(orderID).SetAction(action).SetOperator(operator).SetDetail(detailText).Save(ctx)
	return err
}

func paymentOrderLookupError(err error) error {
	if dbent.IsNotFound(err) {
		return infraerrors.NotFound("PAYMENT_ORDER_NOT_FOUND", "payment order not found")
	}
	return err
}

func normalizeRefundOperator(operator string) string {
	operator = strings.TrimSpace(operator)
	if operator == "" {
		return "admin:unknown"
	}
	if len(operator) > 100 {
		return operator[:100]
	}
	return operator
}

func sameRefundRequest(order *dbent.PaymentOrder, input RequestPaymentRefundInput) bool {
	if order == nil || order.RefundMode != input.Mode || order.RefundReason == nil || *order.RefundReason != input.Reason || order.RefundForce != input.Force {
		return false
	}
	currentReference := ""
	if order.RefundExternalReference != nil {
		currentReference = *order.RefundExternalReference
	}
	return currentReference == input.ExternalReference
}

func orderRefundReason(order *dbent.PaymentOrder) string {
	if order != nil && order.RefundReason != nil {
		return *order.RefundReason
	}
	return ""
}
