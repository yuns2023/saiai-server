package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type maintenanceSettingRepo struct {
	values map[string]string
}

func (r *maintenanceSettingRepo) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (r *maintenanceSettingRepo) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}
func (r *maintenanceSettingRepo) Set(context.Context, string, string) error { return nil }
func (r *maintenanceSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = r.values[key]
	}
	return out, nil
}
func (r *maintenanceSettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (r *maintenanceSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}
func (r *maintenanceSettingRepo) Delete(context.Context, string) error { return nil }

func TestMaintenanceModeBlocksBusinessRequestsAndKeepsRecoveryRoutes(t *testing.T) {
	repo := &maintenanceSettingRepo{values: map[string]string{
		service.SettingKeyMaintenanceModeEnabled: "true",
		service.SettingKeyMaintenanceMessage:     "系统升级中，请稍候。",
	}}
	svc := service.NewSettingService(repo, &config.Config{})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MaintenanceMode(svc))
	for _, path := range []string{
		"/api/v1/messages",
		"/v1/messages",
		"/v1beta/messages",
		"/sora/v1/generations",
		"/antigravity/v1/messages",
		"/responses",
		"/responses/compact",
	} {
		r.Any(path, func(c *gin.Context) { c.Status(http.StatusOK) })
	}
	r.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/api/v1/settings/public", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/api/v1/admin/settings", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, path := range []string{
		"/api/v1/messages",
		"/v1/messages",
		"/v1beta/messages",
		"/sora/v1/generations",
		"/antigravity/v1/messages",
		"/responses",
		"/responses/compact",
	} {
		blocked := httptest.NewRecorder()
		r.ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, path, nil))
		if blocked.Code != http.StatusServiceUnavailable {
			t.Errorf("business request %s status=%d, want 503", path, blocked.Code)
			continue
		}
		if blocked.Header().Get("Retry-After") != "60" {
			t.Errorf("business request %s Retry-After=%q, want 60", path, blocked.Header().Get("Retry-After"))
		}
		var body struct {
			Message string `json:"message"`
			Reason  string `json:"reason"`
			Meta    struct {
				RetryAfterSeconds string `json:"retry_after_seconds"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(blocked.Body.Bytes(), &body); err != nil {
			t.Errorf("business request %s response is not JSON: %v", path, err)
			continue
		}
		if body.Message != "系统升级中，请稍候。" || body.Reason != "MAINTENANCE" || body.Meta.RetryAfterSeconds != "60" {
			t.Errorf("business request %s body=%s, want maintenance contract", path, blocked.Body.String())
		}
	}

	for _, path := range []string{"/health", "/api/v1/settings/public", "/api/v1/admin/settings"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("recovery path %s status=%d, want 200", path, rec.Code)
		}
	}
}
