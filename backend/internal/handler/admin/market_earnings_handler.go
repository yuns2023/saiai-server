package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MarketEarningsHandler serves /api/v1/admin/market/earnings (sharing
// market, P3a): per-owner earnings overview, the ledger and the reverse of
// frozen earnings.
type MarketEarningsHandler struct {
	service *service.OwnerEarningService
}

func NewMarketEarningsHandler(s *service.OwnerEarningService) *MarketEarningsHandler {
	return &MarketEarningsHandler{service: s}
}

// reverseEarningsRequest: account_id and/or owner_user_id (at least one; both
// = that owner's earnings on that account); as_of (RFC3339, required) is the
// cutoff — use the as_of returned by GET /api/v1/admin/market/earnings, not
// the browser clock (it may not be later than the server time).
type reverseEarningsRequest struct {
	AccountID   *int64 `json:"account_id"`
	OwnerUserID *int64 `json:"owner_user_id"`
	AsOf        string `json:"as_of"`
	Note        string `json:"note"`
}

// parseOptionalID reads a positive id query value ("" = absent).
func parseOptionalID(raw string) (*int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return nil, false
	}
	return &v, true
}

// Owners handles GET /api/v1/admin/market/earnings?owner_user_id=&page=&page_size=.
func (h *MarketEarningsHandler) Owners(c *gin.Context) {
	owner, ok := parseOptionalID(c.Query("owner_user_id"))
	if !ok {
		response.ErrorFrom(c, service.ErrOwnerEarningInvalidQuery)
		return
	}
	page, pageSize := response.ParsePagination(c)
	out, err := h.service.AdminOwners(c.Request.Context(), owner, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

// Entries handles GET /api/v1/admin/market/earnings/entries?owner_user_id=&cursor=&limit=.
func (h *MarketEarningsHandler) Entries(c *gin.Context) {
	owner, ok := parseOptionalID(c.Query("owner_user_id"))
	if !ok {
		response.ErrorFrom(c, service.ErrOwnerEarningInvalidQuery)
		return
	}
	cursor, ok := parseOptionalID(c.Query("cursor"))
	if !ok {
		response.ErrorFrom(c, service.ErrOwnerEarningInvalidQuery)
		return
	}
	limit := 0
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			response.ErrorFrom(c, service.ErrOwnerEarningInvalidQuery)
			return
		}
		limit = v
	}
	var ownerID, cursorID int64
	if owner != nil {
		ownerID = *owner
	}
	if cursor != nil {
		cursorID = *cursor
	}
	out, err := h.service.AdminEntries(c.Request.Context(), ownerID, cursorID, limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

// Reverse handles POST /api/v1/admin/market/earnings/reverse
// {account_id?, owner_user_id?, as_of, note?}: reverses the earnings of the
// scope committed when it runs, recorded at or before as_of and still frozen
// and unreversed. A retry selects its scope again (as_of is not an
// idempotency key).
func (h *MarketEarningsHandler) Reverse(c *gin.Context) {
	var req reverseEarningsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, service.ErrOwnerEarningReverseScope)
		return
	}
	asOf, err := time.Parse(time.RFC3339, strings.TrimSpace(req.AsOf))
	if err != nil {
		response.ErrorFrom(c, service.ErrOwnerEarningReverseAsOf)
		return
	}
	out, err := h.service.AdminReverse(c.Request.Context(), service.OwnerEarningReverseInput{
		AccountID:   req.AccountID,
		OwnerUserID: req.OwnerUserID,
		AsOf:        asOf,
		AdminUserID: marketListingAdminID(c),
		Note:        req.Note,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}
