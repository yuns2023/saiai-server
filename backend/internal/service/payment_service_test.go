package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	paymentprovider "github.com/Wei-Shaw/sub2api/internal/payment/provider"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newPaymentTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(driver)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type paymentTestSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func newPaymentTestSettings(enabled bool) *paymentTestSettings {
	return &paymentTestSettings{values: map[string]string{
		SettingPaymentEnabled: strconvBool(enabled), SettingPaymentMinAmount: "1",
		SettingPaymentMaxAmount: "1000", SettingPaymentTimeoutMin: "5",
		SettingPaymentMaxPending: "3", SettingPaymentRechargeFee: "0",
	}}
}

func (s *paymentTestSettings) Get(_ context.Context, key string) (*Setting, error) {
	value, err := s.GetValue(context.Background(), key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: value}, nil
}
func (s *paymentTestSettings) GetValue(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}
func (s *paymentTestSettings) Set(_ context.Context, key, value string) error {
	return s.SetMultiple(context.Background(), map[string]string{key: value})
}
func (s *paymentTestSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		result[key] = s.values[key]
	}
	return result, nil
}
func (s *paymentTestSettings) SetMultiple(_ context.Context, values map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}
func (s *paymentTestSettings) GetAll(_ context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]string, len(s.values))
	for key, value := range s.values {
		result[key] = value
	}
	return result, nil
}
func (s *paymentTestSettings) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func strconvBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

type paymentTestEncryptor struct{}

func (paymentTestEncryptor) Encrypt(value string) (string, error) {
	return "encrypted:" + base64.RawStdEncoding.EncodeToString([]byte(value)), nil
}
func (paymentTestEncryptor) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, "encrypted:") {
		return "", errors.New("invalid ciphertext")
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "encrypted:"))
	return string(decoded), err
}

func paymentTestProviderConfig() map[string]string {
	return map[string]string{
		"pid": "merchant-1", "pkey": "never-return-this-secret", "apiBase": "https://pay.example.test",
		"notifyUrl": "https://gateway.example.test/api/v1/payment/webhook/easypay",
		"returnUrl": "https://gateway.example.test/payment/result", "paymentMode": "popup",
	}
}

type extensibleTestProvider struct{}

func (extensibleTestProvider) Name() string                          { return "Wallet test" }
func (extensibleTestProvider) ProviderKey() string                   { return "wallet_test" }
func (extensibleTestProvider) SupportedTypes() []payment.PaymentType { return []string{"wallet"} }
func (extensibleTestProvider) Currency() string                      { return "CNY" }
func (extensibleTestProvider) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	return nil, errors.New("unused")
}
func (extensibleTestProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return nil, errors.New("unused")
}
func (extensibleTestProvider) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	return nil, errors.New("unused")
}

func TestPaymentConfigEncryptsAndMasksProviderSecrets(t *testing.T) {
	ctx := context.Background()
	client := newPaymentTestClient(t)
	settings := newPaymentTestSettings(false)
	configService := NewPaymentConfigService(client, settings, paymentTestEncryptor{})

	_, err := configService.UpdateProviderInstance(ctx, 999, UpdatePaymentProviderRequest{})
	require.Error(t, err)

	created, err := configService.CreateProviderInstance(ctx, CreatePaymentProviderRequest{
		ProviderKey: payment.TypeEasyPay, Name: "primary", Config: paymentTestProviderConfig(),
		SupportedTypes: []string{payment.TypeAlipay, payment.TypeWxpay}, Enabled: true,
	})
	require.NoError(t, err)
	require.NotContains(t, created.Config, "pkey")
	require.Equal(t, []string{"pkey"}, created.ConfiguredSecrets)
	stored, err := client.PaymentProviderInstance.Get(ctx, created.ID)
	require.NoError(t, err)
	require.NotContains(t, stored.ConfigEncrypted, "never-return-this-secret")

	enabled := true
	require.NoError(t, configService.UpdatePaymentConfig(ctx, UpdatePaymentConfigRequest{Enabled: &enabled}))
	cfg, err := configService.GetPaymentConfig(ctx)
	require.NoError(t, err)
	require.True(t, cfg.Enabled)

	disabled := false
	_, err = configService.UpdateProviderInstance(ctx, created.ID, UpdatePaymentProviderRequest{Enabled: &disabled})
	require.Error(t, err)
	require.Contains(t, err.Error(), "last enabled provider")
	require.NoError(t, configService.UpdatePaymentConfig(ctx, UpdatePaymentConfigRequest{Enabled: &disabled}))
	_, err = configService.UpdateProviderInstance(ctx, created.ID, UpdatePaymentProviderRequest{Enabled: &disabled})
	require.NoError(t, err)
}

