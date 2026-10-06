//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsAuthFilterObservation struct {
	reason           string
	apiKey           *service.APIKey
	subject          AuthSubject
	role             string
	aborted          bool
	observed         bool
	downstreamCalled bool
}

func opsAuthFilterTestKey(platform string) *service.APIKey {
	group := &service.Group{ID: 42, Name: "Test group", Platform: platform, Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard}
	user := &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive, Concurrency: 3, Balance: 10}
	return &service.APIKey{ID: 100, Key: "test-key", UserID: user.ID, Status: service.StatusActive, User: user, GroupID: &group.ID, Group: group}
}

func serveOpsAuthFilterRequest(test *testing.T, google bool, apiKey *service.APIKey, repoError error, provideKey bool, query string) (*httptest.ResponseRecorder, opsAuthFilterObservation, int, int) {
	test.Helper()
	gin.SetMode(gin.TestMode)
	lookups := 0
	touches := 0
	repo := &stubApiKeyRepo{
		getByKey: func(requestContext context.Context, key string) (*service.APIKey, error) {
			lookups++
			require.Equal(test, "test-key", key)
			return apiKey, repoError
		},
		updateLastUsed: func(requestContext context.Context, keyID int64, usedAt time.Time) error {
			touches++
			return nil
		},
	}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	observation := opsAuthFilterObservation{}
	router := gin.New()
	router.Use(func(requestContext *gin.Context) {
		requestContext.Next()
		observation.reason = service.GetOpsLocalAuthRejectReason(requestContext)
		observation.apiKey, _ = GetAPIKeyFromContext(requestContext)
		observation.subject, _ = GetAuthSubjectFromContext(requestContext)
		observation.role, _ = GetUserRoleFromContext(requestContext)
		observation.aborted = requestContext.IsAborted()
		observation.observed = true
	})
	path := "/v1/messages"
	if google {
		path = "/v1beta/test"
		router.Use(APIKeyAuthWithSubscriptionGoogle(apiKeyService, nil, cfg))
	} else {
		router.Use(apiKeyAuthWithSubscription(apiKeyService, nil, nil, cfg))
	}
	router.GET(path, func(requestContext *gin.Context) {
		observation.downstreamCalled = true
		requestContext.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, path+query, nil)
	request.Header.Set("ops_local_auth_reject_reason", service.OpsLocalAuthUserInactive)
	if provideKey {
		if google {
			request.Header.Set("x-goog-api-key", "test-key")
		} else {
			request.Header.Set("x-api-key", "test-key")
		}
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder, observation, lookups, touches
}

func requireOpsAuthFilterEnvelope(test *testing.T, recorder *httptest.ResponseRecorder, google bool, status int, nativeCode, googleMessage string) {
	test.Helper()
	require.Equal(test, status, recorder.Code)
	require.NotContains(test, recorder.Body.String(), "gateway_auth_")
	require.NotContains(test, recorder.Body.String(), "ops_local_auth_reject_reason")
	if google {
		var envelope googleErrorResponse
		require.NoError(test, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		require.Equal(test, status, envelope.Error.Code)
		require.Equal(test, googleMessage, envelope.Error.Message)
		if status == http.StatusUnauthorized {
			require.Equal(test, "UNAUTHENTICATED", envelope.Error.Status)
		}
	} else {
		var envelope ErrorResponse
		require.NoError(test, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		require.Equal(test, nativeCode, envelope.Code)
		require.NotEmpty(test, envelope.Message)
	}
}

func TestAPIKeyAuthLogFilters_MarksOnlyKnownLocalRejections(test *testing.T) {
	for _, transport := range []struct {
		name     string
		google   bool
		platform string
	}{
		{name: "native", platform: service.PlatformAnthropic},
		{name: "google", google: true, platform: service.PlatformGemini},
	} {
		test.Run(transport.name, func(test *testing.T) {
			for _, scenario := range []struct {
				name          string
				provideKey    bool
				repoError     error
				inactiveUser  bool
				reason        string
				nativeCode    string
				googleMessage string
			}{
				{name: "missing key", reason: service.OpsLocalAuthAPIKeyRequired, nativeCode: "API_KEY_REQUIRED", googleMessage: "API key is required"},
				{name: "invalid key", provideKey: true, repoError: service.ErrAPIKeyNotFound, reason: service.OpsLocalAuthInvalidAPIKey, nativeCode: "INVALID_API_KEY", googleMessage: "Invalid API key"},
				{name: "disabled user", provideKey: true, inactiveUser: true, reason: service.OpsLocalAuthUserInactive, nativeCode: "USER_INACTIVE", googleMessage: "User account is not active"},
			} {
				test.Run(scenario.name, func(test *testing.T) {
					apiKey := opsAuthFilterTestKey(transport.platform)
					if scenario.inactiveUser {
						apiKey.User.Status = service.StatusDisabled
					}
					recorder, observation, lookups, touches := serveOpsAuthFilterRequest(test, transport.google, apiKey, scenario.repoError, scenario.provideKey, "")
					requireOpsAuthFilterEnvelope(test, recorder, transport.google, http.StatusUnauthorized, scenario.nativeCode, scenario.googleMessage)
					require.True(test, observation.observed)
					require.True(test, observation.aborted)
					require.False(test, observation.downstreamCalled)
					require.Equal(test, scenario.reason, observation.reason)
					require.Zero(test, touches)
					if scenario.provideKey {
						require.Positive(test, lookups)
					} else {
						require.Zero(test, lookups)
					}
					if scenario.inactiveUser {
						require.NotNil(test, observation.apiKey)
						require.Equal(test, apiKey.ID, observation.apiKey.ID)
						require.Equal(test, apiKey.GroupID, observation.apiKey.GroupID)
						require.NotNil(test, observation.apiKey.Group)
						require.Equal(test, apiKey.Group.ID, observation.apiKey.Group.ID)
						require.Equal(test, transport.platform, observation.apiKey.Group.Platform)
						require.Equal(test, AuthSubject{UserID: apiKey.User.ID, Concurrency: apiKey.User.Concurrency}, observation.subject)
						require.Equal(test, apiKey.User.Role, observation.role)
					} else {
						require.Nil(test, observation.apiKey)
						require.Zero(test, observation.subject.UserID)
					}
				})
			}
		})
	}
}

func TestAPIKeyAuthLogFilters_UnknownFailuresIgnoreSpoofedHeader(test *testing.T) {
	for _, google := range []bool{false, true} {
		for _, scenario := range []struct {
			name          string
			mutate        func(*service.APIKey)
			repoError     error
			query         string
			status        int
			nativeCode    string
			googleMessage string
		}{
			{name: "repository failure", repoError: errors.New("mock database failure"), status: 500, nativeCode: "INTERNAL_ERROR", googleMessage: "Failed to validate API key"},
			{name: "disabled key", mutate: func(apiKey *service.APIKey) { apiKey.Status = service.StatusAPIKeyDisabled }, status: 401, nativeCode: "API_KEY_DISABLED", googleMessage: "API key is disabled"},
			{name: "unknown key status", mutate: func(apiKey *service.APIKey) { apiKey.Status = "unknown" }, status: 401, nativeCode: "API_KEY_DISABLED", googleMessage: "API key is disabled"},
			{name: "unknown user status", mutate: func(apiKey *service.APIKey) { apiKey.User.Status = "unknown" }, status: 401, nativeCode: "USER_INACTIVE", googleMessage: "User account is not active"},
			{name: "mismatched user identity", mutate: func(apiKey *service.APIKey) { apiKey.User.Status = service.StatusDisabled; apiKey.User.ID = 999 }, status: 401, nativeCode: "USER_INACTIVE", googleMessage: "User account is not active"},
			{name: "inactive group", mutate: func(apiKey *service.APIKey) { apiKey.Group.Status = service.StatusDisabled }, status: 403, nativeCode: "GROUP_INACTIVE", googleMessage: "API key group is not active"},
			{name: "billing failure", mutate: func(apiKey *service.APIKey) { apiKey.User.Balance = 0 }, status: 403, nativeCode: "INSUFFICIENT_BALANCE", googleMessage: "Insufficient account balance"},
			{name: "deprecated query key", query: "?api_key=legacy", status: 400, nativeCode: "api_key_in_query_deprecated", googleMessage: "Query parameter api_key is deprecated. Use Authorization header or key instead."},
		} {
			transport := "native"
			if google {
				transport = "google"
			}
			test.Run(transport+"/"+scenario.name, func(test *testing.T) {
				apiKey := opsAuthFilterTestKey(service.PlatformGemini)
				if scenario.mutate != nil {
					scenario.mutate(apiKey)
				}
				recorder, observation, _, touches := serveOpsAuthFilterRequest(test, google, apiKey, scenario.repoError, true, scenario.query)
				requireOpsAuthFilterEnvelope(test, recorder, google, scenario.status, scenario.nativeCode, scenario.googleMessage)
				require.True(test, observation.observed)
				require.True(test, observation.aborted)
				require.False(test, observation.downstreamCalled)
				require.Empty(test, observation.reason)
				require.Zero(test, touches)
			})
		}
	}
}

func TestAPIKeyAuthLogFilters_MissingUserDoesNotMarkInvalidKey(test *testing.T) {
	for _, google := range []bool{false, true} {
		transport := "native"
		if google {
			transport = "google"
		}
		test.Run(transport, func(test *testing.T) {
			apiKey := opsAuthFilterTestKey(service.PlatformGemini)
			apiKey.User = nil
			recorder, observation, _, touches := serveOpsAuthFilterRequest(test, google, apiKey, nil, true, "")
			require.Equal(test, http.StatusUnauthorized, recorder.Code)
			require.True(test, observation.observed)
			require.True(test, observation.aborted)
			require.False(test, observation.downstreamCalled)
			require.Empty(test, observation.reason)
			require.Zero(test, touches)
			requireOpsAuthFilterEnvelope(test, recorder, google, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
		})
	}
}

func TestAPIKeyAuthLogFilters_SuccessDoesNotTrustInboundMarker(test *testing.T) {
	for _, google := range []bool{false, true} {
		transport := "native"
		if google {
			transport = "google"
		}
		test.Run(transport, func(test *testing.T) {
			apiKey := opsAuthFilterTestKey(service.PlatformGemini)
			recorder, observation, _, touches := serveOpsAuthFilterRequest(test, google, apiKey, nil, true, "")
			require.Equal(test, http.StatusNoContent, recorder.Code)
			require.True(test, observation.observed)
			require.True(test, observation.downstreamCalled)
			require.False(test, observation.aborted)
			require.Empty(test, observation.reason)
			require.Equal(test, 1, touches)
		})
	}
}
