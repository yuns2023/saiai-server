//go:build unit

package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type usageViewsRepoStub struct {
	service.UsageLogRepository
	analyticsQuery usagestats.UsageAnalyticsQuery
	analyticsErr   error
	recordsQuery   usagestats.UsageAnalyticsQuery
	recordsAfter   *usagestats.UsageRecordPosition
	recordsLimit   int
	records        []service.UsageLog
	recordsCalls   int
}

func (r *usageViewsRepoStub) GetUserUsageAnalytics(_ context.Context, q usagestats.UsageAnalyticsQuery) (*usagestats.UsageAnalyticsResult, error) {
	r.analyticsQuery = q
	if r.analyticsErr != nil {
		return nil, r.analyticsErr
	}
	return &usagestats.UsageAnalyticsResult{Items: []usagestats.UsageAnalyticsItem{{Model: "gpt"}}, Total: 1}, nil
}

func (r *usageViewsRepoStub) ListUserUsageRecordsPage(_ context.Context, q usagestats.UsageAnalyticsQuery, after *usagestats.UsageRecordPosition, limit int) ([]service.UsageLog, error) {
	r.recordsCalls++
	r.recordsQuery, r.recordsAfter, r.recordsLimit = q, after, limit
	if len(r.records) > limit {
		return r.records[:limit], nil
	}
	return r.records, nil
}

const usageViewsTestSecret = "unit-test-jwt-secret-0123456789abcdef"

func serveUsageView(t *testing.T, repo *usageViewsRepoStub, keyID int64, rawQuery string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage?"+rawQuery, nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: keyID, UserID: 7, Status: service.StatusAPIKeyActive})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
	h := &GatewayHandler{
		usageService: service.NewUsageService(repo, nil, nil, nil),
		cfg:          &config.Config{JWT: config.JWTConfig{Secret: usageViewsTestSecret}},
	}
	h.Usage(c)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return rec, body
}

func usageLogAt(id int64, at time.Time, effort *string) service.UsageLog {
	return service.UsageLog{ID: id, CreatedAt: at, Model: "gpt", InputTokens: 1, ReasoningEffort: effort}
}

func TestUsageViewAnalyticsIsScopedToTheAuthenticatedKey(t *testing.T) {
	repo := &usageViewsRepoStub{}
	rec, body := serveUsageView(t, repo, 100, "view=analytics&group_by=reasoning_effort&start_date=2026-10-01&end_date=2026-10-01&timezone=UTC&model=gpt")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "usage.analytics", body["object"])
	require.Equal(t, "reasoning_effort", body["group_by"])
	require.Equal(t, int64(7), repo.analyticsQuery.UserID)
	require.Equal(t, int64(100), repo.analyticsQuery.APIKeyID)
	require.Equal(t, "gpt", repo.analyticsQuery.Model)
	require.NotContains(t, body, "mode")
	require.NotContains(t, body, "balance")
}

func TestUsageViewRejectsInvalidRequests(t *testing.T) {
	cases := map[string]string{
		"empty view":             "view=",
		"unknown view":           "view=summary",
		"repeated view":          "view=analytics&view=records",
		"other key (analytics)":  "view=analytics&api_key_id=5",
		"legacy paging":          "view=analytics&records_page=2",
		"other key (records)":    "view=records&api_key_id=5",
		"records with page":      "view=records&page=2",
		"repeated filter":        "view=records&model=a&model=b",
		"bad limit":              "view=records&limit=501",
		"strict dates":           "view=records&start_date=2026-02-30",
		"too long range":         "view=analytics&start_date=2026-01-01&end_date=2026-06-01",
		"garbage cursor":         "view=records&cursor=abc",
		"analytics with cursor":  "view=analytics&cursor=abc",
		"records unknown param":  "view=records&group_by=model",
		"effort and missing set": "view=records&reasoning_effort=high&reasoning_effort_missing=true",
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &usageViewsRepoStub{}
			rec, body := serveUsageView(t, repo, 100, query)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, "error", body["type"])
			require.Zero(t, repo.recordsCalls)
			require.Zero(t, repo.analyticsQuery.UserID)
		})
	}
}

func TestUsageViewMapsBusyToTooManyRequests(t *testing.T) {
	repo := &usageViewsRepoStub{analyticsErr: infraerrors.TooManyRequests("BUSY", "busy")}
	rec, body := serveUsageView(t, repo, 100, "view=analytics")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	require.Equal(t, "rate_limit_error", asJSONMap(t, body["error"])["type"])
}

