package service

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
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

func TestOpenAIImagesForwardRejectsOAuthWithoutCallingUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openAIImageUpstreamRecorder{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	_, err := svc.ForwardImage(c.Request.Context(), c, account, OpenAIImageEndpointGenerations, []byte(`{}`), "gpt-image-2", "")
	require.ErrorContains(t, err, "API-key account")
	require.Nil(t, upstream.req)
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
