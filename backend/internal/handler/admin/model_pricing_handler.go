package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

func (h *SettingHandler) SetBillingService(s *service.BillingService) { h.billingService = s }

type modelPriceRow struct {
	Recent        bool                                     `json:"recent"`
	Model         string                                   `json:"model"`
	Alias         string                                   `json:"alias,omitempty"`
	ResolvedModel string                                   `json:"resolved_model"`
	Source        string                                   `json:"source"`
	Version       string                                   `json:"version"`
	Reference     *service.ModelPricing                    `json:"reference"`
	Effective     *service.ModelPricing                    `json:"effective"`
	Override      service.ModelPriceOverride               `json:"override"`
	Tiers         map[string]map[string]map[string]float64 `json:"tiers"`
	Mode          string                                   `json:"mode"`
	Available     bool                                     `json:"available"`
}

func modelPricingRow(pricing *service.PricingService, billing *service.BillingService, model, alias string, override service.ModelPriceOverride) modelPriceRow {
	r := pricing.ResolvePricing(model)
	row := modelPriceRow{Model: model, Alias: alias, Override: override, Version: r.Version, ResolvedModel: r.ResolvedModel, Source: r.Source}
	p, err := billing.GetModelPricing(model)
	if err != nil {
		return row
	}
	row.Available = true
	if r.Dynamic != nil {
		row.Mode = r.Dynamic.Mode
	}
	row.Tiers = map[string]map[string]map[string]float64{}
	for _, tier := range []string{"default", "priority", "flex"} {
		row.Tiers[tier] = map[string]map[string]float64{"reference": billing.DisplayUnitPrices(p.Reference, tier), "effective": billing.DisplayUnitPrices(p, tier)}
	}
	row.Effective = p
	row.Reference = p.Reference
	if p.Snapshot != nil {
		row.Alias = p.Snapshot.Alias
		row.Override = p.Snapshot.Override
		row.Mode = p.Snapshot.Mode
		row.ResolvedModel = p.Snapshot.ResolvedModel
		row.Source = p.Snapshot.Source
		row.Version = p.Snapshot.Version
	}
	return row
}
func (h *SettingHandler) pricingReady(c *gin.Context) bool {
	if h.pricingService == nil || h.billingService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Pricing service unavailable")
		return false
	}
	c.Header("Cache-Control", "no-store")
	return true
}
func (h *SettingHandler) ListModelPricing(c *gin.Context) {
	if !h.pricingReady(c) {
		return
	}
	aliases, overrides := h.pricingService.ModelPricingConfig()
	recent := h.recentPricingModels(c.Request.Context())
	recentRank := map[string]int{}
	names := map[string]bool{}
	for i, name := range recent {
		recentRank[name] = i + 1
		names[name] = true
	}
	for _, name := range h.pricingService.PricingModelNames() {
		names[name] = true
	}
	for _, name := range h.billingService.ListSupportedModels() {
		names[name] = true
	}
	search := strings.ToLower(strings.TrimSpace(c.Query("search")))
	configured := c.Query("configured") == "true"
	models := []string{}
	for name := range names {
		if search != "" && !strings.Contains(strings.ToLower(name), search) {
			continue
		}
		if configured && aliases[name] == "" && overrides[name].Empty() {
			continue
		}
		models = append(models, name)
	}
	// Configured models precede the full provider catalogue.
	sort.Slice(models, func(i, j int) bool {
		ci := aliases[models[i]] != "" || !overrides[models[i]].Empty()
		cj := aliases[models[j]] != "" || !overrides[models[j]].Empty()
		if ci != cj {
			return ci
		}
		ri, rj := recentRank[models[i]], recentRank[models[j]]
		if ri != rj {
			if ri == 0 {
				return false
			}
			if rj == 0 {
				return true
			}
			return ri < rj
		}
		return models[i] < models[j]
	})
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 50
	}
	start := (page - 1) * size
	if start < 0 || start > len(models) {
		start = len(models)
	}
	end := start + size
	if end > len(models) {
		end = len(models)
	}
	items := make([]modelPriceRow, 0, end-start)
	for _, name := range models[start:end] {
		row := modelPricingRow(h.pricingService, h.billingService, name, aliases[name], overrides[name])
		row.Recent = recentRank[name] > 0
		items = append(items, row)
	}
	// A search for a new exact model can still be resolved via existing normalization.
	if len(models) == 0 && !configured && search != "" && strings.IndexFunc(search, func(r rune) bool { return r == ' ' || r == '*' }) == -1 {
		items = append(items, modelPricingRow(h.pricingService, h.billingService, search, aliases[search], overrides[search]))
	}
	response.Success(c, gin.H{"items": items, "total": len(models), "page": page, "page_size": size, "metadata": h.pricingService.PricingMetadata()})
}

