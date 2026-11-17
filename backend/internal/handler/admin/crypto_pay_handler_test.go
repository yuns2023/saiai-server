//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeCryptoPayAdminService struct {
	gotOperator string
	gotManifest []byte
	gotSettings service.UpdateCryptoPaySettingsRequest
	gotFilter   service.CryptoPayAdminDepositFilter
	deposits    []service.CryptoPayAdminDeposit
}

func (f *fakeCryptoPayAdminService) GetAdminSettings(context.Context) (*service.CryptoPayAdminSettings, error) {
	return &service.CryptoPayAdminSettings{MinAmount: "1", MaxAmount: "10000", OrderTTLMinutes: 180, HoldThresholdAmount: "5000", Networks: []string{}}, nil
}
func (f *fakeCryptoPayAdminService) UpdateSettings(_ context.Context, req service.UpdateCryptoPaySettingsRequest, op string) (*service.CryptoPayAdminSettings, error) {
	f.gotSettings, f.gotOperator = req, op
	return f.GetAdminSettings(context.Background())
}
func (f *fakeCryptoPayAdminService) ListKeysets(context.Context) ([]service.CryptoPayKeysetInfo, error) {
	return nil, nil
}
func (f *fakeCryptoPayAdminService) ImportEVMKeyset(_ context.Context, _, _, op string) (*service.CryptoPayKeysetInfo, error) {
	f.gotOperator = op
	return &service.CryptoPayKeysetInfo{ID: 1}, nil
}
func (f *fakeCryptoPayAdminService) ImportSolanaKeyset(_ context.Context, raw []byte, op string) (*service.CryptoPayKeysetInfo, error) {
	f.gotManifest, f.gotOperator = raw, op
	return &service.CryptoPayKeysetInfo{ID: 2}, nil
}
func (f *fakeCryptoPayAdminService) AdminListDeposits(_ context.Context, flt service.CryptoPayAdminDepositFilter, _, _ int) ([]service.CryptoPayAdminDeposit, int64, error) {
	f.gotFilter = flt
	return f.deposits, int64(len(f.deposits)), nil
}
func (f *fakeCryptoPayAdminService) ReleaseDeposit(_ context.Context, id int64, op string) (*service.CryptoPayCreditOutcome, error) {
	f.gotOperator = op
	return &service.CryptoPayCreditOutcome{DepositID: id, Status: "credited", Credited: true}, nil
}
func (f *fakeCryptoPayAdminService) RescanTransaction(_ context.Context, _, _, op string) (*service.CryptoPayRescanResult, error) {
	f.gotOperator = op
	return &service.CryptoPayRescanResult{Results: []service.CryptoPayCreditOutcome{}}, nil
}
func (f *fakeCryptoPayAdminService) Reconciliation(context.Context) (*service.CryptoPayReconciliation, error) {
	return &service.CryptoPayReconciliation{Deposits: []service.CryptoPayReconciliationRow{}}, nil
}

func cryptoPayAdminRouter(h *CryptoPayHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})
		c.Next()
	})
	r.GET("/settings", h.GetSettings)
	r.PUT("/settings", h.UpdateSettings)
	r.GET("/keysets", h.ListKeysets)
	r.POST("/keysets/solana", h.ImportSolanaKeyset)
	r.GET("/deposits", h.ListDeposits)
	r.POST("/deposits/:id/release", h.ReleaseDeposit)
	r.POST("/rescan", h.Rescan)
	return r
}

func cryptoPayAdminDo(t *testing.T, r *gin.Engine, method, path, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestCryptoPayAdminSettingsJSON(t *testing.T) {
	fake := &fakeCryptoPayAdminService{}
	r := cryptoPayAdminRouter(&CryptoPayHandler{svc: fake})
	code, body := cryptoPayAdminDo(t, r, http.MethodGet, "/settings", "")
	require.Equal(t, http.StatusOK, code)
	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &env))
	for _, k := range []string{"allow_new_addresses", "scanning_enabled", "auto_credit_enabled", "min_amount", "max_amount", "order_ttl_minutes", "hold_threshold_amount"} {
		require.Contains(t, env.Data, k)
	}
	require.Equal(t, "5000", env.Data["hold_threshold_amount"])

	code, _ = cryptoPayAdminDo(t, r, http.MethodPut, "/settings", `{"scanning_enabled":true,"hold_threshold_amount":"100"}`)
	require.Equal(t, http.StatusOK, code)
	require.NotNil(t, fake.gotSettings.ScanningEnabled)
	require.True(t, *fake.gotSettings.ScanningEnabled)
	require.Nil(t, fake.gotSettings.AllowNewAddresses, "absent switches are not touched")
	require.Equal(t, "100", *fake.gotSettings.HoldThresholdAmount)
	require.Equal(t, "admin:1", fake.gotOperator)
}

