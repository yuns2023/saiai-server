package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// SettlementHandler serves the read-only shadow settlement reconcile view.
type SettlementHandler struct {
	reconcile *service.SettlementReconcileService
}

func NewSettlementHandler(reconcile *service.SettlementReconcileService) *SettlementHandler {
	return &SettlementHandler{reconcile: reconcile}
}

// Reconcile handles GET /api/v1/admin/settlement/reconcile
// ?from=&to=&coverage_start= (RFC3339) [&page=&page_size=].
func (h *SettlementHandler) Reconcile(c *gin.Context) {
	q := service.SettlementReconcileQuery{}
	var ok bool
	if q.From, ok = parseReconcileTime(c.Query("from")); !ok {
		response.ErrorFrom(c, service.ErrSettlementReconcileInvalidWindow)
		return
	}
	if q.To, ok = parseReconcileTime(c.Query("to")); !ok {
		response.ErrorFrom(c, service.ErrSettlementReconcileInvalidWindow)
		return
	}
	if q.CoverageStart, ok = parseReconcileTime(c.Query("coverage_start")); !ok {
		response.ErrorFrom(c, service.ErrSettlementReconcileCoverage)
		return
	}
	if q.Page, ok = parseReconcileInt(c.Query("page")); !ok {
		response.ErrorFrom(c, service.ErrSettlementReconcileInvalidPage)
		return
	}
	if q.PageSize, ok = parseReconcileInt(c.Query("page_size")); !ok {
		response.ErrorFrom(c, service.ErrSettlementReconcileInvalidPage)
		return
	}
	result, err := h.reconcile.Reconcile(c.Request.Context(), q)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// parseReconcileTime returns the zero time for an empty value (the service
// rejects missing required times).
func parseReconcileTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// parseReconcileInt returns 0 for an empty value (service default).
func parseReconcileInt(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 0, false
	}
	return v, true
}
