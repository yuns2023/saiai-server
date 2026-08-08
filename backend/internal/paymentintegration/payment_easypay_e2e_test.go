package paymentintegration

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

const (
	easyPayE2EPID   = "TEST_ONLY_MERCHANT"
	easyPayE2EKey   = "TEST_ONLY_PROVIDER_SECRET"
	easyPayE2EPrice = "10.00"
)

type easyPayE2EMock struct {
	mu          sync.Mutex
	createCalls int
	refundCalls int
	createForms []url.Values
	refundForms []url.Values
	server      *httptest.Server
}

func newEasyPayE2EMock(t *testing.T) *easyPayE2EMock {
	t.Helper()
	mock := &easyPayE2EMock{}
	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		switch {
		case r.URL.Path == "/mapi.php":
			if !easyPayE2EVerifyForm(r.PostForm, easyPayE2EKey) {
				http.Error(w, "invalid create signature", http.StatusBadRequest)
				return
			}
			mock.mu.Lock()
			mock.createCalls++
			mock.createForms = append(mock.createForms, cloneE2EValues(r.PostForm))
			mock.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 1, "trade_no": "provider-" + r.PostForm.Get("out_trade_no"),
				"payurl": mock.server.URL + "/checkout/" + r.PostForm.Get("out_trade_no"),
				"qrcode": "TEST_ONLY_QR",
			})
		case r.URL.Path == "/api.php" && r.URL.Query().Get("act") == "refund":
			if r.PostForm.Get("pid") != easyPayE2EPID || r.PostForm.Get("key") != easyPayE2EKey {
				http.Error(w, "invalid refund credentials", http.StatusUnauthorized)
				return
			}
			mock.mu.Lock()
			mock.refundCalls++
			mock.refundForms = append(mock.refundForms, cloneE2EValues(r.PostForm))
			mock.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":1,"msg":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(mock.server.Close)
	return mock
}

func TestNativePaymentEasyPaySignedCallbackAutomaticAndManualRefundE2E(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	mock := newEasyPayE2EMock(t)
	db, client := newPaymentE2ETestDatabase(t)

	settingRepo := repository.NewSettingRepository(client)
	require.NoError(t, settingRepo.SetMultiple(ctx, map[string]string{
		service.SettingPaymentEnabled:     "false",
		service.SettingPaymentMinAmount:   "1",
		service.SettingPaymentMaxAmount:   "1000",
		service.SettingPaymentTimeoutMin:  "5",
		service.SettingPaymentMaxPending:  "3",
		service.SettingPaymentRechargeFee: "0",
	}))
	configService := service.NewPaymentConfigService(client, settingRepo, paymentE2ETestEncryptor{})
	providerInstance, err := configService.CreateProviderInstance(ctx, service.CreatePaymentProviderRequest{
		ProviderKey: payment.TypeEasyPay, Name: "Local signed mock",
		Config: map[string]string{
			"pid": easyPayE2EPID, "pkey": easyPayE2EKey, "apiBase": mock.server.URL,
			"notifyUrl": mock.server.URL + "/api/v1/payment/webhook/easypay",
			"returnUrl": mock.server.URL + "/purchase", "paymentMode": "qrcode", "currency": "CNY",
		},
		SupportedTypes: []string{payment.TypeAlipay}, BalanceCreditRate: 1, Enabled: true,
	})
	require.NoError(t, err)
	enabled := true
	require.NoError(t, configService.UpdatePaymentConfig(ctx, service.UpdatePaymentConfigRequest{Enabled: &enabled}))

	userEntity, err := client.User.Create().
		SetEmail("payment-e2e@example.test").SetPasswordHash("TEST_ONLY_HASH").SetBalance(0).Save(ctx)
	require.NoError(t, err)
	userRepo := repository.NewUserRepository(client, db)
	redeemService := service.NewRedeemService(
		repository.NewRedeemCodeRepository(client), userRepo, nil, nil, nil, client, nil,
	)
	paymentService := service.NewPaymentService(client, userRepo, redeemService, configService)
	webhookHandler := handler.NewPaymentWebhookHandler(paymentService)
	router := gin.New()
	router.POST("/api/v1/payment/webhook/:provider", webhookHandler.Notify)

	first := createEasyPayE2EOrder(t, paymentService, userEntity.ID, providerInstance.ID)
	second := createEasyPayE2EOrder(t, paymentService, userEntity.ID, providerInstance.ID)
	require.Equal(t, 2, mock.createCallCount())
	require.Equal(t, easyPayE2EPrice, mock.createForm(0).Get("money"))
	require.Equal(t, easyPayE2EPID, mock.createForm(0).Get("pid"))

	invalid := easyPayE2ECallback(first.OutTradeNo, "provider-"+first.OutTradeNo, easyPayE2EPrice)
	invalid.Set("sign", "00000000000000000000000000000000")
	response := postEasyPayE2ECallback(t, router, invalid)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, payment.OrderStatusPending, mustPaymentE2EOrder(t, client, first.OrderID).Status)

	response = postEasyPayE2ECallback(t, router, signedEasyPayE2ECallback(first.OutTradeNo))
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "success", response.Body.String())
	response = postEasyPayE2ECallback(t, router, signedEasyPayE2ECallback(first.OutTradeNo))
	require.Equal(t, http.StatusOK, response.Code)
	response = postEasyPayE2ECallback(t, router, signedEasyPayE2ECallback(second.OutTradeNo))
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, 20.0, mustPaymentE2EUser(t, client, userEntity.ID).Balance)

	disabled := false
	require.NoError(t, configService.UpdatePaymentConfig(ctx, service.UpdatePaymentConfigRequest{Enabled: &disabled}))
	_, err = configService.UpdateProviderInstance(ctx, providerInstance.ID, service.UpdatePaymentProviderRequest{Enabled: &disabled})
	require.NoError(t, err)

	automaticRequest := service.RequestPaymentRefundInput{
		Mode: payment.RefundModeAutomatic, Reason: "automatic local mock refund", Operator: "admin:test",
	}
	automatic, err := paymentService.RequestRefund(ctx, first.OrderID, automaticRequest)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefunded, automatic.Status)
	require.Equal(t, easyPayE2EPrice, mock.refundForm(0).Get("money"))
	require.Equal(t, first.OutTradeNo, mock.refundForm(0).Get("out_trade_no"))
	require.Equal(t, 10.0, mustPaymentE2EUser(t, client, userEntity.ID).Balance)

	_, err = paymentService.RequestRefund(ctx, first.OrderID, automaticRequest)
	require.NoError(t, err)
	require.Equal(t, 1, mock.refundCallCount(), "idempotent retry must not call EasyPay twice")

	manual, err := paymentService.RequestRefund(ctx, second.OrderID, service.RequestPaymentRefundInput{
		Mode: payment.RefundModeManual, Reason: "refunded in provider console",
		ExternalReference: "TEST_ONLY_MANUAL_REFUND_2", Operator: "admin:test",
	})
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefunded, manual.Status)
	require.Equal(t, "TEST_ONLY_MANUAL_REFUND_2", manual.RefundID)
	require.Equal(t, 0.0, mustPaymentE2EUser(t, client, userEntity.ID).Balance)
	require.Equal(t, 1, mock.refundCallCount(), "manual refund must not call EasyPay")
}

