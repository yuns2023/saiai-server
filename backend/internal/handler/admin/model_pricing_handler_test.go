package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelPriceSettingsRepo struct {
	service.SettingRepository
	values   map[string]string
	writeErr error
}

func (r *modelPriceSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	v, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return v, nil
}
func (r *modelPriceSettingsRepo) GetAll(context.Context) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}
func (r *modelPriceSettingsRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (r *modelPriceSettingsRepo) SetMultiple(_ context.Context, values map[string]string) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	for k, v := range values {
		r.values[k] = v
	}
	return nil
}
func modelPriceHandlerFixture(t *testing.T) (*SettingHandler, *service.PricingService, *modelPriceSettingsRepo, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model_pricing.json"), []byte(`{"model-base":{"input_cost_per_token":0.00001,"output_cost_per_token":0.00005,"cache_read_input_token_cost":0.000001,"cache_creation_input_token_cost":0.0000125,"cache_creation_input_token_cost_above_1hr":0.00002,"long_context_input_token_threshold":272000,"long_context_input_cost_multiplier":2,"long_context_output_cost_multiplier":1.5,"long_context_cache_read_cost_multiplier":2,"long_context_cache_creation_cost_multiplier":2,"mode":"chat"}}`), 0600))
	cfg := &config.Config{}
	cfg.Pricing.DataDir = dir
	cfg.Pricing.UpdateIntervalHours = 24
	cfg.Pricing.HashCheckIntervalMinutes = 60
	p := service.NewPricingService(cfg, nil)
	require.NoError(t, p.Initialize())
	t.Cleanup(p.Stop)
	repo := &modelPriceSettingsRepo{values: map[string]string{"site_name": "preserved"}}
	h := NewSettingHandler(service.NewSettingService(repo, cfg), nil, nil, nil)
	h.SetPricingService(p)
	h.SetBillingService(service.NewBillingService(cfg, p))
	router := gin.New()
	router.GET("/prices", h.ListModelPricing)
	router.PUT("/prices", h.UpdateModelPricing)
	router.POST("/preview", h.PreviewModelPricing)
	return h, p, repo, router
}
func priceHandlerRequest(router *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}
func TestModelPricingAdminSaveValidationHistoryAndWriteFailure(t *testing.T) {
	_, p, repo, router := modelPriceHandlerFixture(t)
	v := p.ResolvePricing("new-model").Version
	edit := map[string]any{"model": "new-model", "alias": "model-base", "override": map[string]any{"cache_read": 0}, "expected_version": v}
	w := priceHandlerRequest(router, http.MethodPut, "/prices", edit)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "preserved", repo.values["site_name"])
	var saved map[string]service.ModelPriceOverride
	require.NoError(t, json.Unmarshal([]byte(repo.values[service.SettingKeyModelPriceOverrides]), &saved))
	require.NotNil(t, saved["new-model"].CacheRead)
	require.Zero(t, *saved["new-model"].CacheRead)
	var history []service.PriceEdit
	require.NoError(t, json.Unmarshal([]byte(repo.values[service.SettingKeyModelPricingHistory]), &history))
	require.Len(t, history, 1)
	require.Equal(t, "model-base", history[0].AfterAlias)
	w = priceHandlerRequest(router, http.MethodPut, "/prices", edit)
	require.Equal(t, http.StatusConflict, w.Code)
	edit["expected_version"] = p.ResolvePricing("new-model").Version
	edit["override"] = map[string]any{"cache_read": -1}
	require.Equal(t, http.StatusBadRequest, priceHandlerRequest(router, http.MethodPut, "/prices", edit).Code)
	edit["override"] = map[string]any{"cache_read": 3}
	repo.writeErr = errors.New("fixture write failure")
	require.Equal(t, http.StatusInternalServerError, priceHandlerRequest(router, http.MethodPut, "/prices", edit).Code)
	_, overrides := p.ModelPricingConfig()
	require.Zero(t, *overrides["new-model"].CacheRead, "database failure must not change runtime prices")
}
func TestModelPricingAdminPreviewSubscriptionUsesCustomBaseAndNoCategoryDiscount(t *testing.T) {
	_, p, repo, router := modelPriceHandlerFixture(t)
	body := map[string]any{"model": "model-base", "override": map[string]any{"cache_read": 0.25}, "tokens": map[string]any{"input_tokens": 100, "cache_read_tokens": 1000}, "group_rate": 2, "model_rate": 0.8, "user_discount": 0.5, "subscription": true}
	w := priceHandlerRequest(router, http.MethodPost, "/preview", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var reply struct {
		Data struct {
			Charged float64               `json:"charged_amount"`
			Cost    service.CostBreakdown `json:"cost"`
		}
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reply))
	require.InDelta(t, 0.00125, reply.Data.Charged, 1e-12)
	require.InDelta(t, 0.002, reply.Data.Cost.PricingSnapshot.ReferenceTotalCost, 1e-12)
	_, overrides := p.ModelPricingConfig()
	require.Empty(t, overrides)
	require.Len(t, repo.values, 1)
	body["subscription"] = false
	w = priceHandlerRequest(router, http.MethodPost, "/preview", body)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reply))
	require.InDelta(t, 0.00125*2*0.8*0.5, reply.Data.Charged, 1e-12)
}
func TestModelPricingAdminListShowsActualTierAndSource(t *testing.T) {
	_, _, _, router := modelPriceHandlerFixture(t)
	w := priceHandlerRequest(router, http.MethodGet, "/prices?search=model-base", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var reply struct {
		Data struct {
			Items []modelPriceRow `json:"items"`
		}
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reply))
	require.Len(t, reply.Data.Items, 1)
	row := reply.Data.Items[0]
	require.Equal(t, "local_cache", row.Source)
	require.Equal(t, "model-base", row.ResolvedModel)
	require.Equal(t, 20.0, row.Tiers["priority"]["effective"]["input"])
	require.Equal(t, 10.0, row.Tiers["flex"]["effective"]["cache_write_1h"])
}