func TestPaymentConfigCannotEnableWithoutProvider(t *testing.T) {
	client := newPaymentTestClient(t)
	configService := NewPaymentConfigService(client, newPaymentTestSettings(false), paymentTestEncryptor{})
	enabled := true
	err := configService.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{Enabled: &enabled})
	require.Error(t, err)
	require.Contains(t, err.Error(), "enable at least one payment provider")
}

func TestPaymentConfigAcceptsRegisteredProviderWithoutServiceBranch(t *testing.T) {
	require.NoError(t, paymentprovider.Register(paymentprovider.Definition{
		Key: "wallet_test", Name: "Wallet test", PaymentTypes: []string{"wallet"},
		ConfigFields: []paymentprovider.ConfigField{{Key: "access_token", Label: "Access token", Kind: "password", Required: true, Secret: true}},
		Constructor: func(_ string, config map[string]string) (payment.Provider, error) {
			if strings.TrimSpace(config["access_token"]) == "" {
				return nil, errors.New("missing access token")
			}
			return extensibleTestProvider{}, nil
		},
		NotificationOrderID: func(raw string, _ map[string]string) (string, error) { return raw, nil },
	}))
	client := newPaymentTestClient(t)
	configService := NewPaymentConfigService(client, newPaymentTestSettings(false), paymentTestEncryptor{})
	created, err := configService.CreateProviderInstance(context.Background(), CreatePaymentProviderRequest{
		ProviderKey: "wallet_test", Name: "wallet", Config: map[string]string{"access_token": "top-secret"},
		SupportedTypes: []string{"wallet"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"wallet"}, created.SupportedTypes)
	require.NotContains(t, created.Config, "access_token")
	require.Equal(t, []string{"access_token"}, created.ConfiguredSecrets)
}

func TestRetiredPurchaseIframeDoesNotExpandFrameSrc(t *testing.T) {
	settings := newPaymentTestSettings(false)
	settings.values[SettingKeyPurchaseSubscriptionEnabled] = "true"
	settings.values[SettingKeyPurchaseSubscriptionURL] = "https://retired-pay.example.test/checkout"
	settings.values[SettingKeyCustomMenuItems] = `[{"url":"https://custom.example.test/page"}]`
	service := NewSettingService(settings, &config.Config{})
	origins, err := service.GetFrameSrcOrigins(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"https://custom.example.test"}, origins)
}

type paymentTestUserReader struct{ user *User }

func (r paymentTestUserReader) GetByID(context.Context, int64) (*User, error) { return r.user, nil }

type paymentTestRedeemer struct {
	codes       map[string]*RedeemCode
	redeemCalls int
}

func (r *paymentTestRedeemer) GetByCode(_ context.Context, code string) (*RedeemCode, error) {
	item, ok := r.codes[code]
	if !ok {
		return nil, ErrRedeemCodeNotFound
	}
	copy := *item
	return &copy, nil
}
func (r *paymentTestRedeemer) CreateCode(_ context.Context, code *RedeemCode) error {
	copy := *code
	r.codes[code.Code] = &copy
	return nil
}
func (r *paymentTestRedeemer) Redeem(_ context.Context, userID int64, code string) (*RedeemCode, error) {
	r.redeemCalls++
	item := r.codes[code]
	if item.UsedBy != nil {
		return nil, ErrRedeemCodeUsed
	}
	item.Status = StatusUsed
	item.UsedBy = &userID
	copy := *item
	return &copy, nil
}

type paymentTestProvider struct {
	notification        *payment.PaymentNotification
	queryResult         *payment.QueryOrderResponse
	queryErr            error
	refundResponse      *payment.RefundResponse
	refundErr           error
	refundQueryResponse *payment.RefundResponse
	refundQueryErr      error
	refundCalls         int
	lastRefundRequest   payment.RefundRequest
}

