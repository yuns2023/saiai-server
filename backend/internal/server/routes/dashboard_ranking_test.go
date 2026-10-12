package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeySpendingRankingRegisteredUnderAdminGroup(t *testing.T) {
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
	registerDashboardRoutes(group, &handler.Handlers{
		Admin: &handler.AdminHandlers{Dashboard: admin.NewDashboardHandler(nil, nil)},
	})
	path := "/api/v1/admin/dashboard/api-keys-ranking?user_id=-1"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusForbidden, w.Code)

	allow = true
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "user_id must be a positive integer")
	require.Equal(t, 2, authCalls)
}
