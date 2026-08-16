package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIImageUpstreamRecorder struct {
	req  *http.Request
	body []byte
	resp *http.Response
}

func (u *openAIImageUpstreamRecorder) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.req = req
	u.body, _ = io.ReadAll(req.Body)
	return u.resp, nil
}

func (u *openAIImageUpstreamRecorder) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ bool) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestOpenAIImagesForwardPreservesBodyAndUsesCustomBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	responseBody := `{"created":1,"data":[{"b64_json":"abc"}],"usage":{"input_tokens":13,"input_tokens_details":{"text_tokens":5,"image_tokens":8},"output_tokens":21}}`
	upstream := &openAIImageUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"img_req_1"}},
		Body:       io.NopCloser(bytes.NewBufferString(responseBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}, Gateway: config.GatewayConfig{OpenAIImagesResponseReadMaxBytes: 1024}},
		httpUpstream: upstream,
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Authorization", "Bearer inbound-secret")
	c.Request.Header.Set("OpenAI-Organization", "org_test")
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 2, Credentials: map[string]any{
		"api_key": "upstream-secret", "base_url": "https://images.example.test/openai/v1",
	}}
	requestBody := []byte(`{"model":"gpt-image-2","prompt":"draw it"}`)

	result, err := svc.ForwardImage(c.Request.Context(), c, account, OpenAIImageEndpointGenerations, requestBody, "gpt-image-2", "1024x1024")
	require.NoError(t, err)
	require.Equal(t, "https://images.example.test/openai/v1/images/generations", upstream.req.URL.String())
	require.Equal(t, requestBody, upstream.body)
	require.Equal(t, "Bearer upstream-secret", upstream.req.Header.Get("Authorization"))
	require.Equal(t, "org_test", upstream.req.Header.Get("OpenAI-Organization"))
	require.Equal(t, 5, result.TextInputTokens)
	require.Equal(t, 8, result.ImageInputTokens)
	require.Equal(t, 21, result.ImageOutputTokens)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, responseBody, recorder.Body.String())
}

func TestOpenAIImagesForwardOAuthBridgesResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openAIImageUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"oauth_img_1"}},
		Body: io.NopCloser(bytes.NewBufferString(
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"tools\":[{\"type\":\"image_generation\",\"model\":\"gpt-image-2\",\"quality\":\"auto\",\"size\":\"1024x1024\"}],\"tool_usage\":{\"image_gen\":{\"input_tokens\":46,\"output_tokens\":2459,\"output_tokens_details\":{\"image_tokens\":2459}}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2U=\",\"revised_prompt\":\"draw a cat\",\"output_format\":\"png\",\"model\":\"gpt-image-2-codex\"}]}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"access_token": "oauth-secret", "chatgpt_account_id": "acct-test",
	}}

	disconnectedCtx, cancel := context.WithCancel(c.Request.Context())
	cancel()
	result, err := svc.ForwardImage(disconnectedCtx, c, account, OpenAIImageEndpointGenerations,
		[]byte(`{"model":"gpt-image-2","prompt":"draw a cat","quality":"high","size":"1024x1024"}`),
		"gpt-image-2", "1024x1024")
	require.NoError(t, err)
	require.Equal(t, chatgptCodexURL, upstream.req.URL.String())
	require.Equal(t, "chatgpt.com", upstream.req.Host)
	require.Equal(t, "Bearer oauth-secret", upstream.req.Header.Get("Authorization"))
	require.Equal(t, "acct-test", upstream.req.Header.Get("chatgpt-account-id"))
	require.Equal(t, "opencode", upstream.req.Header.Get("originator"))
	require.NotEmpty(t, upstream.req.Header.Get("conversation_id"))
	require.Equal(t, upstream.req.Header.Get("conversation_id"), upstream.req.Header.Get("session_id"))
	require.Equal(t, "application/json", upstream.req.Header.Get("Content-Type"))
	require.Equal(t, openAIImageOAuthMainModel, gjson.GetBytes(upstream.body, "model").String())
	require.Equal(t, "image_generation", gjson.GetBytes(upstream.body, "tools.0.type").String())
	require.Equal(t, "generate", gjson.GetBytes(upstream.body, "tools.0.action").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.body, "tools.0.model").String())
	require.Equal(t, "high", gjson.GetBytes(upstream.body, "tools.0.quality").String())
	require.Equal(t, "draw a cat", gjson.GetBytes(upstream.body, "input.0.content.0.text").String())
	require.Equal(t, 46, result.TextInputTokens)
	require.Equal(t, 2459, result.ImageOutputTokens)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, "gpt-image-2-codex", gjson.Get(recorder.Body.String(), "model").String())
	require.Equal(t, "aW1hZ2U=", gjson.Get(recorder.Body.String(), "data.0.b64_json").String())
}

func TestParseOpenAIImageOAuthUsageSplitsLifecycleImageInput(t *testing.T) {
	response := gjson.Parse(`{
		"usage":{"input_tokens":46,"input_tokens_details":{"text_tokens":35,"image_tokens":11}},
		"tool_usage":{"image_gen":{"input_tokens":46,"output_tokens":2459,"output_tokens_details":{"image_tokens":2459}}}
	}`)

	usage, raw := parseOpenAIImageOAuthUsage(response)
	require.Equal(t, 35, usage.TextInputTokens)
	require.Equal(t, 11, usage.ImageInputTokens)
	require.Equal(t, 2459, usage.ImageOutputTokens)
	require.EqualValues(t, 46, raw["input_tokens"])
}

