package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	SettingPaymentEnabled      = "payment_enabled"
	SettingPaymentMinAmount    = "payment_min_amount"
	SettingPaymentMaxAmount    = "payment_max_amount"
	SettingPaymentTimeoutMin   = "payment_order_timeout_minutes"
	SettingPaymentMaxPending   = "payment_max_pending_orders"
	SettingPaymentRechargeFee  = "payment_recharge_fee_rate"
	defaultPaymentMinAmount    = 1.0
	defaultPaymentMaxAmount    = 1000.0
	defaultPaymentTimeoutMin   = 5
	defaultPaymentMaxPending   = 3
	maxPaymentProviderNameSize = 100
)

var paymentConfigKeys = []string{
	SettingPaymentEnabled,
	SettingPaymentMinAmount,
	SettingPaymentMaxAmount,
	SettingPaymentTimeoutMin,
	SettingPaymentMaxPending,
	SettingPaymentRechargeFee,
}

// PaymentConfig is the public, non-secret configuration for native payments.
type PaymentConfig struct {
	Enabled             bool    `json:"enabled"`
	MinAmount           float64 `json:"min_amount"`
	MaxAmount           float64 `json:"max_amount"`
	OrderTimeoutMinutes int     `json:"order_timeout_minutes"`
	MaxPendingOrders    int     `json:"max_pending_orders"`
	RechargeFeeRate     float64 `json:"recharge_fee_rate"`
}

type UpdatePaymentConfigRequest struct {
	Enabled             *bool    `json:"enabled"`
	MinAmount           *float64 `json:"min_amount"`
	MaxAmount           *float64 `json:"max_amount"`
	OrderTimeoutMinutes *int     `json:"order_timeout_minutes"`
	MaxPendingOrders    *int     `json:"max_pending_orders"`
	RechargeFeeRate     *float64 `json:"recharge_fee_rate"`
}

type CreatePaymentProviderRequest struct {
	ProviderKey       string            `json:"provider_key"`
	Name              string            `json:"name"`
	Config            map[string]string `json:"config"`
	SupportedTypes    []string          `json:"supported_types"`
	BalanceCreditRate float64           `json:"balance_credit_rate"`
	Enabled           bool              `json:"enabled"`
	SortOrder         int               `json:"sort_order"`
	Limits            string            `json:"limits"`
}

type UpdatePaymentProviderRequest struct {
	Name              *string           `json:"name"`
	Config            map[string]string `json:"config"`
	SupportedTypes    *[]string         `json:"supported_types"`
	BalanceCreditRate *float64          `json:"balance_credit_rate"`
	Enabled           *bool             `json:"enabled"`
	SortOrder         *int              `json:"sort_order"`
	Limits            *string           `json:"limits"`
}

// PaymentProviderResponse never contains provider credentials.
type PaymentProviderResponse struct {
	ID                int64             `json:"id"`
	ProviderKey       string            `json:"provider_key"`
	Name              string            `json:"name"`
	Config            map[string]string `json:"config"`
	ConfiguredSecrets []string          `json:"configured_secrets"`
	SupportedTypes    []string          `json:"supported_types"`
	BalanceCreditRate float64           `json:"balance_credit_rate"`
	Enabled           bool              `json:"enabled"`
	SortOrder         int               `json:"sort_order"`
	Limits            string            `json:"limits"`
}

type PaymentMethodResponse struct {
	ID                 string  `json:"id"`
	ProviderInstanceID int64   `json:"provider_instance_id"`
	Type               string  `json:"type"`
	ProviderKey        string  `json:"provider_key"`
	Currency           string  `json:"currency"`
	BalanceCreditRate  float64 `json:"balance_credit_rate"`
}

type PaymentConfigService struct {
	entClient   *dbent.Client
	settingRepo SettingRepository
	encryptor   SecretEncryptor
}

func NewPaymentConfigService(entClient *dbent.Client, settingRepo SettingRepository, encryptor SecretEncryptor) *PaymentConfigService {
	return &PaymentConfigService{entClient: entClient, settingRepo: settingRepo, encryptor: encryptor}
}

