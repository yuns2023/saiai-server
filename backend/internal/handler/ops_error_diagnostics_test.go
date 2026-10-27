package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsFailedUploadReader struct{}

func (opsFailedUploadReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (opsFailedUploadReader) Close() error             { return nil }

func TestOpsDiagnosticsIngressHostOnFailedUpload(t *testing.T) {
	prepareOpsLogFilterTestQueue(t)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/v1/messages", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Failed to read request body"}})
	})
	req := httptest.NewRequest(http.MethodPost, "https://gateway.example.test/v1/messages", opsFailedUploadReader{})
	req.Header.Set("X-Forwarded-Host", "Edge.Example.Test:443")
	req.Header.Set("Authorization", "Bearer TEST_ONLY_SECRET")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.EqualValues(t, 1, OpsErrorLogEnqueuedTotal())
	entry := (<-opsErrorLogQueue).entry
	require.Empty(t, entry.Model)
	require.Nil(t, entry.AccountID)
	require.Nil(t, entry.RequestBodyJSON)
	require.Empty(t, entry.Diagnostics.Checks)
	require.Equal(t, "gateway.example.test", entry.Diagnostics.RequestHost)
	require.Equal(t, "edge.example.test", entry.Diagnostics.ForwardedHost)
	encoded, err := json.Marshal(entry.Diagnostics)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "TEST_ONLY_SECRET")
}

func TestOpsDiagnosticsRejectsAmbiguousForwardedHosts(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "https://gateway.example.test/v1/models", nil)
	c.Request.Header.Add("X-Forwarded-Host", "first.example.test")
	c.Request.Header.Add("X-Forwarded-Host", "second.example.test")
	entry := &service.OpsInsertErrorLogInput{}
	attachOpsErrorDiagnostics(c, entry)
	require.Empty(t, entry.Diagnostics.ForwardedHost)
	require.Equal(t, "gateway.example.test", entry.Diagnostics.RequestHost)
}

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
	attachOpsErrorDiagnostics(models, entry)
	require.Empty(t, entry.Diagnostics.Checks, "model discovery must not claim identity checks were evaluated")
}
