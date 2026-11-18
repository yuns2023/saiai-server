package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// HostedSelfOnboardHandler serves the user side of hosted self-onboarding
// and the owner's pause / resume / withdraw actions. Responses use explicit
// DTOs only; the upstream token never leaves the server.
type HostedSelfOnboardHandler struct {
	service *service.HostedSelfOnboardService
}

func NewHostedSelfOnboardHandler(s *service.HostedSelfOnboardService) *HostedSelfOnboardHandler {
	return &HostedSelfOnboardHandler{service: s}
}

func (h *HostedSelfOnboardHandler) userID(c *gin.Context) (int64, bool) {
	c.Header("Cache-Control", "no-store")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	return subject.UserID, true
}

// Config handles GET /api/v1/hosted/self-onboard/config.
func (h *HostedSelfOnboardHandler) Config(c *gin.Context) {
	if _, ok := h.userID(c); !ok {
		return
	}
	cfg, err := h.service.Config(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// CreateAuthURL handles POST /api/v1/hosted/applications/auth-url.
func (h *HostedSelfOnboardHandler) CreateAuthURL(c *gin.Context) {
	userID, ok := h.userID(c)
	if !ok {
		return
	}
	out, err := h.service.CreateAuthURL(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

type hostedProxyRequest struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type hostedSubmitRequest struct {
	SessionID   string              `json:"session_id"`
	Code        string              `json:"code"`
	DisplayName string              `json:"display_name"`
	Proxy       *hostedProxyRequest `json:"proxy"`
}

// Submit handles POST /api/v1/hosted/applications.
func (h *HostedSelfOnboardHandler) Submit(c *gin.Context) {
	userID, ok := h.userID(c)
	if !ok {
		return
	}
	var req hostedSubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, service.ErrSelfOnboardInvalidInput)
		return
	}
	in := service.HostedSubmitInput{SessionID: req.SessionID, Code: req.Code, DisplayName: req.DisplayName}
	if req.Proxy != nil {
		in.Proxy = &service.HostedProxyInput{
			Protocol: req.Proxy.Protocol,
			Host:     req.Proxy.Host,
			Port:     req.Proxy.Port,
			Username: req.Proxy.Username,
			Password: req.Proxy.Password,
		}
	}
	app, err := h.service.Submit(c.Request.Context(), userID, in)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.HostedApplicationFromService(app))
}

// List handles GET /api/v1/hosted/applications (own, newest first).
func (h *HostedSelfOnboardHandler) List(c *gin.Context) {
	userID, ok := h.userID(c)
	if !ok {
		return
	}
	items, err := h.service.ListMine(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.HostedApplicationListFromService(items))
}

// Cancel handles POST /api/v1/hosted/applications/:id/cancel.
func (h *HostedSelfOnboardHandler) Cancel(c *gin.Context) {
	userID, ok := h.userID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, service.ErrSelfOnboardNotFound)
		return
	}
	app, err := h.service.Cancel(c.Request.Context(), userID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.HostedApplicationFromService(app))
}

// PauseOwnedAccount handles POST /api/v1/hosted/owned-accounts/:id/pause.
func (h *HostedSelfOnboardHandler) PauseOwnedAccount(c *gin.Context) {
	h.ownedAccountAction(c, service.HostedOwnerActionPause)
}

// ResumeOwnedAccount handles POST /api/v1/hosted/owned-accounts/:id/resume.
func (h *HostedSelfOnboardHandler) ResumeOwnedAccount(c *gin.Context) {
	h.ownedAccountAction(c, service.HostedOwnerActionResume)
}

// WithdrawOwnedAccount handles POST /api/v1/hosted/owned-accounts/:id/withdraw.
func (h *HostedSelfOnboardHandler) WithdrawOwnedAccount(c *gin.Context) {
	h.ownedAccountAction(c, service.HostedOwnerActionWithdraw)
}

func (h *HostedSelfOnboardHandler) ownedAccountAction(c *gin.Context, action string) {
	userID, ok := h.userID(c)
	if !ok {
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.ErrorFrom(c, service.ErrSelfOnboardNotFound)
		return
	}
	state, err := h.service.SetOwnedAccountState(c.Request.Context(), userID, accountID, action)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
