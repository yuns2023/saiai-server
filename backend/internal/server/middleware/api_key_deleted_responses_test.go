//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDeletedKeyResponsesStopBeforeProxyPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		for _, path := range []string{"/v1/responses", "/responses", "/v1/responses/compact", "/responses/compact"} {
			t.Run(method+path, func(t *testing.T) {
				repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
					return nil, service.ErrAPIKeyNotFound
				}}
				cfg := &config.Config{RunMode: config.RunModeStandard}
				svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
				router := gin.New()
				router.Handle(method, path, gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, nil, cfg)), func(c *gin.Context) {
					t.Error("invalid Key reached proxy policy/model handler")
				})
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("Authorization", "Bearer TEST_ONLY_DELETED")
				req.Header.Set("User-Agent", "Codex Desktop/0.162.0-alpha.17.2 (Windows 10.0.26200; x86_64)")
				if method == http.MethodGet {
					req.Header.Set("Connection", "Upgrade")
					req.Header.Set("Upgrade", "websocket")
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				require.Equal(t, http.StatusUnauthorized, w.Code)
				var body struct {
					ErrorResponse
					Error ErrorResponse `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				require.Equal(t, "INVALID_API_KEY", body.Code)
				require.Equal(t, body.ErrorResponse, body.Error)
				require.Contains(t, body.Error.Message, "API key 无效或已删除")
				require.NotContains(t, w.Body.String(), "saiai_local_proxy_required")
			})
		}
	}
}
