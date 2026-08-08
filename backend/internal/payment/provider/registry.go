package provider

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

type ConfigField struct {
	Key         string            `json:"key"`
	Label       string            `json:"label"`
	Kind        string            `json:"kind"`
	Required    bool              `json:"required"`
	Secret      bool              `json:"secret"`
	Placeholder string            `json:"placeholder,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
}

type Definition struct {
	Key                 string                                                                      `json:"key"`
	Name                string                                                                      `json:"name"`
	PaymentTypes        []string                                                                    `json:"payment_types"`
	ConfigFields        []ConfigField                                                               `json:"config_fields"`
	SupportsRefund      bool                                                                        `json:"supports_refund"`
	Constructor         func(instanceID string, config map[string]string) (payment.Provider, error) `json:"-"`
	NotificationOrderID func(rawBody string, headers map[string]string) (string, error)             `json:"-"`
	WebhookSuccessBody  string                                                                      `json:"webhook_success_body"`
	WebhookFailureBody  string                                                                      `json:"webhook_failure_body"`
}

var registry = struct {
	sync.RWMutex
	definitions map[string]Definition
}{definitions: make(map[string]Definition)}

// Register makes a provider adapter available to configuration, order
// selection, callbacks, and recovery. New providers register one definition;
// payment-domain services do not require provider-specific branches.
func Register(def Definition) error {
	def.Key = strings.ToLower(strings.TrimSpace(def.Key))
	if def.Key == "" || len(def.Key) > 30 || def.Name == "" || def.Constructor == nil || def.NotificationOrderID == nil || len(def.PaymentTypes) == 0 {
		return fmt.Errorf("invalid payment provider definition")
	}
	seenFields := make(map[string]struct{}, len(def.ConfigFields))
	for _, field := range def.ConfigFields {
		field.Key = strings.TrimSpace(field.Key)
		if field.Key == "" {
			return fmt.Errorf("payment provider %s has an empty config field", def.Key)
		}
		if _, exists := seenFields[field.Key]; exists {
			return fmt.Errorf("payment provider %s has duplicate config field %s", def.Key, field.Key)
		}
		seenFields[field.Key] = struct{}{}
	}
	registry.Lock()
	defer registry.Unlock()
	if _, exists := registry.definitions[def.Key]; exists {
		return fmt.Errorf("payment provider already registered: %s", def.Key)
	}
	registry.definitions[def.Key] = cloneDefinition(def)
	return nil
}

func mustRegister(def Definition) {
	if err := Register(def); err != nil {
		panic(err)
	}
}

func CreateProvider(providerKey, instanceID string, config map[string]string) (payment.Provider, error) {
	def, ok := GetDefinition(providerKey)
	if !ok {
		return nil, fmt.Errorf("unsupported payment provider: %s", providerKey)
	}
	adapter, err := def.Constructor(instanceID, config)
	if err != nil {
		return nil, err
	}
	if adapter == nil || adapter.ProviderKey() != def.Key {
		return nil, fmt.Errorf("payment provider adapter identity mismatch: %s", def.Key)
	}
	if _, err := payment.NormalizePaymentCurrency(adapter.Currency()); err != nil {
		return nil, fmt.Errorf("payment provider %s returned an invalid currency: %w", def.Key, err)
	}
	if payment.CurrencyMaxFractionDigits(adapter.Currency()) > 2 {
		return nil, fmt.Errorf("payment provider %s currency precision exceeds the order ledger precision", def.Key)
	}
	supported := make(map[string]struct{})
	for _, method := range adapter.SupportedTypes() {
		supported[strings.TrimSpace(method)] = struct{}{}
	}
	for _, method := range def.PaymentTypes {
		if _, exists := supported[method]; !exists {
			return nil, fmt.Errorf("payment provider %s does not implement advertised method %s", def.Key, method)
		}
	}
	if def.SupportsRefund {
		if _, ok := adapter.(payment.RefundProvider); !ok {
			return nil, fmt.Errorf("payment provider %s advertises refund support without implementing it", def.Key)
		}
	}
	return adapter, nil
}

func GetDefinition(providerKey string) (Definition, bool) {
	key := strings.ToLower(strings.TrimSpace(providerKey))
	registry.RLock()
	defer registry.RUnlock()
	def, ok := registry.definitions[key]
	return cloneDefinition(def), ok
}

func cloneDefinition(def Definition) Definition {
	def.PaymentTypes = append([]string(nil), def.PaymentTypes...)
	def.ConfigFields = append([]ConfigField(nil), def.ConfigFields...)
	for i := range def.ConfigFields {
		if def.ConfigFields[i].Options == nil {
			continue
		}
		options := make(map[string]string, len(def.ConfigFields[i].Options))
		for key, value := range def.ConfigFields[i].Options {
			options[key] = value
		}
		def.ConfigFields[i].Options = options
	}
	return def
}

func ListDefinitions() []Definition {
	registry.RLock()
	result := make([]Definition, 0, len(registry.definitions))
	for _, def := range registry.definitions {
		def.Constructor = nil
		def.NotificationOrderID = nil
		result = append(result, def)
	}
	registry.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

func ExtractNotificationOrderID(providerKey, rawBody string, headers map[string]string) (string, error) {
	def, ok := GetDefinition(providerKey)
	if !ok || def.NotificationOrderID == nil {
		return "", fmt.Errorf("payment provider does not support notifications: %s", providerKey)
	}
	return def.NotificationOrderID(rawBody, headers)
}

func IsSecretConfigKey(providerKey, configKey string) bool {
	def, ok := GetDefinition(providerKey)
	if !ok {
		return false
	}
	for _, field := range def.ConfigFields {
		if field.Secret && strings.EqualFold(field.Key, configKey) {
			return true
		}
	}
	return false
}

func ValidateConfigShape(providerKey string, config map[string]string) error {
	def, ok := GetDefinition(providerKey)
	if !ok {
		return fmt.Errorf("unsupported payment provider: %s", providerKey)
	}
	fields := make(map[string]ConfigField, len(def.ConfigFields))
	for _, field := range def.ConfigFields {
		fields[field.Key] = field
		if field.Required && strings.TrimSpace(config[field.Key]) == "" {
			return fmt.Errorf("payment provider %s is missing required config field %s", def.Key, field.Key)
		}
	}
	for key := range config {
		if _, declared := fields[key]; !declared {
			return fmt.Errorf("payment provider %s has undeclared config field %s", def.Key, key)
		}
	}
	return nil
}

func SecretConfigKeys() []string {
	registry.RLock()
	seen := map[string]struct{}{"token": {}, "secret": {}, "api_key": {}, "password": {}}
	for _, def := range registry.definitions {
		for _, field := range def.ConfigFields {
			if field.Secret {
				seen[field.Key] = struct{}{}
			}
		}
	}
	registry.RUnlock()
	result := make([]string, 0, len(seen))
	for key := range seen {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func init() {
	mustRegister(Definition{
		Key: payment.TypeEasyPay, Name: "EasyPay",
		PaymentTypes:       []string{payment.TypeAlipay, payment.TypeWxpay},
		SupportsRefund:     true,
		WebhookSuccessBody: "success", WebhookFailureBody: "fail",
		ConfigFields: []ConfigField{
			{Key: "pid", Label: "Merchant PID", Kind: "text", Required: true},
			{Key: "pkey", Label: "Merchant secret", Kind: "password", Required: true, Secret: true},
			{Key: "apiBase", Label: "API base URL", Kind: "url", Required: true, Placeholder: "https://pay.example.com"},
			{Key: "notifyUrl", Label: "Webhook URL", Kind: "url", Required: true},
			{Key: "returnUrl", Label: "Return URL", Kind: "url", Required: true},
			{Key: "paymentMode", Label: "Payment display", Kind: "select", Required: true, Options: map[string]string{"popup": "Hosted page", "qrcode": "QR code"}},
			{Key: "currency", Label: "Settlement currency", Kind: "text", Placeholder: payment.DefaultPaymentCurrency},
			{Key: "cid", Label: "Default channel ID", Kind: "text"},
			{Key: "cidAlipay", Label: "Alipay channel ID", Kind: "text"},
			{Key: "cidWxpay", Label: "WeChat channel ID", Kind: "text"},
			{Key: "customMethods", Label: "Custom methods JSON", Kind: "text"},
		},
		Constructor: func(instanceID string, config map[string]string) (payment.Provider, error) {
			return NewEasyPay(instanceID, config)
		},
		NotificationOrderID: func(rawBody string, headers map[string]string) (string, error) {
			return (&EasyPay{}).NotificationOrderID(rawBody, headers)
		},
	})
}
