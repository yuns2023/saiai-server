package admin

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"io"
)

func (h *SettingHandler) SetJevSettingsService(s *service.JevSettingsService) { h.jevSettings = s }
func (h *SettingHandler) GetJevSettings(c *gin.Context) {
	v, err := h.jevSettings.Get(c.Request.Context())
	if err != nil {
		response.InternalError(c, "Jev settings unavailable")
		return
	}
	response.Success(c, v)
}
func (h *SettingHandler) UpdateJevSettings(c *gin.Context) {
	var v service.JevSettingsUpdate
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 8193))
	if err != nil || len(raw) > 8192 || json.Unmarshal(raw, &v) != nil {
		response.BadRequest(c, "Invalid Jev settings")
		return
	}
	if err = h.jevSettings.Update(c.Request.Context(), v); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.GetJevSettings(c)
}
