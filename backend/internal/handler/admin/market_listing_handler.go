package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MarketListingHandler serves /api/v1/admin/accounts/:id/market-listing
// (sharing market, P3a): the account's plan, which decides the tiers it may
// be shared in.
type MarketListingHandler struct {
	service *service.MarketListingService
}

func NewMarketListingHandler(s *service.MarketListingService) *MarketListingHandler {
	return &MarketListingHandler{service: s}
}

// marketListingRequest: plan is required. The legacy fields
// allowed_group_ids and reserved_* are not read (ignored if sent).
type marketListingRequest struct {
	Plan *string `json:"plan"`
}

func marketListingAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	return id, true
}

func marketListingAdminID(c *gin.Context) int64 {
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		return subject.UserID
	}
	return 0
}

// Get handles GET /api/v1/admin/accounts/:id/market-listing.
func (h *MarketListingHandler) Get(c *gin.Context) {
	accountID, ok := marketListingAccountID(c)
	if !ok {
		return
	}
	view, err := h.service.AdminGet(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// Put handles PUT /api/v1/admin/accounts/:id/market-listing.
func (h *MarketListingHandler) Put(c *gin.Context) {
	accountID, ok := marketListingAccountID(c)
	if !ok {
		return
	}
	var req marketListingRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Plan == nil || *req.Plan == "" {
		response.BadRequest(c, "plan is required")
		return
	}
	view, err := h.service.AdminConfigure(c.Request.Context(), marketListingAdminID(c), accountID, *req.Plan)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// Delete handles DELETE /api/v1/admin/accounts/:id/market-listing: revokes
// the permission and removes the market membership (history kept).
func (h *MarketListingHandler) Delete(c *gin.Context) {
	accountID, ok := marketListingAccountID(c)
	if !ok {
		return
	}
	view, err := h.service.AdminRevoke(c.Request.Context(), marketListingAdminID(c), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}
