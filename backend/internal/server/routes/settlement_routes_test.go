package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettlementReconcileRegisteredUnderAdminAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	allow := false
	authCalls := 0
	group := router.Group("/api/v1/admin", func(c *gin.Context) {
		authCalls++
		if !allow {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	})
	registerSettlementRoutes(group, &handler.Handlers{
		Admin: &handler.AdminHandlers{Settlement: admin.NewSettlementHandler(service.NewSettlementReconcileService(nil, nil))},
	})
	path := "/api/v1/admin/settlement/reconcile"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusForbidden, w.Code)

	allow = true
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_RECONCILE_WINDOW")
	require.Equal(t, 2, authCalls)
}

// The production admin group installs the admin auth middleware before any
// settlement route.
func TestSettlementReconcileUsesAdminAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	denied := 0
	RegisterAdminRoutes(v1, &handler.Handlers{Admin: &handler.AdminHandlers{}}, func(c *gin.Context) {
		denied++
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/settlement/reconcile", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, 1, denied)
}
