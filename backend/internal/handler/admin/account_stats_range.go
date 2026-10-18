package admin

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/gin-gonic/gin"
)

// Explicit ranges are half-open. Retain days for existing API clients.
func parseAccountStatsRange(c *gin.Context, now time.Time) (time.Time, time.Time, error) {
	loc := timezone.Location()
	if name := c.Query("timezone"); name != "" {
		var err error
		loc, err = time.LoadLocation(name)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid timezone")
		}
	}
	startRaw, endRaw := c.Query("start_time"), c.Query("end_time")
	if startRaw != "" || endRaw != "" {
		start, errStart := time.Parse(time.RFC3339, startRaw)
		end, errEnd := time.Parse(time.RFC3339, endRaw)
		if errStart != nil || errEnd != nil || !start.Before(end) {
			return time.Time{}, time.Time{}, fmt.Errorf("start_time and end_time must be valid RFC3339 timestamps with start_time before end_time")
		}
		if end.Sub(start) > 90*24*time.Hour {
			return time.Time{}, time.Time{}, fmt.Errorf("range cannot exceed 90 days")
		}
		return start.In(loc), end.In(loc), nil
	}
	days := 30
	if raw := c.Query("days"); raw != "" {
		d, err := strconv.Atoi(raw)
		if err != nil || d < 1 || d > 90 {
			return time.Time{}, time.Time{}, fmt.Errorf("days must be between 1 and 90")
		}
		days = d
	}
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	return today.AddDate(0, 0, -days+1), today.AddDate(0, 0, 1), nil
}
