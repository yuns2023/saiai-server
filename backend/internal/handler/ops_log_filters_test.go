package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsLogFilterSettingsStub struct {
	service.SettingRepository
	values map[string]string
}

func (repo *opsLogFilterSettingsStub) GetValue(ctx context.Context, key string) (string, error) {
	value, exists := repo.values[key]
	if !exists {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (repo *opsLogFilterSettingsStub) Set(ctx context.Context, key, value string) error {
	repo.values[key] = value
	return nil
}

func newOpsLogFilterTestService(test *testing.T, rules []service.OpsLogFilterRule) *service.OpsService {
	test.Helper()
	encoded, err := json.Marshal(rules)
	require.NoError(test, err)
	repo := &opsLogFilterSettingsStub{values: map[string]string{service.SettingKeyOpsLogFilters: string(encoded)}}
	return service.NewOpsService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func prepareOpsLogFilterTestQueue(test *testing.T) {
	test.Helper()
	resetOpsErrorLoggerStateForTest(test)
	test.Cleanup(func() { resetOpsErrorLoggerStateForTest(test) })
	opsErrorLogOnce.Do(func() {})
	opsErrorLogQueue = make(chan opsErrorLogJob, 10)
}

func TestOpsLogFilter_LocalAuthPreservesResponseAndKeepsUpstream401(test *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name     string
		marked   bool
		enabled  bool
		filtered bool
	}{
		{name: "confirmed local rejection", marked: true, enabled: true, filtered: true},
		{name: "disabled rule", marked: true},
		{name: "same code without trusted source", enabled: true},
	} {
		test.Run(testCase.name, func(test *testing.T) {
			prepareOpsLogFilterTestQueue(test)
			ops := newOpsLogFilterTestService(test, []service.OpsLogFilterRule{{
				ID: "inactive", Name: "Inactive user", Enabled: testCase.enabled,
				Source: "local_auth", Reason: service.OpsLocalAuthUserInactive, MatchMode: "all",
			}})
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.GET("/v1/responses", func(ctx *gin.Context) {
				setOpsRequestContext(ctx, "TEST_ONLY_MODEL", false, []byte(`{"messages":[{"content":"TEST_ONLY_BODY"}]}`))
				if testCase.marked {
					service.MarkOpsLocalAuthRejected(ctx, service.OpsLocalAuthUserInactive)
				}
				middleware2.AbortWithError(ctx, http.StatusUnauthorized, "USER_INACTIVE", "User account is not active")
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			request.Header.Set("ops_local_auth_reject_reason", "user_inactive")
			router.ServeHTTP(recorder, request)
			require.Equal(test, 401, recorder.Code)
			require.JSONEq(test, `{"code":"USER_INACTIVE","message":"User account is not active"}`, recorder.Body.String())
			if testCase.filtered {
				require.Zero(test, OpsErrorLogEnqueuedTotal())
				require.Zero(test, OpsErrorLogSanitizedTotal())
			} else {
				require.Equal(test, int64(1), OpsErrorLogEnqueuedTotal())
				job := <-opsErrorLogQueue
				if testCase.marked {
					require.Equal(test, service.OpsLocalAuthErrorType(service.OpsLocalAuthUserInactive), job.entry.ErrorType)
					require.Equal(test, "auth", job.entry.ErrorPhase)
				}
			}
		})
	}
}

func TestOpsLogFilter_UpstreamOnlySkipsWhenAllAttemptsMatch(test *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name           string
		clientStatus   int
		mixed          bool
		filtered       bool
		terminalStatus int
	}{
		{name: "upstream error", clientStatus: 422, filtered: true},
		{name: "recovered upstream error", clientStatus: 200, filtered: true},
		{name: "mixed real failure", clientStatus: 502, mixed: true},
		{name: "mixed recovered failures", clientStatus: 200, mixed: true},
		{name: "context-only terminal stream failure", clientStatus: 200, terminalStatus: 502},
	} {
		test.Run(testCase.name, func(test *testing.T) {
			prepareOpsLogFilterTestQueue(test)
			ops := newOpsLogFilterTestService(test, []service.OpsLogFilterRule{{
				ID: "context", Name: "Context limit", Enabled: true, Source: "upstream",
				StatusCodes: []int{422}, Keywords: []string{"context limit"}, MatchMode: "all",
			}})
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.GET("/v1/responses", func(ctx *gin.Context) {
				events := []*service.OpsUpstreamErrorEvent{{UpstreamStatusCode: 422, Message: "context limit", Platform: "openai"}}
				if testCase.mixed {
					events = append(events, &service.OpsUpstreamErrorEvent{UpstreamStatusCode: 502, Message: "provider unavailable", Platform: "openai"})
				}
				ctx.Set(service.OpsUpstreamErrorsKey, events)
				last := events[len(events)-1]
				service.SetOpsUpstreamError(ctx, last.UpstreamStatusCode, last.Message, last.Detail)
				if testCase.terminalStatus != 0 {
					service.SetOpsUpstreamError(ctx, testCase.terminalStatus, "response too large", "")
				}
				setOpsRequestContext(ctx, "TEST_ONLY_MODEL", false, []byte(`{"input":"TEST_ONLY_BODY"}`))
				ctx.JSON(testCase.clientStatus, gin.H{"error": gin.H{"type": "upstream_error", "message": "context limit"}})
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
			require.Equal(test, testCase.clientStatus, recorder.Code)
			if testCase.filtered {
				require.Zero(test, OpsErrorLogEnqueuedTotal())
				require.Zero(test, OpsErrorLogSanitizedTotal())
			} else {
				require.Equal(test, int64(1), OpsErrorLogEnqueuedTotal())
			}
		})
	}
}

func TestOpsLogFilter_UnknownOrDistinctTerminalFailureIsKept(test *testing.T) {
	for _, testCase := range []struct {
		name            string
		clientStatus    int
		terminalStatus  int
		terminalMessage string
	}{
		{name: "unclassified forwarding failure", clientStatus: 502},
		{name: "distinct terminal response failure", clientStatus: 502, terminalStatus: 502, terminalMessage: "response too large"},
		{name: "distinct terminal stream failure", clientStatus: 200, terminalStatus: 502, terminalMessage: "response too large"},
	} {
		test.Run(testCase.name, func(test *testing.T) {
			ops := newOpsLogFilterTestService(test, []service.OpsLogFilterRule{{ID: "context", Name: "Context limit", Enabled: true, Source: "upstream", StatusCodes: []int{422}, Keywords: []string{"context limit"}, MatchMode: "all"}})
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			status := 422
			message := "context limit"
			entry := &service.OpsInsertErrorLogInput{
				ErrorPhase: "upstream", StatusCode: testCase.clientStatus, Platform: "openai",
				UpstreamErrors:     []*service.OpsUpstreamErrorEvent{{UpstreamStatusCode: status, Message: message}},
				UpstreamStatusCode: &status, UpstreamErrorMessage: &message,
			}
			if testCase.terminalStatus != 0 {
				service.SetOpsUpstreamError(ctx, testCase.terminalStatus, testCase.terminalMessage, "")
				entry.UpstreamStatusCode = &testCase.terminalStatus
				entry.UpstreamErrorMessage = &testCase.terminalMessage
			}
			require.False(test, filterOpsUpstreamLog(ctx, ops, entry))
		})
	}
}

func TestOpsLogFilter_TrustedUpstream401IsIndependentOfInferredPhase(test *testing.T) {
	for _, phase := range []string{"auth", "request", "upstream"} {
		test.Run(phase, func(test *testing.T) {
			ops := newOpsLogFilterTestService(test, []service.OpsLogFilterRule{{ID: "upstream-auth", Name: "Known upstream rejection", Enabled: true, Source: "upstream", StatusCodes: []int{401}, Keywords: []string{"TEST_ONLY_REJECTION"}, MatchMode: "all"}})
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			status := 401
			message := "TEST_ONLY_REJECTION"
			service.SetOpsUpstreamError(ctx, status, message, "")
			entry := &service.OpsInsertErrorLogInput{ErrorPhase: phase, StatusCode: status, ErrorMessage: message, UpstreamStatusCode: &status, UpstreamErrorMessage: &message}
			require.True(test, filterOpsUpstreamLog(ctx, ops, entry))
		})
	}
}

func TestOpsLogFilter_LegacyInvalidKeySwitchRequiresLocalAuthSource(test *testing.T) {
	repo := &opsLogFilterSettingsStub{values: map[string]string{
		service.SettingKeyOpsAdvancedSettings: `{"ignore_invalid_api_key_errors":true}`,
	}}
	ops := service.NewOpsService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, reason := range []string{service.OpsLocalAuthInvalidAPIKey, service.OpsLocalAuthAPIKeyRequired} {
		require.True(test, shouldSkipOpsErrorLog(context.Background(), ops, "Invalid API key", `{"code":"INVALID_API_KEY"}`, "/v1/responses", reason))
	}
	require.False(test, shouldSkipOpsErrorLog(context.Background(), ops, "Invalid API key", `{"code":"INVALID_API_KEY"}`, "/v1/responses", ""))
}

func TestOpsLogFilter_StaleSameStatusCannotHideUnknownTerminalFailure(test *testing.T) {
	ops := newOpsLogFilterTestService(test, []service.OpsLogFilterRule{{ID: "temporary", Name: "Known temporary condition", Enabled: true, Source: "upstream", StatusCodes: []int{502}, Keywords: []string{"known temporary condition"}, MatchMode: "all"}})
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	status := 502
	message := "known temporary condition"
	service.SetOpsUpstreamError(ctx, status, message, "")
	entry := &service.OpsInsertErrorLogInput{
		StatusCode: status, ErrorPhase: "upstream", ErrorMessage: "Upstream request failed",
		UpstreamStatusCode: &status, UpstreamErrorMessage: &message,
		UpstreamErrors: []*service.OpsUpstreamErrorEvent{{UpstreamStatusCode: status, Message: message}},
	}
	require.False(test, filterOpsUpstreamLog(ctx, ops, entry))
}
