package admin

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// HostedApplicationHandler serves the admin review of hosting applications.
// Explicit DTOs only: never credentials or the proxy password.
type HostedApplicationHandler struct {
	service *service.HostedSelfOnboardService
}

func NewHostedApplicationHandler(s *service.HostedSelfOnboardService) *HostedApplicationHandler {
	return &HostedApplicationHandler{service: s}
}

func adminSubjectID(c *gin.Context) int64 {
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		return subject.UserID
	}
	return 0
}

func parseHostedApplicationID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, service.ErrSelfOnboardNotFound)
		return 0, false
	}
	return id, true
}

// List handles GET /api/v1/admin/hosted-applications?status=&page=&page_size=.
func (h *HostedApplicationHandler) List(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	page, pageSize := response.ParsePagination(c)
	pageSize = min(pageSize, 100)
	items, total, err := h.service.ListAdmin(c.Request.Context(), strings.TrimSpace(c.Query("status")), page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]dto.AdminHostedApplication, 0, len(items))
	for i := range items {
		out = append(out, dto.AdminHostedApplicationFromService(&items[i]))
	}
	response.Paginated(c, out, total, page, pageSize)
}

type hostedApproveRequest struct {
	ProxyMode                 string `json:"proxy_mode"`
	ProxyID                   int64  `json:"proxy_id"`
	Concurrency               *int   `json:"concurrency"`
	Priority                  *int   `json:"priority"`
	CarpoolDeviceLimit        *int   `json:"carpool_device_limit"`
	CarpoolAdmissionsPerCycle *int   `json:"carpool_admissions_per_cycle"`
	CarpoolIntervalHours      *int   `json:"carpool_interval_hours"`
}

// Approve handles POST /api/v1/admin/hosted-applications/:id/approve.
// Replaying an approved application returns the stored result.
func (h *HostedApplicationHandler) Approve(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := parseHostedApplicationID(c)
	if !ok {
		return
	}
	var req hostedApproveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, service.ErrSelfOnboardInvalidInput)
		return
	}
	result, err := h.service.Approve(c.Request.Context(), adminSubjectID(c), id, service.HostedApproveRequest{
		ProxyMode:                 strings.TrimSpace(req.ProxyMode),
		ProxyID:                   req.ProxyID,
		Concurrency:               req.Concurrency,
		Priority:                  req.Priority,
		CarpoolDeviceLimit:        req.CarpoolDeviceLimit,
		CarpoolAdmissionsPerCycle: req.CarpoolAdmissionsPerCycle,
		CarpoolIntervalHours:      req.CarpoolIntervalHours,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.AdminHostedApproveResult{
		Application: dto.AdminHostedApplicationFromService(result.Application),
		AccountID:   result.AccountID,
		GroupID:     result.GroupID,
	})
}

type hostedRejectRequest struct {
	Reason string `json:"reason"`
}

// Reject handles POST /api/v1/admin/hosted-applications/:id/reject.
func (h *HostedApplicationHandler) Reject(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := parseHostedApplicationID(c)
	if !ok {
		return
	}
	var req hostedRejectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, service.ErrSelfOnboardInvalidInput)
		return
	}
	app, err := h.service.Reject(c.Request.Context(), adminSubjectID(c), id, req.Reason)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.AdminHostedApplicationFromService(app))
}
