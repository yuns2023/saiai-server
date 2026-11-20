//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cdpHandlerStore struct {
	query service.ClaudeDevicePlatformFlaggedQuery
	err   error
}

func (s *cdpHandlerStore) RecordClaudeDevicePlatform(context.Context, service.ClaudeDevicePlatformObservation, service.ClaudeDevicePlatformLimits) (service.ClaudeDevicePlatformWriteStatus, error) {
	return service.ClaudeDevicePlatformWriteOK, nil
}

func (s *cdpHandlerStore) ListFlaggedClaudeDevicePlatforms(_ context.Context, q service.ClaudeDevicePlatformFlaggedQuery) (service.ClaudeDevicePlatformFlaggedPage, error) {
	s.query = q
	if s.err != nil {
		return service.ClaudeDevicePlatformFlaggedPage{}, s.err
	}
	at := time.Unix(1_800_000_000, 0).UTC()
	return service.ClaudeDevicePlatformFlaggedPage{
		Items: []service.ClaudeDevicePlatformDevice{{
			DisplayPrefix: "abcd1234",
			LastMatchedAt: at,
			Reasons:       []service.ClaudeDevicePlatformReasonMatch{{Reason: "cross_family", LastMatchedAt: at, Evidence: "macos+windows"}},
			Platforms:     []service.ClaudeDevicePlatformArchEntry{{Family: "macos", Arch: "arm64", FirstSeenAt: at, LastSeenAt: at}},
			Versions:      []service.ClaudeDevicePlatformVersionEntry{{Family: "macos", Major: "15", FirstSeenAt: at, LastSeenAt: at}},
			Users:         []service.ClaudeDevicePlatformUser{{UserID: 3, APIKeyID: 9, LastSeenAt: at}},
		}},
		NextCursor: "1800000000:" + "ab",
	}, nil
}

type cdpHandlerUsers struct{}

func (cdpHandlerUsers) LookupClaudeDevicePlatformUsers(context.Context, []int64) (map[int64]service.ClaudeDevicePlatformUserName, error) {
	return map[int64]service.ClaudeDevicePlatformUserName{3: {Username: "alice", Email: "alice@example.invalid"}}, nil
}

func serveCDP(t *testing.T, store *cdpHandlerStore, query string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewClaudeDevicePlatformHandler(service.NewClaudeDevicePlatformService(store, cdpHandlerUsers{}))
	r := gin.New()
	r.GET("/flagged", h.ListFlagged)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/flagged"+query, nil))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func TestClaudeDevicePlatformHandler_JSONShape(t *testing.T) {
	store := &cdpHandlerStore{}
	w, body := serveCDP(t, store, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 50, store.query.Limit)
	require.Equal(t, service.ClaudeDevicePlatformDefaultReasons, store.query.Reasons)

	data, ok := body["data"].(map[string]any)
	require.True(t, ok, w.Body.String())
	require.Contains(t, data, "next_cursor")
	require.Contains(t, data, "reasons")
	require.Contains(t, data, "stats")
	items, ok := data["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	item, ok := items[0].(map[string]any)
	require.True(t, ok)
	for _, k := range []string{"display_prefix", "last_matched_at", "reasons", "platforms", "versions", "users", "users_truncated"} {
		require.Contains(t, item, k)
	}
	require.NotContains(t, w.Body.String(), "digest")
	reasons, ok := item["reasons"].([]any)
	require.True(t, ok)
	reason, ok := reasons[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{"reason": "cross_family", "last_matched_at": "2027-01-15T08:00:00Z", "evidence": "macos+windows"}, reason)
	platforms, ok := item["platforms"].([]any)
	require.True(t, ok)
	platform, ok := platforms[0].(map[string]any)
	require.True(t, ok)
	for _, k := range []string{"family", "arch", "first_seen_at", "last_seen_at"} {
		require.Contains(t, platform, k)
	}
	versions, ok := item["versions"].([]any)
	require.True(t, ok)
	version, ok := versions[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "15", version["major"])
	users, ok := item["users"].([]any)
	require.True(t, ok)
	user, ok := users[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "alice", user["username"])
	for _, k := range []string{"user_id", "username", "email", "api_key_id", "last_seen_at"} {
		require.Contains(t, user, k)
	}
	stats, ok := data["stats"].(map[string]any)
	require.True(t, ok)
	for _, k := range []string{"enqueued", "written", "dedupe_skipped", "queue_full", "invalid_device_id", "parse_overflow", "global_cap", "user_cap", "no_owner", "write_errors"} {
		require.Contains(t, stats, k)
	}
}

func TestClaudeDevicePlatformHandler_QueryValidation(t *testing.T) {
	store := &cdpHandlerStore{}
	w, _ := serveCDP(t, store, "?limit=5000&reasons=arch_only,sequential_upgrade&cursor=123:abc")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, service.ClaudeDevicePlatformListMaxLimit, store.query.Limit)
	require.Equal(t, []string{"arch_only", "sequential_upgrade"}, store.query.Reasons)
	require.Equal(t, "123:abc", store.query.Cursor)

	for _, q := range []string{"?limit=0", "?limit=abc", "?reasons=bogus", "?cursor=" + strings.Repeat("a", 200)} {
		w, _ := serveCDP(t, &cdpHandlerStore{}, q)
		require.Equal(t, http.StatusBadRequest, w.Code, q)
	}

	w, _ = serveCDP(t, &cdpHandlerStore{err: service.ErrClaudeDevicePlatformInvalidQuery}, "?cursor=bad")
	require.Equal(t, http.StatusBadRequest, w.Code)
}