func (*paymentTestProvider) Name() string        { return "test" }
func (*paymentTestProvider) ProviderKey() string { return payment.TypeEasyPay }
func (*paymentTestProvider) Currency() string    { return payment.DefaultPaymentCurrency }
func (*paymentTestProvider) SupportedTypes() []payment.PaymentType {
	return []string{payment.TypeAlipay, payment.TypeWxpay}
}
func (*paymentTestProvider) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	return &payment.CreatePaymentResponse{TradeNo: "created-trade", PayURL: "https://pay.example.test/checkout"}, nil
}
func (p *paymentTestProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return p.queryResult, p.queryErr
}
func (p *paymentTestProvider) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	return p.notification, nil
}
func (p *paymentTestProvider) Refund(_ context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	p.refundCalls++
	p.lastRefundRequest = req
	return p.refundResponse, p.refundErr
}
func (p *paymentTestProvider) QueryRefund(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	return p.refundQueryResponse, p.refundQueryErr
}

func newPaymentServiceFixture(t *testing.T) (*PaymentService, *dbent.Client, *paymentTestProvider, *paymentTestRedeemer) {
	t.Helper()
	ctx := context.Background()
	client := newPaymentTestClient(t)
	configService := NewPaymentConfigService(client, newPaymentTestSettings(true), paymentTestEncryptor{})
	providerRow, err := configService.CreateProviderInstance(ctx, CreatePaymentProviderRequest{
		ProviderKey: payment.TypeEasyPay, Name: "primary", Config: paymentTestProviderConfig(),
		SupportedTypes: []string{payment.TypeAlipay}, Enabled: true,
	})
	require.NoError(t, err)
	require.Positive(t, providerRow.ID)
	fakeProvider := &paymentTestProvider{}
	redeemer := &paymentTestRedeemer{codes: make(map[string]*RedeemCode)}
	service := &PaymentService{
		entClient:     client,
		userRepo:      paymentTestUserReader{user: &User{ID: 42, Email: "user@example.test", Username: "user", Status: StatusActive}},
		redeemService: redeemer, configService: configService,
		providerFactory: func(string, string, map[string]string) (payment.Provider, error) { return fakeProvider, nil },
	}
	return service, client, fakeProvider, redeemer
}

func TestPaymentWebhookFulfillsBalanceExactlyOnce(t *testing.T) {
	ctx := context.Background()
	service, client, fakeProvider, redeemer := newPaymentServiceFixture(t)
	created, err := service.CreateOrder(ctx, CreatePaymentOrderRequest{UserID: 42, Amount: 10, ProviderInstanceID: 1, PaymentType: payment.TypeAlipay})
	require.NoError(t, err)
	fakeProvider.notification = &payment.PaymentNotification{
		OrderID: created.OutTradeNo, TradeNo: "paid-trade", Amount: 10,
		Status: payment.ProviderStatusSuccess, Metadata: map[string]string{"pid": "merchant-1"},
	}
	rawBody := "out_trade_no=" + created.OutTradeNo
	require.NoError(t, service.HandlePaymentNotification(ctx, payment.TypeEasyPay, rawBody, nil))
	require.NoError(t, service.HandlePaymentNotification(ctx, payment.TypeEasyPay, rawBody, nil))

	order, err := client.PaymentOrder.Get(ctx, created.OrderID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, order.Status)
	require.Equal(t, 1, redeemer.redeemCalls)
	require.NotNil(t, redeemer.codes[order.RechargeCode].UsedBy)
	require.Equal(t, int64(42), *redeemer.codes[order.RechargeCode].UsedBy)
}

