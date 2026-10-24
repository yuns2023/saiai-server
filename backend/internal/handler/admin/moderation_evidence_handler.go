package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ModerationEvidenceHandler serves encrypted moderation evidence. Every
// endpoint requires a personal administrator JWT; the shared admin API key
// cannot attribute access to a person and is rejected.
type ModerationEvidenceHandler struct {
	evidenceService *service.InputModerationEvidenceService
}

func NewModerationEvidenceHandler(evidenceService *service.InputModerationEvidenceService) *ModerationEvidenceHandler {
	return &ModerationEvidenceHandler{evidenceService: evidenceService}
}

func moderationEvidenceActor(c *gin.Context) (service.InputModerationEvidenceActor, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return service.InputModerationEvidenceActor{}, false
	}
	method, _ := c.Get("auth_method")
	actor := service.InputModerationEvidenceActor{UserID: subject.UserID}
	actor.AuthMethod, _ = method.(string)
	if actor.AuthMethod != service.ModerationEvidenceAuthJWT {
		response.ErrorFrom(c, service.ErrModerationEvidenceForbidden)
		return service.InputModerationEvidenceActor{}, false
	}
	return actor, true
}

func moderationEvidenceEventID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid event id")
		return 0, false
	}
	return id, true
}

// View GET /api/v1/admin/usage/input-moderation-events/:id/evidence
func (h *ModerationEvidenceHandler) View(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	actor, ok := moderationEvidenceActor(c)
	if !ok {
		return
	}
	eventID, ok := moderationEvidenceEventID(c)
	if !ok {
		return
	}
	view, err := h.evidenceService.View(c.Request.Context(), eventID, actor)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

type moderationEvidenceHoldRequest struct {
	Days   int    `json:"days"`
	Reason string `json:"reason"`
}

// Hold POST /api/v1/admin/usage/input-moderation-events/:id/evidence/hold
func (h *ModerationEvidenceHandler) Hold(c *gin.Context) {
	actor, ok := moderationEvidenceActor(c)
	if !ok {
		return
	}
	eventID, ok := moderationEvidenceEventID(c)
	if !ok {
		return
	}
	var req moderationEvidenceHoldRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request")
		return
	}
	until, err := h.evidenceService.Hold(c.Request.Context(), eventID, actor, req.Days, req.Reason)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"event_id": eventID, "hold_until": until})
}

type moderationEvidenceReleaseRequest struct {
	Reason string `json:"reason"`
}

// Release POST /api/v1/admin/usage/input-moderation-events/:id/evidence/release
func (h *ModerationEvidenceHandler) Release(c *gin.Context) {
	actor, ok := moderationEvidenceActor(c)
	if !ok {
		return
	}
	eventID, ok := moderationEvidenceEventID(c)
	if !ok {
		return
	}
	var req moderationEvidenceReleaseRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BadRequest(c, "Invalid request")
			return
		}
	}
	if err := h.evidenceService.Release(c.Request.Context(), eventID, actor, req.Reason); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"event_id": eventID})
}

// ListHeld GET /api/v1/admin/usage/input-moderation-evidence?held=1
func (h *ModerationEvidenceHandler) ListHeld(c *gin.Context) {
	actor, ok := moderationEvidenceActor(c)
	if !ok {
		return
	}
	if c.Query("held") != "1" {
		response.Error(c, http.StatusBadRequest, "Only held=1 is supported")
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.evidenceService.ListHeld(c.Request.Context(), actor, page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, size)
}
