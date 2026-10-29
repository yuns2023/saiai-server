package handler

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const (
	// usageAnalyticsMaxDays matches the default raw usage-log retention.
	usageAnalyticsMaxDays         = 90
	usageAnalyticsDefaultPageSize = 50
	usageAnalyticsMaxPageSize     = 1000  // an export of the heaviest user's Key x model groups fits one page
	usageAnalyticsMaxPage         = 10000 // keeps OFFSET far from integer overflow
	usageAnalyticsMaxModelLen     = 200
	usageAnalyticsMaxEffortLen    = 20 // usage_logs.reasoning_effort is VARCHAR(20)
)

// usageAnalyticsRange is an inclusive calendar-date range in one location,
// converted to the half-open instant range [Start, End).
type usageAnalyticsRange struct {
	Location  *time.Location
	StartDate string
	EndDate   string
	Start     time.Time
	End       time.Time
}

// parseUsageAnalyticsRange is the strict counterpart of parseUserTimeRange:
// invalid timezones or dates are rejected instead of silently replaced, and
// both dates default to today in the selected timezone.
func parseUsageAnalyticsRange(c *gin.Context) (usageAnalyticsRange, error) {
	loc := timezone.Location()
	if tz := strings.TrimSpace(c.Query("timezone")); tz != "" {
		userLoc, err := time.LoadLocation(tz)
		if err != nil {
			return usageAnalyticsRange{}, errors.New("invalid timezone")
		}
		loc = userLoc
	}
	today := time.Now().In(loc).Format("2006-01-02")
	startDate := strings.TrimSpace(c.DefaultQuery("start_date", today))
	endDate := strings.TrimSpace(c.DefaultQuery("end_date", today))
	// Parse the calendar dates themselves (in UTC) and derive each local
	// boundary from them, so DST changes neither the day count nor the range.
	startDay, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return usageAnalyticsRange{}, errors.New("invalid start_date, use YYYY-MM-DD")
	}
	endDay, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return usageAnalyticsRange{}, errors.New("invalid end_date, use YYYY-MM-DD")
	}
	if endDay.Before(startDay) {
		return usageAnalyticsRange{}, errors.New("end_date must not be before start_date")
	}
	if int(endDay.Sub(startDay).Hours()/24)+1 > usageAnalyticsMaxDays {
		return usageAnalyticsRange{}, errors.New("date range must not exceed 90 days")
	}
	return usageAnalyticsRange{
		Location:  loc,
		StartDate: startDate,
		EndDate:   endDate,
		Start:     startOfLocalDay(startDay, loc),
		End:       startOfLocalDay(endDay.AddDate(0, 0, 1), loc),
	}, nil
}

// startOfLocalDay returns the first instant of the calendar day in loc. Where
// a DST change skips local midnight, time.Date normalizes 00:00 into the
// previous day; the day then starts at the end of that zone period.
func startOfLocalDay(day time.Time, loc *time.Location) time.Time {
	t := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	if t.Day() != day.Day() {
		if _, end := t.ZoneBounds(); !end.IsZero() {
			return end
		}
	}
	return t
}

func parseUsageAnalyticsPositiveInt(c *gin.Context, name string, def, max int) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || (max > 0 && v > max) {
		if max > 0 {
			return 0, errors.New("invalid " + name + ", use 1-" + strconv.Itoa(max))
		}
		return 0, errors.New("invalid " + name)
	}
	return v, nil
}

