package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type singleDeviceSlotsAdminService struct {
	*stubAdminService
	account service.Account
}

func (s *singleDeviceSlotsAdminService) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if s.account.ID == id {
		acc := s.account
		return &acc, nil
	}
	return s.stubAdminService.GetAccount(context.Background(), id)
}

func setupSingleDeviceSlotsRouter(adminSvc service.AdminService, identitySvc ...*service.IdentityService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var identity *service.IdentityService
	if len(identitySvc) > 0 {
		identity = identitySvc[0]
	}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, identity, nil, nil, nil, nil, nil)
	router.GET("/api/v1/admin/accounts/:id/claude-single-device-slots", handler.ListClaudeSingleDeviceSlots)
	return router
}

func TestListClaudeSingleDeviceSlots_NonSingleDeviceAccountReturnsEmpty(t *testing.T) {
	svc := &singleDeviceSlotsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:       42,
			Name:     "setup-token-carpool",
			Platform: service.PlatformAnthropic,
			Type:     service.AccountTypeSetupToken,
			Status:   service.StatusActive,
			Extra: map[string]any{
				"claude_oauth_mode": service.ClaudeOAuthModeCarpool,
			},
		},
	}
	router := setupSingleDeviceSlotsRouter(svc, &service.IdentityService{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/42/claude-single-device-slots", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Items []service.SingleDeviceSlotInfo `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Empty(t, resp.Data.Items)
}