func TestOpenAIImagesForwardOAuthReturnsUserError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openAIImageUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(bytes.NewBufferString(
			"data: {\"type\":\"error\",\"error\":{\"type\":\"image_generation_user_error\",\"code\":\"content_policy_violation\",\"message\":\"request rejected\"}}\n\n",
		)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"access_token": "oauth-secret", "chatgpt_account_id": "acct-test",
	}}

	result, err := svc.ForwardImage(c.Request.Context(), c, account, OpenAIImageEndpointGenerations,
		[]byte(`{"model":"gpt-image-2","prompt":"draw it"}`), "gpt-image-2", "")
	require.Nil(t, result)
	require.ErrorContains(t, err, "request rejected")
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "content_policy_violation", gjson.Get(recorder.Body.String(), "error.code").String())
	require.Empty(t, gjson.Get(recorder.Body.String(), "data").Array())
}

func TestParseOpenAIImageOAuthSSEClassifiesContentFilterIncomplete(t *testing.T) {
	parsed := parseOpenAIImageOAuthSSE([]byte(
		"data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"content_filter\"}}}\n\n",
	))
	require.Equal(t, "image_generation_user_error", parsed.ErrorType)
	require.Equal(t, "response_incomplete", parsed.ErrorCode)
	require.Contains(t, parsed.ErrorText, "content_filter")
}

func TestForEachOpenAIImageSSEPayloadJoinsDataLines(t *testing.T) {
	var payloads []string
	forEachOpenAIImageSSEPayload([]byte("event: response.completed\ndata: {\"type\":\ndata: \"response.completed\"}\n\ndata: [DONE]\n\n"), func(payload []byte) {
		payloads = append(payloads, string(payload))
	})
	require.Equal(t, []string{"{\"type\":\n\"response.completed\"}"}, payloads)
}

func TestBoundedOpenAIImageJSONNonNegativeInt(t *testing.T) {
	value, ok := boundedOpenAIImageJSONNonNegativeInt(gjson.Parse(`2.459e3`))
	require.True(t, ok)
	require.Equal(t, 2459, value)
	_, ok = boundedOpenAIImageJSONNonNegativeInt(gjson.Parse(`-1`))
	require.False(t, ok)
	_, ok = boundedOpenAIImageJSONNonNegativeInt(gjson.Parse(`1.5`))
	require.False(t, ok)
	_, ok = boundedOpenAIImageJSONNonNegativeInt(gjson.Parse(`1e1000`))
	require.False(t, ok)
}

func TestBuildOpenAIImageOAuthResponsesRequestForEdit(t *testing.T) {
	parsed := &openAIImageOAuthRequest{
		Prompt: "replace the sky", N: 1, OutputFormat: "webp", InputFidelity: "high",
		Images: []openAIImageOAuthUpload{{FileName: "source.png", ContentType: "image/png", Data: []byte("source")}},
		Mask:   &openAIImageOAuthUpload{FileName: "mask.png", ContentType: "image/png", Data: []byte("mask")},
	}
	body, err := buildOpenAIImageOAuthResponsesRequest(parsed, "gpt-image-2", OpenAIImageEndpointEdits)
	require.NoError(t, err)
	require.Equal(t, "edit", gjson.GetBytes(body, "tools.0.action").String())
	require.Equal(t, "webp", gjson.GetBytes(body, "tools.0.output_format").String())
	require.False(t, gjson.GetBytes(body, "tools.0.input_fidelity").Exists())
	require.Contains(t, gjson.GetBytes(body, "input.0.content.1.image_url").String(), "data:image/png;base64,")
	require.Contains(t, gjson.GetBytes(body, "tools.0.input_image_mask.image_url").String(), "data:image/png;base64,")
}

func TestBuildOpenAIImageOAuthResponsesRequestOmitsUnsupportedDallE3N(t *testing.T) {
	body, err := buildOpenAIImageOAuthResponsesRequest(&openAIImageOAuthRequest{Prompt: "draw it", N: 2}, "dall-e-3", OpenAIImageEndpointGenerations)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "tools.0.n").Exists())
}

func TestOpenAIImagesForwardBoundsResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openAIImageUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString("12345")),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}, Gateway: config.GatewayConfig{OpenAIImagesResponseReadMaxBytes: 4}},
		httpUpstream: upstream,
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key"}}

	_, err := svc.ForwardImage(c.Request.Context(), c, account, OpenAIImageEndpointGenerations, []byte(`{}`), "gpt-image-2", "")
	require.ErrorContains(t, err, "exceeds 4 bytes")
	require.Empty(t, recorder.Body.String())
}

func TestCalculateImageTokenCostUsesSeparateModalities(t *testing.T) {
	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-image-2": {
			InputCostPerToken: 5e-6, OutputCostPerToken: 10e-6,
			InputCostPerImageToken: 8e-6, OutputCostPerImageToken: 30e-6,
		},
	}}
	billing := NewBillingService(&config.Config{}, pricing)
	cost, err := billing.CalculateImageTokenCost("gpt-image-2", ImageUsageTokens{
		TextInputTokens: 10, ImageInputTokens: 20, ImageOutputTokens: 30,
	}, 1.5)
	require.NoError(t, err)
	require.InDelta(t, 0.00021, cost.InputCost, 1e-12)
	require.InDelta(t, 0.0009, cost.OutputCost, 1e-12)
	require.InDelta(t, 0.00111, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.001665, cost.ActualCost, 1e-12)
}

func TestCalculateImageTokenCostClassifiesIncompletePricing(t *testing.T) {
	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"custom-image-model": {InputCostPerToken: 5e-6, OutputCostPerToken: 10e-6},
	}}
	billing := NewBillingService(&config.Config{}, pricing)
	_, err := billing.CalculateImageTokenCost("custom-image-model", ImageUsageTokens{ImageOutputTokens: 1}, 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrModelPricingUnavailable))
}
