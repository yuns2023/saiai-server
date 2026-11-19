package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MarketListingHandler serves the owner side of the sharing market:
// /api/v1/hosted/owned-accounts/:id/market. Only the current owner of a live
// account sees or changes it (others: 404).
type MarketListingHandler struct {
	service *service.MarketListingService
}

func NewMarketListingHandler(s *service.MarketListingService) *MarketListingHandler {
	return &MarketListingHandler{service: s}
}

type ownerMarketRequest struct {
	GroupID int64 `json:"group_id"`
	Listed  *bool `json:"listed"`
}

func (h *MarketListingHandler) ownerAndAccount(c *gin.Context) (int64, int64, bool) {
	c.Header("Cache-Control", "no-store")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return 0, 0, false
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.ErrorFrom(c, service.ErrHostedOwnedAccountNotFound)
		return 0, 0, false
	}
	return subject.UserID, accountID, true
}

// Get handles GET /api/v1/hosted/owned-accounts/:id/market.
func (h *MarketListingHandler) Get(c *gin.Context) {
	userID, accountID, ok := h.ownerAndAccount(c)
	if !ok {
		return
	}
	view, err := h.service.OwnerGet(c.Request.Context(), userID, accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// Put handles PUT /api/v1/hosted/owned-accounts/:id/market with
// {"group_id": <allowed tier>, "listed": true|false}. group_id is required
// when listing; unlisting ignores it.
func (h *MarketListingHandler) Put(c *gin.Context) {
	userID, accountID, ok := h.ownerAndAccount(c)
	if !ok {
		return
	}
	var req ownerMarketRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Listed == nil || (*req.Listed && req.GroupID <= 0) {
		response.BadRequest(c, "listed is required; group_id is required when listing")
		return
	}
	view, err := h.service.OwnerSet(c.Request.Context(), userID, accountID, req.GroupID, *req.Listed)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}
