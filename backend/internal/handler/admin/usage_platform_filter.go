package admin

import (
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var errInvalidUsagePlatformFilter = errors.New("invalid platform, use anthropic/openai/gemini/antigravity/sora")

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
