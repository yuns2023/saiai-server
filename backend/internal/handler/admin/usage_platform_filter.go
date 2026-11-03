package admin

import (
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var (
	errInvalidUsagePlatformFilter  = errors.New("invalid platform, use anthropic/openai/gemini/antigravity/sora")
	errRepeatedUsagePlatformFilter = errors.New("platform must be given at most once")
)

// parseUsagePlatformFilter reads the optional platform query. A repeated
// parameter is rejected (even if one value is empty) so every endpoint
// interprets ?platform=a&platform=b the same way instead of silently using
// the first value.
func parseUsagePlatformFilter(c *gin.Context) (string, error) {
	values := c.QueryArray("platform")
	switch len(values) {
	case 0:
		return "", nil
	case 1:
		return normalizeUsagePlatformFilter(values[0])
	default:
		return "", errRepeatedUsagePlatformFilter
	}
}

// normalizeUsagePlatformFilter validates the optional platform query of the
// admin usage and dashboard endpoints. The value filters by the platform of
// the account that served the request; empty disables the filter. Retired
// platforms stay accepted because their history is still stored.
func normalizeUsagePlatformFilter(raw string) (string, error) {
	platform := strings.ToLower(strings.TrimSpace(raw))
	switch platform {
	case "":
		return "", nil
	case service.PlatformAnthropic, service.PlatformOpenAI, service.PlatformGemini,
		service.PlatformAntigravity, service.PlatformSora:
		return platform, nil
	default:
		return "", errInvalidUsagePlatformFilter
	}
}
