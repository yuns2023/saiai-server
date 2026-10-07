package service

import (
	"context"
	"errors"
	"math"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAIChatGPTBillingSettings applies to native Chat, independently of the
// requested model and the token prices used by Responses/Codex.
type OpenAIChatGPTBillingSettings struct {
	SuccessTurnPriceUSD float64 `json:"success_turn_price_usd"`
}

func validChatGPTConfiguredPrice(price float64) bool {
	return price >= 0 && !math.IsNaN(price) && !math.IsInf(price, 0)
}

// GetOpenAIChatGPTBillingSettings reads through to the database so an admin
// update applies to the next turn across all Gateway instances. Only an absent
// setting falls back to startup configuration; read failures fail closed.
func (s *SettingService) GetOpenAIChatGPTBillingSettings(ctx context.Context) (*OpenAIChatGPTBillingSettings, error) {
	price := 0.0
	if s.cfg != nil {
		price = s.cfg.Gateway.OpenAIChatSuccessTurnPriceUSD
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIChatSuccessTurnPriceUSD)
	if err == nil {
		price, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, infraerrors.InternalServer("CHATGPT_BILLING_SETTINGS_INVALID", "Native Chat billing settings are invalid")
		}
	} else if !errors.Is(err, ErrSettingNotFound) {
		return nil, infraerrors.ServiceUnavailable("CHATGPT_BILLING_SETTINGS_UNAVAILABLE", "Native Chat billing settings are unavailable")
	}
	if !validChatGPTConfiguredPrice(price) {
		return nil, infraerrors.InternalServer("CHATGPT_BILLING_SETTINGS_INVALID", "Native Chat billing settings are invalid")
	}
	return &OpenAIChatGPTBillingSettings{SuccessTurnPriceUSD: price}, nil
}

// SetOpenAIChatGPTBillingSettings changes only this price. Zero disables paid
// native Chat turns; it never opts an installation into unaccounted traffic.
func (s *SettingService) SetOpenAIChatGPTBillingSettings(ctx context.Context, settings *OpenAIChatGPTBillingSettings) error {
	if settings == nil || !validChatGPTConfiguredPrice(settings.SuccessTurnPriceUSD) {
		return infraerrors.BadRequest("CHATGPT_TURN_PRICE_INVALID", "Price must be a finite, non-negative USD amount")
	}
	return s.settingRepo.Set(ctx, SettingKeyOpenAIChatSuccessTurnPriceUSD,
		strconv.FormatFloat(settings.SuccessTurnPriceUSD, 'f', -1, 64))
}