func TestCryptoPayAdminManifestAndLists(t *testing.T) {
	fake := &fakeCryptoPayAdminService{}
	r := cryptoPayAdminRouter(&CryptoPayHandler{svc: fake})
	code, _ := cryptoPayAdminDo(t, r, http.MethodPost, "/keysets/solana", `{"manifest":{"version":1}}`)
	require.Equal(t, http.StatusCreated, code)
	require.JSONEq(t, `{"version":1}`, string(fake.gotManifest))
	code, _ = cryptoPayAdminDo(t, r, http.MethodPost, "/keysets/solana", `{"manifest":"{\"version\":1}"}`)
	require.Equal(t, http.StatusCreated, code)
	require.Equal(t, `{"version":1}`, string(fake.gotManifest), "string form is unwrapped")

	code, body := cryptoPayAdminDo(t, r, http.MethodGet, "/keysets", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"data":[]`)
	code, body = cryptoPayAdminDo(t, r, http.MethodGet, "/deposits?status=held&user_id=5", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"items":[]`)
	require.Equal(t, "held", fake.gotFilter.Status)
	require.Equal(t, int64(5), fake.gotFilter.UserID)

	code, body = cryptoPayAdminDo(t, r, http.MethodPost, "/deposits/7/release", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"deposit_id":7`)
	code, _ = cryptoPayAdminDo(t, r, http.MethodPost, "/rescan", `{"network":"arbitrum-one"}`)
	require.Equal(t, http.StatusBadRequest, code, "tx_hash is required")
}

// The admin held-deposits page (frontend/src/api/admin/cryptoPay.ts) reads
// these exact keys; a missing json tag would silently blank a column.
func TestCryptoPayAdminDepositListJSONFields(t *testing.T) {
	credited := time.Date(2026, 11, 17, 1, 2, 3, 0, time.UTC)
	fake := &fakeCryptoPayAdminService{deposits: []service.CryptoPayAdminDeposit{{
		ID: 9, UserID: 5, UserEmail: "u@example.test", UserName: "alice", UserStatus: "active",
		Network: "arbitrum-one", Asset: "USDT", Address: "0xabc", Amount: "6000", TxHash: "0xtx",
		Status: "credited", HoldReason: "amount_over_threshold", ObservedAt: credited, BlockTime: credited,
		CreditedAt: &credited, CreditedBy: "admin:1", ExplorerTxURL: "https://arbiscan.io/tx/{tx}", ExplorerAddressURL: "https://arbiscan.io/address/{addr}",
	}}}
	r := cryptoPayAdminRouter(&CryptoPayHandler{svc: fake})
	code, body := cryptoPayAdminDo(t, r, http.MethodGet, "/deposits?hold=released&page=2&page_size=10", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, service.CryptoPayHoldFilterReleased, fake.gotFilter.Hold)

	var env struct {
		Data struct {
			Items    []map[string]any `json:"items"`
			Total    *int64           `json:"total"`
			Page     *int             `json:"page"`
			PageSize *int             `json:"page_size"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &env))
	require.NotNil(t, env.Data.Total)
	require.NotNil(t, env.Data.Page)
	require.NotNil(t, env.Data.PageSize)
	require.Equal(t, 2, *env.Data.Page)
	require.Len(t, env.Data.Items, 1)
	item := env.Data.Items[0]
	for _, k := range []string{"id", "user_id", "user_email", "user_name", "user_status", "user_deleted", "network", "asset",
		"address", "amount", "tx_hash", "status", "hold_reason", "observed_at", "block_time", "credited_at", "credited_by", "explorer_tx_url", "explorer_address_url"} {
		require.Contains(t, item, k)
	}
	require.Equal(t, "alice", item["user_name"])
	require.Equal(t, "admin:1", item["credited_by"])
	require.Equal(t, "amount_over_threshold", item["hold_reason"])
	require.Equal(t, "6000", item["amount"], "amounts stay decimal strings")

	code, body = cryptoPayAdminDo(t, r, http.MethodPost, "/deposits/9/release", "")
	require.Equal(t, http.StatusOK, code)
	var rel struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &rel))
	for _, k := range []string{"deposit_id", "status", "hold_reason", "credited"} {
		require.Contains(t, rel.Data, k)
	}
	code, _ = cryptoPayAdminDo(t, r, http.MethodPost, "/deposits/abc/release", "")
	require.Equal(t, http.StatusBadRequest, code)
}
