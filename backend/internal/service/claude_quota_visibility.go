package service

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Subscription meters belong to the selected upstream account. Never expose
// them through a generic header allowlist or a cached group permission.
func (s *GatewayService) writeClaudeQuotaHeaders(ctx context.Context, c *gin.Context, account *Account, src http.Header) {
	dst := c.Writer.Header()
	for name := range dst {
		if strings.HasPrefix(strings.ToLower(name), "anthropic-ratelimit-unified-") {
			delete(dst, name)
		}
	}
	if account == nil || !account.IsAnthropicOAuthOrSetupToken() || s.groupRepo == nil || s.userRepo == nil {
		return
	}
	value, _ := c.Get("api_key")
	key, _ := value.(*APIKey)
	if key == nil || key.GroupID == nil || key.UserID <= 0 || !slices.Contains(account.GroupIDs, *key.GroupID) {
		return
	}
	// Re-read authorization so changing a group to public or revoking access also
	// takes effect when the request's API-key authentication snapshot is cached.
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	group, err := s.groupRepo.GetByIDLite(readCtx, *key.GroupID)
	if err != nil || group == nil || !group.IsExclusive || !group.ShowAccountQuota {
		return
	}
	user, err := s.userRepo.GetByID(readCtx, key.UserID)
	if err != nil || !canViewHostedAccounts(user, group) {
		return
	}
	for _, suffix := range []string{"status", "reset", "5h-status", "5h-utilization", "5h-reset", "5h-surpassed-threshold", "7d-status", "7d-utilization", "7d-reset", "7d-surpassed-threshold"} {
		name := "anthropic-ratelimit-unified-" + suffix
		if value := src.Get(name); value != "" {
			dst.Set(name, value)
		}
	}
}
