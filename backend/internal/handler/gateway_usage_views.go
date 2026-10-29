package handler

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// GET /v1/usage?view=analytics|records gives the holder of an API Key
// programmatic access to that Key's usage. Without view the endpoint keeps its
// legacy response unchanged.

const (
	usageViewAnalytics = "analytics"
	usageViewRecords   = "records"

	usageRecordsDefaultLimit = 100
	usageRecordsMaxLimit     = 500
	usageRecordsCursorTTL    = 24 * time.Hour
	usageRecordsCursorV1     = 1
	usageRecordsMaxCursorLen = 2048 // a sealed cursor is a few hundred bytes
)

var (
	usageRangeAndFilterParams = []string{
		"start_date", "end_date", "timezone", "group_id", "model",
		"reasoning_effort", "reasoning_effort_missing", "request_type", "billing_type",
	}
	usageAnalyticsViewParams     = append([]string{"view", "group_by", "sort", "page", "page_size"}, usageRangeAndFilterParams...)
	usageRecordsViewParams       = append([]string{"view", "limit", "cursor"}, usageRangeAndFilterParams...)
	usageRecordsCursorViewParams = []string{"view", "limit", "cursor"}
)

// usageViewParamsError rejects unknown or repeated parameters, so a caller
// never believes an ignored parameter (for example api_key_id or records_page)
// took effect.
func usageViewParamsError(c *gin.Context, allowed []string) error {
	allow := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allow[name] = true
	}
	for name, values := range c.Request.URL.Query() {
		if !allow[name] {
			return errors.New("unsupported parameter " + name + " for this view")
		}
		if len(values) > 1 {
			return errors.New("parameter " + name + " must not be repeated")
		}
	}
	return nil
}

// usageView handles the view modes. It reports whether the request was a view
// request; legacy requests (no view parameter) return false untouched.
func (h *GatewayHandler) usageView(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject) bool {
	values, present := c.Request.URL.Query()["view"]
	if !present {
		return false
	}
	if len(values) != 1 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "parameter view must not be repeated")
		return true
	}
	if h.usageService == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "usage data is unavailable")
		return true
	}
	switch values[0] {
	case usageViewAnalytics:
		h.usageAnalyticsView(c, apiKey, subject)
	case usageViewRecords:
		h.usageRecordsView(c, apiKey, subject)
	default:
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "invalid view, use analytics or records")
	}
	return true
}

func (h *GatewayHandler) usageViewServiceError(c *gin.Context, err error, what string) {
	if infraerrors.IsTooManyRequests(err) {
		c.Header("Retry-After", "2")
		h.errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", "too many concurrent usage queries, retry shortly")
		return
	}
	slog.Warn("usage view query failed", "view", what, "error", err)
	h.errorResponse(c, http.StatusInternalServerError, "api_error", "failed to query usage")
}

func (h *GatewayHandler) usageAnalyticsView(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject) {
	if err := usageViewParamsError(c, usageAnalyticsViewParams); err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	q, rng, err := parseUsageAnalyticsQuery(c, subject.UserID)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	q.APIKeyID = apiKey.ID
	result, err := h.usageService.GetUserUsageAnalytics(c.Request.Context(), q)
	if err != nil {
		h.usageViewServiceError(c, err, usageViewAnalytics)
		return
	}
	resp := usageAnalyticsResponse(q, rng, result)
	resp["object"] = "usage.analytics"
	c.JSON(http.StatusOK, resp)
}

// publicUsageRecord is the records-view row: the public whitelist plus the
// normalized recorded reasoning effort (null when none was recorded).
type publicUsageRecord struct {
	publicKeyUsageRecord
	ReasoningEffort *string `json:"reasoning_effort"`
}

// usageRecordsCursor is sealed with AES-GCM: it hides internal record IDs and
// cannot be forged. It carries the whole normalized query, so later pages
// cannot drift (for example a default "today" crossing midnight).
type usageRecordsCursor struct {
	V         int    `json:"v"`
	UserID    int64  `json:"u"`
	APIKeyID  int64  `json:"k"`
	Start     int64  `json:"s"` // unix microseconds
	End       int64  `json:"e"`
	StartDate string `json:"sd"`
	EndDate   string `json:"ed"`
	Timezone  string `json:"tz"`
	GroupID   int64  `json:"g,omitempty"`
	Model     string `json:"m,omitempty"`
	Effort    string `json:"re,omitempty"`
	Missing   bool   `json:"rm,omitempty"`
	RequestT  *int16 `json:"rt,omitempty"`
	BillingT  *int8  `json:"bt,omitempty"`
	LastAt    int64  `json:"t"` // created_at of the last returned record, unix microseconds
	LastID    int64  `json:"i"`
	ExpiresAt int64  `json:"x"`
}

func usageRecordsCursorAEAD(secret string) (cipher.AEAD, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("cursor secret is not configured")
	}
	key := sha256.Sum256([]byte("saiai/v1-usage-records-cursor/v1\x00" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealUsageRecordsCursor(secret string, cur usageRecordsCursor) (string, error) {
	aead, err := usageRecordsCursorAEAD(secret)
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(cur)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, nil)), nil
}

