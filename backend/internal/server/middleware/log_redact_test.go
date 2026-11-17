package middleware

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httptrace"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const redactTestCode = "Q7M2K9XW4TRB"

// Built at run time so secret scanners do not flag a fake key.
var redactTestKey = "sk-" + strings.Repeat("00112233aabbccdd", 4)

func TestRedactedLogPath(t *testing.T) {
	for _, p := range []string{"/i/" + redactTestCode, "/i/", "/i/x/y", "//i/" + redactTestCode, "/I/" + redactTestCode, "///i/abc"} {
		require.Equal(t, "/i/[redacted]", RedactedLogPath(p), p)
	}
	for _, p := range []string{"/", "/i", "/install", "/api/v1/keys/5/install-code", "/v1/messages", "i/abc", "/invite/abc", "/ii/abc"} {
		require.Equal(t, p, RedactedLogPath(p), p)
	}
	require.True(t, traceIsAITPInstallCodePath("/api/v1/keys/5/install-code"))
	require.True(t, traceIsAITPInstallCodePath("/api/v1/keys/5/install-code/"))
	require.True(t, traceIsAITPInstallCodePath("/i/"+redactTestCode))
	require.False(t, traceIsAITPInstallCodePath("/api/v1/keys/5"))
	require.False(t, traceIsAITPInstallCodePath("/api/v1/keys"))
}

func TestAccessLogsRedactInstallCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLogger(t)
	r := gin.New()
	r.Use(RequestLogger(), Logger())
	r.POST("/i/:code", func(c *gin.Context) { c.String(http.StatusOK, "echo ok") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/i/"+redactTestCode, nil))
	require.Equal(t, http.StatusOK, w.Code)
	// An unrouted variant (double slash) is logged too and must be redacted as well.
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "//i/"+redactTestCode, nil))

	events := sink.list()
	require.NotEmpty(t, events)
	accessLogs := 0
	for _, ev := range events {
		text := fmt.Sprintf("%s %v", ev.Message, ev.Fields)
		require.NotContains(t, text, redactTestCode)
		if ev.Message == "http request completed" {
			accessLogs++
			require.Equal(t, "/i/[redacted]", ev.Fields["path"])
		}
	}
	require.Equal(t, 2, accessLogs)
}

func TestHTTPTraceOmitsInstallCodeAndBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tracePath := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv("SAIAI_HTTP_TRACE", "1")
	t.Setenv("SAIAI_HTTP_TRACE_PATH", tracePath)
	tracer := httptrace.NewFromEnv()
	require.True(t, tracer.Enabled())

	r := gin.New()
	r.Use(RequestLogger(), httpTrace(tracer))
	redeemResponse := "bash setup --api-key '" + redactTestKey + "'"
	createResponse := `{"code":0,"data":{"code":"` + redactTestCode + `"}}`
	createRequest := `{"shell":"unix"}`
	r.POST("/i/:code", func(c *gin.Context) { c.String(http.StatusOK, redeemResponse) })
	r.POST("/api/v1/keys/:id/install-code", func(c *gin.Context) { c.String(http.StatusOK, createResponse) })
	r.POST("/api/v1/other", func(c *gin.Context) { c.String(http.StatusOK, "visible-response") })

	for _, tc := range []struct{ target, body string }{
		{"/i/" + redactTestCode + "?x=" + redactTestCode, "request-body-" + redactTestCode},
		{"/api/v1/keys/5/install-code", createRequest},
		{"/api/v1/other", "visible-request"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(tc.body)))
		require.Equal(t, http.StatusOK, w.Code)
	}
	raw, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	trace := string(raw)
	require.NotContains(t, trace, redactTestCode)
	require.NotContains(t, trace, redactTestKey)
	b64 := base64.StdEncoding.EncodeToString
	for _, body := range []string{redeemResponse, createResponse, createRequest, "request-body-" + redactTestCode} {
		require.NotContains(t, trace, b64([]byte(body)))
	}
	require.Contains(t, trace, `"url":"/i/[redacted]"`)
	require.Contains(t, trace, `"url":"/api/v1/keys/5/install-code"`)
	require.Contains(t, trace, `"body_omitted":true`)
	// Other routes keep full bodies (the trace is a debugging tool).
	require.Contains(t, trace, b64([]byte("visible-request")))
	require.Contains(t, trace, b64([]byte("visible-response")))
}
