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

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeCryptoPayUserService struct {
	cfg         *service.CryptoPayUserConfig
	order       *service.CryptoPayOrder
	orders      []service.CryptoPayOrder
	deposits    []service.CryptoPayDeposit
	addresses   []service.CryptoPayUserAddress
	gotAmount   string
	gotUserID   int64
	gotPage     int
	gotPageSize int
}

func (f *fakeCryptoPayUserService) GetUserConfig(context.Context) (*service.CryptoPayUserConfig, error) {
	return f.cfg, nil
}
func (f *fakeCryptoPayUserService) CreateOrder(_ context.Context, userID int64, _, _, amount string) (*service.CryptoPayOrder, error) {
	f.gotUserID, f.gotAmount = userID, amount
	return f.order, nil
}
func (f *fakeCryptoPayUserService) GetUserOrder(_ context.Context, userID, _ int64) (*service.CryptoPayOrder, error) {
	f.gotUserID = userID
	return f.order, nil
}
func (f *fakeCryptoPayUserService) ListUserOrders(_ context.Context, _ int64, page, pageSize int) ([]service.CryptoPayOrder, int64, error) {
	f.gotPage, f.gotPageSize = page, pageSize
	return f.orders, int64(len(f.orders)), nil
}
func (f *fakeCryptoPayUserService) ListUserDeposits(_ context.Context, _ int64, _, _ int) ([]service.CryptoPayDeposit, int64, error) {
	return f.deposits, int64(len(f.deposits)), nil
}
func (f *fakeCryptoPayUserService) ListUserAddresses(context.Context, int64) ([]service.CryptoPayUserAddress, error) {
	return f.addresses, nil
}

func cryptoPayTestRouter(h *CryptoPayHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
		c.Next()
	})
	r.GET("/config", h.GetConfig)
	r.POST("/orders", h.CreateOrder)
	r.GET("/orders/my", h.ListMyOrders)
	r.GET("/orders/:id", h.GetOrder)
	r.GET("/deposits", h.ListDeposits)
	r.GET("/addresses", h.ListAddresses)
	return r
}

func cryptoPayDo(t *testing.T, r *gin.Engine, method, path string, body string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Less(t, w.Code, 300, w.Body.String())
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	return envelope
}

func cryptoPayKeys(t *testing.T, v any) []string {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "expected object, got %T", v)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cryptoPaySorted(keys ...string) []string {
	sort.Strings(keys)
	return keys
}

var (
	cryptoPayOrderKeys = cryptoPaySorted("id", "network", "asset", "amount", "address", "status", "created_at", "expires_at",
		"received_amount", "confirming_amount", "credited_amount", "deposits", "deposits_truncated")
	cryptoPayDepositKeys = cryptoPaySorted("id", "network", "asset", "amount", "tx_hash", "status", "block_time", "credited_at")
)

func sampleCryptoPayOrder() *service.CryptoPayOrder {
	now := time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC)
	return &service.CryptoPayOrder{
		ID: 9, Network: "arbitrum-one", Asset: "USDC", Amount: "12.50", Address: "0xabc", Status: "pending",
		CreatedAt: now, ExpiresAt: now.Add(3 * time.Hour), ReceivedAmount: "1.01", ConfirmingAmount: "0", CreditedAmount: "1.01",
		Deposits: []service.CryptoPayDeposit{{ID: 1, Network: "arbitrum-one", Asset: "USDC", Amount: "1.01", TxHash: "0xt", Status: "credited", BlockTime: now, CreditedAt: &now}},
	}
}

