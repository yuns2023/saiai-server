package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ClientBrand takes the site's client brand snapshot once per request and
// stores it in the request context, so a stream or WebSocket session words
// every client-facing text with the same brand and per-frame code never reads
// settings. Unbranded sites attach nothing: readers fall back to the zero
// value, which is the original wording.
func ClientBrand(settingService *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if settingService != nil && c.Request != nil {
			if brand := settingService.ClientBrand(c.Request.Context()); brand.Customized() {
				c.Request = c.Request.WithContext(service.WithClientBrand(c.Request.Context(), brand))
			}
		}
		c.Next()
	}
}