// parseUsageAnalyticsQuery validates every analytics parameter. Unknown
// enumeration values are rejected, never mapped to a default.
func parseUsageAnalyticsQuery(c *gin.Context, userID int64) (usagestats.UsageAnalyticsQuery, usageAnalyticsRange, error) {
	q := usagestats.UsageAnalyticsQuery{UserID: userID}
	rng, err := parseUsageAnalyticsRange(c)
	if err != nil {
		return q, rng, err
	}
	q.StartTime, q.EndTime = rng.Start, rng.End

	q.GroupBy = strings.TrimSpace(c.DefaultQuery("group_by", usagestats.UsageAnalyticsByModel))
	switch q.GroupBy {
	case usagestats.UsageAnalyticsByModel, usagestats.UsageAnalyticsByReasoningEffort,
		usagestats.UsageAnalyticsByAPIKey, usagestats.UsageAnalyticsByAPIKeyModel:
	default:
		return q, rng, errors.New("invalid group_by, use model, reasoning_effort, api_key or api_key_model")
	}
	q.Sort = strings.TrimSpace(c.DefaultQuery("sort", usagestats.UsageAnalyticsSortActualCost))
	switch q.Sort {
	case usagestats.UsageAnalyticsSortActualCost, usagestats.UsageAnalyticsSortCost,
		usagestats.UsageAnalyticsSortUsageCost, usagestats.UsageAnalyticsSortTokens,
		usagestats.UsageAnalyticsSortRequests:
	default:
		return q, rng, errors.New("invalid sort value")
	}
	if q.Page, err = parseUsageAnalyticsPositiveInt(c, "page", 1, usageAnalyticsMaxPage); err != nil {
		return q, rng, err
	}
	if q.PageSize, err = parseUsageAnalyticsPositiveInt(c, "page_size", usageAnalyticsDefaultPageSize, usageAnalyticsMaxPageSize); err != nil {
		return q, rng, err
	}

	// A Key from another user cannot leak anything: the query is always
	// restricted to the caller's records, so it simply matches nothing. Keys
	// deleted since remain selectable for their historical usage.
	if raw := strings.TrimSpace(c.Query("api_key_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return q, rng, errors.New("invalid api_key_id")
		}
		q.APIKeyID = id
	}
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return q, rng, errors.New("invalid group_id")
		}
		q.GroupID = id
	}
	q.Model = strings.TrimSpace(c.Query("model"))
	if len(q.Model) > usageAnalyticsMaxModelLen {
		return q, rng, errors.New("invalid model")
	}
	q.ReasoningEffort = strings.ToLower(strings.TrimSpace(c.Query("reasoning_effort")))
	if len(q.ReasoningEffort) > usageAnalyticsMaxEffortLen {
		return q, rng, errors.New("invalid reasoning_effort")
	}
	if raw := strings.TrimSpace(c.Query("reasoning_effort_missing")); raw != "" {
		missing, err := strconv.ParseBool(raw)
		if err != nil {
			return q, rng, errors.New("invalid reasoning_effort_missing, use true or false")
		}
		q.ReasoningEffortMissing = missing
	}
	if q.ReasoningEffortMissing && q.ReasoningEffort != "" {
		return q, rng, errors.New("reasoning_effort and reasoning_effort_missing cannot be combined")
	}
	if raw := strings.TrimSpace(c.Query("request_type")); raw != "" {
		rt, err := service.ParseUsageRequestType(raw)
		if err != nil {
			return q, rng, err
		}
		v := int16(rt)
		q.RequestType = &v
	}
	if raw := strings.TrimSpace(c.Query("billing_type")); raw != "" {
		var bt int8
		switch raw {
		case "0":
			bt = service.BillingTypeBalance
		case "1":
			bt = service.BillingTypeSubscription
		default:
			return q, rng, errors.New("invalid billing_type, use 0 (balance) or 1 (subscription)")
		}
		q.BillingType = &bt
	}
	return q, rng, nil
}

// Analytics aggregates the current user's usage by model, reasoning effort,
// API Key or API Key x model, with shares of the whole filtered result.
// GET /api/v1/usage/analytics
func (h *UsageHandler) Analytics(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	q, rng, err := parseUsageAnalyticsQuery(c, subject.UserID)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.usageService.GetUserUsageAnalytics(c.Request.Context(), q)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	pages := int((result.Total + int64(q.PageSize) - 1) / int64(q.PageSize))
	if pages < 1 {
		pages = 1
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{
		"group_by":      q.GroupBy,
		"sort":          q.Sort,
		"start_date":    rng.StartDate,
		"end_date":      rng.EndDate,
		"timezone":      rng.Location.String(),
		"start":         rng.Start.Format(time.RFC3339),
		"end_exclusive": rng.End.Format(time.RFC3339),
		"items":         result.Items,
		"total":         result.Total,
		"page":          q.Page,
		"page_size":     q.PageSize,
		"pages":         pages,
		"summary":       result.Summary,
	})
}
