package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type cryptoPayAdminService interface {
	GetAdminSettings(ctx context.Context) (*service.CryptoPayAdminSettings, error)
	UpdateSettings(ctx context.Context, req service.UpdateCryptoPaySettingsRequest, operator string) (*service.CryptoPayAdminSettings, error)
	ListKeysets(ctx context.Context) ([]service.CryptoPayKeysetInfo, error)
	ImportEVMKeyset(ctx context.Context, network, xpub, operator string) (*service.CryptoPayKeysetInfo, error)
	ImportSolanaKeyset(ctx context.Context, raw []byte, operator string) (*service.CryptoPayKeysetInfo, error)
	AdminListDeposits(ctx context.Context, f service.CryptoPayAdminDepositFilter, page, pageSize int) ([]service.CryptoPayAdminDeposit, int64, error)
	ReleaseDeposit(ctx context.Context, depositID int64, operator string) (*service.CryptoPayCreditOutcome, error)
	RescanTransaction(ctx context.Context, network, txHash, operator string) (*service.CryptoPayRescanResult, error)
	Reconciliation(ctx context.Context) (*service.CryptoPayReconciliation, error)
}

// CryptoPayHandler exposes the admin side of the stablecoin top-up.
type CryptoPayHandler struct {
	svc cryptoPayAdminService
}

func NewCryptoPayHandler(svc *service.CryptoPayService) *CryptoPayHandler {
	return &CryptoPayHandler{svc: svc}
}

func cryptoPayOperator(c *gin.Context) string {
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		return "admin:" + strconv.FormatInt(subject.UserID, 10)
	}
	return "admin"
}

func cryptoPayAdminPagination(c *gin.Context) (int, int) {
	page, pageSize := response.ParsePagination(c)
	if pageSize > 200 {
		pageSize = 200
	}
	if page > 100000 {
		page = 100000
	}
	return page, pageSize
}

func (h *CryptoPayHandler) GetSettings(c *gin.Context) {
	out, err := h.svc.GetAdminSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

func (h *CryptoPayHandler) UpdateSettings(c *gin.Context) {
	var req service.UpdateCryptoPaySettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	out, err := h.svc.UpdateSettings(c.Request.Context(), req, cryptoPayOperator(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

func (h *CryptoPayHandler) ListKeysets(c *gin.Context) {
	out, err := h.svc.ListKeysets(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if out == nil {
		out = []service.CryptoPayKeysetInfo{}
	}
	response.Success(c, out)
}

type importEVMKeysetRequest struct {
	Network string `json:"network" binding:"required"`
	Xpub    string `json:"xpub" binding:"required"`
}

func (h *CryptoPayHandler) ImportEVMKeyset(c *gin.Context) {
	var req importEVMKeysetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	out, err := h.svc.ImportEVMKeyset(c.Request.Context(), req.Network, req.Xpub, cryptoPayOperator(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, out)
}

type importSolanaKeysetRequest struct {
	// Manifest is the signed manifest, either as a JSON object or as a string
	// containing the JSON document.
	Manifest json.RawMessage `json:"manifest" binding:"required"`
}

func (h *CryptoPayHandler) ImportSolanaKeyset(c *gin.Context) {
	var req importSolanaKeysetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	raw := bytes.TrimSpace(req.Manifest)
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			response.BadRequest(c, "Invalid manifest")
			return
		}
		raw = []byte(s)
	}
	if len(raw) == 0 {
		response.BadRequest(c, "Invalid manifest")
		return
	}
	out, err := h.svc.ImportSolanaKeyset(c.Request.Context(), raw, cryptoPayOperator(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, out)
}

func (h *CryptoPayHandler) ListDeposits(c *gin.Context) {
	page, pageSize := cryptoPayAdminPagination(c)
	f := service.CryptoPayAdminDepositFilter{Status: c.Query("status"), Network: c.Query("network"), Hold: c.Query("hold")}
	if raw := c.Query("user_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid user_id")
			return
		}
		f.UserID = id
	}
	items, total, err := h.svc.AdminListDeposits(c.Request.Context(), f, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if items == nil {
		items = []service.CryptoPayAdminDeposit{}
	}
	response.Paginated(c, items, total, page, pageSize)
}

func (h *CryptoPayHandler) ReleaseDeposit(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid deposit ID")
		return
	}
	out, err := h.svc.ReleaseDeposit(c.Request.Context(), id, cryptoPayOperator(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

type cryptoPayRescanRequest struct {
	Network string `json:"network" binding:"required"`
	TxHash  string `json:"tx_hash" binding:"required"`
}

func (h *CryptoPayHandler) Rescan(c *gin.Context) {
	var req cryptoPayRescanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	out, err := h.svc.RescanTransaction(c.Request.Context(), req.Network, req.TxHash, cryptoPayOperator(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

func (h *CryptoPayHandler) Reconciliation(c *gin.Context) {
	out, err := h.svc.Reconciliation(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}
