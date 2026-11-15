//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type inviteHandlerSettingRepo struct {
	service.SettingRepository
	values map[string]string
}

func (r inviteHandlerSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

type inviteHandlerRepo struct {
	service.UserInviteRepository
	counts    service.UserInviteCounts
	createErr error
}

func (r *inviteHandlerRepo) HasQualifyingTopUp(context.Context, int64) (bool, error) {
	return true, nil
}
func (r *inviteHandlerRepo) Counts(context.Context, int64) (service.UserInviteCounts, error) {
	return r.counts, nil
}
func (r *inviteHandlerRepo) ListByOwner(_ context.Context, _ int64, p pagination.PaginationParams) ([]service.UserInviteCode, *pagination.PaginationResult, error) {
	return nil, &pagination.PaginationResult{Page: p.Page, PageSize: p.PageSize, Pages: 1}, nil
}
func (r *inviteHandlerRepo) CreateForOwner(context.Context, int64, string, service.UserInviteLimits) (*service.UserInviteCode, error) {
	return nil, r.createErr
}

func newInviteHandlerRouter(repo *inviteHandlerRepo, settings map[string]string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc := service.NewUserInviteService(repo, service.NewSettingService(inviteHandlerSettingRepo{values: settings}, &config.Config{}))
	h := NewUserInviteHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
		c.Next()
	})
	r.GET("/invites", h.Overview)
	r.POST("/invites", h.Create)
	return r
}

func TestUserInviteHandler_OverviewSerializesWeeklyCap(t *testing.T) {
	repo := &inviteHandlerRepo{counts: service.UserInviteCounts{CreatedThisWeek: 2}}
	r := newInviteHandlerRouter(repo, map[string]string{
		service.SettingKeyUserInviteEnabled:    "true",
		service.SettingKeyUserInviteMaxPerWeek: "2",
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/invites", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.EqualValues(t, 2, body.Data["max_per_week"])
	require.EqualValues(t, 2, body.Data["created_this_week"])
	require.Equal(t, true, body.Data["weekly_limit_reached"])
	require.Equal(t, false, body.Data["can_create"])
}

func TestUserInviteHandler_CreateWeeklyLimitIs429(t *testing.T) {
	repo := &inviteHandlerRepo{createErr: service.ErrUserInviteWeeklyLimit}
	r := newInviteHandlerRouter(repo, map[string]string{
		service.SettingKeyUserInviteEnabled:    "true",
		service.SettingKeyUserInviteMaxPerWeek: "2",
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/invites", nil))
	require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "USER_INVITE_WEEKLY_LIMIT")
}
