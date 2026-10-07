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

func TestOpenAIImagesUsageRecordsActualProviderEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path, accountType, upstreamPath string
		native                                bool
	}{
		{"native_generation", "/v1/codex/images/generations", service.AccountTypeOAuth, "/backend-api/codex/images/generations", true},
		{"native_edit", "/v1/codex/images/edits", service.AccountTypeOAuth, "/backend-api/codex/images/edits", true},
		{"api_key_custom_base", "/v1/images/generations", service.AccountTypeAPIKey, "/custom/v1/images/generations", false},
		{"oauth_compatibility", "/v1/images/generations", service.AccountTypeOAuth, "/backend-api/codex/responses", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groupID := int64(17)
			account := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: tc.accountType,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"access_token": "TEST_ONLY_OAUTH", "chatgpt_account_id": "TEST_ONLY_ACCOUNT",
					"api_key": "TEST_ONLY_API_KEY", "base_url": "https://images.example.test/custom/v1"}}
			responseBody := `{"data":[{"b64_json":"TEST_ONLY"}],"usage":{"input_tokens":5,"output_tokens":21}}`
			if !tc.native && tc.accountType == service.AccountTypeOAuth {
				responseBody = "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"image_generation_call\",\"result\":\"TEST_ONLY\"}],\"tool_usage\":{\"image_gen\":{\"input_tokens\":5,\"output_tokens\":21}}}}\n\n"
			}
			upstream := &chatGPTReplayUpstream{responseBody: responseBody}
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
			body := []byte(`{ "model":"gpt-image-2", "prompt":"TEST_ONLY", "future_extension":true }`)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path+"?opaque=TEST_ONLY", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			if tc.native {
				c.Request.Header.Set("User-Agent", "codex_cli_rs/0.160.0")
				c.Request.Header.Set("originator", "codex_cli_rs")
			}
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 23, GroupID: &groupID,
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 1}, User: &service.User{ID: 21}})
			c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 21})
			switch {
			case tc.name == "native_edit":
				h.CodexImagesEdits(c)
			case tc.native:
				h.CodexImagesGenerations(c)
			default:
				h.ImagesGenerations(c)
			}
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Len(t, usageRepo.logs, 1)
			require.Equal(t, tc.upstreamPath, upstream.req.URL.Path)
			require.Equal(t, tc.upstreamPath, *usageRepo.logs[0].UpstreamEndpoint)
			require.Equal(t, tc.path, *usageRepo.logs[0].InboundEndpoint)
			require.Equal(t, "gpt-image-2", usageRepo.logs[0].Model)
			require.Equal(t, 1, usageRepo.logs[0].ImageCount)
			require.Empty(t, slots.active)
			if tc.native || tc.accountType == service.AccountTypeAPIKey {
				require.Equal(t, body, upstream.body)
			}
		})
	}
}

func TestParseOpenAIImageGenerationRequest(t *testing.T) {
	model, size, err := parseOpenAIImageRequest(service.OpenAIImageEndpointGenerations, "application/json; charset=utf-8", []byte(`{
		"model":"custom/image-model-v2","prompt":"hello","unknown_future_field":{"keep":true},"size":"1024x1536"
	}`))
	require.NoError(t, err)
	require.Equal(t, "custom/image-model-v2", model)
	require.Equal(t, "1024x1536", size)
}

func TestParseOpenAIImageGenerationRejectsStreaming(t *testing.T) {
	_, _, err := parseOpenAIImageRequest(service.OpenAIImageEndpointGenerations, "application/json", []byte(`{"model":"gpt-image-2","stream":true}`))
	require.ErrorContains(t, err, "streaming is not supported")
}

func TestParseOpenAIImageEditMultipart(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("model", "gpt-image-2"))
	require.NoError(t, w.WriteField("size", "1536x1024"))
	imagePart, err := w.CreateFormFile("image[]", "source.png")
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("fake-png-data"))
	require.NoError(t, err)
	require.NoError(t, w.WriteField("future_field", "preserved"))
	require.NoError(t, w.Close())

	model, size, err := parseOpenAIImageRequest(service.OpenAIImageEndpointEdits, w.FormDataContentType(), body.Bytes())
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", model)
	require.Equal(t, "1536x1024", size)
}
