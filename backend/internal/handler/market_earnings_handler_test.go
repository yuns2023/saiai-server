//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// earningsRepo records which owner every call was made for.
type earningsRepo struct {
	service.OwnerEarningRepository
	owners   []int64
	converts []service.OwnerEarningConvertInput
}

func (r *earningsRepo) Balances(_ context.Context, owner int64) (*service.OwnerEarningBalances, error) {
	r.owners = append(r.owners, owner)
	return &service.OwnerEarningBalances{Frozen: decimal.RequireFromString("1.2"), Available: decimal.RequireFromString("3.5"),
		Converted: decimal.RequireFromString("2"), AsOf: time.Unix(1_800_000_000, 0).UTC()}, nil
}

func (r *earningsRepo) ListEntries(_ context.Context, owner, before int64, limit int) ([]service.OwnerEarningEntry, error) {
	r.owners = append(r.owners, owner)
	account, event, model := int64(11), int64(5), "claude-test"
	at := time.Unix(1_800_000_000, 0).UTC()
	return []service.OwnerEarningEntry{{ID: 9, OwnerUserID: owner, Kind: service.OwnerEarningEntryAccrue, Amount: decimal.RequireFromString("0.8"),
		AvailableAt: &at, CreatedAt: at, Status: service.OwnerEarningStatusFrozen, AccountID: &account, Model: &model, SettlementEventID: &event}}, nil
}

func (r *earningsRepo) Convert(_ context.Context, in service.OwnerEarningConvertInput) (*service.OwnerEarningConvertRecord, error) {
	r.converts = append(r.converts, in)
	return &service.OwnerEarningConvertRecord{EntryID: 4, Amount: in.Amount, RequestID: in.RequestID, CreatedAt: time.Unix(1_800_000_000, 0).UTC()}, nil
}

type earningsSettings struct{ service.SettingRepository }

func (earningsSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{service.SettingKeyMarketMinConvertUSD: "2.5"}, nil
}

func newEarningsRouter(repo *earningsRepo, userID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewMarketEarningsHandler(service.NewOwnerEarningService(repo, earningsSettings{}, nil, nil))
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
		c.Next()
	})
	r.GET("/hosted/earnings", h.Summary)
	r.GET("/hosted/earnings/entries", h.Entries)
	r.POST("/hosted/earnings/convert", h.Convert)
	return r
}

func earningsData(t *testing.T, w *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Data
}

func TestMarketEarningsHandlerJSONAndOwnerScope(t *testing.T) {
	repo := &earningsRepo{}
	w := httptest.NewRecorder()
	newEarningsRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hosted/earnings", nil))
	data := earningsData(t, w)
	require.Equal(t, []string{"as_of", "available", "converted_total", "frozen", "min_convert"}, jsonKeys(t, data))
	require.JSONEq(t, `{"frozen":"1.2","available":"3.5","converted_total":"2","min_convert":"2.5","as_of":"2027-01-15T08:00:00Z"}`, string(data))

	w = httptest.NewRecorder()
	newEarningsRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hosted/earnings/entries?cursor=50&limit=1", nil))
	data = earningsData(t, w)
	require.Equal(t, []string{"items", "next_cursor"}, jsonKeys(t, data))
	var page struct {
		Items      []json.RawMessage `json:"items"`
		NextCursor *int64            `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(data, &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, int64(9), *page.NextCursor)
	// No buyer, key, request, charged amount or ledger references for owners.
	require.Equal(t, []string{"account_id", "amount", "available_at", "created_at", "id", "kind", "model", "status"}, jsonKeys(t, page.Items[0]))

	w = httptest.NewRecorder()
	newEarningsRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/hosted/earnings/convert",
		bytes.NewBufferString(`{"amount":"2.50","request_id":"9F1C2B9E-6F00-4E43-9C5A-111111111111"}`)))
	data = earningsData(t, w)
	require.Equal(t, []string{"amount", "created_at", "entry_id", "replayed", "request_id"}, jsonKeys(t, data))
	require.Contains(t, string(data), `"amount":"2.50"`)

	// User 8 only ever reaches its own ledger: the owner is always the
	// authenticated subject, never a parameter.
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/hosted/earnings?owner_user_id=7", nil),
		httptest.NewRequest(http.MethodGet, "/hosted/earnings/entries?owner_user_id=7", nil),
		httptest.NewRequest(http.MethodPost, "/hosted/earnings/convert", bytes.NewBufferString(`{"amount":1,"owner_user_id":7,"request_id":"9f1c2b9e-6f00-4e43-9c5a-222222222222"}`)),
	} {
		w = httptest.NewRecorder()
		newEarningsRouter(repo, 8).ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	require.Equal(t, []int64{7, 7, 8, 8}, repo.owners)
	require.Len(t, repo.converts, 2)
	require.Equal(t, int64(7), repo.converts[0].OwnerUserID)
	require.Equal(t, "9f1c2b9e-6f00-4e43-9c5a-111111111111", repo.converts[0].RequestID)
	require.Equal(t, int64(8), repo.converts[1].OwnerUserID)

	// Validation: more than 2 decimals, bad request id, bad cursor/limit.
	for _, raw := range []string{`{"amount":"1.234","request_id":"9f1c2b9e-6f00-4e43-9c5a-111111111111"}`,
		`{"amount":1,"request_id":"nope"}`, `{"amount":0,"request_id":"9f1c2b9e-6f00-4e43-9c5a-111111111111"}`, `not json`} {
		w = httptest.NewRecorder()
		newEarningsRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/hosted/earnings/convert", bytes.NewBufferString(raw)))
		require.Equal(t, http.StatusBadRequest, w.Code, raw)
	}
	require.Len(t, repo.converts, 2)
	for _, q := range []string{"?limit=101", "?limit=0", "?cursor=-1", "?cursor=x"} {
		w = httptest.NewRecorder()
		newEarningsRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hosted/earnings/entries"+q, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, q)
	}
}