func TestBalanceOrderSeparatesSettlementAmountFromUsageCredit(t *testing.T) {
	ctx := context.Background()
	service, client, fakeProvider, redeemer := newPaymentServiceFixture(t)
	providerRow, err := client.PaymentProviderInstance.Query().Only(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.UpdateOne(providerRow).SetBalanceCreditRate(0.2).Save(ctx)
	require.NoError(t, err)

	created, err := service.CreateOrder(ctx, CreatePaymentOrderRequest{
		UserID: 42, Amount: 10, OrderType: payment.OrderTypeBalance, ProviderInstanceID: 1, PaymentType: payment.TypeAlipay,
	})
	require.NoError(t, err)
	require.Equal(t, 10.0, created.Amount)
	require.Equal(t, 50.0, created.PayAmount)
	require.Equal(t, 0.2, created.BalanceCreditRate)

	fakeProvider.notification = &payment.PaymentNotification{
		OrderID: created.OutTradeNo, TradeNo: "credit-paid", Amount: 50,
		Status: payment.ProviderStatusSuccess, Metadata: map[string]string{"pid": "merchant-1"},
	}
	require.NoError(t, service.HandlePaymentNotification(ctx, payment.TypeEasyPay, "out_trade_no="+created.OutTradeNo, nil))
	order, err := client.PaymentOrder.Get(ctx, created.OrderID)
	require.NoError(t, err)
	require.Equal(t, 10.0, redeemer.codes[order.RechargeCode].Value)
}

func TestSubscriptionOrderUsesPlanSnapshotAndFulfillsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	service, client, fakeProvider, redeemer := newPaymentServiceFixture(t)
	groupEntity, err := client.Group.Create().
		SetName("paid-subscription").
		SetStatus(StatusActive).
		SetSubscriptionType(SubscriptionTypeSubscription).
		Save(ctx)
	require.NoError(t, err)
	plan, err := service.configService.CreatePlan(ctx, CreatePlanRequest{
		GroupID: groupEntity.ID, Name: "Pro 2 months", Price: 29.90, Currency: "CNY",
		ValidityDays: 2, ValidityUnit: "month", ProductName: "SAIAI Pro", ForSale: true,
	})
	require.NoError(t, err)

	created, err := service.CreateOrder(ctx, CreatePaymentOrderRequest{
		UserID: 42, Amount: 0.01, OrderType: payment.OrderTypeSubscription,
		PlanID: plan.ID, ProviderInstanceID: 1, PaymentType: payment.TypeAlipay,
	})
	require.NoError(t, err)
	require.Equal(t, 29.90, created.Amount)
	require.Equal(t, payment.OrderTypeSubscription, created.OrderType)
	require.Equal(t, plan.ID, *created.PlanID)
	order, err := client.PaymentOrder.Get(ctx, created.OrderID)
	require.NoError(t, err)
	require.Equal(t, plan.ID, *order.PlanID)
	require.Equal(t, groupEntity.ID, *order.SubscriptionGroupID)
	require.Equal(t, 60, *order.SubscriptionDays)
	require.Zero(t, order.FeeRate)

	fakeProvider.notification = &payment.PaymentNotification{
		OrderID: created.OutTradeNo, TradeNo: "subscription-paid", Amount: 29.90,
		Status: payment.ProviderStatusSuccess, Metadata: map[string]string{"pid": "merchant-1"},
	}
	rawBody := "out_trade_no=" + created.OutTradeNo
	require.NoError(t, service.HandlePaymentNotification(ctx, payment.TypeEasyPay, rawBody, nil))
	require.NoError(t, service.HandlePaymentNotification(ctx, payment.TypeEasyPay, rawBody, nil))

	code := redeemer.codes[order.RechargeCode]
	require.Equal(t, RedeemTypeSubscription, code.Type)
	require.Equal(t, groupEntity.ID, *code.GroupID)
	require.Equal(t, 60, code.ValidityDays)
	require.Equal(t, 1, redeemer.redeemCalls)
	completed, err := client.PaymentOrder.Get(ctx, created.OrderID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, completed.Status)
}

