package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type HostedAccountHandler struct{ service *service.HostedAccountService }

func NewHostedAccountHandler(s *service.HostedAccountService) *HostedAccountHandler {
	return &HostedAccountHandler{service: s}
}

func (h *HostedAccountHandler) ListGroups(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	groups, err := h.service.ListGroups(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, groups)
}

func (h *HostedAccountHandler) ListAccounts(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	page, size := response.ParsePagination(c)
	if size > 100 {
		size = 100
	}
	accounts, result, err := h.service.ListAccounts(c.Request.Context(), subject.UserID, groupID, pagination.PaginationParams{Page: page, PageSize: size})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.PaginatedWithResult(c, accounts, &response.PaginationResult{Total: result.Total, Page: result.Page, PageSize: result.PageSize, Pages: result.Pages})
}
