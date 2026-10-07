package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAIChatGPTBillingSettings applies to native Chat, independently of the
// requested model and the token prices used by Responses/Codex.
type OpenAIChatGPTBillingSettings struct {
	SuccessTurnPriceUSD float64            `json:"success_turn_price_usd"`
	TierPricesUSD       map[string]float64 `json:"tier_prices_usd,omitempty"`
}

var chatGPTInstantModel = regexp.MustCompile(`^gpt-[0-9]+(-[0-9]+)*(-instant)?$`)

// ChatGPTBillingTier reads native Chat metadata without changing the request.
// Unknown/automatic choices use the explicitly configured fallback price.
func ChatGPTBillingTier(model, effort string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	effort = strings.ToLower(strings.TrimSpace(effort))
	if strings.HasSuffix(model, "-pro") && (strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o")) {
		return "pro"
	}
	switch effort {
	case "standard":
		return "medium"
	case "extended":
		return "high"
	case "max":
		return "extreme"
	}
	if model == "" || model == "auto" {
		return "default"
	}
	if effort == "" && chatGPTInstantModel.MatchString(model) {
		return "instant"
	}
	return "default"
}

func (s *OpenAIChatGPTBillingSettings) PriceFor(model, effort string) float64 {
	if price, ok := s.TierPricesUSD[ChatGPTBillingTier(model, effort)]; ok {
		return price
	}
	return s.SuccessTurnPriceUSD
}

func validChatGPTBillingSettings(s *OpenAIChatGPTBillingSettings) bool {
	if s == nil || !validChatGPTConfiguredPrice(s.SuccessTurnPriceUSD) {
		return false
	}
	if s.TierPricesUSD == nil {
		return true
	}
	if len(s.TierPricesUSD) != 5 {
		return false
	}
	for _, tier := range []string{"instant", "medium", "high", "extreme", "pro"} {
		price, ok := s.TierPricesUSD[tier]
		if !ok || !validChatGPTConfiguredPrice(price) {
			return false
		}
	}
	return true
}

// DecodeChatGPTBillingSettings rejects omitted/null prices instead of silently
// treating them as a zero-price tier. The legacy numeric storage stays valid.
func DecodeChatGPTBillingSettings(raw []byte) (*OpenAIChatGPTBillingSettings, error) {
	var wire struct {
		Success *float64            `json:"success_turn_price_usd"`
		Tiers   map[string]*float64 `json:"tier_prices_usd"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || wire.Success == nil {
		return nil, errors.New("missing numeric fallback price")
	}
	s := &OpenAIChatGPTBillingSettings{SuccessTurnPriceUSD: *wire.Success}
	if wire.Tiers != nil {
		s.TierPricesUSD = make(map[string]float64, len(wire.Tiers))
		for tier, price := range wire.Tiers {
			if price == nil {
				return nil, errors.New("missing numeric tier price")
			}
			s.TierPricesUSD[tier] = *price
		}
	}
	if !validChatGPTBillingSettings(s) {
		return nil, errors.New("invalid Chat billing prices")
	}
	return s, nil
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
		if strings.HasPrefix(strings.TrimSpace(raw), "{") {
			settings, decodeErr := DecodeChatGPTBillingSettings([]byte(raw))
			if decodeErr != nil {
				return nil, infraerrors.InternalServer("CHATGPT_BILLING_SETTINGS_INVALID", "Native Chat billing settings are invalid")
			}
			return settings, nil
		}
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
	if !validChatGPTBillingSettings(settings) {
		return infraerrors.BadRequest("CHATGPT_TURN_PRICE_INVALID", "Price must be a finite, non-negative USD amount")
	}
	if settings.TierPricesUSD != nil {
		raw, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		return s.settingRepo.Set(ctx, SettingKeyOpenAIChatSuccessTurnPriceUSD, string(raw))
	}
	return s.settingRepo.Set(ctx, SettingKeyOpenAIChatSuccessTurnPriceUSD,
		strconv.FormatFloat(settings.SuccessTurnPriceUSD, 'f', -1, 64))
}
