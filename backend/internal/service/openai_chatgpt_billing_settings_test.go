package service

import (
	"context"
	"errors"
	"math"
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
