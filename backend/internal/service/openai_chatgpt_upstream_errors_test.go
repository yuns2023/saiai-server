package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestChatGPTUpstreamErrorObservationPreservesResponseAndBoundsCapture(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, marker, want string
	}{
		{"challenge", "text/html", `<html>enable JavaScript<script src="/cdn-cgi/challenge-platform/TEST_ONLY_PRIVATE_TOKEN"></script></html>`, "", "browser verification page"},
		{"challenge_header", "text/html", "TEST_ONLY_PRIVATE_TOKEN", "challenge", "browser verification page"},
		{"html", "text/html", "<html>TEST_ONLY_PRIVATE_TOKEN</html>", "", "HTML error page"},
		{"json", "application/json", `{"detail":"TEST_ONLY_PRIVATE_TOKEN"}`, "", "returned HTTP 403"},
		{"bounded", "application/octet-stream", strings.Repeat("x", chatGPTErrorPrefixLimit+1) + "<html>challenge-platform", "", "returned HTTP 403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			original := &http.Response{StatusCode: 403, Header: http.Header{
				"Content-Type": {tc.contentType}, "Cf-Mitigated": {tc.marker}, "X-Request-Id": {"TEST_ONLY_REQUEST"},
			}, Body: io.NopCloser(strings.NewReader(tc.body))}
			resp, err := observeChatGPTUpstreamResult(c, &Account{ID: 7}, original, nil, time.Now())
			require.NoError(t, err)
			require.Same(t, original, resp)
			require.Equal(t, 403, c.GetInt(OpsUpstreamStatusCodeKey))
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.body, string(body))
			require.NoError(t, resp.Body.Close())
			events, ok := c.MustGet(OpsUpstreamErrorsKey).([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, events, 1, "EOF and Close must not duplicate attempts")
			require.Equal(t, 403, events[0].UpstreamStatusCode)
			require.Equal(t, int64(7), events[0].AccountID)
			require.True(t, events[0].Passthrough)
			require.Contains(t, events[0].Message, tc.want)
			require.NotContains(t, events[0].Message, "TEST_ONLY_PRIVATE_TOKEN")
			require.Empty(t, events[0].UpstreamResponseBody)
			require.Empty(t, events[0].Detail)
			require.Equal(t, "TEST_ONLY_REQUEST", events[0].UpstreamRequestID)
			require.Contains(t, c.GetString(OpsUpstreamErrorMessageKey), tc.want)
		})
	}
}

func TestChatGPTUpstreamErrorsSeparateTransportFailureFromLocalBudget(t *testing.T) {
	for _, cause := range []error{errors.New("TEST_ONLY_PRIVATE_NETWORK_DETAIL"), ErrOpenAIProviderAttemptBudgetExhausted} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		_, err := observeChatGPTUpstreamResult(c, &Account{ID: 7}, nil, cause, time.Now())
		require.ErrorIs(t, err, cause)
		if isOpenAIProviderAttemptBudgetError(cause) {
			_, exists := c.Get(OpsUpstreamErrorsKey)
			require.False(t, exists)
		} else {
			events, ok := c.MustGet(OpsUpstreamErrorsKey).([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, events, 1)
			require.Equal(t, "request_error", events[0].Kind)
			require.NotContains(t, events[0].Message, "TEST_ONLY_PRIVATE_NETWORK_DETAIL")
		}
	}
}

func TestChatGPTSuccessAndRedirectResponsesAreNotErrorAttempts(t *testing.T) {
	for _, status := range []int{200, 302, 307} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		body := io.NopCloser(strings.NewReader("TEST_ONLY_UNCHANGED"))
		resp, err := observeChatGPTUpstreamResult(c, &Account{ID: 7}, &http.Response{StatusCode: status, Body: body}, nil, time.Now())
		require.NoError(t, err)
		require.Equal(t, body, resp.Body, "successful streams must not be inspected by error diagnostics")
		_, exists := c.Get(OpsUpstreamErrorsKey)
		require.False(t, exists)
	}
}
