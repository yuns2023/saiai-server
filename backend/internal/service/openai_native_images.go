package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ForwardNativeCodexImage preserves the standalone image_gen protocol used by
// official Codex. It does not use the public Images-to-Responses adapter.
func (s *OpenAIGatewayService) ForwardNativeCodexImage(
	ctx context.Context, c *gin.Context, account *Account, endpoint string, body []byte, model, imageSize string,
) (*OpenAIForwardResult, error) {
	if account == nil || !account.IsOpenAIOAuth() || account.IsOpenAICodexNativeRelay() {
		return nil, fmt.Errorf("native Codex images require an OpenAI OAuth account")
	}
	if c == nil || c.Request == nil || !openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) {
		return nil, fmt.Errorf("native Codex images require the official Codex client")
	}
	if endpoint != OpenAIImageEndpointGenerations && endpoint != OpenAIImageEndpointEdits {
		return nil, fmt.Errorf("unsupported native Codex image endpoint")
	}
	ctx = s.withOpenAIProviderAttemptBudget(ctx, c, account)
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(strings.TrimSuffix(chatgptCodexURL, "/responses") + "/images/" + endpoint)
	if err != nil {
		return nil, err
	}
	target.RawQuery = c.Request.URL.RawQuery
	req, err := http.NewRequestWithContext(WithNativeCodexRedirectPolicy(ctx), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyOpenAINativeRequestHeaders(req.Header, c.Request.Header, false)
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	if accountID := account.GetChatGPTAccountID(); accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
	if err := s.validateOpenAINativeTurnState(account, getOpenAIUserIDFromContext(c), s.openAINativeResponseSessionHash(c), c.Request.Header); err != nil {
		return nil, err
	}
	// Image generation is not replay-safe. Preserve any supplied idempotency
	// header but do not allow Transport to replay the POST body implicitly.
	req.GetBody = nil
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	started := time.Now()
	resp, err := s.doOpenAIUpstream(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, fmt.Errorf("native Codex image request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := s.readOpenAIImageResponse(resp.Body)
	if err != nil {
		return nil, err
	}
	s.writeOpenAINativeResponseHeaders(c.Writer.Header(), resp.Header)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, responseBody)
	if resp.StatusCode >= http.StatusBadRequest {
		// Return the provider's actual error once. Do not automatically repeat
		// a possibly accepted image job on this or another pooled account.
		return nil, fmt.Errorf("native Codex image upstream returned status %d", resp.StatusCode)
	}
	usage := parseOpenAIImageUsage(responseBody)
	requestID := resp.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = resp.Header.Get("X-Codex-Imagegen-Request-Id")
	}
	return &OpenAIForwardResult{
		RequestID: requestID, Usage: OpenAIUsage{InputTokens: usage.totalInputTokens(), OutputTokens: usage.ImageOutputTokens},
		Model: model, BillingModel: model, UpstreamModel: model, ResponseHeaders: resp.Header.Clone(), Duration: time.Since(started),
		TextInputTokens: usage.TextInputTokens, ImageInputTokens: usage.ImageInputTokens, ImageOutputTokens: usage.ImageOutputTokens,
		ImageCount: len(gjson.GetBytes(responseBody, "data").Array()), ImageSize: imageSize, MediaType: "image",
	}, nil
}
