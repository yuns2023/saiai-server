package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type chatGPTBillingSettingsRepo struct {
	SettingRepository
	values   map[string]string
	readErr  error
	writeErr error
}

func TestChatGPTFiveTierPricesAndLegacyFallback(t *testing.T) {
	repo := &chatGPTBillingSettingsRepo{values: map[string]string{SettingKeyOpenAIChatSuccessTurnPriceUSD: "0.02"}}
	svc := NewSettingService(repo, nil)
	settings, err := svc.GetOpenAIChatGPTBillingSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0.02, settings.PriceFor("gpt-6-pro", ""))
	settings.TierPricesUSD = map[string]float64{"instant": 0.01, "medium": 0.02, "high": 0.03, "extreme": 0.04, "pro": 0.05}
	require.NoError(t, svc.SetOpenAIChatGPTBillingSettings(context.Background(), settings))
	loaded, err := svc.GetOpenAIChatGPTBillingSettings(context.Background())
	require.NoError(t, err)
	for _, row := range []struct {
		model, effort, tier string
		price               float64
	}{
		{"gpt-5-6", "", "instant", 0.01}, {"gpt-5-5-instant", "", "instant", 0.01},
		{"gpt-5-6-thinking", "standard", "medium", 0.02}, {"gpt-5-5-thinking", "extended", "high", 0.03},
		{"gpt-5-6-thinking", "max", "extreme", 0.04}, {"gpt-6-pro", "", "pro", 0.05},
		{"gpt-5-6-pro", "", "pro", 0.05}, {"auto", "", "default", 0.02}, {"auto", "max", "extreme", 0.04},
		{"new-native-model", "new-effort", "default", 0.02}, {"gpt-5-6-thinking", "", "default", 0.02},
	} {
		require.Equal(t, row.tier, ChatGPTBillingTier(row.model, row.effort))
		require.Equal(t, row.price, loaded.PriceFor(row.model, row.effort))
	}
	loaded.TierPricesUSD["pro"] = 0
	require.NoError(t, svc.SetOpenAIChatGPTBillingSettings(context.Background(), loaded))
	loaded, err = svc.GetOpenAIChatGPTBillingSettings(context.Background())
	require.NoError(t, err)
	require.Zero(t, loaded.PriceFor("gpt-6-pro", ""))
	require.Equal(t, 0.02, loaded.PriceFor("auto", ""))
	// A legacy uniform-price PUT atomically switches back to a uniform price.
	require.NoError(t, svc.SetOpenAIChatGPTBillingSettings(context.Background(), &OpenAIChatGPTBillingSettings{SuccessTurnPriceUSD: 0.06}))
	loaded, err = svc.GetOpenAIChatGPTBillingSettings(context.Background())
	require.NoError(t, err)
	require.Nil(t, loaded.TierPricesUSD)
	require.Equal(t, 0.06, loaded.PriceFor("gpt-6-pro", ""))
}

func TestChatGPTTierPriceStorageRejectsPartialAndNullTables(t *testing.T) {
	valid := `{"success_turn_price_usd":0.02,"tier_prices_usd":{"instant":0.01,"medium":0.02,"high":0.03,"extreme":0.04,"pro":0.05}}`
	var shape map[string]any
	require.NoError(t, json.Unmarshal([]byte(valid), &shape))
	for _, raw := range []string{
		`{"tier_prices_usd":{}}`,
		`{"success_turn_price_usd":0.02,"tier_prices_usd":{}}`,
		`{"success_turn_price_usd":0.02,"tier_prices_usd":{"pro":0.05}}`,
		strings.Replace(valid, `"pro":0.05`, `"pro":null`, 1),
		strings.Replace(valid, `"pro":0.05`, `"pro":-1`, 1),
		strings.Replace(valid, `"pro":0.05`, `"pro":1e999`, 1),
		strings.Replace(valid, `"pro":0.05`, `"other":0.05`, 1),
	} {
		repo := &chatGPTBillingSettingsRepo{values: map[string]string{SettingKeyOpenAIChatSuccessTurnPriceUSD: raw}}
		settings, err := NewSettingService(repo, &config.Config{}).GetOpenAIChatGPTBillingSettings(context.Background())
		require.Error(t, err, raw)
		require.Nil(t, settings)
	}
}

