package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpenAIImageFloorTestService() *OpenAIGatewayService {
	imagePrices := &LiteLLMModelPricing{
		InputCostPerToken: 5e-6, OutputCostPerToken: 10e-6,
		InputCostPerImageToken: 8e-6, OutputCostPerImageToken: 30e-6,
	}
	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-image-2":        imagePrices,
		"custom-image-model": imagePrices,
	}}
	return &OpenAIGatewayService{billingService: NewBillingService(&config.Config{}, pricing)}
}

func TestResolveOpenAIImageResponseUsage(t *testing.T) {
	svc := newOpenAIImageFloorTestService()
	cases := []struct {
		name, contentType, body string
		images, input, output   int
	}{
		{name: "json_with_usage", contentType: "application/json",
			body:   `{"data":[{"b64_json":"A"}],"usage":{"input_tokens":5,"output_tokens":21}}`,
			images: 1, input: 5, output: 21},
		{name: "json_without_usage", contentType: "application/json",
			body:   `{"data":[{"b64_json":"A"},{"url":"https://example.test/b.png"}]}`,
			images: 2, output: 2 * openAIImageMissingUsageOutputTokensPerImage},
		{name: "json_input_only_usage_still_bills_output", contentType: "application/json",
			body:   `{"data":[{"b64_json":"A"}],"usage":{"input_tokens":9}}`,
			images: 1, input: 9, output: openAIImageMissingUsageOutputTokensPerImage},
		{name: "json_entries_without_image_data_not_charged", contentType: "application/json",
			body: `{"data":[{"revised_prompt":"x"}]}`},
		{name: "sse_multiline_data_event", contentType: "text/event-stream",
			body:   "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\ndata: \"b64_json\":\"A\",\"usage\":{\"input_tokens\":7,\"output_tokens\":4160}}\n\n",
			images: 1, input: 7, output: 4160},
		{name: "sse_completed_without_image_not_charged", contentType: "text/event-stream",
			body: "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"},
		{name: "sse_image_without_usage", contentType: "text/event-stream",
			body:   "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"P\"}\n\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"A\"}\n\n",
			images: 1, output: openAIImageMissingUsageOutputTokensPerImage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usage, images := svc.resolveOpenAIImageResponseUsage(context.Background(), &Account{ID: 1}, "gpt-image-2", "req", tc.contentType, []byte(tc.body))
			require.Equal(t, tc.images, images)
			require.Equal(t, tc.input, usage.totalInputTokens())
			require.Equal(t, tc.output, usage.ImageOutputTokens)
		})
	}
}

func TestResolveOpenAIImageResponseUsage_SSEPerImageAndAggregate(t *testing.T) {
	svc := newOpenAIImageFloorTestService()
	floor := openAIImageMissingUsageOutputTokensPerImage
	cases := []struct {
		name, body            string
		images, input, output int
	}{
		{name: "per_image_usage_is_summed",
			body:   "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"A\",\"usage\":{\"input_tokens\":5,\"output_tokens\":100}}\n\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"B\",\"usage\":{\"input_tokens\":5,\"output_tokens\":200}}\n\n",
			images: 2, input: 10, output: 300},
		{name: "one_image_without_usage_gets_floor",
			body:   "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"A\",\"usage\":{\"input_tokens\":5,\"output_tokens\":100}}\n\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"B\"}\n\n",
			images: 2, input: 5, output: 100 + floor},
		{name: "aggregate_usage_is_not_double_counted",
			body:   "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"A\",\"usage\":{\"input_tokens\":5,\"output_tokens\":100}}\n\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"B\",\"usage\":{\"input_tokens\":5,\"output_tokens\":200}}\n\ndata: {\"type\":\"response.done\",\"usage\":{\"input_tokens\":10,\"output_tokens\":300}}\n\n",
			images: 2, input: 10, output: 300},
		{name: "aggregate_without_output_floors_every_image",
			body:   "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"A\"}\n\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"B\"}\n\ndata: {\"type\":\"response.done\",\"usage\":{\"input_tokens\":10}}\n\n",
			images: 2, input: 10, output: 2 * floor},
		{name: "input_only_aggregate_keeps_per_image_output",
			body:   "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"A\",\"usage\":{\"input_tokens\":5,\"output_tokens\":100}}\n\ndata: {\"type\":\"response.done\",\"usage\":{\"input_tokens\":12}}\n\n",
			images: 1, input: 12, output: 100},
		{name: "aggregate_data_array_with_uncovered_images_floors_them",
			body:   "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"A\",\"usage\":{\"input_tokens\":5,\"output_tokens\":100}}\n\ndata: {\"type\":\"response.done\",\"data\":[{\"b64_json\":\"A\"},{\"b64_json\":\"B\"},{\"b64_json\":\"C\"}]}\n\n",
			images: 3, input: 5, output: 100 + 2*floor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usage, images := svc.resolveOpenAIImageResponseUsage(context.Background(), nil, "gpt-image-2", "req", "text/event-stream", []byte(tc.body))
			require.Equal(t, tc.images, images)
			require.Equal(t, tc.input, usage.totalInputTokens())
			require.Equal(t, tc.output, usage.ImageOutputTokens)
		})
	}
}

func TestResolveOpenAIImageResponseUsage_FloorOnlyForOfficialGPTImagePricing(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()
	svc := newOpenAIImageFloorTestService()
	body := []byte(`{"data":[{"b64_json":"A"}]}`)

	usage, images := svc.resolveOpenAIImageResponseUsage(context.Background(), &Account{ID: 9}, "custom-image-model", "req_custom", "application/json", body)
	require.Equal(t, 1, images)
	require.Zero(t, usage.ImageOutputTokens, "a compatible model keeps its previous billing")
	require.True(t, logSink.ContainsMessageAtLevel("output not billed", "warn"))
	require.True(t, logSink.ContainsFieldValue("upstream_request_id", "req_custom"))

	usage, _ = svc.resolveOpenAIImageResponseUsage(context.Background(), &Account{ID: 9}, "gpt-image-2", "req_official", "application/json", body)
	require.Equal(t, openAIImageMissingUsageOutputTokensPerImage, usage.ImageOutputTokens)
	require.True(t, logSink.ContainsMessageAtLevel("conservative gpt-image floor", "warn"))

	// Without image token pricing the floor never applies.
	usage, _ = (&OpenAIGatewayService{}).resolveOpenAIImageResponseUsage(context.Background(), nil, "gpt-image-2", "req", "application/json", body)
	require.Zero(t, usage.ImageOutputTokens)
}

func TestOpenAIImagesForwardOAuthMissingUsageBillsFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openAIImageUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(bytes.NewBufferString(
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2U=\",\"output_format\":\"png\"}]}}\n\ndata: [DONE]\n\n",
		)),
	}}
	svc := newOpenAIImageFloorTestService()
	svc.httpUpstream = upstream
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"access_token": "oauth-secret", "chatgpt_account_id": "acct-test",
	}}
	result, err := svc.ForwardImage(c.Request.Context(), c, account, OpenAIImageEndpointGenerations,
		[]byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`), "gpt-image-2", "1024x1024")
	require.NoError(t, err)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, openAIImageMissingUsageOutputTokensPerImage, result.ImageOutputTokens)
	require.Equal(t, openAIImageMissingUsageOutputTokensPerImage, result.Usage.OutputTokens)
}
