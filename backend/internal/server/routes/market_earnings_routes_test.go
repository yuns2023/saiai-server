package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The market earnings admin endpoints (overview, ledger, reverse) sit behind
// the admin auth middleware.
func TestMarketEarningsAdminRoutesUseAdminAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	denied := 0
	RegisterAdminRoutes(v1, &handler.Handlers{Admin: &handler.AdminHandlers{}}, func(c *gin.Context) {
		denied++
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/admin/market/earnings", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/admin/market/earnings/entries", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/admin/market/earnings/reverse", nil),
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, req.URL.Path)
	}
	require.Equal(t, 3, denied)
}
