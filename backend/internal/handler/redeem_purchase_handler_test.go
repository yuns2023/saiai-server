//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type purchaseHandlerSettings struct {
	cfg service.RedeemPurchaseSettings
}

func (s purchaseHandlerSettings) LoadRedeemPurchaseSettings(context.Context) (service.RedeemPurchaseSettings, error) {
	return s.cfg, nil
}

// purchaseHandlerRepo serves one stored purchase; writes are recorded.
type purchaseHandlerRepo struct {
	service.RedeemPurchaseRepository
	records  []service.RedeemPurchaseRecord
	existing *service.RedeemPurchaseRecord
	creates  int
}

func (r *purchaseHandlerRepo) Now(context.Context) (time.Time, error) { return time.Now(), nil }
func (r *purchaseHandlerRepo) WindowUsage(context.Context, int64, time.Time) (decimal.Decimal, int, error) {
	return decimal.RequireFromString("12.5"), 2, nil
}
func (r *purchaseHandlerRepo) ListByBuyer(_ context.Context, _ int64, p pagination.PaginationParams) ([]service.RedeemPurchaseRecord, *pagination.PaginationResult, error) {
	return r.records, &pagination.PaginationResult{Total: int64(len(r.records)), Page: p.Page, PageSize: p.PageSize, Pages: 1}, nil
}
func (r *purchaseHandlerRepo) LockBuyer(context.Context, int64) (*service.RedeemPurchaseBuyer, error) {
	return &service.RedeemPurchaseBuyer{Status: service.StatusActive, Balance: decimal.NewFromInt(100), Now: time.Now()}, nil
}
func (r *purchaseHandlerRepo) GetByRequestID(context.Context, int64, string) (*service.RedeemPurchaseRecord, error) {
	return r.existing, nil
}
func (r *purchaseHandlerRepo) Create(_ context.Context, in service.RedeemPurchaseCreateInput) (*service.RedeemPurchaseRecord, error) {
	r.creates++
	return &service.RedeemPurchaseRecord{ID: 77, Amount: in.Amount, Status: service.RedeemPurchaseStatusActive, Code: in.Code, CodeStatus: service.StatusUnused, CreatedAt: in.Now}, nil
}

func newPurchaseRouter(repo *purchaseHandlerRepo, enabled bool, authed bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	cfg := service.RedeemPurchaseSettings{
		Enabled:         enabled,
		MinAmount:       decimal.NewFromInt(1),
		MaxAmount:       decimal.NewFromInt(500),
		WindowMaxAmount: decimal.NewFromInt(1000),
		WindowMaxCount:  20,
	}
	runTx := func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
	h := NewRedeemPurchaseHandler(service.NewRedeemPurchaseServiceWithTx(repo, purchaseHandlerSettings{cfg: cfg}, runTx, nil, nil))
	r := gin.New()
	if authed {
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
			c.Next()
		})
	}
	r.GET("/redeem/purchases", h.List)
	r.POST("/redeem/purchases", h.Create)
	r.POST("/redeem/purchases/:id/revoke", h.Revoke)
	return r
}

func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return body.Data
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRedeemPurchaseHandler_ListIsAWhitelist(t *testing.T) {
	usedAt := time.Now()
	repo := &purchaseHandlerRepo{records: []service.RedeemPurchaseRecord{
		{ID: 1, BuyerUserID: 7, RedeemCodeID: 10, Amount: decimal.RequireFromString("12.50000000"), RequestID: "internal-request-id", Status: service.RedeemPurchaseStatusActive, Code: "c1", CodeStatus: service.StatusUsed, CodeUsedAt: &usedAt, CreatedAt: time.Now()},
		{ID: 2, BuyerUserID: 7, RedeemCodeID: 11, Amount: decimal.RequireFromString("3"), Status: service.RedeemPurchaseStatusActive, Code: "c2", CodeStatus: service.StatusExpired, CreatedAt: time.Now()},
	}}
	w := httptest.NewRecorder()
	newPurchaseRouter(repo, false, true).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/redeem/purchases?page=1&page_size=500", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	data := decodeData(t, w)
	require.Equal(t, []string{"config", "items", "pagination"}, sortedKeys(data))
	cfg, ok := data["config"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"enabled", "max_amount", "min_amount", "remaining_amount_24h", "remaining_count_24h"}, sortedKeys(cfg))
	require.Equal(t, false, cfg["enabled"], "listing works with the switch off")
	require.EqualValues(t, 987.5, cfg["remaining_amount_24h"])
	require.EqualValues(t, 18, cfg["remaining_count_24h"])

	page, ok := data["pagination"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"page", "page_size", "pages", "total"}, sortedKeys(page))
	require.EqualValues(t, 50, page["page_size"], "page size is capped")

	items, ok := data["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		require.True(t, ok)
		require.Equal(t, []string{"amount", "code", "created_at", "id", "revoked_at", "status", "used_at"}, sortedKeys(item))
	}
	first, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, 12.5, first["amount"])
	require.Equal(t, "used", first["status"])
	require.NotNil(t, first["used_at"])
	second, ok := items[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "unknown", second["status"], "an expired code on an active purchase is not disguised")
	require.NotContains(t, w.Body.String(), "internal-request-id")
	require.NotContains(t, w.Body.String(), "used_by")
	require.NotContains(t, w.Body.String(), "notes")
}