func TestSubscriptionOrderRejectsUnavailablePlan(t *testing.T) {
	ctx := context.Background()
	service, client, _, _ := newPaymentServiceFixture(t)
	groupEntity, err := client.Group.Create().
		SetName("inactive-subscription").
		SetStatus(StatusActive).
		SetSubscriptionType(SubscriptionTypeSubscription).
		Save(ctx)
	require.NoError(t, err)
	plan, err := service.configService.CreatePlan(ctx, CreatePlanRequest{
		GroupID: groupEntity.ID, Name: "Hidden", Price: 10, Currency: "CNY",
		ValidityDays: 30, ValidityUnit: "day", ForSale: false,
	})
	require.NoError(t, err)

	_, err = service.CreateOrder(ctx, CreatePaymentOrderRequest{
		UserID: 42, OrderType: payment.OrderTypeSubscription, PlanID: plan.ID, ProviderInstanceID: 1, PaymentType: payment.TypeAlipay,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not available")
}

func TestSubscriptionOrderRejectsProviderCurrencyMismatch(t *testing.T) {
	ctx := context.Background()
	service, client, _, _ := newPaymentServiceFixture(t)
	groupEntity, err := client.Group.Create().
		SetName("usd-subscription").
		SetStatus(StatusActive).
		SetSubscriptionType(SubscriptionTypeSubscription).
		Save(ctx)
	require.NoError(t, err)
	plan, err := service.configService.CreatePlan(ctx, CreatePlanRequest{
		GroupID: groupEntity.ID, Name: "USD plan", Price: 10, Currency: "USD",
		ValidityDays: 30, ValidityUnit: "day", ForSale: true,
	})
	require.NoError(t, err)

	_, err = service.CreateOrder(ctx, CreatePaymentOrderRequest{
		UserID: 42, OrderType: payment.OrderTypeSubscription, PlanID: plan.ID, ProviderInstanceID: 1, PaymentType: payment.TypeAlipay,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "currency")
}

func TestExpiredOrderIsDeferredWhenProviderQueryFails(t *testing.T) {
	ctx := context.Background()
	service, client, fakeProvider, _ := newPaymentServiceFixture(t)
	created, err := service.CreateOrder(ctx, CreatePaymentOrderRequest{UserID: 42, Amount: 10, ProviderInstanceID: 1, PaymentType: payment.TypeAlipay})
	require.NoError(t, err)
	_, err = client.PaymentOrder.UpdateOneID(created.OrderID).SetExpiresAt(time.Now().Add(-time.Minute)).Save(ctx)
	require.NoError(t, err)
	fakeProvider.queryErr = errors.New("temporary upstream failure pkey=secret-value")

	expired, err := service.ExpireTimedOutOrders(ctx)
	require.NoError(t, err)
	require.Zero(t, expired)
	order, err := client.PaymentOrder.Get(ctx, created.OrderID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusPending, order.Status)
	require.NotContains(t, safePaymentFailureReason(fakeProvider.queryErr), "secret-value")
}

func TestRecoverIncompleteFulfillmentAfterStaleLease(t *testing.T) {
	ctx := context.Background()
	service, client, _, redeemer := newPaymentServiceFixture(t)
	created, err := service.CreateOrder(ctx, CreatePaymentOrderRequest{UserID: 42, Amount: 10, ProviderInstanceID: 1, PaymentType: payment.TypeAlipay})
	require.NoError(t, err)
	_, err = client.PaymentOrder.UpdateOneID(created.OrderID).
		SetStatus(payment.OrderStatusRecharging).
		SetPaidAt(time.Now().Add(-10 * time.Minute)).
		SetUpdatedAt(time.Now().Add(-paymentFulfillmentLeaseDuration - time.Minute)).
		Save(ctx)
	require.NoError(t, err)

	recovered, err := service.RecoverIncompleteFulfillments(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	order, err := client.PaymentOrder.Get(ctx, created.OrderID)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusCompleted, order.Status)
	require.Equal(t, 1, redeemer.redeemCalls)
}

func createRefundTestUser(t *testing.T, client *dbent.Client, balance float64) *dbent.User {
	t.Helper()
	user, err := client.User.Create().
		SetEmail("refund-user-" + strings.ReplaceAll(t.Name(), "/", "-") + "@example.test").
		SetPasswordHash("test-password-hash").
		SetBalance(balance).
		Save(context.Background())
	require.NoError(t, err)
	return user
}

func createCompletedRefundOrder(t *testing.T, service *PaymentService, client *dbent.Client, user *dbent.User, orderType string, amount, payAmount float64, groupID *int64, days *int) *dbent.PaymentOrder {
	t.Helper()
	ctx := context.Background()
	instance, err := client.PaymentProviderInstance.Query().Only(ctx)
	require.NoError(t, err)
	create := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName("refund-user").
		SetAmount(amount).
		SetPayAmount(payAmount).
		SetCurrency("CNY").
		SetBalanceCreditRate(1).
		SetFeeRate(0).
		SetRechargeCode("PAYREFUND" + fmt.Sprintf("%08d", user.ID)).
		SetOrderType(orderType).
		SetOutTradeNo("SA-REFUND-" + fmt.Sprintf("%08d", user.ID)).
		SetPaymentType(payment.TypeAlipay).
		SetProviderKey(instance.ProviderKey).
		SetProviderInstanceID(instance.ID).
		SetProviderSnapshotEncrypted(instance.ConfigEncrypted).
		SetStatus(payment.OrderStatusCompleted).
		SetPaymentTradeNo("provider-trade-" + fmt.Sprintf("%d", user.ID)).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetCompletedAt(time.Now())
	if groupID != nil {
		create.SetSubscriptionGroupID(*groupID)
	}
	if days != nil {
		create.SetSubscriptionDays(*days)
	}
	order, err := create.Save(ctx)
	require.NoError(t, err)
	service.userRepo = paymentTestUserReader{user: &User{ID: user.ID, Email: user.Email, Username: "refund-user", Status: StatusActive}}
	return order
}

func TestManualBalanceRefundIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	service, client, provider, _ := newPaymentServiceFixture(t)
	user := createRefundTestUser(t, client, 25)
	order := createCompletedRefundOrder(t, service, client, user, payment.OrderTypeBalance, 10, 72, nil, nil)
	request := RequestPaymentRefundInput{
		Mode: payment.RefundModeManual, Reason: "approved support refund",
		ExternalReference: "manual-ticket-1001", Operator: "admin:7",
	}

	view, err := service.RequestRefund(ctx, order.ID, request)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefunded, view.Status)
	require.Equal(t, 72.0, view.RefundAmount)
	require.Equal(t, 15.0, mustPaymentTestUser(t, client, user.ID).Balance)
	require.Zero(t, provider.refundCalls)

	view, err = service.RequestRefund(ctx, order.ID, request)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefunded, view.Status)
	require.Equal(t, 15.0, mustPaymentTestUser(t, client, user.ID).Balance)
	userView, err := service.GetUserOrder(ctx, user.ID, order.ID)
	require.NoError(t, err)
	require.Nil(t, userView.RefundExternalReference)
	require.Empty(t, userView.RefundRequestedBy)
	require.Empty(t, userView.RefundID)
}

