package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClientHandlerBootstrap(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := NewClientHandler(BuildInfo{Version: "1.2.3-test"})
	router.GET("/bootstrap", withBootstrapAPIKey(openAIKey(true)), handler.Bootstrap)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/bootstrap", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "Authorization", recorder.Header().Get("Vary"))
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {
			"schema_version": 1,
			"gateway_version": "1.2.3-test",
			"capabilities": {
				"claude": true,
				"codex": true,
				"codex_responses": true,
				"codex_websockets": false
			}
		}
	}`, recorder.Body.String())
}

func TestClientHandlerBootstrapKeepsEmptyGatewayVersionInContract(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handler := NewClientHandler(BuildInfo{})
	router.GET("/bootstrap", withBootstrapAPIKey(openAIKey(true)), handler.Bootstrap)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/bootstrap", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {
			"schema_version": 1,
			"gateway_version": "",
			"capabilities": {
				"claude": true,
				"codex": true,
				"codex_responses": true,
				"codex_websockets": false
			}
		}
	}`, recorder.Body.String())
}

func TestClientCapabilitiesAreEffectiveForAuthenticatedKey(t *testing.T) {
	tests := []struct {
		name string
		key  *service.APIKey
		want ClientCapabilities
	}{
		{name: "missing key", key: nil, want: ClientCapabilities{}},
		{name: "ungrouped key", key: &service.APIKey{}, want: ClientCapabilities{}},
		{
			name: "OpenAI without Messages dispatch",
			key:  openAIKey(false),
			want: ClientCapabilities{Codex: true, CodexResponses: true},
		},
		{
			name: "OpenAI with Messages dispatch",
			key:  openAIKey(true),
			want: ClientCapabilities{Claude: true, Codex: true, CodexResponses: true},
		},
		{
			name: "Anthropic",
			key:  &service.APIKey{Group: activeGroup(service.PlatformAnthropic)},
			want: ClientCapabilities{Claude: true},
		},
		{
			name: "Antigravity",
			key:  &service.APIKey{Group: activeGroup(service.PlatformAntigravity)},
			want: ClientCapabilities{Claude: true},
		},
		{
			name: "inactive group",
			key: &service.APIKey{Group: &service.Group{
				ID:       9,
				Platform: service.PlatformOpenAI,
				Status:   service.StatusDisabled,
				Hydrated: true,
			}},
			want: ClientCapabilities{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, clientCapabilitiesForAPIKey(test.key))
			require.False(t, clientCapabilitiesForAPIKey(test.key).CodexWebSockets)
		})
	}
}

func openAIKey(allowMessages bool) *service.APIKey {
	group := activeGroup(service.PlatformOpenAI)
	group.AllowMessagesDispatch = allowMessages
	return &service.APIKey{Group: group}
}

func activeGroup(platform string) *service.Group {
	return &service.Group{
		ID:       7,
		Platform: platform,
		Status:   service.StatusActive,
		Hydrated: true,
	}
}

func withBootstrapAPIKey(apiKey *service.APIKey) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyAPIKey), apiKey)
		c.Next()
	}
}
