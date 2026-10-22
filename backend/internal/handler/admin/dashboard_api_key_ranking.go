package admin

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
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
	// Keep the zone offset: start_date/end_date in the cached payload derive from it.
	keyRaw, _ := json.Marshal(struct {
		Start  string `json:"start"`
		End    string `json:"end"`
		UserID int64  `json:"user_id"`
		Limit  int    `json:"limit"`
	}{startTime.Format(time.RFC3339Nano), endTime.Format(time.RFC3339Nano), userID, limit})

	cached, hit, err := dashboardAPIKeysRankingCache.GetOrLoad(string(keyRaw), func() (any, error) {
		ranking, err := h.dashboardService.GetAPIKeySpendingRanking(c.Request.Context(), startTime, endTime, userID, limit)
		if err != nil {
			return nil, err
		}
		return gin.H{
			"ranking":           ranking.Ranking,
			"total_actual_cost": ranking.TotalActualCost,
			"total_requests":    ranking.TotalRequests,
			"total_tokens":      ranking.TotalTokens,
			"start_date":        startTime.Format("2006-01-02"),
			"end_date":          inclusiveRangeEndDate(endTime),
		}, nil
	})
	if err != nil {
		response.Error(c, 500, "Failed to get API Key spending ranking")
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, cached.Payload)
}