func (s *PaymentConfigService) GetPaymentConfig(ctx context.Context) (*PaymentConfig, error) {
	values, err := s.settingRepo.GetMultiple(ctx, paymentConfigKeys)
	if err != nil {
		return nil, fmt.Errorf("get payment config: %w", err)
	}
	cfg := &PaymentConfig{
		Enabled:             values[SettingPaymentEnabled] == "true",
		MinAmount:           parsePositiveFloat(values[SettingPaymentMinAmount], defaultPaymentMinAmount),
		MaxAmount:           parsePositiveFloat(values[SettingPaymentMaxAmount], defaultPaymentMaxAmount),
		OrderTimeoutMinutes: parsePositiveInt(values[SettingPaymentTimeoutMin], defaultPaymentTimeoutMin),
		MaxPendingOrders:    parsePositiveInt(values[SettingPaymentMaxPending], defaultPaymentMaxPending),
		RechargeFeeRate:     parseNonNegativeFloat(values[SettingPaymentRechargeFee], 0),
	}
	if cfg.MaxAmount < cfg.MinAmount {
		return nil, infraerrors.BadRequest("PAYMENT_CONFIG_INVALID", "payment maximum amount is less than minimum amount")
	}
	return cfg, nil
}

func (s *PaymentConfigService) IsPaymentEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingPaymentEnabled)
	return err == nil && value == "true"
}

func (s *PaymentConfigService) UpdatePaymentConfig(ctx context.Context, req UpdatePaymentConfigRequest) error {
	current, err := s.GetPaymentConfig(ctx)
	if err != nil {
		return err
	}
	next := *current
	updates := make(map[string]string)
	if req.Enabled != nil {
		next.Enabled = *req.Enabled
		updates[SettingPaymentEnabled] = strconv.FormatBool(*req.Enabled)
	}
	if req.MinAmount != nil {
		next.MinAmount = *req.MinAmount
		updates[SettingPaymentMinAmount] = strconv.FormatFloat(*req.MinAmount, 'f', -1, 64)
	}
	if req.MaxAmount != nil {
		next.MaxAmount = *req.MaxAmount
		updates[SettingPaymentMaxAmount] = strconv.FormatFloat(*req.MaxAmount, 'f', -1, 64)
	}
	if req.OrderTimeoutMinutes != nil {
		next.OrderTimeoutMinutes = *req.OrderTimeoutMinutes
		updates[SettingPaymentTimeoutMin] = strconv.Itoa(*req.OrderTimeoutMinutes)
	}
	if req.MaxPendingOrders != nil {
		next.MaxPendingOrders = *req.MaxPendingOrders
		updates[SettingPaymentMaxPending] = strconv.Itoa(*req.MaxPendingOrders)
	}
	if req.RechargeFeeRate != nil {
		next.RechargeFeeRate = *req.RechargeFeeRate
		updates[SettingPaymentRechargeFee] = strconv.FormatFloat(*req.RechargeFeeRate, 'f', -1, 64)
	}
	if next.MinAmount <= 0 || next.MaxAmount <= 0 || next.MaxAmount < next.MinAmount || next.OrderTimeoutMinutes <= 0 || next.MaxPendingOrders <= 0 || next.RechargeFeeRate < 0 || next.RechargeFeeRate > 100 {
		return infraerrors.BadRequest("PAYMENT_CONFIG_INVALID", "invalid payment configuration")
	}
	if next.Enabled {
		count, countErr := s.entClient.PaymentProviderInstance.Query().Where(paymentproviderinstance.EnabledEQ(true)).Count(ctx)
		if countErr != nil {
			return fmt.Errorf("check enabled payment providers: %w", countErr)
		}
		if count == 0 {
			return infraerrors.BadRequest("PAYMENT_PROVIDER_REQUIRED", "enable at least one payment provider first")
		}
	}
	if len(updates) == 0 {
		return nil
	}
	return s.settingRepo.SetMultiple(ctx, updates)
}

