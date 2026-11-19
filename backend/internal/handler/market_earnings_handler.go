package handler

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MarketEarningsHandler serves the owner earnings of the sharing market
// (P3a): /api/v1/hosted/earnings[/entries|/convert]. Every endpoint works on
// the signed-in user's own ledger only; there is no user id parameter.
type MarketEarningsHandler struct {
	service *service.OwnerEarningService
}

func NewMarketEarningsHandler(s *service.OwnerEarningService) *MarketEarningsHandler {
	return &MarketEarningsHandler{service: s}
}

// convertEarningsRequest: amount is kept as the literal number (or a
// numeric string) and parsed exactly (at most 2 decimal places).
type convertEarningsRequest struct {
	Amount    json.Number `json:"amount"`
	RequestID string      `json:"request_id"`
}

func earningsSubject(c *gin.Context) (int64, bool) {
	c.Header("Cache-Control", "no-store")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	return subject.UserID, true
}

// parseEarningsCursor reads ?cursor= (entry id, 0 = first page) and ?limit=.
func parseEarningsCursor(c *gin.Context) (int64, int, bool) {
	var cursor int64
	var limit int
	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			return 0, 0, false
		}
		cursor = v
	}
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			return 0, 0, false
		}
		limit = v
	}
	return cursor, limit, true
}

// Summary handles GET /api/v1/hosted/earnings.
func (h *MarketEarningsHandler) Summary(c *gin.Context) {
	userID, ok := earningsSubject(c)
	if !ok {
		return
	}
	view, err := h.service.Summary(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// Entries handles GET /api/v1/hosted/earnings/entries?cursor=&limit= (≤100).
func (h *MarketEarningsHandler) Entries(c *gin.Context) {
	userID, ok := earningsSubject(c)
	if !ok {
		return
	}
	cursor, limit, ok := parseEarningsCursor(c)
	if !ok {
		response.ErrorFrom(c, service.ErrOwnerEarningInvalidQuery)
		return
	}
	page, err := h.service.Entries(c.Request.Context(), userID, cursor, limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, page)
}

// Convert handles POST /api/v1/hosted/earnings/convert {amount, request_id}.
func (h *MarketEarningsHandler) Convert(c *gin.Context) {
	userID, ok := earningsSubject(c)
	if !ok {
		return
	}
	var req convertEarningsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, service.ErrOwnerEarningInvalidAmount)
		return
	}
	view, err := h.service.Convert(c.Request.Context(), userID, req.Amount.String(), req.RequestID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}
