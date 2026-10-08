package admin

import (
	"encoding/json"
	"io"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) GetChatGPTBillingSettings(c *gin.Context) {
	settings, err := h.settingService.GetOpenAIChatGPTBillingSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *SettingHandler) UpdateChatGPTBillingSettings(c *gin.Context) {
	raw, readErr := io.ReadAll(io.LimitReader(c.Request.Body, 8193))
	settings, err := service.DecodeChatGPTBillingSettings(raw)
	if readErr != nil || len(raw) > 8192 || err != nil {
		response.BadRequest(c, "Chat and image prices must be finite non-negative numbers with complete tier tables")
		return
	}
	// A legacy UI can still update Chat prices without knowing about images.
	// Omission preserves the surcharge; explicit null disables it.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, supplied := fields["image_prices_usd"]; !supplied {
		current, readErr := h.settingService.GetOpenAIChatGPTBillingSettings(c.Request.Context())
		if readErr != nil {
			response.ErrorFrom(c, readErr)
			return
		}
		settings.ImagePricesUSD = current.ImagePricesUSD
	}
	if err := h.settingService.SetOpenAIChatGPTBillingSettings(c.Request.Context(), settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}
