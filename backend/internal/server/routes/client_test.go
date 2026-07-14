package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClientBootstrapRouteRequiresAPIKeyAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authCalls := 0
	router := gin.New()
	v1 := router.Group("/api/v1")
	RegisterClientRoutes(
		v1,
		&handler.Handlers{Client: handler.NewClientHandler(handler.BuildInfo{Version: "test-version"})},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			authCalls++
			if c.GetHeader("Authorization") != "Bearer valid-key" {
				servermiddleware.AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
				return
			}
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{
				ID:                    7,
				Platform:              service.PlatformOpenAI,
				Status:                service.StatusActive,
				Hydrated:              true,
				AllowMessagesDispatch: true,
			}})
			c.Next()
		}),
	)

	t.Run("rejects unauthenticated request", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/client/bootstrap", nil))

		require.Equal(t, http.StatusUnauthorized, recorder.Code)
		require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
		require.Equal(t, "Authorization", recorder.Header().Get("Vary"))
		require.JSONEq(t, `{"code":"INVALID_API_KEY","message":"Invalid API key"}`, recorder.Body.String())
	})

	t.Run("returns bootstrap contract after authentication", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/client/bootstrap", nil)
		request.Header.Set("Authorization", "Bearer valid-key")
		router.ServeHTTP(recorder, request)

		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
		require.JSONEq(t, `{
			"code": 0,
			"message": "success",
			"data": {
				"schema_version": 1,
				"gateway_version": "test-version",
				"capabilities": {
					"claude": true,
					"codex": true,
					"codex_responses": true,
					"codex_websockets": false
				}
			}
		}`, recorder.Body.String())
	})

	require.Equal(t, 2, authCalls)
}

func TestClientBootstrapRouteWithRealAPIKeyAuthNeedsNoGatewayServices(t *testing.T) {
	gin.SetMode(gin.TestMode)

	keys := map[string]*service.APIKey{
		"bootstrap-secret-ungrouped": bootstrapRouteAPIKey(1, nil),
		"bootstrap-secret-inactive": bootstrapRouteAPIKey(2, &service.Group{
			ID:       2,
			Platform: service.PlatformOpenAI,
			Status:   service.StatusDisabled,
			Hydrated: true,
		}),
		"bootstrap-secret-anthropic":            bootstrapRouteAPIKey(3, bootstrapRouteGroup(3, service.PlatformAnthropic, false)),
		"bootstrap-secret-openai-no-messages":   bootstrapRouteAPIKey(4, bootstrapRouteGroup(4, service.PlatformOpenAI, false)),
		"bootstrap-secret-openai-with-messages": bootstrapRouteAPIKey(5, bootstrapRouteGroup(5, service.PlatformOpenAI, true)),
	}
	repo := &bootstrapRouteAPIKeyRepository{keys: keys}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)

	router := gin.New()
	RegisterClientRoutes(
		router.Group("/api/v1"),
		&handler.Handlers{Client: handler.NewClientHandler(handler.BuildInfo{Version: "integration-test"})},
		servermiddleware.NewAPIKeyAuthMiddleware(apiKeyService, nil, cfg),
	)

	tests := []struct {
		name   string
		secret string
		want   handler.ClientCapabilities
	}{
		{name: "ungrouped key", secret: "bootstrap-secret-ungrouped", want: handler.ClientCapabilities{}},
		{name: "inactive group", secret: "bootstrap-secret-inactive", want: handler.ClientCapabilities{}},
		{name: "Anthropic group", secret: "bootstrap-secret-anthropic", want: handler.ClientCapabilities{Claude: true}},
		{name: "OpenAI without Messages dispatch", secret: "bootstrap-secret-openai-no-messages", want: handler.ClientCapabilities{Codex: true, CodexResponses: true}},
		{name: "OpenAI with Messages dispatch", secret: "bootstrap-secret-openai-with-messages", want: handler.ClientCapabilities{Claude: true, Codex: true, CodexResponses: true}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/client/bootstrap", nil)
			request.Header.Set("Authorization", "Bearer "+test.secret)
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
			require.Equal(t, "Authorization", recorder.Header().Get("Vary"))
			require.NotContains(t, recorder.Body.String(), test.secret)

			var body struct {
				Code int                         `json:"code"`
				Data handler.ClientBootstrapData `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			require.Zero(t, body.Code)
			require.Equal(t, test.want, body.Data.Capabilities)
			require.False(t, body.Data.Capabilities.CodexWebSockets)
		})
	}

	t.Run("invalid key returns 401 without reflecting the key", func(t *testing.T) {
		const invalidSecret = "bootstrap-secret-not-present"
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/client/bootstrap", nil)
		request.Header.Set("Authorization", "Bearer "+invalidSecret)
		router.ServeHTTP(recorder, request)

		require.Equal(t, http.StatusUnauthorized, recorder.Code)
		require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
		require.Equal(t, "Authorization", recorder.Header().Get("Vary"))
		require.NotContains(t, recorder.Body.String(), invalidSecret)
		require.JSONEq(t, `{"code":"INVALID_API_KEY","message":"Invalid API key"}`, recorder.Body.String())
	})

	// A real APIKeyAuthMiddleware performed every lookup and successful touch.
	// No account selector, gateway service, or upstream transport is constructed.
	require.Equal(t, len(tests)+1, repo.lookupCalls)
	require.Equal(t, len(tests), repo.touchCalls)
}

func bootstrapRouteAPIKey(id int64, group *service.Group) *service.APIKey {
	user := &service.User{
		ID:          id,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     1,
		Concurrency: 1,
	}
	key := &service.APIKey{
		ID:     id,
		UserID: user.ID,
		Status: service.StatusActive,
		User:   user,
		Group:  group,
	}
	if group != nil {
		key.GroupID = &group.ID
	}
	return key
}

func bootstrapRouteGroup(id int64, platform string, allowMessages bool) *service.Group {
	return &service.Group{
		ID:                    id,
		Platform:              platform,
		Status:                service.StatusActive,
		Hydrated:              true,
		AllowMessagesDispatch: allowMessages,
	}
}

// Embedding the production repository interface intentionally leaves all
// unrelated operations unavailable. The bootstrap path may only authenticate
// and touch the key; an unexpected repository dependency will panic this test.
type bootstrapRouteAPIKeyRepository struct {
	service.APIKeyRepository
	keys        map[string]*service.APIKey
	lookupCalls int
	touchCalls  int
}

func (r *bootstrapRouteAPIKeyRepository) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	r.lookupCalls++
	apiKey, ok := r.keys[key]
	if !ok {
		return nil, service.ErrAPIKeyNotFound
	}
	clone := *apiKey
	return &clone, nil
}

func (r *bootstrapRouteAPIKeyRepository) UpdateLastUsed(_ context.Context, _ int64, _ time.Time) error {
	r.touchCalls++
	return nil
}
