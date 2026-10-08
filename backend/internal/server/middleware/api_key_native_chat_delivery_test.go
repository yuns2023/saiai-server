//go:build unit

package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNativeChatDeliveryDoesNotRequireNewCreditOrAdmitGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, method, route, path, status string
		enabled                           bool
		want                              int
	}{
		{"owned snapshot", "GET", "/chatgpt/backend-api/conversation/:conversation_id", "/chatgpt/backend-api/conversation/TEST_ONLY", service.StatusAPIKeyQuotaExhausted, true, 200},
		{"asset", "GET", "/chatgpt/backend-api/files/download/:file_id", "/chatgpt/backend-api/files/download/file_TEST_ONLY", service.StatusAPIKeyQuotaExhausted, true, 200},
		{"subscription", "GET", "/chatgpt/backend-api/celsius/ws/user", "/chatgpt/backend-api/celsius/ws/user", service.StatusActive, true, 200},
		{"delivery leg", "POST", "/chatgpt/backend-api/f/conversation/*subpath", "/chatgpt/backend-api/f/conversation/resume", service.StatusActive, true, 200},
		{"new generation", "POST", "/chatgpt/backend-api/f/conversation", "/chatgpt/backend-api/f/conversation", service.StatusActive, true, 403},
		{"preparation", "POST", "/chatgpt/backend-api/f/conversation/*subpath", "/chatgpt/backend-api/f/conversation/prepare", service.StatusActive, true, 403},
		{"disabled feature", "GET", "/chatgpt/backend-api/conversation/:conversation_id", "/chatgpt/backend-api/conversation/TEST_ONLY", service.StatusActive, false, 403},
		{"expired credential", "GET", "/chatgpt/backend-api/conversation/:conversation_id", "/chatgpt/backend-api/conversation/TEST_ONLY", service.StatusAPIKeyExpired, true, 403},
		{"disabled credential", "GET", "/chatgpt/backend-api/conversation/:conversation_id", "/chatgpt/backend-api/conversation/TEST_ONLY", service.StatusDisabled, true, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			cfg := &config.Config{RunMode: config.RunModeStandard}
			cfg.Gateway.OpenAIChatEnabled = true
			cfg.Gateway.OpenAIChatUpdatesEnabled = tc.enabled
			group := &service.Group{ID: 42, Status: service.StatusActive, Hydrated: true, Platform: service.PlatformOpenAI}
			user := &service.User{ID: 7, Status: service.StatusActive, Role: service.RoleUser, Balance: 0}
			key := &service.APIKey{ID: 100, Key: "TEST_ONLY", UserID: user.ID, User: user, GroupID: &group.ID, Group: group, Status: tc.status}
			repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
			auth := apiKeyAuthWithSubscription(service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg), nil, nil, cfg)
			router := gin.New()
			router.Handle(tc.method, tc.route, auth, func(c *gin.Context) { c.Status(200) })
			w := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.Header.Set("Authorization", "Bearer TEST_ONLY")
			router.ServeHTTP(w, request)
			require.Equal(t, tc.want, w.Code, w.Body.String())
		})
	}
}
