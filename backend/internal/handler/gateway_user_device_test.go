package handler

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

type userDeviceAdmissionRepo struct {
	service.ClaudeDeviceRepository
	calls int
}

func (r *userDeviceAdmissionRepo) CheckAndRegister(_ context.Context, _, _, _ int64, _, _, _ string, _ int, _ service.ClientHints) (*service.ClaudeDeviceRegistrationResult, error) {
	r.calls++
	return &service.ClaudeDeviceRegistrationResult{Allowed: false, EffectiveLimit: 3}, nil
}
func TestClaudeDeviceHandlerScope(t *testing.T) {
	for _, tc := range []struct {
		name, path                  string
		official, metadata, allowed bool
		calls                       int
	}{
		{"generic API", "/v1/messages", false, false, true, 0},
		{"official token counting exception", "/v1/messages/count_tokens", true, false, true, 0},
		{"official missing identity", "/v1/messages", true, false, false, 0},
		{"official over capacity", "/v1/messages", true, true, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &userDeviceAdmissionRepo{}
			h := &GatewayHandler{claudeDeviceService: service.NewClaudeDeviceService(repo)}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", tc.path, nil)
			c.Request = c.Request.WithContext(service.SetClaudeCodeClient(c.Request.Context(), tc.official))
			groupID := int64(1)
			key := &service.APIKey{ID: 1, UserID: 1, GroupID: &groupID, Group: &service.Group{}, User: &service.User{}}
			parsed := &service.ParsedRequest{}
			if tc.metadata {
				parsed.MetadataUserID = `{"device_id":"device-a","session_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}`
			}
			require.Equal(t, tc.allowed, h.checkClaudeDeviceLimit(c, key, parsed, false))
			require.Equal(t, tc.calls, repo.calls)
		})
	}
}