func TestRedeemPurchaseHandler_RequiresAuth(t *testing.T) {
	r := newPurchaseRouter(&purchaseHandlerRepo{}, true, false)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/redeem/purchases", nil),
		httptest.NewRequest(http.MethodPost, "/redeem/purchases", bytes.NewBufferString(`{"amount":10,"request_id":"3f2b8c1e-6a4d-4b7e-9c2a-1d5e8f7a6b3c"}`)),
		httptest.NewRequest(http.MethodPost, "/redeem/purchases/1/revoke", nil),
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, req.URL.Path)
	}
}

func TestRedeemPurchaseHandler_CreateSwitchOffIs403(t *testing.T) {
	repo := &purchaseHandlerRepo{}
	w := httptest.NewRecorder()
	newPurchaseRouter(repo, false, true).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/redeem/purchases",
		bytes.NewBufferString(`{"amount":10,"request_id":"3f2b8c1e-6a4d-4b7e-9c2a-1d5e8f7a6b3c"}`)))
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "REDEEM_PURCHASE_DISABLED")
	require.Zero(t, repo.creates)
}

func TestRedeemPurchaseHandler_CreateReplayWithSwitchOffReturnsOriginal(t *testing.T) {
	repo := &purchaseHandlerRepo{existing: &service.RedeemPurchaseRecord{ID: 5, Amount: decimal.NewFromInt(10), Status: service.RedeemPurchaseStatusActive, Code: "orig", CodeStatus: service.StatusUnused}}
	w := httptest.NewRecorder()
	newPurchaseRouter(repo, false, true).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/redeem/purchases",
		bytes.NewBufferString(`{"amount":"10.00","request_id":"3F2B8C1E-6A4D-4B7E-9C2A-1D5E8F7A6B3C"}`)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	data := decodeData(t, w)
	require.Equal(t, []string{"amount", "code", "created_at", "id", "replayed", "revoked_at", "status", "used_at"}, sortedKeys(data))
	require.EqualValues(t, 5, data["id"])
	require.Equal(t, "orig", data["code"])
	require.Equal(t, true, data["replayed"])
	require.Zero(t, repo.creates)
}

func TestRedeemPurchaseHandler_CreateValidatesInput(t *testing.T) {
	cases := map[string]string{
		`{"amount":10.001,"request_id":"3f2b8c1e-6a4d-4b7e-9c2a-1d5e8f7a6b3c"}`: "REDEEM_PURCHASE_INVALID_AMOUNT",
		`{"amount":"1e3","request_id":"3f2b8c1e-6a4d-4b7e-9c2a-1d5e8f7a6b3c"}`:  "REDEEM_PURCHASE_INVALID_AMOUNT",
		`{"request_id":"3f2b8c1e-6a4d-4b7e-9c2a-1d5e8f7a6b3c"}`:                 "REDEEM_PURCHASE_INVALID_AMOUNT",
		`{"amount":10,"request_id":"not-a-uuid"}`:                               "REDEEM_PURCHASE_INVALID_REQUEST_ID",
	}
	for body, reason := range cases {
		repo := &purchaseHandlerRepo{}
		w := httptest.NewRecorder()
		newPurchaseRouter(repo, true, true).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/redeem/purchases", bytes.NewBufferString(body)))
		require.Equal(t, http.StatusBadRequest, w.Code, body)
		require.Contains(t, w.Body.String(), reason, body)
		require.Zero(t, repo.creates)
	}

	w := httptest.NewRecorder()
	newPurchaseRouter(&purchaseHandlerRepo{}, true, true).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/redeem/purchases", bytes.NewBufferString(`{"amount":true}`)))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRedeemPurchaseHandler_CreateSuccess(t *testing.T) {
	repo := &purchaseHandlerRepo{}
	w := httptest.NewRecorder()
	newPurchaseRouter(repo, true, true).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/redeem/purchases",
		bytes.NewBufferString(`{"amount":19.99,"request_id":"3f2b8c1e-6a4d-4b7e-9c2a-1d5e8f7a6b3c"}`)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	data := decodeData(t, w)
	require.EqualValues(t, 19.99, data["amount"])
	require.Equal(t, "unused", data["status"])
	require.Equal(t, false, data["replayed"])
	require.Equal(t, 1, repo.creates)
}

func TestRedeemPurchaseHandler_RevokeBadIDIsNotFound(t *testing.T) {
	for _, id := range []string{"abc", "0", "-1"} {
		w := httptest.NewRecorder()
		newPurchaseRouter(&purchaseHandlerRepo{}, true, true).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/redeem/purchases/"+id+"/revoke", nil))
		require.Equal(t, http.StatusNotFound, w.Code, id)
		require.Contains(t, w.Body.String(), "REDEEM_PURCHASE_NOT_FOUND")
	}
}
