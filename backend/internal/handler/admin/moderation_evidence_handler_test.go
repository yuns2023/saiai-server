package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestModerationEvidenceRejectsSharedAdminKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, method := range []string{"admin_api_key", ""} {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
			if method != "" {
				c.Set("auth_method", method)
			}
			c.Next()
		})
		h := NewModerationEvidenceHandler(service.NewInputModerationEvidenceService(nil, &config.Config{}))
		router.GET("/evidence/:id", h.View)
		router.POST("/evidence/:id/hold", h.Hold)
		router.GET("/held", h.ListHeld)
		for _, req := range []*http.Request{
			httptest.NewRequest(http.MethodGet, "/evidence/5", nil),
			httptest.NewRequest(http.MethodPost, "/evidence/5/hold", nil),
			httptest.NewRequest(http.MethodGet, "/held?held=1", nil),
		} {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			require.Equal(t, http.StatusForbidden, recorder.Code, req.URL.Path)
		}
	}
}

func TestModerationEvidenceViewIsNotCached(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Set("auth_method", "jwt")
		c.Next()
	})
	h := NewModerationEvidenceHandler(service.NewInputModerationEvidenceService(nil, &config.Config{}))
	router.GET("/evidence/:id", h.View)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/evidence/5", nil))
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}
