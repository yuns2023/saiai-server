package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func (s *SettingService) GetModelPricingHistory(ctx context.Context) ([]PriceEdit, error) {
	history := []PriceEdit{}
	values, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyModelPricingHistory})
	if err != nil {
		return nil, err
	}
	if raw := values[SettingKeyModelPricingHistory]; raw != "" {
		if err = json.Unmarshal([]byte(raw), &history); err != nil {
			return nil, fmt.Errorf("decode pricing history: %w", err)
		}
	}
	return history, nil
}

// Persist only pricing keys, leaving other settings intact. SetMultiple uses
// one upsert statement so aliases, overrides and their audit entry are atomic.
func (s *SettingService) SaveModelPricing(ctx context.Context, aliases map[string]string, overrides map[string]ModelPriceOverride, change PriceEdit) error {
	if err := validatePricingModelAliases(aliases); err != nil {
		return err
	}
	for _, o := range overrides {
		if err := o.Validate(); err != nil {
			return err
		}
	}
	history, err := s.GetModelPricingHistory(ctx)
	if err != nil {
		return err
	}
	if change.At.IsZero() {
		change.At = time.Now().UTC()
	}
	history = append(history, change)
	if len(history) > 100 {
		history = history[len(history)-100:]
	}
	updates := map[string]string{}
	for k, v := range map[string]any{SettingKeyPricingModelAliases: aliases, SettingKeyModelPriceOverrides: overrides, SettingKeyModelPricingHistory: history} {
		body, err := json.Marshal(v)
		if err != nil {
			return err
		}
		updates[k] = string(body)
	}
	return s.settingRepo.SetMultiple(ctx, updates)
}