func TestManualBalanceRefundRequiresForceAfterCreditWasConsumed(t *testing.T) {
	ctx := context.Background()
	service, client, _, _ := newPaymentServiceFixture(t)
	user := createRefundTestUser(t, client, 3)
	order := createCompletedRefundOrder(t, service, client, user, payment.OrderTypeBalance, 10, 10, nil, nil)
	request := RequestPaymentRefundInput{
		Mode: payment.RefundModeManual, Reason: "manual provider refund", ExternalReference: "manual-1002", Operator: "admin:7",
	}

	_, err := service.RequestRefund(ctx, order.ID, request)
	require.Error(t, err)
	require.Equal(t, payment.OrderStatusCompleted, mustPaymentTestOrder(t, client, order.ID).Status)
	require.Equal(t, 3.0, mustPaymentTestUser(t, client, user.ID).Balance)

	request.Force = true
	view, err := service.RequestRefund(ctx, order.ID, request)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefunded, view.Status)
	require.Equal(t, -7.0, mustPaymentTestUser(t, client, user.ID).Balance)
}

func TestAutomaticRefundSuccessUsesFullSettlementAmount(t *testing.T) {
	ctx := context.Background()
	service, client, provider, _ := newPaymentServiceFixture(t)
	provider.refundResponse = &payment.RefundResponse{RefundID: "provider-refund-1", Status: payment.ProviderStatusSuccess}
	user := createRefundTestUser(t, client, 40)
	order := createCompletedRefundOrder(t, service, client, user, payment.OrderTypeBalance, 10, 72, nil, nil)

	view, err := service.RequestRefund(ctx, order.ID, RequestPaymentRefundInput{
		Mode: payment.RefundModeAutomatic, Reason: "duplicate purchase", Operator: "admin:8",
	})
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefunded, view.Status)
	require.Equal(t, 1, provider.refundCalls)
	require.Equal(t, "72.00", provider.lastRefundRequest.Amount)
	require.Equal(t, 30.0, mustPaymentTestUser(t, client, user.ID).Balance)
}

