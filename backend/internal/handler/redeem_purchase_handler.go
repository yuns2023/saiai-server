package handler

import (
	"encoding/json"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// maxRedeemPurchasePageSize bounds one page of a user's bought codes.
const maxRedeemPurchasePageSize = 50

// RedeemPurchaseHandler serves "buy a redeem code with balance" for the
// signed-in user. The buyer is always the authenticated user.
type RedeemPurchaseHandler struct {
	purchases *service.RedeemPurchaseService
}

func NewRedeemPurchaseHandler(purchases *service.RedeemPurchaseService) *RedeemPurchaseHandler {
	return &RedeemPurchaseHandler{purchases: purchases}
}

// createRedeemPurchaseRequest: amount is kept as the literal number (or a
// numeric string), never a float, and parsed exactly by the service.
type createRedeemPurchaseRequest struct {
	Amount    json.Number `json:"amount"`
	RequestID string      `json:"request_id"`
}

// List GET /api/v1/redeem/purchases?page=&page_size=
func (h *RedeemPurchaseHandler) List(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	page, pageSize := response.ParsePagination(c)
	if pageSize > maxRedeemPurchasePageSize {
		pageSize = maxRedeemPurchasePageSize
	}
	out, err := h.purchases.Overview(c.Request.Context(), subject.UserID, pagination.PaginationParams{Page: page, PageSize: pageSize})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.RedeemPurchaseOverviewFromService(out))
}

// Create POST /api/v1/redeem/purchases {amount, request_id}
func (h *RedeemPurchaseHandler) Create(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req createRedeemPurchaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	view, replayed, err := h.purchases.Purchase(c.Request.Context(), subject.UserID, req.Amount.String(), req.RequestID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.RedeemPurchaseResult{RedeemPurchase: dto.RedeemPurchaseFromService(view), Replayed: replayed})
}

// Revoke POST /api/v1/redeem/purchases/:id/revoke — only the signed-in
// user's own purchases; another user's id is "not found".
func (h *RedeemPurchaseHandler) Revoke(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, service.ErrRedeemPurchaseNotFound)
		return
	}
	view, err := h.purchases.Revoke(c.Request.Context(), subject.UserID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.RedeemPurchaseFromService(view))
}