func TestUsageViewRecordsKeysetCursorRoundTrip(t *testing.T) {
	high, blank := " High ", " "
	at := time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)
	repo := &usageViewsRepoStub{records: []service.UsageLog{
		usageLogAt(30, at, &high),
		usageLogAt(20, at, &blank),
		usageLogAt(10, at.Add(-time.Second), nil),
	}}
	rec, body := serveUsageView(t, repo, 100, "view=records&limit=2&start_date=2026-10-01&end_date=2026-10-01&timezone=UTC&model=gpt&billing_type=0")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "usage.records", body["object"])
	require.Equal(t, 3, repo.recordsLimit, "limit+1 probes for more")
	require.Nil(t, repo.recordsAfter)
	require.Equal(t, int64(100), repo.recordsQuery.APIKeyID)
	require.Equal(t, true, body["has_more"])
	records := asJSONSlice(t, body["records"])
	require.Len(t, records, 2)
	first := asJSONMap(t, records[0])
	require.Equal(t, "high", first["reasoning_effort"])
	require.NotContains(t, first, "id")
	second := asJSONMap(t, records[1])
	require.Contains(t, second, "reasoning_effort")
	require.Nil(t, second["reasoning_effort"])
	cursor, ok := body["next_cursor"].(string)
	require.True(t, ok)
	sealed, err := base64.RawURLEncoding.DecodeString(cursor)
	require.NoError(t, err)
	require.NotContains(t, string(sealed), `"i":20`, "cursor must not expose the record id in clear text")

	// Next page: the cursor alone restores range and filters; position = last returned row.
	repo.records = []service.UsageLog{usageLogAt(10, at.Add(-time.Second), nil)}
	rec, body = serveUsageView(t, repo, 100, "view=records&limit=2&cursor="+url.QueryEscape(cursor))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, repo.recordsAfter)
	require.Equal(t, int64(20), repo.recordsAfter.ID)
	require.True(t, repo.recordsAfter.CreatedAt.Equal(at.Truncate(time.Microsecond)))
	require.Equal(t, "gpt", repo.recordsQuery.Model)
	require.NotNil(t, repo.recordsQuery.BillingType)
	require.Equal(t, "2026-10-01", body["start_date"])
	require.Equal(t, false, body["has_more"])
	require.Nil(t, body["next_cursor"])

	// A cursor cannot be combined with filters, reused by another Key, or tampered with.
	rec, _ = serveUsageView(t, repo, 100, "view=records&model=gpt&cursor="+url.QueryEscape(cursor))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec, _ = serveUsageView(t, repo, 101, "view=records&cursor="+url.QueryEscape(cursor))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	tampered := []byte(cursor)
	tampered[len(tampered)/2] ^= 1
	rec, _ = serveUsageView(t, repo, 100, "view=records&cursor="+url.QueryEscape(string(tampered)))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUsageViewRecordsRejectsExpiredCursor(t *testing.T) {
	sealed, err := sealUsageRecordsCursor(usageViewsTestSecret, usageRecordsCursor{
		V: usageRecordsCursorV1, UserID: 7, APIKeyID: 100, Timezone: "UTC",
		ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	})
	require.NoError(t, err)
	rec, body := serveUsageView(t, &usageViewsRepoStub{}, 100, "view=records&cursor="+url.QueryEscape(sealed))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, asJSONMap(t, body["error"])["message"], "expired")
}

func TestUsageWithoutViewKeepsLegacyPath(t *testing.T) {
	repo := &usageViewsRepoStub{}
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage?api_key_id=5&records_page=2", nil)
	handled := (&GatewayHandler{usageService: service.NewUsageService(repo, nil, nil, nil)}).usageView(c, &service.APIKey{ID: 100}, middleware2.AuthSubject{UserID: 7})
	require.False(t, handled)
	require.False(t, c.Writer.Written())
}

func asJSONMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "expected JSON object, got %T", v)
	return m
}

func asJSONSlice(t *testing.T, v any) []any {
	t.Helper()
	items, ok := v.([]any)
	require.True(t, ok, "expected JSON array, got %T", v)
	return items
}

func TestUsageViewRecordsCursorValidation(t *testing.T) {
	valid := func(mut func(*usageRecordsCursor)) string {
		cur := usageRecordsCursor{V: usageRecordsCursorV1, UserID: 7, APIKeyID: 100, Timezone: "UTC", ExpiresAt: time.Now().Add(time.Hour).Unix()}
		if mut != nil {
			mut(&cur)
		}
		sealed, err := sealUsageRecordsCursor(usageViewsTestSecret, cur)
		require.NoError(t, err)
		return sealed
	}
	otherSecret, err := sealUsageRecordsCursor("another-secret-0123456789abcdef", usageRecordsCursor{V: usageRecordsCursorV1, UserID: 7, APIKeyID: 100, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	require.NoError(t, err)
	cases := map[string]string{
		"empty cursor":      "",
		"blank cursor":      "%20",
		"other user":        url.QueryEscape(valid(func(c *usageRecordsCursor) { c.UserID = 8 })),
		"unknown version":   url.QueryEscape(valid(func(c *usageRecordsCursor) { c.V = 2 })),
		"other secret":      url.QueryEscape(otherSecret),
		"truncated":         url.QueryEscape(valid(nil)[:10]),
		"oversized":         strings.Repeat("A", usageRecordsMaxCursorLen+1),
		"repeated cursor":   url.QueryEscape(valid(nil)) + "&cursor=" + url.QueryEscape(valid(nil)),
		"expires right now": url.QueryEscape(valid(func(c *usageRecordsCursor) { c.ExpiresAt = time.Now().Unix() })),
	}
	for name, cursor := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &usageViewsRepoStub{}
			rec, _ := serveUsageView(t, repo, 100, "view=records&cursor="+cursor)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Zero(t, repo.recordsCalls)
		})
	}

	a, b := valid(nil), valid(nil)
	require.NotEqual(t, a, b, "each seal uses a fresh nonce")
	for _, token := range []string{a, b} {
		cur, err := openUsageRecordsCursor(usageViewsTestSecret, token)
		require.NoError(t, err)
		require.Equal(t, int64(100), cur.APIKeyID)
	}
}

func TestUsageViewRecordsWithoutSecretIsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage?view=records", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 100, UserID: 7})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
	repo := &usageViewsRepoStub{}
	cfg := &config.Config{}
	h := &GatewayHandler{usageService: service.NewUsageService(repo, nil, nil, nil), cfg: cfg}
	h.Usage(c)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Zero(t, repo.recordsCalls)

	// The secret is read per request, so one filled in after startup works.
	cfg.JWT.Secret = usageViewsTestSecret
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage?view=records&limit=1", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 100, UserID: 7})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
	repo.records = []service.UsageLog{usageLogAt(2, time.Now(), nil), usageLogAt(1, time.Now(), nil)}
	h.Usage(c)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"next_cursor":"`)
}