func TestUncertainAutomaticRefundRequiresManualResolutionAndCompensates(t *testing.T) {
	ctx := context.Background()
	service, client, provider, _ := newPaymentServiceFixture(t)
	provider.refundErr = errors.New("connection reset after request")
	user := createRefundTestUser(t, client, 20)
	order := createCompletedRefundOrder(t, service, client, user, payment.OrderTypeBalance, 10, 10, nil, nil)

	view, err := service.RequestRefund(ctx, order.ID, RequestPaymentRefundInput{
		Mode: payment.RefundModeAutomatic, Reason: "customer request", Operator: "admin:9",
	})
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefundPending, view.Status)
	require.Equal(t, 10.0, mustPaymentTestUser(t, client, user.ID).Balance)

	view, err = service.ResolveRefund(ctx, order.ID, ResolvePaymentRefundInput{
		Outcome: "not_refunded", Reason: "provider console confirms no refund",
		ExternalReference: "support-case-22", Operator: "admin:9",
	})
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefundFailed, view.Status)
	require.False(t, view.RefundEntitlementReversed)
	require.Equal(t, 20.0, mustPaymentTestUser(t, client, user.ID).Balance)

	_, err = service.RequestRefund(ctx, order.ID, RequestPaymentRefundInput{
		Mode: payment.RefundModeAutomatic, Reason: "customer request", Operator: "admin:9",
	})
	require.Error(t, err)
	require.Equal(t, 1, provider.refundCalls)
	require.Equal(t, 20.0, mustPaymentTestUser(t, client, user.ID).Balance)
}

func TestDefinitiveAutomaticRefundRejectionCompensatesImmediately(t *testing.T) {
	ctx := context.Background()
	service, client, provider, _ := newPaymentServiceFixture(t)
	provider.refundErr = payment.NewRefundRejectedError(errors.New("refund rejected by provider"))
	user := createRefundTestUser(t, client, 20)
	order := createCompletedRefundOrder(t, service, client, user, payment.OrderTypeBalance, 10, 10, nil, nil)

	view, err := service.RequestRefund(ctx, order.ID, RequestPaymentRefundInput{
		Mode: payment.RefundModeAutomatic, Reason: "customer request", Operator: "admin:9",
	})
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefundFailed, view.Status)
	require.False(t, view.RefundEntitlementReversed)
	require.Equal(t, 20.0, mustPaymentTestUser(t, client, user.ID).Balance)
}

func TestManualConfirmationOfUncertainRefundKeepsReversedEntitlement(t *testing.T) {
	ctx := context.Background()
	service, client, provider, _ := newPaymentServiceFixture(t)
	provider.refundErr = errors.New("response lost")
	user := createRefundTestUser(t, client, 20)
	order := createCompletedRefundOrder(t, service, client, user, payment.OrderTypeBalance, 10, 10, nil, nil)

	_, err := service.RequestRefund(ctx, order.ID, RequestPaymentRefundInput{
		Mode: payment.RefundModeAutomatic, Reason: "customer request", Operator: "admin:9",
	})
	require.NoError(t, err)
	resolution := ResolvePaymentRefundInput{
		Outcome: "refunded", Reason: "provider console confirms success",
		ExternalReference: "provider-refund-proof-9", Operator: "admin:9",
	}
	view, err := service.ResolveRefund(ctx, order.ID, resolution)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefunded, view.Status)
	require.Equal(t, 10.0, mustPaymentTestUser(t, client, user.ID).Balance)

	view, err = service.ResolveRefund(ctx, order.ID, resolution)
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefunded, view.Status)
	require.Equal(t, 10.0, mustPaymentTestUser(t, client, user.ID).Balance)
}

