package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	OpenAIImageEndpointGenerations = "generations"
	OpenAIImageEndpointEdits       = "edits"
	defaultOpenAIImageResponseMax  = int64(64 * 1024 * 1024)
)

// ValidateOpenAIImagePricing is the financial circuit breaker for direct image
// requests. Model IDs are not allowlisted, but standard billing requires all
// three token price categories before an upstream request can incur cost.
func (s *OpenAIGatewayService) ValidateOpenAIImagePricing(model string) error {
	if s != nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil
	}
	if s == nil || s.billingService == nil {
		return fmt.Errorf("image billing service is unavailable")
	}
	pricing, err := s.billingService.GetModelPricing(model)
	if err != nil {
		return err
	}
	if pricing.InputPricePerToken <= 0 || pricing.InputImagePricePerToken <= 0 || pricing.OutputImagePricePerToken <= 0 {
		return fmt.Errorf("image token pricing incomplete for model: %s", model)
	}
	return nil
}

// ForwardImage forwards a direct OpenAI Image API request without translating
// its JSON or multipart body. The first implementation deliberately supports
// API-key accounts only; ChatGPT OAuth is a different, undocumented protocol.
func (s *OpenAIGatewayService) ForwardImage(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	endpoint string,
	body []byte,
	model string,
	imageSize string,
) (*OpenAIForwardResult, error) {
	if account == nil || !account.IsOpenAIApiKey() {
		return nil, fmt.Errorf("OpenAI Image API requires an OpenAI API-key account")
	}
	if endpoint != OpenAIImageEndpointGenerations && endpoint != OpenAIImageEndpointEdits {
		return nil, fmt.Errorf("unsupported OpenAI Image API endpoint: %s", endpoint)
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	targetURL, err := s.buildOpenAIImageURL(account.GetOpenAIBaseURL(), endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, values := range c.Request.Header {
		if !shouldCopyOpenAIRequestHeader(key) {
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Authorization", "Bearer "+token)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	started := time.Now()
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, fmt.Errorf("OpenAI Image API upstream request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	maxBytes := defaultOpenAIImageResponseMax
	if s.cfg != nil && s.cfg.Gateway.OpenAIImagesResponseReadMaxBytes > 0 {
		maxBytes = s.cfg.Gateway.OpenAIImagesResponseReadMaxBytes
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read OpenAI Image API response: %w", err)
	}
	if int64(len(responseBody)) > maxBytes {
		return nil, fmt.Errorf("OpenAI Image API response exceeds %d bytes", maxBytes)
	}

	if resp.StatusCode >= http.StatusBadRequest && s.shouldFailoverOpenAIUpstreamResponse(
		resp.StatusCode,
		extractUpstreamErrorMessage(responseBody),
		responseBody,
	) {
		return nil, &UpstreamFailoverError{
			StatusCode:      resp.StatusCode,
			ResponseBody:    responseBody,
			ResponseHeaders: resp.Header.Clone(),
			Kind:            classifyUpstreamFailure(account.Platform, resp.StatusCode, responseBody),
		}
	}

	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, responseBody)
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("OpenAI Image API upstream returned status %d", resp.StatusCode)
	}

	usage := parseOpenAIImageUsage(responseBody)
	return &OpenAIForwardResult{
		RequestID:         resp.Header.Get("x-request-id"),
		Usage:             OpenAIUsage{InputTokens: usage.totalInputTokens(), OutputTokens: usage.ImageOutputTokens},
		Model:             model,
		BillingModel:      model,
		UpstreamModel:     model,
		ResponseHeaders:   resp.Header.Clone(),
		Duration:          time.Since(started),
		TextInputTokens:   usage.TextInputTokens,
		ImageInputTokens:  usage.ImageInputTokens,
		ImageOutputTokens: usage.ImageOutputTokens,
		ImageCount:        len(gjson.GetBytes(responseBody, "data").Array()),
		ImageSize:         imageSize,
		MediaType:         "image",
	}, nil
}

func (s *OpenAIGatewayService) buildOpenAIImageURL(baseURL, endpoint string) (string, error) {
	validated, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(strings.TrimSpace(validated), "/")
	suffix := "/images/" + endpoint
	if strings.HasSuffix(base, suffix) {
		return base, nil
	}
	if strings.HasSuffix(base, "/v1") {
		return base + suffix, nil
	}
	return base + "/v1" + suffix, nil
}

type openAIImageUsage struct {
	TextInputTokens   int
	ImageInputTokens  int
	ImageOutputTokens int
}

func (u openAIImageUsage) totalInputTokens() int {
	return u.TextInputTokens + u.ImageInputTokens
}

func parseOpenAIImageUsage(body []byte) openAIImageUsage {
	usage := openAIImageUsage{
		TextInputTokens:   int(gjson.GetBytes(body, "usage.input_tokens_details.text_tokens").Int()),
		ImageInputTokens:  int(gjson.GetBytes(body, "usage.input_tokens_details.image_tokens").Int()),
		ImageOutputTokens: int(gjson.GetBytes(body, "usage.output_tokens_details.image_tokens").Int()),
	}
	if usage.ImageOutputTokens == 0 {
		usage.ImageOutputTokens = int(gjson.GetBytes(body, "usage.output_tokens").Int())
	}
	if usage.totalInputTokens() == 0 {
		// Custom OpenAI-compatible upstreams may expose only the aggregate field.
		usage.TextInputTokens = int(gjson.GetBytes(body, "usage.input_tokens").Int())
	}
	return usage
}
