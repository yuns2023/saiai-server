package admin

import (
	"fmt"
	"strings"
)

const minSessionIDPrefixLength = 6

func normalizeSessionIDFilter(raw string) (string, error) {
	sessionID := strings.TrimSpace(raw)
	if sessionID == "" {
		return "", nil
	}
	if len(sessionID) < minSessionIDPrefixLength {
		return "", fmt.Errorf("session_id prefix must be at least %d characters", minSessionIDPrefixLength)
	}
	return sessionID, nil
}