func (s *PaymentConfigService) CreateProviderInstance(ctx context.Context, req CreatePaymentProviderRequest) (*PaymentProviderResponse, error) {
	req.ProviderKey = strings.ToLower(strings.TrimSpace(req.ProviderKey))
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > maxPaymentProviderNameSize {
		return nil, infraerrors.BadRequest("PAYMENT_PROVIDER_NAME_INVALID", "invalid payment provider name")
	}
	config := clonePaymentConfig(req.Config)
	if err := provider.ValidateConfigShape(req.ProviderKey, config); err != nil {
		return nil, infraerrors.BadRequest("PAYMENT_PROVIDER_CONFIG_INVALID", safePaymentFailureReason(err))
	}
	if req.BalanceCreditRate == 0 {
		req.BalanceCreditRate = 1
	}
	if !validPaymentAmount(req.BalanceCreditRate) {
		return nil, infraerrors.BadRequest("PAYMENT_CREDIT_RATE_INVALID", "balance credit rate must be positive and finite")
	}
	adapter, err := provider.CreateProvider(req.ProviderKey, "validate", config)
	if err != nil {
		return nil, infraerrors.BadRequest("PAYMENT_PROVIDER_CONFIG_INVALID", safePaymentFailureReason(err))
	}
	supportedTypes, err := normalizeProviderPaymentTypes(req.SupportedTypes, adapter.SupportedTypes())
	if err != nil {
		return nil, err
	}
	encrypted, err := s.encryptProviderConfig(config)
	if err != nil {
		return nil, err
	}
	created, err := s.entClient.PaymentProviderInstance.Create().
		SetProviderKey(req.ProviderKey).
		SetName(req.Name).
		SetConfigEncrypted(encrypted).
		SetSupportedTypes(supportedTypes).
		SetBalanceCreditRate(req.BalanceCreditRate).
		SetEnabled(req.Enabled).
		SetSortOrder(req.SortOrder).
		SetLimits(req.Limits).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create payment provider: %w", err)
	}
	return s.providerResponse(created, config), nil
}

