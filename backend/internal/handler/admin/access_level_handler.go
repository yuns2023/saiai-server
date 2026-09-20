package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type AccessLevelHandler struct {
	service *service.AccessLevelService
}

func NewAccessLevelHandler(accessLevelService *service.AccessLevelService) *AccessLevelHandler {
	return &AccessLevelHandler{service: accessLevelService}
}

type accessLevelRequest struct {
	Name                   string  `json:"name" binding:"required"`
	Rank                   int     `json:"rank" binding:"min=0"`
	BalanceThreshold       float64 `json:"balance_threshold" binding:"min=0"`
	PaygDiscountMultiplier float64 `json:"payg_discount_multiplier" binding:"gte=0,lte=1"`
}

func (h *AccessLevelHandler) List(c *gin.Context) {
	levels, err := h.service.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, levels)
}

func (h *AccessLevelHandler) Create(c *gin.Context) {
	var req accessLevelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	level, err := h.service.Create(c.Request.Context(), &service.AccessLevel{
		Name: req.Name, Rank: req.Rank, BalanceThreshold: req.BalanceThreshold,
		PaygDiscountMultiplier: req.PaygDiscountMultiplier,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, level)
}

func (h *AccessLevelHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid access level ID")
		return
	}
	var req accessLevelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	level, err := h.service.Update(c.Request.Context(), &service.AccessLevel{
		ID: id, Name: req.Name, Rank: req.Rank, BalanceThreshold: req.BalanceThreshold,
		PaygDiscountMultiplier: req.PaygDiscountMultiplier,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, level)
}

func (h *AccessLevelHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid access level ID")
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Access level deleted successfully"})
}

func (h *AccessLevelHandler) GetSettings(c *gin.Context) {
	enabled, err := h.service.BalanceMaintenanceEnabled(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"balance_maintenance_enabled": enabled})
}

func (h *AccessLevelHandler) UpdateSettings(c *gin.Context) {
	var req struct {
		Enabled bool `json:"balance_maintenance_enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.service.SetBalanceMaintenanceEnabled(c.Request.Context(), req.Enabled); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"balance_maintenance_enabled": req.Enabled})
}

func (h *AccessLevelHandler) SetUserManualLevel(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	var req struct {
		LevelID int64 `json:"level_id" binding:"required,gt=0"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.service.SetManualLevel(c.Request.Context(), userID, req.LevelID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "User access level override updated"})
}

func (h *AccessLevelHandler) ClearUserManualLevel(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	if err := h.service.ClearManualLevel(c.Request.Context(), userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "User access level returned to automatic mode"})
}
