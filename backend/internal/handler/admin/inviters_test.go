package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type inviterRefsRepo struct{ refs map[int64]service.InviterRef }

func (r inviterRefsRepo) HasQualifyingTopUp(context.Context, int64) (bool, error) { return false, nil }
func (r inviterRefsRepo) Counts(context.Context, int64) (service.UserInviteCounts, error) {
	return service.UserInviteCounts{}, nil
}
func (r inviterRefsRepo) ListByOwner(context.Context, int64, pagination.PaginationParams) ([]service.UserInviteCode, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (r inviterRefsRepo) CreateForOwner(context.Context, int64, string, service.UserInviteLimits) (*service.UserInviteCode, error) {
	return nil, nil
}
func (r inviterRefsRepo) RevokeByOwner(context.Context, int64, string) error { return nil }
func (r inviterRefsRepo) InviterRefs(_ context.Context, ids []int64) (map[int64]service.InviterRef, error) {
	out := map[int64]service.InviterRef{}
	for _, id := range ids {
		if ref, ok := r.refs[id]; ok {
			out[id] = ref
		}
	}
	return out, nil
}

// The new fields must reach the JSON of every admin endpoint that shows them.
func TestAdminUserAndRedeemEndpoints_SerializeInviter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	inviter := int64(42)
	adminSvc.users[0].InvitedByUserID = &inviter
	adminSvc.redeems = []service.RedeemCode{{ID: 5, Code: "INV", Type: service.RedeemTypeInvitation, Status: service.StatusUnused, OwnerUserID: &inviter}}
	invites := service.NewUserInviteService(inviterRefsRepo{refs: map[int64]service.InviterRef{
		42: {ID: 42, Email: "owner@example.com", Deleted: true},
	}}, service.NewSettingService(&modelPriceSettingsRepo{values: map[string]string{}}, &config.Config{}))

	router := gin.New()
	users := NewUserHandler(adminSvc, nil, nil, nil, invites)
	redeems := NewRedeemHandler(adminSvc, nil, invites)
	router.GET("/users", users.List)
	router.GET("/users/:id", users.GetByID)
	router.GET("/redeem", redeems.List)

	get := func(path string) map[string]any {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		return body["data"].(map[string]any)
	}

	list := get("/users")["items"].([]any)[0].(map[string]any)
	require.EqualValues(t, 42, list["invited_by_user_id"])
	require.Equal(t, map[string]any{"id": float64(42), "email": "owner@example.com", "deleted": true}, list["invited_by"])

	one := get("/users/1")
	require.EqualValues(t, 42, one["invited_by_user_id"])
	require.Equal(t, "owner@example.com", one["invited_by"].(map[string]any)["email"])

	code := get("/redeem")["items"].([]any)[0].(map[string]any)
	require.EqualValues(t, 42, code["owner_user_id"])
	require.Equal(t, "owner@example.com", code["owner"].(map[string]any)["email"])

	// Filter parsing.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users?invited_by_user_id=abc", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
}