func (s *PaymentConfigService) ListProviderInstances(ctx context.Context) ([]PaymentProviderResponse, error) {
	instances, err := s.entClient.PaymentProviderInstance.Query().Order(dbent.Asc(paymentproviderinstance.FieldSortOrder), dbent.Asc(paymentproviderinstance.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list payment providers: %w", err)
	}
	result := make([]PaymentProviderResponse, 0, len(instances))
	for _, instance := range instances {
		config, decErr := s.decryptProviderConfig(instance.ConfigEncrypted)
		if decErr != nil {
			return nil, fmt.Errorf("decrypt payment provider %d: %w", instance.ID, decErr)
		}
		result = append(result, *s.providerResponse(instance, config))
	}
	return result, nil
}

func (s *PaymentConfigService) ListProviderDefinitions() []provider.Definition {
	return provider.ListDefinitions()
}

func (s *PaymentConfigService) GetAvailablePaymentMethods(ctx context.Context) ([]PaymentMethodResponse, error) {
	instances, err := s.entClient.PaymentProviderInstance.Query().Where(paymentproviderinstance.EnabledEQ(true)).
		Order(dbent.Asc(paymentproviderinstance.FieldSortOrder), dbent.Asc(paymentproviderinstance.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]PaymentMethodResponse, 0)
	for _, instance := range instances {
		config, decryptErr := s.decryptProviderConfig(instance.ConfigEncrypted)
		if decryptErr != nil {
			return nil, decryptErr
		}
		adapter, createErr := provider.CreateProvider(instance.ProviderKey, strconv.FormatInt(instance.ID, 10), config)
		if createErr != nil {
			return nil, fmt.Errorf("create payment provider %d: %w", instance.ID, createErr)
		}
		for _, paymentType := range splitPaymentTypes(instance.SupportedTypes) {
			result = append(result, PaymentMethodResponse{
				ID:                 strconv.FormatInt(instance.ID, 10) + ":" + paymentType,
				ProviderInstanceID: instance.ID,
				Type:               paymentType, ProviderKey: instance.ProviderKey, Currency: adapter.Currency(),
				BalanceCreditRate: instance.BalanceCreditRate,
			})
		}
	}
	return result, nil
}

func (s *PaymentConfigService) UpdateProviderInstance(ctx context.Context, id int64, req UpdatePaymentProviderRequest) (*PaymentProviderResponse, error) {
	current, err := s.entClient.PaymentProviderInstance.Get(ctx, id)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("PAYMENT_PROVIDER_NOT_FOUND", "payment provider not found")
		}
		return nil, err
	}
	config, err := s.decryptProviderConfig(current.ConfigEncrypted)
	if err != nil {
		return nil, err
	}
	if req.Config != nil {
		for key, value := range req.Config {
			if strings.TrimSpace(value) != "" {
				config[key] = value
			}
		}
	}
	if err := provider.ValidateConfigShape(current.ProviderKey, config); err != nil {
		return nil, infraerrors.BadRequest("PAYMENT_PROVIDER_CONFIG_INVALID", safePaymentFailureReason(err))
	}
	adapter, err := provider.CreateProvider(current.ProviderKey, strconv.FormatInt(id, 10), config)
	if err != nil {
		return nil, infraerrors.BadRequest("PAYMENT_PROVIDER_CONFIG_INVALID", safePaymentFailureReason(err))
	}
	nextEnabled := current.Enabled
	if req.Enabled != nil {
		nextEnabled = *req.Enabled
	}
	if current.Enabled && !nextEnabled {
		if err := s.ensureAnotherEnabledProviderWhenPaymentEnabled(ctx, id); err != nil {
			return nil, err
		}
	}
	update := s.entClient.PaymentProviderInstance.UpdateOne(current)
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > maxPaymentProviderNameSize {
			return nil, infraerrors.BadRequest("PAYMENT_PROVIDER_NAME_INVALID", "invalid payment provider name")
		}
		update.SetName(name)
	}
	if req.Config != nil {
		encrypted, encErr := s.encryptProviderConfig(config)
		if encErr != nil {
			return nil, encErr
		}
		update.SetConfigEncrypted(encrypted)
	}
	if req.SupportedTypes != nil {
		supportedTypes, normalizeErr := normalizeProviderPaymentTypes(*req.SupportedTypes, adapter.SupportedTypes())
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		update.SetSupportedTypes(supportedTypes)
	}
	if req.BalanceCreditRate != nil {
		if !validPaymentAmount(*req.BalanceCreditRate) {
			return nil, infraerrors.BadRequest("PAYMENT_CREDIT_RATE_INVALID", "balance credit rate must be positive and finite")
		}
		update.SetBalanceCreditRate(*req.BalanceCreditRate)
	}
	if req.Enabled != nil {
		update.SetEnabled(*req.Enabled)
	}
	if req.SortOrder != nil {
		update.SetSortOrder(*req.SortOrder)
	}
	if req.Limits != nil {
		update.SetLimits(*req.Limits)
	}
	saved, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update payment provider: %w", err)
	}
	return s.providerResponse(saved, config), nil
}

func (s *PaymentConfigService) DeleteProviderInstance(ctx context.Context, id int64) error {
	instance, err := s.entClient.PaymentProviderInstance.Get(ctx, id)
	if err != nil {
		if dbent.IsNotFound(err) {
			return infraerrors.NotFound("PAYMENT_PROVIDER_NOT_FOUND", "payment provider not found")
		}
		return err
	}
	if instance.Enabled {
		if err := s.ensureAnotherEnabledProviderWhenPaymentEnabled(ctx, id); err != nil {
			return err
		}
	}
	count, err := s.entClient.PaymentOrder.Query().Where(paymentorder.ProviderInstanceIDEQ(id)).Count(ctx)
	if err != nil {
		return fmt.Errorf("check payment provider order history: %w", err)
	}
	if count > 0 {
		return infraerrors.Conflict("PAYMENT_PROVIDER_HAS_ORDER_HISTORY", "payment provider with order history cannot be deleted; disable it instead").WithMetadata(map[string]string{"count": strconv.Itoa(count)})
	}
	err = s.entClient.PaymentProviderInstance.DeleteOneID(id).Exec(ctx)
	if dbent.IsNotFound(err) {
		return infraerrors.NotFound("PAYMENT_PROVIDER_NOT_FOUND", "payment provider not found")
	}
	return err
}

