package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"io"
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
		response.BadRequest(c, "A numeric success_turn_price_usd is required")
		return
	}
	if err := h.settingService.SetOpenAIChatGPTBillingSettings(c.Request.Context(), settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}