// Pricing edits and simulations reject misspelled fields instead of silently
// inheriting a price or dropping a token category.
func bindModelPricingJSON(c *gin.Context, target any) error {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return binding.Validator.ValidateStruct(target)
}

type modelPriceEditRequest struct {
	Model           string                     `json:"model" binding:"required"`
	Alias           string                     `json:"alias"`
	Override        service.ModelPriceOverride `json:"override"`
	ExpectedVersion string                     `json:"expected_version" binding:"required"`
}

func (h *SettingHandler) UpdateModelPricing(c *gin.Context) {
	if !h.pricingReady(c) {
		return
	}
	var req modelPriceEditRequest
	if err := bindModelPricingJSON(c, &req); err != nil {
		response.BadRequest(c, "Invalid pricing edit")
		return
	}
	req.Model = strings.ToLower(strings.TrimSpace(req.Model))
	req.Alias = strings.ToLower(strings.TrimSpace(req.Alias))
	if req.Model == "" || len(req.Model) > 200 || len(req.Alias) > 200 || strings.ContainsAny(req.Model, "* \t\n") || strings.ContainsAny(req.Alias, "* \t\n") {
		response.BadRequest(c, "Use an exact model ID")
		return
	}
	if err := req.Override.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.pricingConfigMu.Lock()
	defer h.pricingConfigMu.Unlock()
	before := h.pricingService.ResolvePricing(req.Model)
	if before.Version != req.ExpectedVersion {
		response.Error(c, http.StatusConflict, "Prices changed; reload and review before saving")
		return
	}
	aliases, overrides := h.pricingService.ModelPricingConfig()
	change := service.PriceEdit{At: time.Now().UTC(), Model: req.Model, BeforeAlias: aliases[req.Model], AfterAlias: req.Alias, Before: overrides[req.Model], After: req.Override}
	if req.Alias != "" {
		if req.Alias == req.Model {
			response.BadRequest(c, "A model cannot alias itself")
			return
		}
		if _, err := h.billingService.GetModelPricing(req.Alias); err != nil {
			response.BadRequest(c, fmt.Sprintf("Pricing model not found: %s", req.Alias))
			return
		}
		aliases[req.Model] = req.Alias
	} else {
		delete(aliases, req.Model)
	}
	if _, err := h.pricingService.PreviewPricing(req.Model, &req.Alias, &req.Override); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if req.Override.Empty() {
		delete(overrides, req.Model)
	} else {
		overrides[req.Model] = req.Override
	}
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		change.ActorID = subject.UserID
	}
	if err := h.pricingService.ApplyPricingConfiguration(req.ExpectedVersion, aliases, overrides, func() error {
		return h.settingService.SaveModelPricing(c.Request.Context(), aliases, overrides, change)
	}); err != nil {
		if errors.Is(err, service.ErrPricingChanged) {
			response.Error(c, http.StatusConflict, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	body, _ := json.Marshal(change)
	log.Printf("AUDIT: model pricing updated %s", body)
	response.Success(c, modelPricingRow(h.pricingService, h.billingService, req.Model, req.Alias, req.Override))
}
func (h *SettingHandler) ModelPricingHistory(c *gin.Context) {
	if !h.pricingReady(c) {
		return
	}
	history, err := h.settingService.GetModelPricingHistory(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, history)
}

type pricePreviewRequest struct {
	Model        string                      `json:"model" binding:"required"`
	Alias        *string                     `json:"alias"`
	Override     *service.ModelPriceOverride `json:"override"`
	Tokens       service.UsageTokens         `json:"tokens"`
	ServiceTier  string                      `json:"service_tier"`
	GroupRate    float64                     `json:"group_rate"`
	ModelRate    float64                     `json:"model_rate"`
	UserDiscount float64                     `json:"user_discount"`
	Subscription bool                        `json:"subscription"`
}

func (h *SettingHandler) PreviewModelPricing(c *gin.Context) {
	if !h.pricingReady(c) {
		return
	}
	var req pricePreviewRequest
	if err := bindModelPricingJSON(c, &req); err != nil {
		response.BadRequest(c, "Invalid price preview")
		return
	}
	tier := strings.ToLower(strings.TrimSpace(req.ServiceTier))
	if tier != "" && tier != "default" && tier != "priority" && tier != "flex" {
		response.BadRequest(c, "Invalid service tier")
		return
	}
	for _, v := range []int{req.Tokens.InputTokens, req.Tokens.OutputTokens, req.Tokens.CacheCreationTokens, req.Tokens.CacheReadTokens, req.Tokens.CacheCreation5mTokens, req.Tokens.CacheCreation1hTokens} {
		if v < 0 || v > 1000000000 {
			response.BadRequest(c, "Invalid token count")
			return
		}
	}
	r := h.pricingService.ResolvePricing(req.Model)
	if r.Dynamic != nil && r.Dynamic.Mode == "image_generation" {
		response.BadRequest(c, "Image generation uses separate image billing")
		return
	}
	if sum := req.Tokens.CacheCreation5mTokens + req.Tokens.CacheCreation1hTokens; sum > 0 {
		if req.Tokens.CacheCreationTokens > 0 && req.Tokens.CacheCreationTokens != sum {
			response.BadRequest(c, "Cache token totals do not match their 5m/1h breakdown")
			return
		}
		req.Tokens.CacheCreationTokens = sum
	}
	for _, v := range []float64{req.GroupRate, req.ModelRate, req.UserDiscount} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
			response.BadRequest(c, "Invalid overall multiplier")
			return
		}
	}
	if req.GroupRate <= 0 || req.UserDiscount > 1 {
		response.BadRequest(c, "Invalid overall multiplier")
		return
	}
	// Isolated candidate pricing copies use the same BillingService calculation;
	// preview never writes settings, calls a provider, or changes the live cache.
	candidate, err := h.pricingService.PreviewPricing(req.Model, req.Alias, req.Override)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	cost, err := candidate.CalculateCostWithServiceTier(req.Model, req.Tokens, req.GroupRate, tier)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	discount := req.UserDiscount
	if req.Subscription {
		discount = 1
	}
	cost.ActualCost *= req.ModelRate * discount
	quotaCost := cost.ActualCost
	if req.Subscription {
		quotaCost = cost.TotalCost
	}
	response.Success(c, gin.H{"cost": cost, "charged_amount": quotaCost, "subscription": req.Subscription, "unit_prices": gin.H{"reference": candidate.DisplayUnitPrices(cost.PricingSnapshot.Reference, tier), "effective": candidate.DisplayUnitPrices(cost.PricingSnapshot.Effective, tier)}})
}

func (h *SettingHandler) SetPricingUsageReader(repo service.UsageLogRepository) {
	h.pricingUsageReader, _ = repo.(service.ModelPricingUsageReader)
}
func (h *SettingHandler) recentPricingModels(ctx context.Context) []string {
	if h.pricingUsageReader == nil {
		return nil
	}
	h.pricingRecentMu.Lock()
	defer h.pricingRecentMu.Unlock()
	if time.Since(h.pricingRecentAt) < 30*time.Second {
		return append([]string(nil), h.pricingRecentModels...)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	models, err := h.pricingUsageReader.ListRecentPricingModels(readCtx)
	h.pricingRecentAt = time.Now()
	if err == nil {
		h.pricingRecentModels = nil
		for _, model := range models {
			model = strings.ToLower(strings.TrimSpace(model))
			if model != "" && len(model) <= 200 {
				h.pricingRecentModels = append(h.pricingRecentModels, model)
			}
		}
	}
	return append([]string(nil), h.pricingRecentModels...)
}
