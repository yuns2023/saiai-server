package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// HostedOwnerHandler serves the owner-facing hosted account views.
type HostedOwnerHandler struct{ service *service.HostedOwnerService }

func NewHostedOwnerHandler(s *service.HostedOwnerService) *HostedOwnerHandler {
	return &HostedOwnerHandler{service: s}
}

// ListOwnedAccounts handles GET /api/v1/hosted/owned-accounts.
func (h *HostedOwnerHandler) ListOwnedAccounts(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	list, err := h.service.ListOwnedAccounts(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, list)
}

// ListOwnedAccountUsage handles GET /api/v1/hosted/owned-accounts/:id/usage.
func (h *HostedOwnerHandler) ListOwnedAccountUsage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.ErrorFrom(c, service.ErrHostedOwnedAccountNotFound)
		return
	}
	page, err := h.service.ListOwnedAccountUsage(c.Request.Context(), subject.UserID, accountID, service.HostedOwnerUsageParams{
		Start:  c.Query("start"),
		End:    c.Query("end"),
		Limit:  c.Query("limit"),
		Cursor: c.Query("cursor"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, page)
}
