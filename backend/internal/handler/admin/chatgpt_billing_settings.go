package admin

import (
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
	var req struct {
		SuccessTurnPriceUSD *float64 `json:"success_turn_price_usd"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.SuccessTurnPriceUSD == nil {
		response.BadRequest(c, "A numeric success_turn_price_usd is required")
		return
	}
	settings := &service.OpenAIChatGPTBillingSettings{SuccessTurnPriceUSD: *req.SuccessTurnPriceUSD}
	if err := h.settingService.SetOpenAIChatGPTBillingSettings(c.Request.Context(), settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}
