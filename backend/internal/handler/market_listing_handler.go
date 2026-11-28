package handler

import (
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
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
// {"listed": true|false, "group_id"?: <tier>}: 共享 (listed) or 自用.
// group_id is optional when the plan has exactly one tier (more than one →
// 400 MARKET_TIER_REQUIRED); unlisting ignores it. A switch inside the
// cooldown → 429 MARKET_SWITCH_COOLDOWN with Retry-After and
// metadata.next_switch_at.
func (h *MarketListingHandler) Put(c *gin.Context) {
	userID, accountID, ok := h.ownerAndAccount(c)
	if !ok {
		return
	}
	var req ownerMarketRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Listed == nil || req.GroupID < 0 {
		response.BadRequest(c, "listed is required")
		return
	}
	groupID := req.GroupID
	if !*req.Listed {
		groupID = 0
	}
	view, err := h.service.OwnerSet(c.Request.Context(), userID, accountID, groupID, *req.Listed)
	if err != nil {
		if app := infraerrors.FromError(err); app != nil && app.Reason == "MARKET_SWITCH_COOLDOWN" {
			if seconds := app.Metadata["retry_after_seconds"]; seconds != "" {
				c.Header("Retry-After", seconds)
			}
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}