func TestCryptoPayHandlerConfigJSON(t *testing.T) {
	fake := &fakeCryptoPayUserService{cfg: &service.CryptoPayUserConfig{
		Enabled: true, MinAmount: "1", MaxAmount: "10000", OrderTTLMinutes: 180,
		Networks: []service.CryptoPayNetworkInfo{{Network: "arbitrum-one", Label: "Arbitrum",
			Assets: []service.CryptoPayAssetInfo{{Symbol: "USDC", Contract: "0xaf88"}}, FinalityHintMinutes: 20,
			ExplorerTxURL: "https://arbiscan.io/tx/{tx}", ExplorerAddressURL: "https://arbiscan.io/address/{addr}"}},
	}}
	env := cryptoPayDo(t, cryptoPayTestRouter(&CryptoPayHandler{svc: fake}), http.MethodGet, "/config", "")
	data := env["data"].(map[string]any)
	require.Equal(t, cryptoPaySorted("enabled", "min_amount", "max_amount", "order_ttl_minutes", "networks"), cryptoPayKeys(t, data))
	require.Equal(t, "1", data["min_amount"], "money is a string")
	nets := data["networks"].([]any)
	require.Equal(t, cryptoPaySorted("network", "label", "assets", "finality_hint_minutes", "explorer_tx_url", "explorer_address_url"), cryptoPayKeys(t, nets[0]))
	assets := nets[0].(map[string]any)["assets"].([]any)
	require.Equal(t, cryptoPaySorted("symbol", "contract"), cryptoPayKeys(t, assets[0]))

	// Disabled: networks is [] (not null).
	fake.cfg = &service.CryptoPayUserConfig{Enabled: false, MinAmount: "1", MaxAmount: "10000", OrderTTLMinutes: 180, Networks: []service.CryptoPayNetworkInfo{}}
	w := httptest.NewRecorder()
	cryptoPayTestRouter(&CryptoPayHandler{svc: fake}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/config", nil))
	require.Contains(t, w.Body.String(), `"networks":[]`)
}

func TestCryptoPayHandlerOrderJSONAndAmountParsing(t *testing.T) {
	fake := &fakeCryptoPayUserService{order: sampleCryptoPayOrder()}
	r := cryptoPayTestRouter(&CryptoPayHandler{svc: fake})

	env := cryptoPayDo(t, r, http.MethodPost, "/orders", `{"network":"arbitrum-one","asset":"USDC","amount":"12.5"}`)
	require.Equal(t, "12.5", fake.gotAmount)
	require.Equal(t, int64(42), fake.gotUserID)
	order := env["data"]
	require.Equal(t, cryptoPayOrderKeys, cryptoPayKeys(t, order))
	deps := order.(map[string]any)["deposits"].([]any)
	require.Equal(t, cryptoPayDepositKeys, cryptoPayKeys(t, deps[0]))
	require.Equal(t, "1.01", order.(map[string]any)["received_amount"])
	require.Equal(t, "12.50", order.(map[string]any)["amount"])
	require.Equal(t, false, order.(map[string]any)["deposits_truncated"])

	// A JSON number keeps its exact text (no float64 round trip).
	cryptoPayDo(t, r, http.MethodPost, "/orders", `{"network":"arbitrum-one","asset":"USDC","amount":0.1000001}`)
	require.Equal(t, "0.1000001", fake.gotAmount)

	// Empty deposits serialize as [] and a nil credited_at as null.
	o := sampleCryptoPayOrder()
	o.Deposits = []service.CryptoPayDeposit{}
	fake.order = o
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/orders/9", nil))
	require.Contains(t, w.Body.String(), `"deposits":[]`)
}

func TestCryptoPayHandlerListsJSON(t *testing.T) {
	fake := &fakeCryptoPayUserService{orders: []service.CryptoPayOrder{*sampleCryptoPayOrder()}}
	r := cryptoPayTestRouter(&CryptoPayHandler{svc: fake})
	env := cryptoPayDo(t, r, http.MethodGet, "/orders/my?page=2&page_size=500", "")
	data := env["data"].(map[string]any)
	for _, k := range []string{"items", "total", "page", "page_size"} {
		require.Contains(t, data, k)
	}
	require.Equal(t, 2, fake.gotPage)
	require.Equal(t, 100, fake.gotPageSize, "page size is clamped")
	require.Equal(t, cryptoPayOrderKeys, cryptoPayKeys(t, data["items"].([]any)[0]))

	// nil lists become [].
	fake = &fakeCryptoPayUserService{}
	r = cryptoPayTestRouter(&CryptoPayHandler{svc: fake})
	for _, path := range []string{"/orders/my", "/deposits"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Contains(t, w.Body.String(), `"items":[]`, path)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/addresses", nil))
	require.Contains(t, w.Body.String(), `"data":[]`)

	now := time.Now()
	fake.deposits = []service.CryptoPayDeposit{{ID: 1, Status: "confirming", BlockTime: now}}
	fake.addresses = []service.CryptoPayUserAddress{{Network: "solana-mainnet", Address: "So1"}}
	env = cryptoPayDo(t, r, http.MethodGet, "/deposits", "")
	item := env["data"].(map[string]any)["items"].([]any)[0]
	require.Equal(t, cryptoPayDepositKeys, cryptoPayKeys(t, item))
	require.Nil(t, item.(map[string]any)["credited_at"])
	env = cryptoPayDo(t, r, http.MethodGet, "/addresses", "")
	require.Equal(t, cryptoPaySorted("network", "address"), cryptoPayKeys(t, env["data"].([]any)[0]))
}

type cryptoPayHandlerSettings struct{ values map[string]string }

func (s cryptoPayHandlerSettings) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (s cryptoPayHandlerSettings) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}
func (s cryptoPayHandlerSettings) Set(context.Context, string, string) error { return nil }
func (s cryptoPayHandlerSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (s cryptoPayHandlerSettings) SetMultiple(context.Context, map[string]string) error { return nil }
func (s cryptoPayHandlerSettings) GetAll(context.Context) (map[string]string, error) {
	return s.values, nil
}
func (s cryptoPayHandlerSettings) Delete(context.Context, string) error { return nil }

type failingCryptoPayUserService struct {
	fakeCryptoPayUserService
	err error
}

func (f *failingCryptoPayUserService) CreateOrder(context.Context, int64, string, string, string) (*service.CryptoPayOrder, error) {
	return nil, f.err
}

func cryptoPayErrorReason(t *testing.T, r *gin.Engine, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	var env struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
	return w.Code, env.Reason
}

func TestCryptoPayHandlerErrorReasons(t *testing.T) {
	cfg := config.CryptoPayConfig{Enabled: true, ScanIntervalSeconds: 30, ScanBatchTimeoutSeconds: 60, EVMBlocksPerScan: 2000, EVMNetwork: "arbitrum-one", SolanaNetwork: "solana-mainnet"}
	on := service.NewCryptoPayService(nil, cryptoPayHandlerSettings{values: map[string]string{service.SettingCryptoPayAllowNewAddresses: "true"}},
		cfg, service.CryptoPayChainDeps{}, nil, nil)
	r := cryptoPayTestRouter(NewCryptoPayHandler(on))

	code, reason := cryptoPayErrorReason(t, r, `{"network":"arbitrum-one","asset":"USDC","amount":"25.555"}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "CRYPTO_PAY_AMOUNT_INVALID", reason, "more than 2 decimals")
	code, reason = cryptoPayErrorReason(t, r, `{"network":"arbitrum-one","asset":"USDC","amount":"0.5"}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "CRYPTO_PAY_AMOUNT_INVALID", reason, "below min")
	code, reason = cryptoPayErrorReason(t, r, `{"network":"arbitrum-one","asset":"USDC","amount":"abc"}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "CRYPTO_PAY_AMOUNT_INVALID", reason)
	for _, body := range []string{`{"network":"arbitrum-one","asset":"USDC"}`, `{"network":"arbitrum-one","asset":"USDC","amount":""}`,
		`{"network":"arbitrum-one","asset":"USDC","amount":true}`, `{"network":"arbitrum-one","asset":"USDC","amount":{"v":1}}`,
		`{"network":"arbitrum-one","asset":"USDC","amount":null}`} {
		code, reason = cryptoPayErrorReason(t, r, body)
		require.Equal(t, http.StatusBadRequest, code, body)
		require.Equal(t, "CRYPTO_PAY_AMOUNT_INVALID", reason, body)
	}
	code, reason = cryptoPayErrorReason(t, r, `{"asset":"USDC","amount":"10"}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "CRYPTO_PAY_NETWORK_UNSUPPORTED", reason, "missing network")
	code, reason = cryptoPayErrorReason(t, r, `{"network":"ethereum","asset":"USDC","amount":"10"}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "CRYPTO_PAY_NETWORK_UNSUPPORTED", reason)

	off := service.NewCryptoPayService(nil, cryptoPayHandlerSettings{values: map[string]string{}}, cfg, service.CryptoPayChainDeps{}, nil, nil)
	code, reason = cryptoPayErrorReason(t, cryptoPayTestRouter(NewCryptoPayHandler(off)), `{"network":"arbitrum-one","asset":"USDC","amount":"10"}`)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Equal(t, "CRYPTO_PAY_DISABLED", reason)

	unavailable := &failingCryptoPayUserService{err: infraerrors.Conflict(service.CryptoPayReasonAddressUnavailable, "no deposit address is available")}
	code, reason = cryptoPayErrorReason(t, cryptoPayTestRouter(&CryptoPayHandler{svc: unavailable}), `{"network":"solana-mainnet","asset":"USDC","amount":"10"}`)
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, "CRYPTO_PAY_ADDRESS_UNAVAILABLE", reason)
}
