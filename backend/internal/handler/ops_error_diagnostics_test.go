package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsDiagnosticsPassedPolicyAndControlPlane(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "internal-correlation"))
	c.Request.Header.Set("User-Agent", "codex-tui/0.163.0")
	c.Request.Header.Set("version", "0.163.0")
	c.Request.Header.Set("chatgpt-account-id", "TEST_ONLY_IDENTITY_SECRET")
	c.Request.Header.Set("Authorization", "Bearer TEST_ONLY_KEY")
	require.True(t, codexClientPolicyMatched(c, "local_proxy_only"))
	entry := &service.OpsInsertErrorLogInput{RequestID: "upstream-id"}
	attachOpsErrorDiagnostics(c, entry)
	require.Equal(t, "internal-correlation", entry.RequestID)
	require.Len(t, entry.Diagnostics.Checks, 4)
	require.Equal(t, "originator", entry.Diagnostics.Checks[1].Field)
	require.Equal(t, "omitted", entry.Diagnostics.Checks[1].Actual)
	for _, check := range entry.Diagnostics.Checks {
		require.True(t, check.Passed)
	}
	data, err := json.Marshal(entry.Diagnostics)
	require.NoError(t, err)
	require.NotContains(t, string(data), "TEST_ONLY")

	c.Request.Header.Set("originator", "TEST_ONLY_INVALID_ORIGINATOR")
	require.False(t, codexClientPolicyMatched(c, "local_proxy_only"))
	attachOpsErrorDiagnostics(c, entry)
	require.True(t, entry.Diagnostics.Checks[0].Passed)
	require.False(t, entry.Diagnostics.Checks[1].Passed)
	require.Equal(t, "unrecognized", entry.Diagnostics.Checks[1].Actual)

	models, _ := gin.CreateTestContext(httptest.NewRecorder())
	models.Request = httptest.NewRequest("GET", "/v1/models", nil)
	models.Request.Header.Set("User-Agent", "codex-tui/0.163.0")
	require.True(t, codexLocalProxyModelsRequestMatched(models))
	attachOpsErrorDiagnostics(models, entry)
	require.Empty(t, entry.Diagnostics.Checks, "model discovery must not claim identity checks were evaluated")
}
