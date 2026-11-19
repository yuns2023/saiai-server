//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type adminEarningsRepo struct {
	service.OwnerEarningRepository
	reverses []service.OwnerEarningReverseInput
	listed   []int64
}

func (r *adminEarningsRepo) AdminSummaries(_ context.Context, owner *int64, offset, limit int) ([]service.AdminOwnerEarningSummary, int64, time.Time, error) {
	return []service.AdminOwnerEarningSummary{{OwnerUserID: 7, Email: "o@example.invalid", Frozen: "1", Available: "2", ConvertedTotal: "3", ReversedTotal: "0.5", AccruedTotal: "6.5"}},
		1, time.Unix(1_800_000_000, 0).UTC(), nil
}

func (r *adminEarningsRepo) ListEntries(_ context.Context, owner, before int64, limit int) ([]service.OwnerEarningEntry, error) {
	r.listed = append(r.listed, owner)
	note, admin, ref := "fraud", int64(1), int64(3)
	return []service.OwnerEarningEntry{{ID: 4, OwnerUserID: 7, Kind: service.OwnerEarningEntryReverse, Amount: decimal.RequireFromString("-0.5"),
		Status: service.OwnerEarningStatusReversed, ReversesEntryID: &ref, AdminUserID: &admin, Note: &note, CreatedAt: time.Unix(1_800_000_000, 0).UTC()}}, nil
}

func (r *adminEarningsRepo) Reverse(_ context.Context, in service.OwnerEarningReverseInput) (*service.OwnerEarningReverseResult, error) {
	r.reverses = append(r.reverses, in)
	at := time.Unix(1_800_000_000, 0).UTC()
	return &service.OwnerEarningReverseResult{AsOf: &at, ReversedCount: 1, ReversedAmount: "0.5",
		Owners: []service.OwnerEarningReverseOwner{{OwnerUserID: 7, Count: 1, Amount: "0.5"}}}, nil
}

func adminKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func adminEarningsData(t *testing.T, w *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Data
}

func TestAdminMarketEarningsHandlerJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &adminEarningsRepo{}
	h := NewMarketEarningsHandler(service.NewOwnerEarningService(repo, nil, nil, nil))
	r := gin.New()
	r.GET("/admin/market/earnings", h.Owners)
	r.GET("/admin/market/earnings/entries", h.Entries)
	r.POST("/admin/market/earnings/reverse", h.Reverse)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/market/earnings?owner_user_id=7", nil))
	data := adminEarningsData(t, w)
	require.Equal(t, []string{"as_of", "items", "page", "page_size", "total"}, adminKeys(t, data))
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal(data, &page))
	require.Equal(t, []string{"accrued_total", "available", "converted_total", "email", "frozen", "owner_user_id", "reversed_total"}, adminKeys(t, page.Items[0]))

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/market/earnings/entries?owner_user_id=7&limit=5", nil))
	data = adminEarningsData(t, w)
	var entries struct {
		Items []json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal(data, &entries))
	require.Equal(t, []string{"account_id", "admin_user_id", "amount", "available_at", "convert_request_id", "created_at", "id", "kind",
		"model", "note", "owner_user_id", "reverses_entry_id", "settlement_event_id", "status"}, adminKeys(t, entries.Items[0]))
	require.Equal(t, []int64{7}, repo.listed)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/market/earnings/reverse", bytes.NewBufferString(`{"account_id":11,"as_of":"2026-11-19T08:00:00Z","note":" fraud "}`)))
	data = adminEarningsData(t, w)
	require.Equal(t, []string{"as_of", "cutoff", "owners", "reversed_amount", "reversed_count"}, adminKeys(t, data))
	require.Equal(t, "2026-11-19T08:00:00Z", repo.reverses[0].AsOf.UTC().Format(time.RFC3339))
	require.Len(t, repo.reverses, 1)
	require.Equal(t, int64(11), *repo.reverses[0].AccountID)
	require.Nil(t, repo.reverses[0].OwnerUserID)
	require.Equal(t, "fraud", repo.reverses[0].Note)

	for _, raw := range []string{`{}`, `{"note":"x"}`, `{"account_id":0,"as_of":"2026-11-19T08:00:00Z"}`, `{"owner_user_id":-1,"as_of":"2026-11-19T08:00:00Z"}`,
		`{"account_id":11}`, `{"account_id":11,"as_of":"yesterday"}`, `not json`} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/market/earnings/reverse", bytes.NewBufferString(raw)))
		require.Equal(t, http.StatusBadRequest, w.Code, raw)
	}
	require.Len(t, repo.reverses, 1)
	for _, q := range []string{"/admin/market/earnings?owner_user_id=x", "/admin/market/earnings/entries?cursor=-2", "/admin/market/earnings/entries?limit=500"} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, q, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, q)
	}
}
