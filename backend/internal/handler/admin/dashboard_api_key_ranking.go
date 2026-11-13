package admin

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/gin-gonic/gin"
)

var dashboardAPIKeysRankingCache = newSnapshotCache(30 * time.Second)

// GetAPIKeySpendingRanking serves global or user-scoped Key spending rankings.
// GET /api/v1/admin/dashboard/api-keys-ranking
func (h *DashboardHandler) GetAPIKeySpendingRanking(c *gin.Context) {
	startTime, endTime, err := parseTimeRange(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	var userID int64
	if raw, provided := c.GetQuery("user_id"); provided {
		userID, err = strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || userID <= 0 {
			response.BadRequest(c, "user_id must be a positive integer")
			return
		}
	}
	limit := parseRankingLimit(c.DefaultQuery("limit", "12"))
	sort, ok := parseRankingSort(c)
	if !ok {
		return
	}
	// Keep the zone offset: start_date/end_date in the cached payload derive from it.
	keyRaw, _ := json.Marshal(struct {
		Start  string `json:"start"`
		End    string `json:"end"`
		UserID int64  `json:"user_id"`
		Limit  int    `json:"limit"`
		Sort   string `json:"sort"`
	}{startTime.Format(time.RFC3339Nano), endTime.Format(time.RFC3339Nano), userID, limit, string(sort)})

	cached, hit, err := dashboardAPIKeysRankingCache.GetOrLoad(string(keyRaw), func() (any, error) {
		ranking, err := h.dashboardService.GetAPIKeySpendingRanking(c.Request.Context(), startTime, endTime, userID, limit, sort)
		if err != nil {
			return nil, err
		}
		return withRevenueTotals(gin.H{
			"ranking":           ranking.Ranking,
			"total_cost":        ranking.TotalCost,
			"total_actual_cost": ranking.TotalActualCost,
			"total_requests":    ranking.TotalRequests,
			"total_tokens":      ranking.TotalTokens,
			"start_date":        startTime.Format("2006-01-02"),
			"end_date":          inclusiveRangeEndDate(endTime),
		}, ranking.TotalSubscriptionRevenueStats), nil
	})
	if err != nil {
		response.Error(c, 500, "Failed to get API Key spending ranking")
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, cached.Payload)
}

// withRevenueTotals adds the scope-wide (pre-LIMIT) revenue totals to a ranking
// payload. The payloads are built by hand, so new response fields must be
// copied explicitly; the admin UI derives the total and the Others row from
// them.
func withRevenueTotals(payload gin.H, totals usagestats.TotalSubscriptionRevenueStats) gin.H {
	payload["total_subscription_revenue"] = totals.TotalSubscriptionRevenue
	payload["total_subscription_revenue_unpriced_cost"] = totals.TotalSubscriptionRevenueUnpricedCost
	payload["total_balance_cost"] = totals.TotalBalanceCost
	payload["total_subscription_cost"] = totals.TotalSubscriptionCost
	payload["total_unclassified_cost"] = totals.TotalUnclassifiedCost
	return payload
}
