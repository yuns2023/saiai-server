package handler

import (
	"errors"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// advanceSmartSwitch is only called before any downstream response is committed.
// One shared exclusion set bounds attempts and prevents group cycles.
func advanceSmartSwitch(c *gin.Context, current **service.APIKey, subscription **service.UserSubscription, excluded map[int64]bool, billing *service.BillingCacheService) bool {
	if c.Writer.Written() || c.Request.Context().Err() != nil || current == nil || *current == nil {
		return false
	}
	raw, _ := c.Get("smart_switch_next")
	next, ok := raw.(middleware.SmartSwitchNext)
	if !ok || next == nil {
		return false
	}
	if (*current).GroupID != nil {
		excluded[*(*current).GroupID] = true
	}
	for len(excluded) < 10 {
		selected, err := next(excluded)
		if err != nil {
			return false
		}
		if err := billing.CheckBillingEligibility(c.Request.Context(), selected.Key.User, selected.Key, selected.Key.Group, selected.Subscription); err != nil {
			if errors.Is(err, service.ErrInsufficientBalance) || errors.Is(err, service.ErrFiveHourLimitExceeded) || errors.Is(err, service.ErrDailyLimitExceeded) || errors.Is(err, service.ErrWeeklyLimitExceeded) || errors.Is(err, service.ErrMonthlyLimitExceeded) {
				excluded[*selected.Key.GroupID] = true
				continue
			}
			return false
		}
		*current = selected.Key
		*subscription = selected.Subscription
		middleware.SetSmartSwitchSelection(c, selected)
		return true
	}
	return false
}