func openUsageRecordsCursor(secret, token string) (usageRecordsCursor, error) {
	var cur usageRecordsCursor
	aead, err := usageRecordsCursorAEAD(secret)
	if err != nil {
		return cur, err
	}
	if len(token) > usageRecordsMaxCursorLen {
		return cur, errors.New("invalid cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < aead.NonceSize() {
		return cur, errors.New("invalid cursor")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return cur, errors.New("invalid cursor")
	}
	if err := json.Unmarshal(plain, &cur); err != nil || cur.V != usageRecordsCursorV1 {
		return cur, errors.New("invalid cursor")
	}
	return cur, nil
}

func (h *GatewayHandler) usageRecordsView(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject) {
	secret := ""
	if h.cfg != nil {
		secret = h.cfg.JWT.Secret
	}
	if strings.TrimSpace(secret) == "" {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "paging is unavailable")
		return
	}
	cursorValues, hasCursor := c.Request.URL.Query()["cursor"]
	token := ""
	allowed := usageRecordsViewParams
	if hasCursor {
		allowed = usageRecordsCursorViewParams
		if len(cursorValues) == 1 {
			token = strings.TrimSpace(cursorValues[0])
		}
	}
	if err := usageViewParamsError(c, allowed); err != nil {
		if hasCursor {
			err = errors.New(err.Error() + "; a cursor already carries the range and filters")
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	limit, err := parseUsageAnalyticsPositiveInt(c, "limit", usageRecordsDefaultLimit, usageRecordsMaxLimit)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	var (
		q     usagestats.UsageAnalyticsQuery
		cur   usageRecordsCursor
		after *usagestats.UsageRecordPosition
	)
	if hasCursor {
		// An empty cursor is an error, not "start over": restarting silently
		// would make an export repeat its first page.
		cur, err = openUsageRecordsCursor(secret, token)
		if err != nil || cur.UserID != subject.UserID || cur.APIKeyID != apiKey.ID {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "invalid cursor")
			return
		}
		if time.Now().Unix() >= cur.ExpiresAt {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "cursor expired, restart the listing")
			return
		}
		q = usagestats.UsageAnalyticsQuery{
			UserID: cur.UserID, APIKeyID: cur.APIKeyID,
			StartTime: time.UnixMicro(cur.Start), EndTime: time.UnixMicro(cur.End),
			GroupID: cur.GroupID, Model: cur.Model, ReasoningEffort: cur.Effort,
			ReasoningEffortMissing: cur.Missing, RequestType: cur.RequestT, BillingType: cur.BillingT,
		}
		after = &usagestats.UsageRecordPosition{CreatedAt: time.UnixMicro(cur.LastAt), ID: cur.LastID}
	} else {
		var rng usageAnalyticsRange
		q, rng, err = parseUsageAnalyticsFilters(c, subject.UserID)
		if err != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		q.APIKeyID = apiKey.ID
		cur = usageRecordsCursor{
			V: usageRecordsCursorV1, UserID: q.UserID, APIKeyID: q.APIKeyID,
			Start: q.StartTime.UnixMicro(), End: q.EndTime.UnixMicro(),
			StartDate: rng.StartDate, EndDate: rng.EndDate, Timezone: rng.Location.String(),
			GroupID: q.GroupID, Model: q.Model, Effort: q.ReasoningEffort,
			Missing: q.ReasoningEffortMissing, RequestT: q.RequestType, BillingT: q.BillingType,
		}
	}

	logs, err := h.usageService.ListUserUsageRecordsPage(c.Request.Context(), q, after, limit+1)
	if err != nil {
		h.usageViewServiceError(c, err, usageViewRecords)
		return
	}
	hasMore := len(logs) > limit
	if hasMore {
		logs = logs[:limit]
	}
	records := make([]publicUsageRecord, 0, len(logs))
	for i, base := range publicKeyUsageRecords(logs) {
		var effort *string
		if logs[i].ReasoningEffort != nil {
			if v := strings.ToLower(strings.TrimSpace(*logs[i].ReasoningEffort)); v != "" {
				effort = &v
			}
		}
		records = append(records, publicUsageRecord{publicKeyUsageRecord: base, ReasoningEffort: effort})
	}

	var next *string
	if hasMore {
		last := logs[len(logs)-1]
		cur.LastAt, cur.LastID = last.CreatedAt.UnixMicro(), last.ID
		cur.ExpiresAt = time.Now().Add(usageRecordsCursorTTL).Unix()
		sealed, err := sealUsageRecordsCursor(secret, cur)
		if err != nil {
			slog.Warn("usage records cursor unavailable", "error", err)
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "paging is unavailable")
			return
		}
		next = &sealed
	}
	c.JSON(http.StatusOK, gin.H{
		"object":        "usage.records",
		"start_date":    cur.StartDate,
		"end_date":      cur.EndDate,
		"timezone":      cur.Timezone,
		"start":         time.UnixMicro(cur.Start).In(usageCursorLocation(cur.Timezone)).Format(time.RFC3339),
		"end_exclusive": time.UnixMicro(cur.End).In(usageCursorLocation(cur.Timezone)).Format(time.RFC3339),
		"records":       records,
		"has_more":      hasMore,
		"next_cursor":   next,
	})
}

func usageCursorLocation(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}
