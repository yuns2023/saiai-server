package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	paymentFulfillmentLeaseDuration = 5 * time.Minute
	paymentExpiryBatchSize          = 100
)

var ErrPaymentOrderNotFound = errors.New("payment order not found")

type CreatePaymentOrderRequest struct {
	UserID             int64
	Amount             float64
	OrderType          string
	PlanID             int64
	ProviderInstanceID int64
	PaymentType        string
	ClientIP           string
	IsMobile           bool
}

type CreatePaymentOrderResponse struct {
	OrderID           int64     `json:"id"`
	OutTradeNo        string    `json:"out_trade_no"`
	Amount            float64   `json:"amount"`
	PayAmount         float64   `json:"pay_amount"`
	Currency          string    `json:"currency"`
	BalanceCreditRate float64   `json:"balance_credit_rate"`
	Status            string    `json:"status"`
	OrderType         string    `json:"order_type"`
	PlanID            *int64    `json:"plan_id,omitempty"`
	PayURL            string    `json:"pay_url,omitempty"`
	QRCode            string    `json:"qr_code,omitempty"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type PaymentOrderView struct {
	ID                          int64      `json:"id"`
	UserID                      int64      `json:"user_id"`
	UserEmail                   string     `json:"user_email"`
	UserName                    string     `json:"user_name"`
	Amount                      float64    `json:"amount"`
	PayAmount                   float64    `json:"pay_amount"`
	Currency                    string     `json:"currency"`
	BalanceCreditRate           float64    `json:"balance_credit_rate"`
	FeeRate                     float64    `json:"fee_rate"`
	OrderType                   string     `json:"order_type"`
	PlanID                      *int64     `json:"plan_id,omitempty"`
	SubscriptionGroupID         *int64     `json:"subscription_group_id,omitempty"`
	SubscriptionDays            *int       `json:"subscription_days,omitempty"`
	PaymentType                 string     `json:"payment_type"`
	ProviderKey                 string     `json:"provider_key"`
	Status                      string     `json:"status"`
	PaymentTradeNo              string     `json:"payment_trade_no,omitempty"`
	PayURL                      *string    `json:"pay_url,omitempty"`
	QRCode                      *string    `json:"qr_code,omitempty"`
	ExpiresAt                   time.Time  `json:"expires_at"`
	PaidAt                      *time.Time `json:"paid_at,omitempty"`
	CompletedAt                 *time.Time `json:"completed_at,omitempty"`
	FailedReason                *string    `json:"failed_reason,omitempty"`
	RefundMode                  string     `json:"refund_mode,omitempty"`
	RefundAmount                float64    `json:"refund_amount,omitempty"`
	RefundReason                *string    `json:"refund_reason,omitempty"`
	RefundExternalReference     *string    `json:"refund_external_reference,omitempty"`
	RefundRequestedBy           string     `json:"refund_requested_by,omitempty"`
	RefundRequestedAt           *time.Time `json:"refund_requested_at,omitempty"`
	RefundProviderCallStartedAt *time.Time `json:"refund_provider_call_started_at,omitempty"`
	RefundedAt                  *time.Time `json:"refunded_at,omitempty"`
	RefundID                    string     `json:"refund_id,omitempty"`
	RefundEntitlementReversed   bool       `json:"refund_entitlement_reversed,omitempty"`
	RefundForce                 bool       `json:"refund_force,omitempty"`
	RefundError                 *string    `json:"refund_error,omitempty"`
	CreatedAt                   time.Time  `json:"created_at"`
	UpdatedAt                   time.Time  `json:"updated_at"`
}

type paymentProviderFactory func(providerKey, instanceID string, config map[string]string) (payment.Provider, error)

type paymentUserReader interface {
	GetByID(ctx context.Context, id int64) (*User, error)
}

type paymentRedeemer interface {
	GetByCode(ctx context.Context, code string) (*RedeemCode, error)
	CreateCode(ctx context.Context, code *RedeemCode) error
	Redeem(ctx context.Context, userID int64, code string) (*RedeemCode, error)
}

type paymentEntitlementCacheInvalidator interface {
	InvalidatePaymentEntitlementCaches(ctx context.Context, userID int64, orderType string, groupID *int64)
}

type PaymentService struct {
	entClient       *dbent.Client
	userRepo        paymentUserReader
	redeemService   paymentRedeemer
	refundCache     paymentEntitlementCacheInvalidator
	configService   *PaymentConfigService
	providerFactory paymentProviderFactory
}

func NewPaymentService(entClient *dbent.Client, userRepo UserRepository, redeemService *RedeemService, configService *PaymentConfigService) *PaymentService {
	return &PaymentService{
		entClient: entClient, userRepo: userRepo, redeemService: redeemService,
		refundCache: redeemService, configService: configService, providerFactory: provider.CreateProvider,
	}
}

func (s *PaymentService) CreateOrder(ctx context.Context, req CreatePaymentOrderRequest) (*CreatePaymentOrderResponse, error) {
	cfg, err := s.configService.GetPaymentConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, infraerrors.ServiceUnavailable("PAYMENT_DISABLED", "native payment is disabled")
	}
	if req.UserID <= 0 {
		return nil, infraerrors.Unauthorized("UNAUTHORIZED", "authenticated user is required")
	}
	req.OrderType = strings.ToLower(strings.TrimSpace(req.OrderType))
	if req.OrderType == "" {
		req.OrderType = payment.OrderTypeBalance
	}
	amount, plan, subscriptionDays, err := s.resolveOrderProduct(ctx, req, cfg)
	if err != nil {
		return nil, err
	}
	user, err := s.userRepo.GetByID(ctx, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("get payment user: %w", err)
	}
	if !user.IsActive() {
		return nil, infraerrors.Forbidden("USER_INACTIVE", "user account is inactive")
	}
	instance, decryptedConfig, selectedProvider, err := s.selectProvider(ctx, req.ProviderInstanceID, req.PaymentType)
	if err != nil {
		return nil, err
	}
	_ = decryptedConfig // The encrypted instance snapshot is persisted below.
	currency := payment.DefaultPaymentCurrency
	if plan != nil {
		currency = plan.Currency
	}
	if selectedProvider.Currency() != currency {
		return nil, infraerrors.BadRequest("PAYMENT_CURRENCY_UNAVAILABLE", "selected payment method does not support the order currency")
	}
	feeRate := cfg.RechargeFeeRate
	balanceCreditRate := instance.BalanceCreditRate
	settlementAmount := amount / balanceCreditRate
	subject := "SAIAI Balance Recharge"
	if plan != nil {
		feeRate = 0
		balanceCreditRate = 1
		settlementAmount = amount
		subject = strings.TrimSpace(plan.ProductName)
		if subject == "" {
			subject = plan.Name
		}
	}
	payAmountString := payment.CalculatePayAmountForCurrency(settlementAmount, feeRate, currency)
	payAmountDecimal, err := decimal.NewFromString(payAmountString)
	if err != nil {
		return nil, fmt.Errorf("calculate payment amount: %w", err)
	}
	if !payAmountDecimal.IsPositive() {
		return nil, infraerrors.BadRequest("PAYMENT_AMOUNT_TOO_SMALL", "payment amount is below the provider currency precision")
	}
	payAmount := payAmountDecimal.InexactFloat64()
	outTradeNo, err := randomPaymentIdentifier("SA", 12)
	if err != nil {
		return nil, fmt.Errorf("generate payment order identifier: %w", err)
	}
	rechargeCode, err := randomPaymentIdentifier("PAY", 12)
	if err != nil {
		return nil, fmt.Errorf("generate payment recharge code: %w", err)
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin payment order transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	pendingCount, err := tx.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(req.UserID),
		paymentorder.StatusEQ(payment.OrderStatusPending),
	).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("count pending payment orders: %w", err)
	}
	if pendingCount >= cfg.MaxPendingOrders {
		return nil, infraerrors.TooManyRequests("PAYMENT_TOO_MANY_PENDING", "too many pending payment orders")
	}
	order := tx.PaymentOrder.Create().
		SetUserID(req.UserID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(amount).
		SetPayAmount(payAmount).
		SetCurrency(currency).
		SetBalanceCreditRate(balanceCreditRate).
		SetFeeRate(feeRate).
		SetRechargeCode(rechargeCode).
		SetOrderType(req.OrderType).
		SetOutTradeNo(outTradeNo).
		SetPaymentType(strings.TrimSpace(req.PaymentType)).
		SetProviderKey(instance.ProviderKey).
		SetProviderInstanceID(instance.ID).
		SetProviderSnapshotEncrypted(instance.ConfigEncrypted).
		SetStatus(payment.OrderStatusPending).
		SetClientIP(strings.TrimSpace(req.ClientIP)).
		SetExpiresAt(time.Now().Add(time.Duration(cfg.OrderTimeoutMinutes) * time.Minute))
	if plan != nil {
		order.SetPlanID(plan.ID).SetSubscriptionGroupID(plan.GroupID).SetSubscriptionDays(subscriptionDays)
	}
	createdOrder, err := order.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create payment order: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit payment order: %w", err)
	}

	providerResult, err := selectedProvider.CreatePayment(ctx, payment.CreatePaymentRequest{
		OrderID: outTradeNo, Amount: payAmountString, PaymentType: req.PaymentType,
		Subject: subject, ClientIP: req.ClientIP, IsMobile: req.IsMobile,
	})
	if err != nil {
		s.markOrderInitializationFailed(ctx, createdOrder.ID, err)
		return nil, infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_ERROR", "payment provider failed to create an order")
	}
	providerResult.PayURL, err = safeProviderPayURL(providerResult.PayURL)
	if err != nil {
		s.markOrderInitializationFailed(ctx, createdOrder.ID, err)
		return nil, infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_RESPONSE_INVALID", "payment provider returned an invalid payment URL")
	}
	createdOrder, err = s.entClient.PaymentOrder.UpdateOneID(createdOrder.ID).
		SetPaymentTradeNo(providerResult.TradeNo).
		SetNillablePayURL(nilIfBlank(providerResult.PayURL)).
		SetNillableQrCode(nilIfBlank(providerResult.QRCode)).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("save payment provider response: %w", err)
	}
	s.writeAuditLog(ctx, createdOrder.ID, "ORDER_CREATED", "user:"+strconv.FormatInt(req.UserID, 10), map[string]any{
		"amount": amount, "pay_amount": payAmount, "currency": currency, "balance_credit_rate": balanceCreditRate,
		"payment_type": req.PaymentType, "order_type": req.OrderType, "plan_id": req.PlanID,
		"provider_instance_id": instance.ID,
	})
	return &CreatePaymentOrderResponse{
		OrderID: createdOrder.ID, OutTradeNo: createdOrder.OutTradeNo, Amount: createdOrder.Amount,
		PayAmount: createdOrder.PayAmount, Currency: createdOrder.Currency, BalanceCreditRate: createdOrder.BalanceCreditRate,
		Status: createdOrder.Status, OrderType: createdOrder.OrderType,
		PlanID: createdOrder.PlanID, PayURL: providerResult.PayURL,
		QRCode: providerResult.QRCode, ExpiresAt: createdOrder.ExpiresAt,
	}, nil
}

func (s *PaymentService) resolveOrderProduct(ctx context.Context, req CreatePaymentOrderRequest, cfg *PaymentConfig) (float64, *dbent.SubscriptionPlan, int, error) {
	switch req.OrderType {
	case payment.OrderTypeBalance:
		if req.PlanID != 0 {
			return 0, nil, 0, infraerrors.BadRequest("PAYMENT_PLAN_NOT_ALLOWED", "balance orders cannot include a subscription plan")
		}
		amount, err := normalizePaymentAmount(req.Amount)
		if err != nil {
			return 0, nil, 0, err
		}
		if amount < cfg.MinAmount || amount > cfg.MaxAmount {
			return 0, nil, 0, infraerrors.BadRequest("PAYMENT_AMOUNT_OUT_OF_RANGE", "payment amount is outside the configured range").WithMetadata(map[string]string{
				"min": strconv.FormatFloat(cfg.MinAmount, 'f', 2, 64),
				"max": strconv.FormatFloat(cfg.MaxAmount, 'f', 2, 64),
			})
		}
		return amount, nil, 0, nil
	case payment.OrderTypeSubscription:
		if req.PlanID <= 0 {
			return 0, nil, 0, infraerrors.BadRequest("PAYMENT_PLAN_REQUIRED", "subscription orders require a plan")
		}
		plan, err := s.configService.GetPlan(ctx, req.PlanID)
		if err != nil || !plan.ForSale {
			return 0, nil, 0, infraerrors.NotFound("PAYMENT_PLAN_NOT_AVAILABLE", "subscription plan is not available")
		}
		if err := s.configService.validatePlanGroup(ctx, plan.GroupID); err != nil {
			return 0, nil, 0, infraerrors.NotFound("PAYMENT_PLAN_NOT_AVAILABLE", "subscription plan is no longer available")
		}
		days, err := subscriptionValidityDays(plan.ValidityDays, plan.ValidityUnit)
		if err != nil {
			return 0, nil, 0, infraerrors.BadRequest("PAYMENT_PLAN_INVALID", "subscription plan has an invalid validity period")
		}
		amount, err := normalizePaymentAmount(plan.Price)
		if err != nil {
			return 0, nil, 0, infraerrors.BadRequest("PAYMENT_PLAN_INVALID", "subscription plan has an invalid price")
		}
		return amount, plan, days, nil
	default:
		return 0, nil, 0, infraerrors.BadRequest("PAYMENT_ORDER_TYPE_UNSUPPORTED", "unsupported payment order type")
	}
}

func (s *PaymentService) GetUserOrder(ctx context.Context, userID, orderID int64) (*PaymentOrderView, error) {
	order, err := s.entClient.PaymentOrder.Query().Where(paymentorder.IDEQ(orderID), paymentorder.UserIDEQ(userID)).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("PAYMENT_ORDER_NOT_FOUND", "payment order not found")
		}
		return nil, err
	}
	view := userPaymentOrderView(order)
	return &view, nil
}

func (s *PaymentService) ListUserOrders(ctx context.Context, userID int64, limit, offset int) ([]PaymentOrderView, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := s.entClient.PaymentOrder.Query().Where(paymentorder.UserIDEQ(userID))
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	orders, err := query.Order(dbent.Desc(paymentorder.FieldCreatedAt)).Limit(limit).Offset(offset).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	result := make([]PaymentOrderView, 0, len(orders))
	for _, order := range orders {
		result = append(result, userPaymentOrderView(order))
	}
	return result, total, nil
}

func (s *PaymentService) ListAdminOrders(ctx context.Context, limit, offset int, status string) ([]PaymentOrderView, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := s.entClient.PaymentOrder.Query()
	if status = strings.TrimSpace(status); status != "" {
		query = query.Where(paymentorder.StatusEQ(status))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	orders, err := query.Order(dbent.Desc(paymentorder.FieldCreatedAt)).Limit(limit).Offset(offset).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	result := make([]PaymentOrderView, 0, len(orders))
	for _, order := range orders {
		result = append(result, paymentOrderView(order))
	}
	return result, total, nil
}

func (s *PaymentService) CancelUserOrder(ctx context.Context, userID, orderID int64) error {
	updated, err := s.entClient.PaymentOrder.Update().Where(
		paymentorder.IDEQ(orderID), paymentorder.UserIDEQ(userID), paymentorder.StatusEQ(payment.OrderStatusPending),
	).SetStatus(payment.OrderStatusCancelled).Save(ctx)
	if err != nil {
		return err
	}
	if updated == 0 {
		order, getErr := s.entClient.PaymentOrder.Query().Where(paymentorder.IDEQ(orderID), paymentorder.UserIDEQ(userID)).Only(ctx)
		if dbent.IsNotFound(getErr) {
			return infraerrors.NotFound("PAYMENT_ORDER_NOT_FOUND", "payment order not found")
		}
		if getErr != nil {
			return getErr
		}
		if order.Status == payment.OrderStatusCancelled {
			return nil
		}
		return infraerrors.Conflict("PAYMENT_ORDER_NOT_CANCELLABLE", "payment order cannot be cancelled")
	}
	s.writeAuditLog(ctx, orderID, "ORDER_CANCELLED", "user:"+strconv.FormatInt(userID, 10), nil)
	return nil
}

func (s *PaymentService) HandlePaymentNotification(ctx context.Context, providerKey, rawBody string, headers map[string]string) error {
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	outTradeNo, err := provider.ExtractNotificationOrderID(providerKey, rawBody, headers)
	if err != nil {
		return err
	}
	order, err := s.entClient.PaymentOrder.Query().Where(paymentorder.OutTradeNo(outTradeNo)).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return fmt.Errorf("%w: %s", ErrPaymentOrderNotFound, outTradeNo)
		}
		return err
	}
	if order.ProviderKey != providerKey {
		s.writeAuditLog(ctx, order.ID, "PAYMENT_PROVIDER_MISMATCH", providerKey, map[string]any{"expected": order.ProviderKey})
		return errors.New("payment webhook provider does not match order")
	}
	config, err := s.configService.decryptProviderConfig(order.ProviderSnapshotEncrypted)
	if err != nil {
		return err
	}
	selectedProvider, err := s.providerFactory(order.ProviderKey, strconv.FormatInt(order.ProviderInstanceID, 10), config)
	if err != nil {
		return err
	}
	notification, err := selectedProvider.VerifyNotification(ctx, rawBody, headers)
	if err != nil {
		return err
	}
	if notification == nil || notification.Status != payment.ProviderStatusSuccess {
		return nil
	}
	if notification.OrderID != order.OutTradeNo {
		return errors.New("payment webhook order identifier mismatch")
	}
	if expectedPID := strings.TrimSpace(config["pid"]); expectedPID != "" && notification.Metadata["pid"] != expectedPID {
		s.writeAuditLog(ctx, order.ID, "PAYMENT_MERCHANT_MISMATCH", providerKey, nil)
		return errors.New("payment webhook merchant does not match order snapshot")
	}
	return s.confirmPayment(ctx, order, notification.TradeNo, notification.Amount, providerKey)
}

func (s *PaymentService) confirmPayment(ctx context.Context, order *dbent.PaymentOrder, tradeNo string, paidAmount float64, operator string) error {
	if !validPaymentAmount(paidAmount) || !samePaymentAmount(paidAmount, order.PayAmount) {
		s.writeAuditLog(ctx, order.ID, "PAYMENT_AMOUNT_MISMATCH", operator, map[string]any{"expected": order.PayAmount, "paid": paidAmount})
		return errors.New("payment amount does not match order")
	}
	now := time.Now()
	grace := now.Add(-5 * time.Minute)
	updated, err := s.entClient.PaymentOrder.Update().Where(
		paymentorder.IDEQ(order.ID),
		paymentorder.Or(
			paymentorder.StatusEQ(payment.OrderStatusPending),
			paymentorder.StatusEQ(payment.OrderStatusCancelled),
			paymentorder.And(paymentorder.StatusEQ(payment.OrderStatusExpired), paymentorder.UpdatedAtGTE(grace)),
		),
	).SetStatus(payment.OrderStatusPaid).
		SetPaymentTradeNo(strings.TrimSpace(tradeNo)).
		SetPayAmount(roundPaymentAmount(paidAmount)).
		SetPaidAt(now).
		ClearFailedAt().ClearFailedReason().
		Save(ctx)
	if err != nil {
		return fmt.Errorf("mark payment order paid: %w", err)
	}
	if updated > 0 {
		s.writeAuditLog(ctx, order.ID, "ORDER_PAID", operator, map[string]any{"trade_no": tradeNo, "paid_amount": paidAmount})
		return s.ExecuteFulfillment(ctx, order.ID)
	}
	current, err := s.entClient.PaymentOrder.Get(ctx, order.ID)
	if err != nil {
		return err
	}
	switch current.Status {
	case payment.OrderStatusCompleted:
		return nil
	case payment.OrderStatusPaid, payment.OrderStatusFailed, payment.OrderStatusRecharging:
		return s.ExecuteFulfillment(ctx, current.ID)
	default:
		return nil
	}
}

func (s *PaymentService) ExecuteFulfillment(ctx context.Context, orderID int64) error {
	order, leaseAcquired, err := s.acquireFulfillmentLease(ctx, orderID)
	if err != nil || !leaseAcquired {
		return err
	}
	if err := s.ensureOrderRedeemed(ctx, order); err != nil {
		s.markFulfillmentFailed(ctx, order.ID, err)
		return err
	}
	now := time.Now()
	updated, err := s.entClient.PaymentOrder.Update().Where(
		paymentorder.IDEQ(order.ID), paymentorder.StatusEQ(payment.OrderStatusRecharging),
	).SetStatus(payment.OrderStatusCompleted).SetCompletedAt(now).ClearFailedAt().ClearFailedReason().Save(ctx)
	if err != nil {
		return fmt.Errorf("complete payment order: %w", err)
	}
	if updated == 0 {
		return errors.New("payment fulfillment lease was lost")
	}
	s.writeAuditLog(ctx, order.ID, "FULFILLMENT_SUCCESS", "system", map[string]any{
		"amount": order.Amount, "order_type": order.OrderType, "recharge_code": order.RechargeCode,
	})
	return nil
}

func (s *PaymentService) RetryFulfillment(ctx context.Context, orderID int64) error {
	order, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return infraerrors.NotFound("PAYMENT_ORDER_NOT_FOUND", "payment order not found")
		}
		return err
	}
	if order.Status != payment.OrderStatusPaid && order.Status != payment.OrderStatusFailed && order.Status != payment.OrderStatusRecharging {
		return infraerrors.Conflict("PAYMENT_ORDER_NOT_RETRYABLE", "payment order fulfillment cannot be retried")
	}
	return s.ExecuteFulfillment(ctx, orderID)
}

// RecoverIncompleteFulfillments resumes orders interrupted after payment was
// confirmed. FAILED orders remain an explicit admin retry so permanent data
// conflicts do not create an unbounded automatic retry loop.
func (s *PaymentService) RecoverIncompleteFulfillments(ctx context.Context) (int, error) {
	staleBefore := time.Now().Add(-paymentFulfillmentLeaseDuration)
	orders, err := s.entClient.PaymentOrder.Query().Where(
		paymentorder.Or(
			paymentorder.StatusEQ(payment.OrderStatusPaid),
			paymentorder.And(
				paymentorder.StatusEQ(payment.OrderStatusRecharging),
				paymentorder.UpdatedAtLTE(staleBefore),
			),
		),
	).Order(dbent.Asc(paymentorder.FieldUpdatedAt)).Limit(paymentExpiryBatchSize).All(ctx)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, order := range orders {
		if err := s.ExecuteFulfillment(ctx, order.ID); err != nil {
			continue
		}
		current, getErr := s.entClient.PaymentOrder.Get(ctx, order.ID)
		if getErr == nil && current.Status == payment.OrderStatusCompleted {
			recovered++
		}
	}
	return recovered, nil
}

func (s *PaymentService) ExpireTimedOutOrders(ctx context.Context) (int, error) {
	orders, err := s.entClient.PaymentOrder.Query().Where(
		paymentorder.StatusEQ(payment.OrderStatusPending), paymentorder.ExpiresAtLTE(time.Now()),
	).Order(dbent.Asc(paymentorder.FieldExpiresAt)).Limit(paymentExpiryBatchSize).All(ctx)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, order := range orders {
		recovered, recoverErr := s.reconcileTimedOutOrder(ctx, order)
		if recoverErr != nil {
			continue
		}
		if recovered {
			continue
		}
		updated, updateErr := s.entClient.PaymentOrder.Update().Where(
			paymentorder.IDEQ(order.ID), paymentorder.StatusEQ(payment.OrderStatusPending), paymentorder.ExpiresAtLTE(time.Now()),
		).SetStatus(payment.OrderStatusExpired).Save(ctx)
		if updateErr != nil {
			return expired, updateErr
		}
		if updated > 0 {
			expired++
			s.writeAuditLog(ctx, order.ID, "ORDER_EXPIRED", "system", nil)
		}
	}
	return expired, nil
}

func (s *PaymentService) reconcileTimedOutOrder(ctx context.Context, order *dbent.PaymentOrder) (bool, error) {
	config, err := s.configService.decryptProviderConfig(order.ProviderSnapshotEncrypted)
	if err != nil {
		return false, err
	}
	selectedProvider, err := s.providerFactory(order.ProviderKey, strconv.FormatInt(order.ProviderInstanceID, 10), config)
	if err != nil {
		return false, err
	}
	result, err := selectedProvider.QueryOrder(ctx, order.OutTradeNo)
	if err != nil || result == nil || result.Status != payment.ProviderStatusPaid {
		return false, err
	}
	if expectedPID := strings.TrimSpace(config["pid"]); expectedPID != "" && result.Metadata["pid"] != expectedPID {
		return false, errors.New("payment query merchant does not match order snapshot")
	}
	if err := s.confirmPayment(ctx, order, result.TradeNo, result.Amount, order.ProviderKey+":query"); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PaymentService) selectProvider(ctx context.Context, providerInstanceID int64, paymentType string) (*dbent.PaymentProviderInstance, map[string]string, payment.Provider, error) {
	paymentType = strings.TrimSpace(paymentType)
	if providerInstanceID <= 0 || paymentType == "" || len(paymentType) > 50 {
		return nil, nil, nil, infraerrors.BadRequest("PAYMENT_TYPE_UNSUPPORTED", "unsupported payment type")
	}
	query := s.entClient.PaymentProviderInstance.Query().Where(paymentproviderinstance.EnabledEQ(true))
	query = query.Where(paymentproviderinstance.IDEQ(providerInstanceID))
	instances, err := query.Order(
		dbent.Asc(paymentproviderinstance.FieldSortOrder), dbent.Asc(paymentproviderinstance.FieldID),
	).All(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, instance := range instances {
		if !paymentTypeSupported(instance.SupportedTypes, paymentType) {
			continue
		}
		config, decErr := s.configService.decryptProviderConfig(instance.ConfigEncrypted)
		if decErr != nil {
			return nil, nil, nil, decErr
		}
		selectedProvider, createErr := s.providerFactory(instance.ProviderKey, strconv.FormatInt(instance.ID, 10), config)
		if createErr != nil {
			return nil, nil, nil, infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_MISCONFIGURED", "payment provider is misconfigured")
		}
		return instance, config, selectedProvider, nil
	}
	return nil, nil, nil, infraerrors.ServiceUnavailable("PAYMENT_PROVIDER_UNAVAILABLE", "no payment provider is available for this payment type")
}

func (s *PaymentService) acquireFulfillmentLease(ctx context.Context, orderID int64) (*dbent.PaymentOrder, bool, error) {
	order, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return nil, false, err
	}
	if order.Status == payment.OrderStatusCompleted {
		return order, false, nil
	}
	staleBefore := time.Now().Add(-paymentFulfillmentLeaseDuration)
	updated, err := s.entClient.PaymentOrder.Update().Where(
		paymentorder.IDEQ(orderID),
		paymentorder.Or(
			paymentorder.StatusEQ(payment.OrderStatusPaid),
			paymentorder.StatusEQ(payment.OrderStatusFailed),
			paymentorder.And(paymentorder.StatusEQ(payment.OrderStatusRecharging), paymentorder.UpdatedAtLTE(staleBefore)),
		),
	).SetStatus(payment.OrderStatusRecharging).ClearFailedAt().ClearFailedReason().Save(ctx)
	if err != nil {
		return nil, false, err
	}
	return order, updated > 0, nil
}

func (s *PaymentService) ensureOrderRedeemed(ctx context.Context, order *dbent.PaymentOrder) error {
	if s.redeemService == nil {
		return errors.New("redeem service is unavailable")
	}
	redeemCode := &RedeemCode{
		Code: order.RechargeCode, Value: order.Amount, Status: StatusUnused,
		Notes: fmt.Sprintf("native payment order:%d", order.ID),
	}
	switch order.OrderType {
	case payment.OrderTypeBalance:
		redeemCode.Type = RedeemTypeBalance
	case payment.OrderTypeSubscription:
		if order.SubscriptionGroupID == nil || order.SubscriptionDays == nil || *order.SubscriptionDays <= 0 {
			return errors.New("subscription order is missing its fulfillment snapshot")
		}
		redeemCode.Type = RedeemTypeSubscription
		redeemCode.GroupID = order.SubscriptionGroupID
		redeemCode.ValidityDays = *order.SubscriptionDays
	default:
		return errors.New("unsupported payment order type")
	}
	existing, err := s.redeemService.GetByCode(ctx, order.RechargeCode)
	if errors.Is(err, ErrRedeemCodeNotFound) {
		createErr := s.redeemService.CreateCode(ctx, redeemCode)
		if createErr != nil {
			existing, err = s.redeemService.GetByCode(ctx, order.RechargeCode)
			if err != nil {
				return fmt.Errorf("create payment redeem code: %w", createErr)
			}
		} else {
			existing, err = s.redeemService.GetByCode(ctx, order.RechargeCode)
		}
	}
	if err != nil {
		return err
	}
	if existing.Type != redeemCode.Type || !samePaymentAmount(existing.Value, order.Amount) {
		return errors.New("payment redeem code conflicts with order")
	}
	if redeemCode.Type == RedeemTypeSubscription && !sameSubscriptionRedeemSnapshot(existing, redeemCode) {
		return errors.New("payment subscription redeem code conflicts with order")
	}
	if existing.UsedBy != nil {
		if *existing.UsedBy == order.UserID {
			return nil
		}
		return errors.New("payment redeem code was used by another user")
	}
	_, err = s.redeemService.Redeem(ctx, order.UserID, order.RechargeCode)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrRedeemCodeUsed) {
		return err
	}
	latest, getErr := s.redeemService.GetByCode(ctx, order.RechargeCode)
	if getErr == nil && latest.UsedBy != nil && *latest.UsedBy == order.UserID {
		return nil
	}
	return err
}

func sameSubscriptionRedeemSnapshot(existing, expected *RedeemCode) bool {
	return existing.GroupID != nil && expected.GroupID != nil &&
		*existing.GroupID == *expected.GroupID && existing.ValidityDays == expected.ValidityDays
}

func (s *PaymentService) markFulfillmentFailed(ctx context.Context, orderID int64, cause error) {
	now := time.Now()
	reason := safePaymentFailureReason(cause)
	_, _ = s.entClient.PaymentOrder.Update().Where(
		paymentorder.IDEQ(orderID), paymentorder.StatusEQ(payment.OrderStatusRecharging),
	).SetStatus(payment.OrderStatusFailed).SetFailedAt(now).SetFailedReason(reason).Save(ctx)
	s.writeAuditLog(ctx, orderID, "FULFILLMENT_FAILED", "system", map[string]any{"reason": reason})
}

func (s *PaymentService) markOrderInitializationFailed(ctx context.Context, orderID int64, cause error) {
	now := time.Now()
	reason := safePaymentFailureReason(cause)
	_, _ = s.entClient.PaymentOrder.Update().Where(
		paymentorder.IDEQ(orderID), paymentorder.StatusEQ(payment.OrderStatusPending),
	).SetStatus(payment.OrderStatusCancelled).SetFailedAt(now).SetFailedReason(reason).Save(ctx)
	s.writeAuditLog(ctx, orderID, "ORDER_CREATE_FAILED", "system", map[string]any{"reason": reason})
}

func (s *PaymentService) writeAuditLog(ctx context.Context, orderID int64, action, operator string, detail any) {
	detailText := ""
	if detail != nil {
		if encoded, err := json.Marshal(detail); err == nil {
			detailText = string(encoded)
		}
	}
	_, _ = s.entClient.PaymentAuditLog.Create().SetOrderID(orderID).SetAction(action).SetOperator(operator).SetDetail(detailText).Save(ctx)
}

func normalizePaymentAmount(amount float64) (float64, error) {
	if !validPaymentAmount(amount) {
		return 0, infraerrors.BadRequest("PAYMENT_AMOUNT_INVALID", "payment amount must be positive and finite")
	}
	value := decimal.NewFromFloat(amount)
	if !value.Equal(value.Round(2)) {
		return 0, infraerrors.BadRequest("PAYMENT_AMOUNT_PRECISION", "payment amount must have at most two decimal places")
	}
	return value.Round(2).InexactFloat64(), nil
}

func validPaymentAmount(amount float64) bool {
	return amount > 0 && !math.IsNaN(amount) && !math.IsInf(amount, 0)
}

func samePaymentAmount(left, right float64) bool {
	return decimal.NewFromFloat(left).Round(2).Equal(decimal.NewFromFloat(right).Round(2))
}

func roundPaymentAmount(amount float64) float64 {
	return decimal.NewFromFloat(amount).Round(2).InexactFloat64()
}

func safePaymentFailureReason(cause error) string {
	if cause == nil {
		return ""
	}
	secretKeys := append(provider.SecretConfigKeys(), "pid", "notify_url", "return_url")
	reason := logredact.RedactText(cause.Error(), secretKeys...)
	const maxReasonLength = 1000
	if len(reason) > maxReasonLength {
		reason = reason[:maxReasonLength]
	}
	return reason
}

func randomPaymentIdentifier(prefix string, byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value), nil
}

func paymentTypeSupported(raw, requested string) bool {
	for _, item := range strings.Split(raw, ",") {
		if strings.TrimSpace(item) == requested {
			return true
		}
	}
	return false
}

func nilIfBlank(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func safeProviderPayURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("payment provider returned a non-HTTP payment URL")
	}
	return parsed.String(), nil
}

func paymentOrderView(order *dbent.PaymentOrder) PaymentOrderView {
	return PaymentOrderView{
		ID: order.ID, UserID: order.UserID, UserEmail: order.UserEmail, UserName: order.UserName,
		Amount: order.Amount, PayAmount: order.PayAmount, Currency: order.Currency,
		BalanceCreditRate: order.BalanceCreditRate, FeeRate: order.FeeRate,
		OrderType: order.OrderType, PlanID: order.PlanID, SubscriptionGroupID: order.SubscriptionGroupID,
		SubscriptionDays: order.SubscriptionDays,
		PaymentType:      order.PaymentType, ProviderKey: order.ProviderKey, Status: order.Status,
		PaymentTradeNo: order.PaymentTradeNo, PayURL: order.PayURL, QRCode: order.QrCode,
		ExpiresAt: order.ExpiresAt, PaidAt: order.PaidAt, CompletedAt: order.CompletedAt,
		FailedReason: order.FailedReason, CreatedAt: order.CreatedAt, UpdatedAt: order.UpdatedAt,
		RefundMode: order.RefundMode, RefundAmount: order.RefundAmount, RefundReason: order.RefundReason,
		RefundExternalReference: order.RefundExternalReference, RefundRequestedBy: order.RefundRequestedBy,
		RefundRequestedAt: order.RefundRequestedAt, RefundProviderCallStartedAt: order.RefundProviderCallStartedAt,
		RefundedAt: order.RefundedAt, RefundID: order.RefundID,
		RefundEntitlementReversed: order.RefundEntitlementReversed, RefundForce: order.RefundForce,
		RefundError: order.RefundError,
	}
}

func userPaymentOrderView(order *dbent.PaymentOrder) PaymentOrderView {
	view := paymentOrderView(order)
	// Provider evidence and operator notes are administrative audit data. Users
	// receive the lifecycle state and refunded timestamp, not internal ticket
	// references, adapter errors, or the identity of the reviewing operator.
	view.RefundReason = nil
	view.RefundExternalReference = nil
	view.RefundRequestedBy = ""
	view.RefundProviderCallStartedAt = nil
	view.RefundID = ""
	view.RefundEntitlementReversed = false
	view.RefundForce = false
	view.RefundError = nil
	return view
}