func TestSubscriptionRefundCompensationRestoresDeductedEntitlement(t *testing.T) {
	ctx := context.Background()
	service, client, provider, _ := newPaymentServiceFixture(t)
	provider.refundErr = errors.New("timeout after refund request")
	user := createRefundTestUser(t, client, 0)
	group, err := client.Group.Create().SetName("refund-subscription").SetStatus(StatusActive).SetSubscriptionType(SubscriptionTypeSubscription).Save(ctx)
	require.NoError(t, err)
	originalExpiry := time.Now().Add(60 * 24 * time.Hour).Round(time.Microsecond)
	sub, err := client.UserSubscription.Create().
		SetUserID(user.ID).SetGroupID(group.ID).SetStartsAt(time.Now()).SetExpiresAt(originalExpiry).
		SetStatus(SubscriptionStatusActive).Save(ctx)
	require.NoError(t, err)
	days := 30
	order := createCompletedRefundOrder(t, service, client, user, payment.OrderTypeSubscription, 29.9, 29.9, &group.ID, &days)

	view, err := service.RequestRefund(ctx, order.ID, RequestPaymentRefundInput{
		Mode: payment.RefundModeAutomatic, Reason: "subscription refund", Operator: "admin:10",
	})
	require.NoError(t, err)
	require.Equal(t, payment.OrderStatusRefundPending, view.Status)
	afterReversal, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.WithinDuration(t, originalExpiry.AddDate(0, 0, -days), afterReversal.ExpiresAt, 10*time.Millisecond)

	_, err = service.ResolveRefund(ctx, order.ID, ResolvePaymentRefundInput{
		Outcome: "not_refunded", Reason: "provider confirmed failure",
		ExternalReference: "provider-check-44", Operator: "admin:10",
	})
	require.NoError(t, err)
	afterCompensation, err := client.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.WithinDuration(t, originalExpiry, afterCompensation.ExpiresAt, 20*time.Millisecond)
	require.Equal(t, SubscriptionStatusActive, afterCompensation.Status)
}

func TestRefundRecoveryExecutesPreparedRequestOnlyOnce(t *testing.T) {
	ctx := context.Background()
	service, client, provider, _ := newPaymentServiceFixture(t)
	provider.refundResponse = &payment.RefundResponse{RefundID: "recovered-refund", Status: payment.ProviderStatusSuccess}
	user := createRefundTestUser(t, client, 20)
	order := createCompletedRefundOrder(t, service, client, user, payment.OrderTypeBalance, 10, 10, nil, nil)
	input := RequestPaymentRefundInput{Mode: payment.RefundModeAutomatic, Reason: "recover after crash", Operator: "admin:11"}

	prepared, created, err := service.prepareRefund(ctx, order.ID, input)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, payment.OrderStatusRefundRequested, prepared.Status)
	require.Zero(t, provider.refundCalls)

	recovered, err := service.RecoverIncompleteRefunds(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.Equal(t, 1, provider.refundCalls)
	require.Equal(t, payment.OrderStatusRefunded, mustPaymentTestOrder(t, client, order.ID).Status)

	recovered, err = service.RecoverIncompleteRefunds(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered)
	require.Equal(t, 1, provider.refundCalls)
}

func TestRefundRecoveryNeverRepeatsPossiblyStartedProviderCall(t *testing.T) {
	ctx := context.Background()
	service, client, provider, _ := newPaymentServiceFixture(t)
	provider.refundQueryResponse = &payment.RefundResponse{RefundID: "queried-refund", Status: payment.ProviderStatusRefunded}
	user := createRefundTestUser(t, client, 20)
	order := createCompletedRefundOrder(t, service, client, user, payment.OrderTypeBalance, 10, 10, nil, nil)
	input := RequestPaymentRefundInput{Mode: payment.RefundModeAutomatic, Reason: "ambiguous crash", Operator: "admin:12"}

	_, _, err := service.prepareRefund(ctx, order.ID, input)
	require.NoError(t, err)
	stale := time.Now().Add(-refundRecoveryStaleAfter - time.Minute)
	_, err = client.PaymentOrder.UpdateOneID(order.ID).
		SetStatus(payment.OrderStatusRefunding).
		SetRefundProviderCallStartedAt(stale).
		SetUpdatedAt(stale).
		Save(ctx)
	require.NoError(t, err)

	recovered, err := service.RecoverIncompleteRefunds(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.Zero(t, provider.refundCalls)
	require.Equal(t, payment.OrderStatusRefunded, mustPaymentTestOrder(t, client, order.ID).Status)
}

func mustPaymentTestUser(t *testing.T, client *dbent.Client, id int64) *dbent.User {
	t.Helper()
	user, err := client.User.Get(context.Background(), id)
	require.NoError(t, err)
	return user
}

func mustPaymentTestOrder(t *testing.T, client *dbent.Client, id int64) *dbent.PaymentOrder {
	t.Helper()
	order, err := client.PaymentOrder.Get(context.Background(), id)
	require.NoError(t, err)
	return order
}