type recentPriceModelsStub struct {
	service.UsageLogRepository
	models []string
	calls  int
	fail   bool
}

func (r *recentPriceModelsStub) ListRecentPricingModels(context.Context) ([]string, error) {
	r.calls++
	if r.fail {
		return nil, errors.New("analytics unavailable")
	}
	return r.models, nil
}
func TestModelPricingRecentModelsAreIncludedAndCached(t *testing.T) {
	h, _, _, router := modelPriceHandlerFixture(t)
	repo := &recentPriceModelsStub{models: []string{"new-model"}}
	h.SetPricingUsageReader(repo)
	for i := 0; i < 2; i++ {
		w := priceHandlerRequest(router, http.MethodGet, "/prices", nil)
		require.Equal(t, http.StatusOK, w.Code)
		var reply struct {
			Data struct {
				Items []modelPriceRow `json:"items"`
			}
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reply))
		var found bool
		for _, row := range reply.Data.Items {
			if row.Model == "new-model" {
				found = true
				require.True(t, row.Recent)
			}
		}
		require.True(t, found, "recent model absent from catalogue")
	}
	require.Equal(t, 1, repo.calls)
	h.pricingRecentAt = time.Time{}
	repo.fail = true
	w := priceHandlerRequest(router, http.MethodGet, "/prices", nil)
	require.Equal(t, http.StatusOK, w.Code, "analytics failure must not hide price catalogue")
}
func TestModelPricingRejectsUnknownPriceFieldsAndInconsistentCacheCounts(t *testing.T) {
	_, p, _, router := modelPriceHandlerFixture(t)
	req := map[string]any{"model": "model-base", "expected_version": p.ResolvePricing("model-base").Version, "override": map[string]any{"cache_discount": 0.5}}
	require.Equal(t, http.StatusBadRequest, priceHandlerRequest(router, http.MethodPut, "/prices", req).Code)
	req = map[string]any{"model": "model-base", "tokens": map[string]any{"cache_creation_tokens": 9, "cache_creation_5m_tokens": 10}, "group_rate": 1, "model_rate": 1, "user_discount": 1}
	require.Equal(t, http.StatusBadRequest, priceHandlerRequest(router, http.MethodPost, "/preview", req).Code)
	req["tokens"] = map[string]any{"cache_creation5m_tokens": 10}
	require.Equal(t, http.StatusBadRequest, priceHandlerRequest(router, http.MethodPost, "/preview", req).Code, "misspelled token categories must not be silently ignored")
	req["tokens"] = map[string]any{"cache_creation_5m_tokens": 10}
	w := priceHandlerRequest(router, http.MethodPost, "/preview", req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var reply struct {
		Data struct {
			Cost service.CostBreakdown `json:"cost"`
		}
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reply))
	require.InDelta(t, 0.000125, reply.Data.Cost.CacheCreationCost, 1e-12)
}

func TestModelPricingGeneralSettingsSavePreservesFallbackAliasesAndOverrides(t *testing.T) {
	h, p, repo, router := modelPriceHandlerFixture(t)
	version := p.ResolvePricing("new-model").Version
	w := priceHandlerRequest(router, http.MethodPut, "/prices", map[string]any{
		"model": "new-model", "alias": "claude-3-5-haiku", "expected_version": version,
		"override": map[string]any{"cache_read": 0},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	version = p.ResolvePricing("new-model").Version
	saved := repo.values[service.SettingKeyModelPriceOverrides]
	router.PUT("/settings", h.UpdateSettings)
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"site_name": "updated"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, version, p.ResolvePricing("new-model").Version)
	require.Equal(t, saved, repo.values[service.SettingKeyModelPriceOverrides])
	require.Equal(t, "claude-3-5-haiku", p.ResolvePricing("new-model").Alias)
}

func TestModelPricingAdminListsAndPreviewsLongContextTierPrices(t *testing.T) {
	_, _, _, router := modelPriceHandlerFixture(t)
	w := priceHandlerRequest(router, http.MethodGet, "/prices?search=model-base", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var list struct {
		Data struct {
			Items []modelPriceRow `json:"items"`
		}
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list.Data.Items, 1)
	row := list.Data.Items[0]
	require.NotEmpty(t, row.LongContextTiers["default"])
	require.NotContains(t, row.LongContextTiers, "priority")
	require.InDelta(t, row.Tiers["default"]["effective"]["input"]*2, row.LongContextTiers["default"]["effective"]["input"], 1e-12)
	body := map[string]any{"model": "model-base", "tokens": map[string]any{"input_tokens": 1000, "cache_read_tokens": 272000}, "service_tier": "default", "group_rate": 1, "model_rate": 1, "user_discount": 1, "subscription": true}
	w = priceHandlerRequest(router, http.MethodPost, "/preview", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var reply struct {
		Data struct {
			Cost   service.CostBreakdown         `json:"cost"`
			Prices map[string]map[string]float64 `json:"long_context_unit_prices"`
		}
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reply))
	require.True(t, reply.Data.Cost.PricingSnapshot.LongContext.Applied)
	require.Equal(t, 273000, reply.Data.Cost.PricingSnapshot.LongContext.TotalInputTokens)
	require.InDelta(t, row.LongContextTiers["default"]["effective"]["output"], reply.Data.Prices["effective"]["output"], 1e-12)
}
