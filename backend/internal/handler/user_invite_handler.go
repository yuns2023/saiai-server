package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// maxUserInvitePageSize bounds one page of a user's own invitation codes.
const maxUserInvitePageSize = 50

// UserInviteHandler serves the user console's "invite" page.
type UserInviteHandler struct {
	invites *service.UserInviteService
}

func NewUserInviteHandler(invites *service.UserInviteService) *UserInviteHandler {
	return &UserInviteHandler{invites: invites}
}

// Overview GET /api/v1/invites?page=&page_size=
func (h *UserInviteHandler) Overview(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	page, pageSize := response.ParsePagination(c)
	if pageSize > maxUserInvitePageSize {
		pageSize = maxUserInvitePageSize
	}
	out, err := h.invites.Overview(c.Request.Context(), subject.UserID, pagination.PaginationParams{Page: page, PageSize: pageSize})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

// Create POST /api/v1/invites — the owner is always the signed-in user; the body is ignored.
func (h *UserInviteHandler) Create(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	code, err := h.invites.Create(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, code)
}

// Revoke DELETE /api/v1/invites/:code — only the signed-in user's own unused codes.
func (h *UserInviteHandler) Revoke(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if err := h.invites.Revoke(c.Request.Context(), subject.UserID, c.Param("code")); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"revoked": true})
}