func createEasyPayE2EOrder(t *testing.T, paymentService *service.PaymentService, userID, providerID int64) *service.CreatePaymentOrderResponse {
	t.Helper()
	created, err := paymentService.CreateOrder(context.Background(), service.CreatePaymentOrderRequest{
		UserID: userID, Amount: 10, OrderType: payment.OrderTypeBalance,
		ProviderInstanceID: providerID, PaymentType: payment.TypeAlipay, ClientIP: "127.0.0.1",
	})
	require.NoError(t, err)
	require.Equal(t, easyPayE2EPrice, payment.FormatAmountForCurrency(created.PayAmount, created.Currency))
	return created
}

func signedEasyPayE2ECallback(orderID string) url.Values {
	values := easyPayE2ECallback(orderID, "provider-"+orderID, easyPayE2EPrice)
	values.Set("sign", easyPayE2ESign(values, easyPayE2EKey))
	values.Set("sign_type", "MD5")
	return values
}

func easyPayE2ECallback(orderID, tradeNo, amount string) url.Values {
	return url.Values{
		"pid": {easyPayE2EPID}, "out_trade_no": {orderID}, "trade_no": {tradeNo},
		"money": {amount}, "trade_status": {"TRADE_SUCCESS"},
	}
}

func postEasyPayE2ECallback(t *testing.T, router http.Handler, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/payment/webhook/easypay", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recorder, request)
	return recorder
}

func easyPayE2EVerifyForm(values url.Values, key string) bool {
	return values.Get("sign") == easyPayE2ESign(values, key)
}

func easyPayE2ESign(values url.Values, key string) string {
	keys := make([]string, 0, len(values))
	for name := range values {
		if name != "sign" && name != "sign_type" && values.Get(name) != "" {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, name := range keys {
		parts = append(parts, name+"="+values.Get(name))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key))
	return hex.EncodeToString(sum[:])
}

func cloneE2EValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, items := range values {
		clone[key] = append([]string(nil), items...)
	}
	return clone
}

func (m *easyPayE2EMock) createCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createCalls
}

func (m *easyPayE2EMock) refundCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refundCalls
}

func (m *easyPayE2EMock) createForm(index int) url.Values {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneE2EValues(m.createForms[index])
}

func (m *easyPayE2EMock) refundForm(index int) url.Values {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneE2EValues(m.refundForms[index])
}

type paymentE2ETestEncryptor struct{}

func (paymentE2ETestEncryptor) Encrypt(value string) (string, error) {
	return "TEST_ONLY_ENCRYPTED:" + base64.RawStdEncoding.EncodeToString([]byte(value)), nil
}

func (paymentE2ETestEncryptor) Decrypt(value string) (string, error) {
	encoded := strings.TrimPrefix(value, "TEST_ONLY_ENCRYPTED:")
	if encoded == value {
		return "", fmt.Errorf("invalid test ciphertext")
	}
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	return string(decoded), err
}

func newPaymentE2ETestDatabase(t *testing.T) (*sql.DB, *dbent.Client) {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_fk=1"
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	driver := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(driver)))
	t.Cleanup(func() {
		_ = client.Close()
		_ = db.Close()
	})
	return db, client
}

func mustPaymentE2EUser(t *testing.T, client *dbent.Client, id int64) *dbent.User {
	t.Helper()
	user, err := client.User.Get(context.Background(), id)
	require.NoError(t, err)
	return user
}

func mustPaymentE2EOrder(t *testing.T, client *dbent.Client, id int64) *dbent.PaymentOrder {
	t.Helper()
	order, err := client.PaymentOrder.Get(context.Background(), id)
	require.NoError(t, err)
	return order
}