func (r *chatGPTBillingSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	if r.readErr != nil {
		return "", r.readErr
	}
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *chatGPTBillingSettingsRepo) Set(_ context.Context, key, value string) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	r.values[key] = value
	return nil
}

func TestChatGPTBillingSettingsOverrideAndLiveUpdates(t *testing.T) {
	ctx := context.Background()
	repo := &chatGPTBillingSettingsRepo{values: map[string]string{"unrelated": "preserved"}}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIChatSuccessTurnPriceUSD = 0.07
	svc := NewSettingService(repo, cfg)
	settings, err := svc.GetOpenAIChatGPTBillingSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 0.07, settings.SuccessTurnPriceUSD)
	for _, price := range []float64{0.02, 0.03, 0} {
		require.NoError(t, svc.SetOpenAIChatGPTBillingSettings(ctx, &OpenAIChatGPTBillingSettings{SuccessTurnPriceUSD: price}))
		settings, err = svc.GetOpenAIChatGPTBillingSettings(ctx)
		require.NoError(t, err)
		require.Equal(t, price, settings.SuccessTurnPriceUSD)
		require.Equal(t, "preserved", repo.values["unrelated"])
	}
	require.Equal(t, "0", repo.values[SettingKeyOpenAIChatSuccessTurnPriceUSD], "explicit zero overrides positive startup price")
	require.Equal(t, 0.07, cfg.Gateway.OpenAIChatSuccessTurnPriceUSD)
}

func TestChatGPTBillingSettingsFailClosed(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIChatSuccessTurnPriceUSD = 0.02
	for _, raw := range []string{"", "invalid", "-0.01", "NaN", "+Inf", "1e999"} {
		t.Run(raw, func(t *testing.T) {
			repo := &chatGPTBillingSettingsRepo{values: map[string]string{SettingKeyOpenAIChatSuccessTurnPriceUSD: raw}}
			settings, err := NewSettingService(repo, cfg).GetOpenAIChatGPTBillingSettings(ctx)
			require.Error(t, err)
			require.Nil(t, settings, "corrupt persisted values must not fall back to startup price")
		})
	}
	repo := &chatGPTBillingSettingsRepo{values: map[string]string{}, readErr: errors.New("database unavailable")}
	settings, err := NewSettingService(repo, cfg).GetOpenAIChatGPTBillingSettings(ctx)
	require.Error(t, err)
	require.Nil(t, settings)
	repo.readErr = nil
	settings, err = NewSettingService(repo, nil).GetOpenAIChatGPTBillingSettings(ctx)
	require.NoError(t, err)
	require.Zero(t, settings.SuccessTurnPriceUSD, "safe default is disabled")
}

func TestChatGPTBillingSettingsRejectInvalidWrites(t *testing.T) {
	repo := &chatGPTBillingSettingsRepo{values: map[string]string{SettingKeyOpenAIChatSuccessTurnPriceUSD: "0.02"}}
	svc := NewSettingService(repo, nil)
	for _, price := range []float64{-0.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		require.Error(t, svc.SetOpenAIChatGPTBillingSettings(context.Background(), &OpenAIChatGPTBillingSettings{SuccessTurnPriceUSD: price}))
		require.Equal(t, "0.02", repo.values[SettingKeyOpenAIChatSuccessTurnPriceUSD])
	}
	require.Error(t, svc.SetOpenAIChatGPTBillingSettings(context.Background(), nil))
	repo.writeErr = errors.New("write failed")
	require.Error(t, svc.SetOpenAIChatGPTBillingSettings(context.Background(), &OpenAIChatGPTBillingSettings{SuccessTurnPriceUSD: 0.03}))
	require.Equal(t, "0.02", repo.values[SettingKeyOpenAIChatSuccessTurnPriceUSD])
}
