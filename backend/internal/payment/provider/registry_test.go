package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

type registryTestProvider struct{}

func (registryTestProvider) Name() string                          { return "Registry test" }
func (registryTestProvider) ProviderKey() string                   { return "registry_test" }
func (registryTestProvider) SupportedTypes() []payment.PaymentType { return []string{"test_wallet"} }
func (registryTestProvider) Currency() string                      { return "USD" }
func (registryTestProvider) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	return nil, errors.New("unused")
}
func (registryTestProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return nil, errors.New("unused")
}
func (registryTestProvider) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	return nil, errors.New("unused")
}
func (registryTestProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, errors.New("unused")
}

func TestProviderRegistryExtensionContract(t *testing.T) {
	definition := Definition{
		Key: "registry_test", Name: "Registry test", PaymentTypes: []string{"test_wallet"},
		ConfigFields:        []ConfigField{{Key: "token", Label: "Token", Kind: "password", Secret: true}},
		Constructor:         func(string, map[string]string) (payment.Provider, error) { return registryTestProvider{}, nil },
		NotificationOrderID: func(raw string, _ map[string]string) (string, error) { return raw, nil },
	}
	require.NoError(t, Register(definition))
	require.Error(t, Register(definition))

	adapter, err := CreateProvider("registry_test", "instance", map[string]string{"token": "secret"})
	require.NoError(t, err)
	require.Equal(t, "USD", adapter.Currency())
	require.Equal(t, "order-1", mustNotificationOrderID(t, "registry_test", "order-1"))
	require.True(t, IsSecretConfigKey("registry_test", "TOKEN"))

	loaded, ok := GetDefinition("registry_test")
	require.True(t, ok)
	loaded.PaymentTypes[0] = "mutated"
	reloaded, ok := GetDefinition("registry_test")
	require.True(t, ok)
	require.Equal(t, "test_wallet", reloaded.PaymentTypes[0])
}

func mustNotificationOrderID(t *testing.T, providerKey, raw string) string {
	t.Helper()
	orderID, err := ExtractNotificationOrderID(providerKey, raw, nil)
	require.NoError(t, err)
	return orderID
}
