//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type hostedHandlerUsers struct {
	service.UserRepository
	grants []int64
}

func (r *hostedHandlerUsers) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, Status: service.StatusActive, AllowedGroups: r.grants}, nil
}

type hostedHandlerGroups struct {
	service.GroupRepository
	visible bool
}

func (r *hostedHandlerGroups) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
	if id != 10 {
		return nil, service.ErrGroupNotFound
	}
	return &service.Group{ID: 10, IsExclusive: true, ShowAccountQuota: r.visible, Status: service.StatusActive}, nil
}
func (r *hostedHandlerGroups) ListActive(context.Context) ([]service.Group, error) {
	g, _ := r.GetByIDLite(context.Background(), 10)
	return []service.Group{*g}, nil
}

type hostedHandlerAccounts struct {
	service.AccountRepository
	size  int
	calls int
}

func (r *hostedHandlerAccounts) ListWithFilters(_ context.Context, params pagination.PaginationParams, _, _, _, _ string, groupID int64) ([]service.Account, *pagination.PaginationResult, error) {
	if groupID != 10 {
		panic("unscoped account query")
	}
	r.calls++
	r.size = params.PageSize
	return []service.Account{{ID: 2, Name: "Hosted account", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "TEST_ONLY_PRIVATE_TOKEN"}, Extra: map[string]any{"email": "TEST_ONLY_PRIVATE_EMAIL"}, ErrorMessage: "TEST_ONLY_RAW_ERROR"}},
		&pagination.PaginationResult{Total: 1, Page: params.Page, PageSize: params.PageSize, Pages: 1}, nil
}

func TestHostedAccountHandlerAuthorizationAndSafeResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	u := &hostedHandlerUsers{grants: []int64{10}}
	g := &hostedHandlerGroups{visible: true}
	a := &hostedHandlerAccounts{}
	h := NewHostedAccountHandler(service.NewHostedAccountService(u, g, a))
	request := func(path string, authenticated bool) *httptest.ResponseRecorder {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			if authenticated {
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
			}
		})
		r.GET("/groups/hosted", h.ListGroups)
		r.GET("/groups/:id/account-quota", h.ListAccounts)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	require.Equal(t, http.StatusUnauthorized, request("/groups/hosted", false).Code)
	require.Equal(t, http.StatusUnauthorized, request("/groups/10/account-quota", false).Code)
	for _, id := range []string{"0", "-1", "invalid"} {
		require.Equal(t, http.StatusBadRequest, request("/groups/"+id+"/account-quota", true).Code)
	}
	w := request("/groups/10/account-quota?page_size=1000", true)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, 100, a.size)
	var envelope struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data.Items, 1)
	row := envelope.Data.Items[0]
	require.Len(t, row, 7)
	require.Equal(t, "Hosted account", row["name"])
	for _, private := range []string{"TEST_ONLY_PRIVATE_TOKEN", "TEST_ONLY_PRIVATE_EMAIL", "TEST_ONLY_RAW_ERROR", "credentials", "extra", "proxy"} {
		require.NotContains(t, w.Body.String(), private)
	}
	u.grants = nil
	before := a.calls
	require.Equal(t, http.StatusNotFound, request("/groups/10/account-quota", true).Code)
	require.Equal(t, before, a.calls)
	w = request("/groups/hosted", true)
	require.JSONEq(t, `{"code":0,"message":"success","data":[]}`, w.Body.String())
	u.grants = []int64{10}
	g.visible = false
	require.Equal(t, http.StatusNotFound, request("/groups/10/account-quota", true).Code)
	require.Equal(t, before, a.calls)
}
