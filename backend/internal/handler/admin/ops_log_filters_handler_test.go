//go:build unit

package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsLogFilterProposalRepo struct {
	service.OpsRepository
	detail       *service.OpsErrorLogDetail
	err          error
	requestedIDs []int64
}

func (repo *opsLogFilterProposalRepo) GetErrorLogByID(requestContext context.Context, errorID int64) (*service.OpsErrorLogDetail, error) {
	repo.requestedIDs = append(repo.requestedIDs, errorID)
	return repo.detail, repo.err
}

func newOpsLogFiltersHandlerService(repo service.OpsRepository, settings service.SettingRepository, cfg *config.Config) *service.OpsService {
	return service.NewOpsService(repo, settings, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
}

func newOpsLogFiltersHandlerRouter(opsService *service.OpsService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewOpsHandler(opsService)
	router := gin.New()
	router.GET("/log-filters", handler.GetLogFilters)
	router.PUT("/log-filters", handler.UpdateLogFilters)
	router.GET("/errors/:id/log-filter-proposal", handler.GetLogFilterProposal)
	return router
}

func serveOpsLogFiltersHandlerRequest(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodeOpsLogFiltersHandlerConfig(test *testing.T, recorder *httptest.ResponseRecorder) service.OpsLogFilterConfig {
	test.Helper()
	require.Equal(test, http.StatusOK, recorder.Code)
	var envelope responseEnvelope
	require.NoError(test, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Zero(test, envelope.Code)
	var configuration service.OpsLogFilterConfig
	require.NoError(test, json.Unmarshal(envelope.Data, &configuration))
	return configuration
}

func opsLogFiltersHandlerRule() service.OpsLogFilterRule {
	return service.OpsLogFilterRule{ID: "inactive-user", Name: "Inactive user", Enabled: true, Source: "local_auth", Reason: service.OpsLocalAuthUserInactive, Platform: service.PlatformAnthropic, MatchMode: "all"}
}

func TestOpsLogFiltersHandler_UnavailableAndDisabled(test *testing.T) {
	for _, scenario := range []struct {
		name       string
		opsService *service.OpsService
		status     int
	}{
		{name: "service unavailable", status: http.StatusServiceUnavailable},
		{name: "Ops hard disabled", opsService: newOpsLogFiltersHandlerService(nil, nil, &config.Config{Ops: config.OpsConfig{Enabled: false}}), status: http.StatusNotFound},
		{name: "Ops runtime disabled", opsService: newOpsLogFiltersHandlerService(nil, &testSettingRepo{values: map[string]string{service.SettingKeyOpsMonitoringEnabled: "false"}}, nil), status: http.StatusNotFound},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			router := newOpsLogFiltersHandlerRouter(scenario.opsService)
			for _, endpoint := range []struct {
				method string
				path   string
			}{
				{method: http.MethodGet, path: "/log-filters"},
				{method: http.MethodPut, path: "/log-filters"},
				{method: http.MethodGet, path: "/errors/17/log-filter-proposal"},
			} {
				recorder := serveOpsLogFiltersHandlerRequest(router, endpoint.method, endpoint.path, "{")
				require.Equal(test, scenario.status, recorder.Code)
				var envelope responseEnvelope
				require.NoError(test, json.Unmarshal(recorder.Body.Bytes(), &envelope))
				require.Equal(test, scenario.status, envelope.Code)
				require.NotEmpty(test, envelope.Message)
			}
		})
	}
}

func TestOpsLogFiltersHandler_GetDefaultsAndUpdateValid(test *testing.T) {
	repo := newTestSettingRepo()
	repo.values["ops_advanced_settings"] = `{"retention_days":30}`
	router := newOpsLogFiltersHandlerRouter(newOpsLogFiltersHandlerService(nil, repo, nil))
	initial := decodeOpsLogFiltersHandlerConfig(test, serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/log-filters", ""))
	require.NotNil(test, initial.Rules)
	require.Empty(test, initial.Rules)
	require.NotNil(test, initial.Counts)
	require.Empty(test, initial.Counts)
	require.Equal(test, "process", initial.CountScope)
	require.Len(test, initial.Revision, 64)
	require.NotContains(test, repo.values, service.SettingKeyOpsLogFilters)
	rule := opsLogFiltersHandlerRule()
	groupID := int64(42)
	rule.GroupID = &groupID
	payload, err := json.Marshal(service.OpsLogFilterUpdate{Rules: []service.OpsLogFilterRule{rule}, Revision: initial.Revision})
	require.NoError(test, err)
	updated := decodeOpsLogFiltersHandlerConfig(test, serveOpsLogFiltersHandlerRequest(router, http.MethodPut, "/log-filters", string(payload)))
	require.Equal(test, []service.OpsLogFilterRule{rule}, updated.Rules)
	require.NotEqual(test, initial.Revision, updated.Revision)
	require.Equal(test, map[string]uint64{rule.ID: 0}, updated.Counts)
	require.Equal(test, "process", updated.CountScope)
	var stored []service.OpsLogFilterRule
	require.NoError(test, json.Unmarshal([]byte(repo.values[service.SettingKeyOpsLogFilters]), &stored))
	require.Equal(test, updated.Rules, stored)
	require.Equal(test, `{"retention_days":30}`, repo.values["ops_advanced_settings"])
	reloaded := decodeOpsLogFiltersHandlerConfig(test, serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/log-filters", ""))
	require.Equal(test, updated, reloaded)
	payload, err = json.Marshal(service.OpsLogFilterUpdate{Rules: []service.OpsLogFilterRule{}, Revision: updated.Revision})
	require.NoError(test, err)
	cleared := decodeOpsLogFiltersHandlerConfig(test, serveOpsLogFiltersHandlerRequest(router, http.MethodPut, "/log-filters", string(payload)))
	require.Empty(test, cleared.Rules)
	require.Empty(test, cleared.Counts)
	require.Equal(test, "[]", repo.values[service.SettingKeyOpsLogFilters])
}

func TestOpsLogFiltersHandler_StaleRevisionReturnsConflict(test *testing.T) {
	repo := newTestSettingRepo()
	router := newOpsLogFiltersHandlerRouter(newOpsLogFiltersHandlerService(nil, repo, nil))
	initial := decodeOpsLogFiltersHandlerConfig(test, serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/log-filters", ""))
	payload, err := json.Marshal(service.OpsLogFilterUpdate{Rules: []service.OpsLogFilterRule{opsLogFiltersHandlerRule()}, Revision: initial.Revision})
	require.NoError(test, err)
	updated := decodeOpsLogFiltersHandlerConfig(test, serveOpsLogFiltersHandlerRequest(router, http.MethodPut, "/log-filters", string(payload)))
	stored := repo.values[service.SettingKeyOpsLogFilters]
	payload, err = json.Marshal(service.OpsLogFilterUpdate{Rules: []service.OpsLogFilterRule{}, Revision: initial.Revision})
	require.NoError(test, err)
	recorder := serveOpsLogFiltersHandlerRequest(router, http.MethodPut, "/log-filters", string(payload))
	require.Equal(test, http.StatusConflict, recorder.Code)
	require.Contains(test, recorder.Body.String(), "OPS_LOG_FILTER_CONFLICT")
	require.Equal(test, stored, repo.values[service.SettingKeyOpsLogFilters])
	reloaded := decodeOpsLogFiltersHandlerConfig(test, serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/log-filters", ""))
	require.Equal(test, updated, reloaded)
}

func TestOpsLogFiltersHandler_RejectsMalformedOrInvalidUpdates(test *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: "{"},
		{name: "array instead of update", body: "[]"},
		{name: "rules wrong type", body: `{"revision":"REVISION","rules":"invalid"}`},
		{name: "status wrong type", body: `{"revision":"REVISION","rules":[{"status_codes":["401"]}]}`},
		{name: "missing revision", body: `{"rules":[]}`},
		{name: "invalid local reason", body: `{"revision":"REVISION","rules":[{"id":"bad","name":"Bad","enabled":true,"source":"local_auth","reason":"unknown","match_mode":"all"}]}`},
		{name: "upstream missing keywords", body: `{"revision":"REVISION","rules":[{"id":"bad","name":"Bad","enabled":true,"source":"upstream","status_codes":[503],"match_mode":"all"}]}`},
		{name: "upstream any mode", body: `{"revision":"REVISION","rules":[{"id":"bad","name":"Bad","source":"upstream","status_codes":[503],"keywords":["overloaded"],"match_mode":"any"}]}`},
		{name: "empty keyword", body: `{"revision":"REVISION","rules":[{"id":"bad","name":"Bad","source":"local_auth","reason":"user_inactive","keywords":[" "],"match_mode":"all"}]}`},
	}
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			repo := newTestSettingRepo()
			router := newOpsLogFiltersHandlerRouter(newOpsLogFiltersHandlerService(nil, repo, nil))
			initial := decodeOpsLogFiltersHandlerConfig(test, serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/log-filters", ""))
			body := strings.ReplaceAll(scenario.body, "REVISION", initial.Revision)
			recorder := serveOpsLogFiltersHandlerRequest(router, http.MethodPut, "/log-filters", body)
			require.Equal(test, http.StatusBadRequest, recorder.Code)
			var envelope responseEnvelope
			require.NoError(test, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			require.Equal(test, http.StatusBadRequest, envelope.Code)
			require.NotContains(test, repo.values, service.SettingKeyOpsLogFilters)
		})
	}
}

func TestOpsLogFiltersHandler_StoredConfigAndRepositoryFailures(test *testing.T) {
	repo := newTestSettingRepo()
	router := newOpsLogFiltersHandlerRouter(newOpsLogFiltersHandlerService(nil, repo, nil))
	initial := decodeOpsLogFiltersHandlerConfig(test, serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/log-filters", ""))
	repo.values[service.SettingKeyOpsLogFilters] = "{"
	recorder := serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/log-filters", "")
	require.Equal(test, http.StatusInternalServerError, recorder.Code)
	require.Contains(test, recorder.Body.String(), "Failed to load log filters")
	payload, err := json.Marshal(service.OpsLogFilterUpdate{Rules: []service.OpsLogFilterRule{}, Revision: initial.Revision})
	require.NoError(test, err)
	recorder = serveOpsLogFiltersHandlerRequest(router, http.MethodPut, "/log-filters", string(payload))
	require.Equal(test, http.StatusInternalServerError, recorder.Code)
	require.Equal(test, "{", repo.values[service.SettingKeyOpsLogFilters])
	router = newOpsLogFiltersHandlerRouter(newOpsLogFiltersHandlerService(nil, nil, nil))
	recorder = serveOpsLogFiltersHandlerRequest(router, http.MethodPut, "/log-filters", string(payload))
	require.Equal(test, http.StatusInternalServerError, recorder.Code)
}

func TestOpsLogFiltersHandler_ProposalRejectsInvalidIDsBeforeLookup(test *testing.T) {
	repo := &opsLogFilterProposalRepo{}
	router := newOpsLogFiltersHandlerRouter(newOpsLogFiltersHandlerService(repo, newTestSettingRepo(), nil))
	for _, invalidID := range []string{"invalid", "0", "-1", "9223372036854775808", "1.5"} {
		test.Run(invalidID, func(test *testing.T) {
			recorder := serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/errors/"+invalidID+"/log-filter-proposal", "")
			require.Equal(test, http.StatusBadRequest, recorder.Code)
			require.Contains(test, recorder.Body.String(), "Invalid error id")
		})
	}
	require.Empty(test, repo.requestedIDs)
}

func TestOpsLogFiltersHandler_ProposalResponsePreservesVerificationAndScope(test *testing.T) {
	groupID := int64(42)
	upstreamStatus := 401
	for _, scenario := range []struct {
		name     string
		detail   *service.OpsErrorLogDetail
		source   string
		verified bool
		global   bool
	}{
		{name: "verified local", detail: &service.OpsErrorLogDetail{OpsErrorLog: service.OpsErrorLog{ID: 17, StatusCode: 401, Type: "gateway_auth_user_inactive", Phase: "auth", Source: "client_request", Platform: service.PlatformAnthropic, GroupID: &groupID}}, source: "local_auth", verified: true},
		{name: "legacy candidate", detail: &service.OpsErrorLogDetail{OpsErrorLog: service.OpsErrorLog{ID: 17, StatusCode: 401, Type: "authentication_error", Phase: "auth", Source: "client_request"}, ErrorBody: `{"code":"USER_INACTIVE"}`}, source: "local_auth", global: true},
		{name: "upstream unauthorized", detail: &service.OpsErrorLogDetail{OpsErrorLog: service.OpsErrorLog{ID: 17, StatusCode: 401, Phase: "upstream", Source: "upstream_http", Platform: service.PlatformAnthropic}, UpstreamStatusCode: &upstreamStatus, UpstreamErrorMessage: "Invalid API key", ErrorBody: `{"code":"INVALID_API_KEY"}`}, source: "upstream", verified: true},
		{name: "unclassified gateway error", detail: &service.OpsErrorLogDetail{OpsErrorLog: service.OpsErrorLog{ID: 17, StatusCode: 500, Phase: "internal", Source: "gateway", Message: "generic internal failure"}}},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			repo := &opsLogFilterProposalRepo{detail: scenario.detail}
			router := newOpsLogFiltersHandlerRouter(newOpsLogFiltersHandlerService(repo, newTestSettingRepo(), nil))
			recorder := serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/errors/17/log-filter-proposal", "")
			require.Equal(test, http.StatusOK, recorder.Code)
			var envelope responseEnvelope
			require.NoError(test, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			require.Zero(test, envelope.Code)
			var proposal service.OpsLogFilterProposal
			require.NoError(test, json.Unmarshal(envelope.Data, &proposal))
			require.Equal(test, []int64{17}, repo.requestedIDs)
			require.Equal(test, scenario.verified, proposal.Verified)
			require.Equal(test, scenario.global, proposal.RequiresGlobalConfirmation)
			if scenario.source == "" {
				require.Nil(test, proposal.Rule)
				return
			}
			require.NotNil(test, proposal.Rule)
			require.Equal(test, scenario.source, proposal.Rule.Source)
			require.False(test, proposal.Rule.Enabled)
			require.Equal(test, scenario.detail.Platform, proposal.Rule.Platform)
			require.Equal(test, scenario.detail.GroupID, proposal.Rule.GroupID)
			require.Equal(test, "all", proposal.Rule.MatchMode)
		})
	}
}

func TestOpsLogFiltersHandler_ProposalLookupFailures(test *testing.T) {
	for _, scenario := range []struct {
		name      string
		repoError error
		status    int
	}{
		{name: "not found", repoError: sql.ErrNoRows, status: http.StatusNotFound},
		{name: "repository failure", repoError: errors.New("mock lookup failure"), status: http.StatusInternalServerError},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			repo := &opsLogFilterProposalRepo{err: scenario.repoError}
			router := newOpsLogFiltersHandlerRouter(newOpsLogFiltersHandlerService(repo, newTestSettingRepo(), nil))
			recorder := serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/errors/17/log-filter-proposal", "")
			require.Equal(test, scenario.status, recorder.Code)
			require.Equal(test, []int64{17}, repo.requestedIDs)
		})
	}
	router := newOpsLogFiltersHandlerRouter(newOpsLogFiltersHandlerService(nil, newTestSettingRepo(), nil))
	recorder := serveOpsLogFiltersHandlerRequest(router, http.MethodGet, "/errors/17/log-filter-proposal", "")
	require.Equal(test, http.StatusNotFound, recorder.Code)
}
