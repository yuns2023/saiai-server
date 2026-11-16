package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	cryptoPayMaxPageSize = 100
	cryptoPayMaxPage     = 100000
)

type cryptoPayUserService interface {
	GetUserConfig(ctx context.Context) (*service.CryptoPayUserConfig, error)
	CreateOrder(ctx context.Context, userID int64, network, asset, amount string) (*service.CryptoPayOrder, error)
	GetUserOrder(ctx context.Context, userID, orderID int64) (*service.CryptoPayOrder, error)
	ListUserOrders(ctx context.Context, userID int64, page, pageSize int) ([]service.CryptoPayOrder, int64, error)
	ListUserDeposits(ctx context.Context, userID int64, page, pageSize int) ([]service.CryptoPayDeposit, int64, error)
	ListUserAddresses(ctx context.Context, userID int64) ([]service.CryptoPayUserAddress, error)
}

// CryptoPayHandler exposes the user side of the stablecoin top-up.
type CryptoPayHandler struct {
	svc cryptoPayUserService
}

func NewCryptoPayHandler(svc *service.CryptoPayService) *CryptoPayHandler {
	return &CryptoPayHandler{svc: svc}
}

// cryptoPayAmount accepts a JSON string ("12.5") or a JSON number (12.5)
// and keeps its exact decimal text; it is never converted to float64.
type cryptoPayAmount string

func (a *cryptoPayAmount) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*a = cryptoPayAmount(s)
		return nil
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&n); err != nil {
		return err
	}
	*a = cryptoPayAmount(n.String())
	return nil
}

// createCryptoPayOrderRequest: network/asset/amount are validated by the
// service so every bad value maps to its contract reason (a missing or
// non-string/number amount → CRYPTO_PAY_AMOUNT_INVALID).
type createCryptoPayOrderRequest struct {
	Network string          `json:"network"`
	Asset   string          `json:"asset"`
	Amount  json.RawMessage `json:"amount"`
}

func parseCryptoPayAmount(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] == 'n' || raw[0] == '{' || raw[0] == '[' || raw[0] == 't' || raw[0] == 'f' {
		return "", false
	}
	var a cryptoPayAmount
	if err := a.UnmarshalJSON(raw); err != nil || string(a) == "" {
		return "", false
	}
	return string(a), true
}

// CryptoPayPagination clamps page/page_size for crypto pay lists.
func CryptoPayPagination(c *gin.Context) (int, int) {
	page, pageSize := response.ParsePagination(c)
	if pageSize > cryptoPayMaxPageSize {
		pageSize = cryptoPayMaxPageSize
	}
	if page > cryptoPayMaxPage {
		page = cryptoPayMaxPage
	}
	return page, pageSize
}

func (h *CryptoPayHandler) GetConfig(c *gin.Context) {
	cfg, err := h.svc.GetUserConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *CryptoPayHandler) CreateOrder(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req createCryptoPayOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	amount, ok := parseCryptoPayAmount(req.Amount)
	if !ok {
		response.ErrorFrom(c, infraerrors.BadRequest(service.CryptoPayReasonAmountInvalid, "amount must be a decimal string"))
		return
	}
	order, err := h.svc.CreateOrder(c.Request.Context(), subject.UserID, req.Network, req.Asset, amount)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, order)
}

func (h *CryptoPayHandler) GetOrder(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || orderID <= 0 {
		response.BadRequest(c, "Invalid order ID")
		return
	}
	order, err := h.svc.GetUserOrder(c.Request.Context(), subject.UserID, orderID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, order)
}

func (h *CryptoPayHandler) ListMyOrders(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	page, pageSize := CryptoPayPagination(c)
	items, total, err := h.svc.ListUserOrders(c.Request.Context(), subject.UserID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if items == nil {
		items = []service.CryptoPayOrder{}
	}
	response.Paginated(c, items, total, page, pageSize)
}

func (h *CryptoPayHandler) ListDeposits(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	page, pageSize := CryptoPayPagination(c)
	items, total, err := h.svc.ListUserDeposits(c.Request.Context(), subject.UserID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if items == nil {
		items = []service.CryptoPayDeposit{}
	}
	response.Paginated(c, items, total, page, pageSize)
}

func (h *CryptoPayHandler) ListAddresses(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	items, err := h.svc.ListUserAddresses(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if items == nil {
		items = []service.CryptoPayUserAddress{}
	}
	response.Success(c, items)
}
