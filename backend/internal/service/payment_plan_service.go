package service

import (
	"context"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/subscriptionplan"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

type CreatePlanRequest struct {
	GroupID       int64    `json:"group_id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Price         float64  `json:"price"`
	OriginalPrice *float64 `json:"original_price"`
	Currency      string   `json:"currency"`
	ValidityDays  int      `json:"validity_days"`
	ValidityUnit  string   `json:"validity_unit"`
	Features      string   `json:"features"`
	ProductName   string   `json:"product_name"`
	ForSale       bool     `json:"for_sale"`
	SortOrder     int      `json:"sort_order"`
}

type UpdatePlanRequest struct {
	GroupID       *int64   `json:"group_id"`
	Name          *string  `json:"name"`
	Description   *string  `json:"description"`
	Price         *float64 `json:"price"`
	OriginalPrice *float64 `json:"original_price"`
	Currency      *string  `json:"currency"`
	ValidityDays  *int     `json:"validity_days"`
	ValidityUnit  *string  `json:"validity_unit"`
	Features      *string  `json:"features"`
	ProductName   *string  `json:"product_name"`
	ForSale       *bool    `json:"for_sale"`
	SortOrder     *int     `json:"sort_order"`
}

func (s *PaymentConfigService) ListPlans(ctx context.Context, forSaleOnly bool) ([]*dbent.SubscriptionPlan, error) {
	query := s.entClient.SubscriptionPlan.Query()
	if forSaleOnly {
		query = query.Where(subscriptionplan.ForSaleEQ(true))
	}
	plans, err := query.Order(dbent.Asc(subscriptionplan.FieldSortOrder), dbent.Asc(subscriptionplan.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list subscription plans: %w", err)
	}
	if forSaleOnly {
		available := make([]*dbent.SubscriptionPlan, 0, len(plans))
		for _, plan := range plans {
			if s.validatePlanGroup(ctx, plan.GroupID) == nil {
				available = append(available, plan)
			}
		}
		plans = available
	}
	return plans, nil
}

func (s *PaymentConfigService) GetPlan(ctx context.Context, id int64) (*dbent.SubscriptionPlan, error) {
	plan, err := s.entClient.SubscriptionPlan.Get(ctx, id)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("PAYMENT_PLAN_NOT_FOUND", "subscription plan not found")
		}
		return nil, err
	}
	return plan, nil
}

func (s *PaymentConfigService) CreatePlan(ctx context.Context, req CreatePlanRequest) (*dbent.SubscriptionPlan, error) {
	normalized, err := normalizeCreatePlan(req)
	if err != nil {
		return nil, err
	}
	if err := s.validatePlanGroup(ctx, normalized.GroupID); err != nil {
		return nil, err
	}
	create := s.entClient.SubscriptionPlan.Create().
		SetGroupID(normalized.GroupID).
		SetName(normalized.Name).
		SetDescription(normalized.Description).
		SetPrice(normalized.Price).
		SetCurrency(normalized.Currency).
		SetValidityDays(normalized.ValidityDays).
		SetValidityUnit(normalized.ValidityUnit).
		SetFeatures(normalized.Features).
		SetProductName(normalized.ProductName).
		SetForSale(normalized.ForSale).
		SetSortOrder(normalized.SortOrder)
	if normalized.OriginalPrice != nil {
		create.SetOriginalPrice(*normalized.OriginalPrice)
	}
	plan, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create subscription plan: %w", err)
	}
	return plan, nil
}

func (s *PaymentConfigService) UpdatePlan(ctx context.Context, id int64, req UpdatePlanRequest) (*dbent.SubscriptionPlan, error) {
	current, err := s.GetPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	next := CreatePlanRequest{
		GroupID: current.GroupID, Name: current.Name, Description: current.Description,
		Price: current.Price, OriginalPrice: current.OriginalPrice, Currency: current.Currency,
		ValidityDays: current.ValidityDays, ValidityUnit: current.ValidityUnit,
		Features: current.Features, ProductName: current.ProductName,
		ForSale: current.ForSale, SortOrder: current.SortOrder,
	}
	applyPlanPatch(&next, req)
	next, err = normalizeCreatePlan(next)
	if err != nil {
		return nil, err
	}
	if err := s.validatePlanGroup(ctx, next.GroupID); err != nil {
		return nil, err
	}
	update := s.entClient.SubscriptionPlan.UpdateOneID(id).
		SetGroupID(next.GroupID).SetName(next.Name).SetDescription(next.Description).
		SetPrice(next.Price).SetCurrency(next.Currency).SetValidityDays(next.ValidityDays).
		SetValidityUnit(next.ValidityUnit).SetFeatures(next.Features).
		SetProductName(next.ProductName).SetForSale(next.ForSale).SetSortOrder(next.SortOrder)
	if next.OriginalPrice == nil {
		update.ClearOriginalPrice()
	} else {
		update.SetOriginalPrice(*next.OriginalPrice)
	}
	plan, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update subscription plan: %w", err)
	}
	return plan, nil
}

func (s *PaymentConfigService) DeletePlan(ctx context.Context, id int64) error {
	if _, err := s.GetPlan(ctx, id); err != nil {
		return err
	}
	count, err := s.entClient.PaymentOrder.Query().Where(
		paymentorder.PlanIDEQ(id),
		paymentorder.StatusIn(payment.OrderStatusPending, payment.OrderStatusPaid, payment.OrderStatusRecharging, payment.OrderStatusFailed),
	).Count(ctx)
	if err != nil {
		return fmt.Errorf("check subscription plan orders: %w", err)
	}
	if count > 0 {
		return infraerrors.Conflict("PAYMENT_PLAN_IN_USE", "subscription plan has an in-progress order and cannot be deleted")
	}
	if err := s.entClient.SubscriptionPlan.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete subscription plan: %w", err)
	}
	return nil
}

func (s *PaymentConfigService) validatePlanGroup(ctx context.Context, groupID int64) error {
	groupEntity, err := s.entClient.Group.Get(ctx, groupID)
	if err != nil {
		return infraerrors.NotFound("PAYMENT_PLAN_GROUP_NOT_FOUND", "subscription group not found")
	}
	if groupEntity.Status != StatusActive || groupEntity.SubscriptionType != SubscriptionTypeSubscription {
		return infraerrors.BadRequest("PAYMENT_PLAN_GROUP_INVALID", "plan group must be an active subscription group")
	}
	return nil
}

func normalizeCreatePlan(req CreatePlanRequest) (CreatePlanRequest, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.Features = strings.TrimSpace(req.Features)
	req.ProductName = strings.TrimSpace(req.ProductName)
	req.ValidityUnit = strings.ToLower(strings.TrimSpace(req.ValidityUnit))
	if req.ValidityUnit == "" {
		req.ValidityUnit = "day"
	}
	if req.Name == "" || len(req.Name) > 100 || req.GroupID <= 0 || req.ValidityDays <= 0 {
		return req, infraerrors.BadRequest("PAYMENT_PLAN_INVALID", "plan name, group, and validity are required")
	}
	price, err := normalizePaymentAmount(req.Price)
	if err != nil {
		return req, infraerrors.BadRequest("PAYMENT_PLAN_PRICE_INVALID", "plan price must be positive with at most two decimal places")
	}
	req.Price = price
	if req.OriginalPrice != nil {
		if *req.OriginalPrice < req.Price {
			return req, infraerrors.BadRequest("PAYMENT_PLAN_ORIGINAL_PRICE_INVALID", "original price must not be less than the sale price")
		}
		original, normalizeErr := normalizePaymentAmount(*req.OriginalPrice)
		if normalizeErr != nil {
			return req, infraerrors.BadRequest("PAYMENT_PLAN_ORIGINAL_PRICE_INVALID", "original price must have at most two decimal places")
		}
		req.OriginalPrice = &original
	}
	currency, err := payment.NormalizePaymentCurrency(req.Currency)
	if err != nil {
		return req, infraerrors.BadRequest("PAYMENT_PLAN_CURRENCY_INVALID", "plan currency must be a 3-letter ISO currency code")
	}
	req.Currency = currency
	priceValue := decimal.NewFromFloat(req.Price)
	if !priceValue.Equal(priceValue.Round(int32(payment.CurrencyMaxFractionDigits(currency)))) {
		return req, infraerrors.BadRequest("PAYMENT_PLAN_PRICE_INVALID", "plan price exceeds the currency precision")
	}
	if req.OriginalPrice != nil {
		originalValue := decimal.NewFromFloat(*req.OriginalPrice)
		if !originalValue.Equal(originalValue.Round(int32(payment.CurrencyMaxFractionDigits(currency)))) {
			return req, infraerrors.BadRequest("PAYMENT_PLAN_ORIGINAL_PRICE_INVALID", "original price exceeds the currency precision")
		}
	}
	if req.ValidityUnit != "day" && req.ValidityUnit != "month" && req.ValidityUnit != "year" {
		return req, infraerrors.BadRequest("PAYMENT_PLAN_VALIDITY_INVALID", "validity unit must be day, month, or year")
	}
	if _, err := subscriptionValidityDays(req.ValidityDays, req.ValidityUnit); err != nil {
		return req, err
	}
	return req, nil
}

func subscriptionValidityDays(value int, unit string) (int, error) {
	multiplier := 1
	switch unit {
	case "day":
	case "month":
		multiplier = 30
	case "year":
		multiplier = 365
	default:
		return 0, infraerrors.BadRequest("PAYMENT_PLAN_VALIDITY_INVALID", "invalid plan validity unit")
	}
	if value <= 0 || value > 36500/multiplier {
		return 0, infraerrors.BadRequest("PAYMENT_PLAN_VALIDITY_INVALID", "plan validity is outside the supported range")
	}
	return value * multiplier, nil
}

func applyPlanPatch(target *CreatePlanRequest, req UpdatePlanRequest) {
	if req.GroupID != nil {
		target.GroupID = *req.GroupID
	}
	if req.Name != nil {
		target.Name = *req.Name
	}
	if req.Description != nil {
		target.Description = *req.Description
	}
	if req.Price != nil {
		target.Price = *req.Price
	}
	if req.OriginalPrice != nil {
		target.OriginalPrice = req.OriginalPrice
	}
	if req.Currency != nil {
		target.Currency = *req.Currency
	}
	if req.ValidityDays != nil {
		target.ValidityDays = *req.ValidityDays
	}
	if req.ValidityUnit != nil {
		target.ValidityUnit = *req.ValidityUnit
	}
	if req.Features != nil {
		target.Features = *req.Features
	}
	if req.ProductName != nil {
		target.ProductName = *req.ProductName
	}
	if req.ForSale != nil {
		target.ForSale = *req.ForSale
	}
	if req.SortOrder != nil {
		target.SortOrder = *req.SortOrder
	}
}
