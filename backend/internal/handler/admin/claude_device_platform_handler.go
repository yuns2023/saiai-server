package admin

import (
	"errors"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ClaudeDevicePlatformHandler serves the read-only flagged Claude device list.
type ClaudeDevicePlatformHandler struct {
	svc *service.ClaudeDevicePlatformService
}

func NewClaudeDevicePlatformHandler(svc *service.ClaudeDevicePlatformService) *ClaudeDevicePlatformHandler {
	return &ClaudeDevicePlatformHandler{svc: svc}
}

// ListFlagged handles GET /api/v1/admin/claude-device-platform/flagged
// ?limit=&cursor=&reasons=cross_family,cross_user
func (h *ClaudeDevicePlatformHandler) ListFlagged(c *gin.Context) {
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.BadRequest(c, "invalid limit")
			return
		}
		limit = min(n, service.ClaudeDevicePlatformListMaxLimit)
	}
	reasons, err := service.ParseClaudeDevicePlatformReasons(c.Query("reasons"))
	if err != nil {
		response.BadRequest(c, "invalid reasons")
		return
	}
	cursor := strings.TrimSpace(c.Query("cursor"))
	if len(cursor) > 128 {
		response.BadRequest(c, "invalid cursor")
		return
	}
	result, err := h.svc.ListFlagged(c.Request.Context(), limit, cursor, reasons)
	if err != nil {
		if errors.Is(err, service.ErrClaudeDevicePlatformInvalidQuery) {
			response.BadRequest(c, "invalid cursor")
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
