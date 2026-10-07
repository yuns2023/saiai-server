//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeCodexImagesPreserveProtocolAndShareAttemptBudget(t *testing.T) {
	for _, endpoint := range []string{OpenAIImageEndpointGenerations, OpenAIImageEndpointEdits} {
		t.Run(endpoint, func(t *testing.T) {
			svc, cache := attemptBudgetTestService(1)
			c, recorder := auditOAuthPolicyContext("/v1/codex/images/" + endpoint + "?opaque=a%2Fb&opaque=%2B")
			c.Set("api_key", &APIKey{ID: 7})
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header["X-Codex-Future"] = []string{"first", "second"}
			c.Request.Header.Set("X-Codex-Image-Turn-Id", "TEST_ONLY-image-turn")
			c.Request.Header.Set("Cookie", "TEST_ONLY-INBOUND-COOKIE")
			body := []byte(`{ "model":"gpt-image-2", "images":[{"image_url":"data:image/png;base64,VEVTVF9PTkxZ"}], "future":true }`)
			responseBody := `{"created":1,"data":[{"b64_json":"TEST_ONLY","generation_id":"gen_TEST_ONLY"}],"usage":{"input_tokens":13,"input_tokens_details":{"text_tokens":5,"image_tokens":8},"output_tokens":21},"future_response":true}`
			upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(200,
				http.Header{"Content-Type": {"application/json"}, "X-Codex-Imagegen-Request-Id": {"TEST_ONLY-image-request"},
					"X-Codex-Future": {"one", "two"}, "Set-Cookie": {"TEST_ONLY-UPSTREAM-COOKIE"}}, responseBody)}}
			svc.httpUpstream = upstream
			account := auditOAuthPolicyAccount()
			result, err := svc.ForwardNativeCodexImage(context.Background(), c, account, endpoint, body, "gpt-image-2", "auto")
			require.NoError(t, err)
			require.Equal(t, "https://chatgpt.com/backend-api/codex/images/"+endpoint, upstream.reqs[0].URL.Scheme+"://"+upstream.reqs[0].URL.Host+upstream.reqs[0].URL.Path)
			require.Equal(t, "/backend-api/codex/images/"+endpoint, result.UpstreamEndpoint)
			require.Equal(t, "opaque=a%2Fb&opaque=%2B", upstream.reqs[0].URL.RawQuery)
			require.Equal(t, body, upstream.bodies[0])
			require.Equal(t, []string{"first", "second"}, upstream.reqs[0].Header.Values("X-Codex-Future"))
			require.Equal(t, "TEST_ONLY-image-turn", upstream.reqs[0].Header.Get("X-Codex-Image-Turn-Id"))
			require.Equal(t, account.GetChatGPTAccountID(), upstream.reqs[0].Header.Get("Chatgpt-Account-Id"))
			require.Empty(t, upstream.reqs[0].Header.Get("Cookie"))
			require.Nil(t, upstream.reqs[0].GetBody)
			require.True(t, NativeCodexRejectsRedirects(upstream.reqs[0].Context()))
			require.Equal(t, responseBody, recorder.Body.String())
			require.Equal(t, []string{"one", "two"}, recorder.Header().Values("X-Codex-Future"))
			require.Empty(t, recorder.Header().Get("Set-Cookie"))
			require.Equal(t, "TEST_ONLY-image-request", result.RequestID)
			require.Equal(t, "gpt-image-2", result.UpstreamModel)
			require.Equal(t, 1, result.ImageCount)
			require.Equal(t, 5, result.TextInputTokens)
			require.Equal(t, 8, result.ImageInputTokens)
			require.Equal(t, 21, result.ImageOutputTokens)
			_, err = svc.ForwardNativeCodexImage(context.Background(), c, account, endpoint, body, "gpt-image-2", "auto")
			require.ErrorIs(t, err, ErrOpenAIProviderAttemptBudgetExhausted)
			require.Equal(t, 1, upstream.callCount)
			require.Equal(t, int64(1), cache.used)
		})
	}
}

func TestNativeCodexImagesDoNotReplayProviderFailure(t *testing.T) {
	svc, _ := attemptBudgetTestService(5)
	c, recorder := auditOAuthPolicyContext("/v1/codex/images/generations")
	c.Set("api_key", &APIKey{ID: 7})
	body := `{"error":{"code":"TEST_ONLY_image_backend_failure"}}`
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{{StatusCode: 500, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}}}
	svc.httpUpstream = upstream
	_, err := svc.ForwardNativeCodexImage(context.Background(), c, auditOAuthPolicyAccount(), OpenAIImageEndpointGenerations, []byte(`{}`), "gpt-image-2", "auto")
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, err, &failover)
	require.Equal(t, 500, recorder.Code)
	require.Equal(t, body, recorder.Body.String())
	require.Equal(t, 1, upstream.callCount)
}