func (s *PaymentConfigService) ensureAnotherEnabledProviderWhenPaymentEnabled(ctx context.Context, excludedID int64) error {
	if !s.IsPaymentEnabled(ctx) {
		return nil
	}
	count, err := s.entClient.PaymentProviderInstance.Query().Where(
		paymentproviderinstance.EnabledEQ(true),
		paymentproviderinstance.IDNEQ(excludedID),
	).Count(ctx)
	if err != nil {
		return fmt.Errorf("check remaining payment providers: %w", err)
	}
	if count == 0 {
		return infraerrors.Conflict("PAYMENT_PROVIDER_LAST_ENABLED", "disable native payment before disabling the last enabled provider")
	}
	return nil
}

func (s *PaymentConfigService) decryptProviderConfig(ciphertext string) (map[string]string, error) {
	if s.encryptor == nil {
		return nil, errors.New("payment secret encryptor is unavailable")
	}
	plaintext, err := s.encryptor.Decrypt(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt payment provider config: %w", err)
	}
	var config map[string]string
	if err := json.Unmarshal([]byte(plaintext), &config); err != nil {
		return nil, fmt.Errorf("decode payment provider config: %w", err)
	}
	return config, nil
}

func (s *PaymentConfigService) encryptProviderConfig(config map[string]string) (string, error) {
	if s.encryptor == nil {
		return "", errors.New("payment secret encryptor is unavailable")
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode payment provider config: %w", err)
	}
	ciphertext, err := s.encryptor.Encrypt(string(encoded))
	if err != nil {
		return "", fmt.Errorf("encrypt payment provider config: %w", err)
	}
	return ciphertext, nil
}

func (s *PaymentConfigService) providerResponse(instance *dbent.PaymentProviderInstance, config map[string]string) *PaymentProviderResponse {
	publicConfig := make(map[string]string)
	configuredSecrets := make([]string, 0, 1)
	for key, value := range config {
		if provider.IsSecretConfigKey(instance.ProviderKey, key) {
			if value != "" {
				configuredSecrets = append(configuredSecrets, key)
			}
			continue
		}
		publicConfig[key] = value
	}
	return &PaymentProviderResponse{
		ID: instance.ID, ProviderKey: instance.ProviderKey, Name: instance.Name,
		Config: publicConfig, ConfiguredSecrets: configuredSecrets,
		SupportedTypes: splitPaymentTypes(instance.SupportedTypes), Enabled: instance.Enabled,
		BalanceCreditRate: instance.BalanceCreditRate,
		SortOrder:         instance.SortOrder, Limits: instance.Limits,
	}
}

func clonePaymentConfig(config map[string]string) map[string]string {
	result := make(map[string]string, len(config))
	for key, value := range config {
		result[key] = strings.TrimSpace(value)
	}
	return result
}

func normalizeProviderPaymentTypes(requested []string, supported []payment.PaymentType) (string, error) {
	allowed := make(map[string]struct{}, len(supported))
	for _, paymentType := range supported {
		if value := strings.TrimSpace(paymentType); value != "" {
			allowed[value] = struct{}{}
		}
	}
	if len(requested) == 0 {
		requested = append(requested, supported...)
	}
	seen := make(map[string]struct{}, len(requested))
	result := make([]string, 0, len(requested))
	for _, paymentType := range requested {
		paymentType = strings.TrimSpace(paymentType)
		if paymentType == "" || len(paymentType) > 50 {
			return "", infraerrors.BadRequest("PAYMENT_TYPE_UNSUPPORTED", "invalid payment type")
		}
		if _, ok := allowed[paymentType]; !ok {
			return "", infraerrors.BadRequest("PAYMENT_TYPE_UNSUPPORTED", "payment type is not supported by the selected provider")
		}
		if _, ok := seen[paymentType]; ok {
			continue
		}
		seen[paymentType] = struct{}{}
		result = append(result, paymentType)
	}
	if len(result) == 0 {
		return "", infraerrors.BadRequest("PAYMENT_TYPE_REQUIRED", "enable at least one payment type")
	}
	joined := strings.Join(result, ",")
	if len(joined) > 200 {
		return "", infraerrors.BadRequest("PAYMENT_TYPES_TOO_LONG", "configured payment types exceed the storage limit")
	}
	return joined, nil
}

func splitPaymentTypes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func parsePositiveFloat(raw string, fallback float64) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func parseNonNegativeFloat(raw string, fallback float64) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func parsePositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
