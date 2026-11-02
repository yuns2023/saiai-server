package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type entrypointCaptureAdminService struct {
	*stubAdminService
	created *service.CreateGroupInput
	updated *service.UpdateGroupInput
}

func (s *entrypointCaptureAdminService) CreateGroup(ctx context.Context, input *service.CreateGroupInput) (*service.Group, error) {
	s.created = input
	return s.stubAdminService.CreateGroup(ctx, input)
}

func (s *entrypointCaptureAdminService) UpdateGroup(ctx context.Context, id int64, input *service.UpdateGroupInput) (*service.Group, error) {
	s.updated = input
	return s.stubAdminService.UpdateGroup(ctx, id, input)
}

type entrypointObservationListRepo struct {
	service.ClaudeEntrypointObservationRepository
	since string
}

func (r *entrypointObservationListRepo) ListClaudeEntrypointObservations(_ context.Context, _ int64, since string, _ int) ([]service.ClaudeEntrypointObservationSummary, error) {
	r.since = since
	return []service.ClaudeEntrypointObservationSummary{{UAEntrypoint: "t3-code", BodyEntrypoint: "t3-code", Requests: 3}}, nil
}

func newEntrypointGroupRouter() (*gin.Engine, *entrypointCaptureAdminService, *entrypointObservationListRepo) {
	gin.SetMode(gin.TestMode)
	svc := &entrypointCaptureAdminService{stubAdminService: newStubAdminService()}
	repo := &entrypointObservationListRepo{}
	h := NewGroupHandler(svc, nil, nil, service.NewClaudeEntrypointObservationService(repo))
	router := gin.New()
	router.POST("/groups", h.Create)
	router.PUT("/groups/:id", h.Update)
	router.GET("/groups/:id/claude-entrypoint-observations", h.GetClaudeEntrypointObservations)
	return router, svc, repo
}

func doJSON(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func TestGroupHandlerClaudeEntrypointAllowlistAbsentEmptyAndNull(t *testing.T) {
	router, svc, _ := newEntrypointGroupRouter()

	rec := doJSON(router, http.MethodPut, "/groups/1", `{"claude_entrypoint_policy":"observe"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, svc.updated.ClaudeEntrypointPolicy)
	require.Equal(t, "observe", *svc.updated.ClaudeEntrypointPolicy)
	require.Nil(t, svc.updated.ClaudeEntrypointAllowlist, "absent keeps the stored list")

	rec = doJSON(router, http.MethodPut, "/groups/1", `{"claude_entrypoint_allowlist":[]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Nil(t, svc.updated.ClaudeEntrypointPolicy)
	require.NotNil(t, svc.updated.ClaudeEntrypointAllowlist)
	require.Empty(t, *svc.updated.ClaudeEntrypointAllowlist, "explicit [] stores an empty list")

	rec = doJSON(router, http.MethodPut, "/groups/1", `{"claude_entrypoint_allowlist":["cli","t3-code"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"cli", "t3-code"}, *svc.updated.ClaudeEntrypointAllowlist)

	for _, bad := range []string{`null`, `"cli"`, `{"a":1}`, `[1]`, `["cli",null]`} {
		svc.updated = nil
		rec = doJSON(router, http.MethodPut, "/groups/1", `{"claude_entrypoint_allowlist":`+bad+`}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
		require.Nil(t, svc.updated, bad)
	}

	rec = doJSON(router, http.MethodPost, "/groups", `{"name":"g"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Nil(t, svc.created.ClaudeEntrypointAllowlist, "absent → service default")
	rec = doJSON(router, http.MethodPost, "/groups", `{"name":"g","claude_entrypoint_allowlist":[]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, svc.created.ClaudeEntrypointAllowlist)
	require.Empty(t, svc.created.ClaudeEntrypointAllowlist)
	rec = doJSON(router, http.MethodPost, "/groups", `{"name":"g","claude_entrypoint_allowlist":null}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGroupHandlerClaudeEntrypointObservations(t *testing.T) {
	router, _, repo := newEntrypointGroupRouter()

	rec := doJSON(router, http.MethodGet, "/groups/5/claude-entrypoint-observations", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			Days      int                                          `json:"days"`
			Items     []service.ClaudeEntrypointObservationSummary `json:"items"`
			Truncated *bool                                        `json:"truncated"`
			Collector *struct {
				DroppedLastFlush *int64 `json:"dropped_last_flush"`
			} `json:"collector"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 7, resp.Data.Days)
	require.NotNil(t, resp.Data.Truncated)
	require.False(t, *resp.Data.Truncated)
	require.NotNil(t, resp.Data.Collector)
	require.NotNil(t, resp.Data.Collector.DroppedLastFlush)
	require.Len(t, resp.Data.Items, 1)
	require.Equal(t, "t3-code", resp.Data.Items[0].UAEntrypoint)
	require.NotEmpty(t, repo.since)

	for _, path := range []string{
		"/groups/5/claude-entrypoint-observations?days=0",
		"/groups/5/claude-entrypoint-observations?days=91",
		"/groups/5/claude-entrypoint-observations?days=abc",
		"/groups/x/claude-entrypoint-observations",
	} {
		rec = doJSON(router, http.MethodGet, path, "")
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
	}
	rec = doJSON(router, http.MethodGet, "/groups/5/claude-entrypoint-observations?days=90", "")
	require.Equal(t, http.StatusOK, rec.Code)
}
