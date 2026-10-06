package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpsHandler) requireLogFilters(ctx *gin.Context) bool {
	if h.opsService == nil {
		response.Error(ctx, http.StatusServiceUnavailable, "Ops service not available")
		return false
	}
	if err := h.opsService.RequireMonitoringEnabled(ctx.Request.Context()); err != nil {
		response.ErrorFrom(ctx, err)
		return false
	}
	return true
}

func (h *OpsHandler) GetLogFilters(ctx *gin.Context) {
	if !h.requireLogFilters(ctx) {
		return
	}
	config, err := h.opsService.GetOpsLogFilters(ctx.Request.Context())
	if err != nil {
		response.Error(ctx, http.StatusInternalServerError, "Failed to load log filters")
		return
	}
	response.Success(ctx, config)
}

func (h *OpsHandler) UpdateLogFilters(ctx *gin.Context) {
	if !h.requireLogFilters(ctx) {
		return
	}
	var update service.OpsLogFilterUpdate
	if err := ctx.ShouldBindJSON(&update); err != nil {
		response.BadRequest(ctx, "Invalid log filter configuration")
		return
	}
	config, err := h.opsService.UpdateOpsLogFilters(ctx.Request.Context(), &update)
	if err != nil {
		response.ErrorFrom(ctx, err)
		return
	}
	response.Success(ctx, config)
}

func (h *OpsHandler) GetLogFilterProposal(ctx *gin.Context) {
	if !h.requireLogFilters(ctx) {
		return
	}
	errorID, err := strconv.ParseInt(ctx.Param("id"), 10, 64)
	if err != nil || errorID <= 0 {
		response.BadRequest(ctx, "Invalid error id")
		return
	}
	detail, err := h.opsService.GetErrorLogByID(ctx.Request.Context(), errorID)
	if err != nil {
		response.ErrorFrom(ctx, err)
		return
	}
	response.Success(ctx, service.BuildOpsLogFilterProposal(detail))
}
