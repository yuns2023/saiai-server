package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParseOpenAIImageGenerationRejectsAmbiguousOrUnbillableFields(t *testing.T) {
	rejected := map[string]string{
		`{"model":"gpt-image-2","stream":false,"stream":true}`:          `duplicate "stream" key`,
		`{"model":"gpt-image-2","model":"other-image-model"}`:           `duplicate "model" key`,
		`{"model":"gpt-image-2","size":"1024x1024","size":"1536x1024"}`: `duplicate "size" key`,
		`{"model":"gpt-image-2","service_tier":"priority"}`:             "unsupported service_tier",
		`{"model":"gpt-image-2","service_tier":"ultrafast"}`:            "unsupported service_tier",
		`{"model":"gpt-image-2","service_tier":1}`:                      "unsupported service_tier",
	}
	for body, want := range rejected {
		_, _, err := parseOpenAIImageRequest(service.OpenAIImageEndpointGenerations, "application/json", []byte(body))
		require.ErrorContains(t, err, want, body)
	}
	for _, body := range []string{
		`{"model":"gpt-image-2","stream":false}`,
		`{"model":"gpt-image-2","service_tier":"auto"}`,
		`{"model":"gpt-image-2","service_tier":"Default"}`,
		`{"model":"gpt-image-2","service_tier":null}`,
		`{"model":"gpt-image-2","metadata":{"a":1},"extra":{"a":2}}`,
	} {
		_, _, err := parseOpenAIImageRequest(service.OpenAIImageEndpointGenerations, "application/json", []byte(body))
		require.NoError(t, err, body)
	}
}

func buildOpenAIImageEditMultipart(t *testing.T, fields [][2]string, images int) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, field := range fields {
		require.NoError(t, w.WriteField(field[0], field[1]))
	}
	for i := 0; i < images; i++ {
		part, err := w.CreateFormFile("image[]", "source.png")
		require.NoError(t, err)
		_, err = part.Write([]byte("fake-png-data"))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return body.Bytes(), w.FormDataContentType()
}

func TestParseOpenAIImageEditMultipartRejectsAmbiguousOrUnbillableFields(t *testing.T) {
	cases := map[string][][2]string{
		`duplicate "model" field`:  {{"model", "gpt-image-2"}, {"model", "other"}},
		`duplicate "stream" field`: {{"model", "gpt-image-2"}, {"stream", "false"}, {"stream", "true"}},
		"unsupported service_tier": {{"model", "gpt-image-2"}, {"service_tier", "priority"}},
	}
	for want, fields := range cases {
		body, contentType := buildOpenAIImageEditMultipart(t, fields, 1)
		_, _, err := parseOpenAIImageRequest(service.OpenAIImageEndpointEdits, contentType, body)
		require.ErrorContains(t, err, want)
	}
	// Several input images are legitimate repeated parts.
	body, contentType := buildOpenAIImageEditMultipart(t, [][2]string{{"model", "gpt-image-2"}, {"service_tier", "auto"}}, 3)
	model, _, err := parseOpenAIImageRequest(service.OpenAIImageEndpointEdits, contentType, body)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", model)
}

func serveOpenAIImageAPIKeyForBillingTest(t *testing.T, requestBody, responseBody, contentType string) (*httptest.ResponseRecorder, *chatGPTReplayUpstream, *chatGPTUsageLogCapture) {
	t.Helper()
	groupID := int64(17)
	account := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "TEST_ONLY_API_KEY", "base_url": "https://images.example.test/custom/v1"}}
	upstream := &chatGPTReplayUpstream{responseBody: responseBody, contentType: contentType}
	usageRepo := &chatGPTUsageLogCapture{}
	slots := &chatGPTSlotCache{active: make(map[string]int64), attempts: make(map[int64]int)}
	concurrency := service.NewConcurrencyService(slots)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(billingCache.Stop)
	svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: account}, usageRepo, nil, nil, nil, nil,
		nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billingCache, upstream, &service.DeferredService{}, nil)
	h := NewOpenAIGatewayHandler(svc, concurrency, billingCache, &service.APIKeyService{}, nil, nil, nil, cfg, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader([]byte(requestBody)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 23, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 1}, User: &service.User{ID: 21}})
	c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 21})
	h.ImagesGenerations(c)
	return w, upstream, usageRepo
}

func TestOpenAIImagesRejectsDuplicateStreamBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w, upstream, usageRepo := serveOpenAIImageAPIKeyForBillingTest(t,
		`{"model":"gpt-image-2","prompt":"TEST_ONLY","stream":false,"stream":true}`, "", "")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `duplicate \"stream\" key`)
	require.Zero(t, upstream.calls)
	require.Empty(t, usageRepo.logs)
}

func TestOpenAIImagesBillsUnexpectedSSEAndMissingUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name, responseBody, contentType string
		images, outputTokens            int
	}{
		{
			name:         "sse_with_usage",
			responseBody: "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"P\"}\n\nevent: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"TEST_ONLY\",\"usage\":{\"input_tokens\":7,\"output_tokens\":4160}}\n\n",
			contentType:  "text/event-stream",
			images:       1, outputTokens: 4160,
		},
		{
			name:         "sse_without_usage",
			responseBody: "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"TEST_ONLY\"}\n\n",
			contentType:  "text/event-stream",
			images:       1, outputTokens: 6240,
		},
		{
			name:         "json_without_usage",
			responseBody: `{"data":[{"b64_json":"A"},{"b64_json":"B"}]}`,
			contentType:  "application/json",
			images:       2, outputTokens: 2 * 6240,
		},
		{
			name:         "json_with_usage_mislabelled_as_sse",
			responseBody: `{"data":[{"b64_json":"A"}],"usage":{"input_tokens":5,"output_tokens":21}}`,
			contentType:  "text/event-stream",
			images:       1, outputTokens: 21,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, upstream, usageRepo := serveOpenAIImageAPIKeyForBillingTest(t,
				`{"model":"gpt-image-2","prompt":"TEST_ONLY"}`, tc.responseBody, tc.contentType)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, 1, upstream.calls)
			require.Len(t, usageRepo.logs, 1)
			require.Equal(t, tc.images, usageRepo.logs[0].ImageCount)
			require.Equal(t, tc.outputTokens, usageRepo.logs[0].OutputTokens)
		})
	}
}
