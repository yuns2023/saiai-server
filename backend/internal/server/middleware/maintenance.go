package middleware

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MaintenanceMode rejects new business/API requests while a deployment is in
// progress. The public settings endpoint and admin settings endpoint remain
// reachable so the UI can render the message and an administrator can recover
// the service without restarting it.
func MaintenanceMode(settingService *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if !isMaintenanceBusinessPath(path) || isMaintenanceAllowedPath(path) {
			c.Next()
			return
		}

		status, err := settingService.GetMaintenanceStatus(c.Request.Context())
		if err != nil || !status.Enabled {
			// A settings read failure must not turn a healthy deployment into a
			// self-inflicted outage. The request's normal handler will report any
			// real dependency failure.
			c.Next()
			return
		}

		c.Header("Retry-After", "60")
		response.ErrorWithDetails(
			c,
			http.StatusServiceUnavailable,
			status.Message,
			"MAINTENANCE",
			map[string]string{"retry_after_seconds": "60"},
		)
		c.Abort()
	}
}

func isMaintenanceBusinessPath(path string) bool {
	return strings.HasPrefix(path, "/api/") ||
		strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/v1beta/") ||
		strings.HasPrefix(path, "/sora/") ||
		strings.HasPrefix(path, "/antigravity/") ||
		path == "/responses" ||
		strings.HasPrefix(path, "/responses/")
}

func isMaintenanceAllowedPath(path string) bool {
	return path == "/api/event_logging/batch" ||
		path == "/api/v1/settings/public" ||
		(path == "/api/v1/admin/settings" || strings.HasPrefix(path, "/api/v1/admin/settings/"))
}
