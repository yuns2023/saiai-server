package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
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
	openAIImageOAuthMainModel      = "gpt-5.4-mini"
	openAIImageOAuthUpstreamMax    = 10 * time.Minute
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

// ForwardImage keeps the native Images API body intact for API-key accounts and
// bridges the same public contract to Codex Responses image_generation for
// OAuth accounts. The OAuth path is intentionally non-streaming to the client,
// even though the internal Codex response is SSE.
func (s *OpenAIGatewayService) ForwardImage(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	endpoint string,
	body []byte,
	model string,
	imageSize string,
) (*OpenAIForwardResult, error) {
	if account == nil || !account.IsOpenAI() {
		return nil, fmt.Errorf("OpenAI Image API requires an OpenAI account")
	}
	if endpoint != OpenAIImageEndpointGenerations && endpoint != OpenAIImageEndpointEdits {
		return nil, fmt.Errorf("unsupported OpenAI Image API endpoint: %s", endpoint)
	}
	switch {
	case account.IsOpenAIApiKey():
		return s.forwardOpenAIImageAPIKey(ctx, c, account, endpoint, body, model, imageSize)
	case account.IsOpenAIOAuth():
		return s.forwardOpenAIImageOAuth(ctx, c, account, endpoint, body, model, imageSize)
	default:
		return nil, fmt.Errorf("unsupported OpenAI account type: %s", account.Type)
	}
}

func (s *OpenAIGatewayService) forwardOpenAIImageAPIKey(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	endpoint string,
	body []byte,
	model string,
	imageSize string,
) (*OpenAIForwardResult, error) {
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

	responseBody, err := s.readOpenAIImageResponse(resp.Body)
	if err != nil {
		return nil, err
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

type openAIImageOAuthRequest struct {
	Prompt            string
	N                 int
	Size              string
	Quality           string
	Background        string
	OutputFormat      string
	ResponseFormat    string
	Moderation        string
	Style             string
	InputFidelity     string
	OutputCompression *int
	PartialImages     *int
	Images            []openAIImageOAuthUpload
	Mask              *openAIImageOAuthUpload
}

type openAIImageOAuthUpload struct {
	FileName    string
	ContentType string
	Data        []byte
}

type openAIImageOAuthResult struct {
	Result        string
	RevisedPrompt string
	OutputFormat  string
	Size          string
	Background    string
	Quality       string
	Model         string
}

type openAIImageOAuthParsedResponse struct {
	Results   []openAIImageOAuthResult
	CreatedAt int64
	Usage     openAIImageUsage
	UsageRaw  map[string]any
	Meta      openAIImageOAuthResult
	ErrorType string
	ErrorCode string
	ErrorText string
}

func (s *OpenAIGatewayService) forwardOpenAIImageOAuth(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	endpoint string,
	body []byte,
	model string,
	imageSize string,
) (*OpenAIForwardResult, error) {
	parsed, err := parseOpenAIImageOAuthRequest(endpoint, c.GetHeader("Content-Type"), body)
	if err != nil {
		return nil, err
	}
	upstreamCtx := context.Background()
	if ctx != nil {
		upstreamCtx = context.WithoutCancel(ctx)
	}
	upstreamCtx, cancelUpstream := context.WithTimeout(upstreamCtx, openAIImageOAuthUpstreamMax)
	defer cancelUpstream()

	token, _, err := s.GetAccessToken(upstreamCtx, account)
	if err != nil {
		return nil, err
	}
	responsesBody, err := buildOpenAIImageOAuthResponsesRequest(parsed, model, endpoint)
	if err != nil {
		return nil, err
	}
	promptCacheKey := strings.Join([]string{"openai-images", endpoint, model, imageSize, parsed.Prompt}, "|")
	upstreamReq, err := s.buildUpstreamRequest(upstreamCtx, c, account, responsesBody, token, true, promptCacheKey, false)
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "text/event-stream")

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	started := time.Now()
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, fmt.Errorf("OpenAI OAuth image upstream request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	responseBody, err := s.readOpenAIImageResponse(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		if s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, extractUpstreamErrorMessage(responseBody), responseBody) {
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
		return nil, fmt.Errorf("OpenAI OAuth image upstream returned status %d", resp.StatusCode)
	}

	parsedResponse := parseOpenAIImageOAuthSSE(responseBody)
	if len(parsedResponse.Results) == 0 {
		if isOpenAIImageOAuthUserError(parsedResponse.ErrorType, parsedResponse.ErrorCode) {
			message := sanitizeUpstreamErrorMessage(parsedResponse.ErrorText)
			if message == "" {
				message = "Image generation was rejected by the upstream service"
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type": parsedResponse.ErrorType, "code": parsedResponse.ErrorCode, "message": message,
			}})
			return nil, fmt.Errorf("OpenAI OAuth image request rejected: %s", message)
		}
		return nil, &UpstreamFailoverError{
			StatusCode:      http.StatusBadGateway,
			ResponseBody:    []byte(`{"error":{"type":"upstream_error","message":"upstream did not return image output"}}`),
			ResponseHeaders: resp.Header.Clone(),
			Kind:            UpstreamFailureNone,
		}
	}

	clientBody, err := buildOpenAIImageOAuthAPIResponse(parsedResponse, parsed.ResponseFormat)
	if err != nil {
		return nil, err
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	c.Data(http.StatusOK, "application/json; charset=utf-8", clientBody)

	actualSize := strings.TrimSpace(parsedResponse.Meta.Size)
	if actualSize == "" {
		actualSize = imageSize
	}
	return &OpenAIForwardResult{
		RequestID:         resp.Header.Get("x-request-id"),
		Usage:             OpenAIUsage{InputTokens: parsedResponse.Usage.totalInputTokens(), OutputTokens: parsedResponse.Usage.ImageOutputTokens},
		Model:             model,
		BillingModel:      model,
		UpstreamModel:     model,
		ResponseHeaders:   resp.Header.Clone(),
		Duration:          time.Since(started),
		TextInputTokens:   parsedResponse.Usage.TextInputTokens,
		ImageInputTokens:  parsedResponse.Usage.ImageInputTokens,
		ImageOutputTokens: parsedResponse.Usage.ImageOutputTokens,
		ImageCount:        len(parsedResponse.Results),
		ImageSize:         actualSize,
		MediaType:         "image",
	}, nil
}

func (s *OpenAIGatewayService) readOpenAIImageResponse(body io.Reader) ([]byte, error) {
	maxBytes := defaultOpenAIImageResponseMax
	if s.cfg != nil && s.cfg.Gateway.OpenAIImagesResponseReadMaxBytes > 0 {
		maxBytes = s.cfg.Gateway.OpenAIImagesResponseReadMaxBytes
	}
	responseBody, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read OpenAI Image API response: %w", err)
	}
	if int64(len(responseBody)) > maxBytes {
		return nil, fmt.Errorf("OpenAI Image API response exceeds %d bytes", maxBytes)
	}
	return responseBody, nil
}

func parseOpenAIImageOAuthRequest(endpoint, contentType string, body []byte) (*openAIImageOAuthRequest, error) {
	parsed := &openAIImageOAuthRequest{N: 1}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, fmt.Errorf("invalid Content-Type")
	}
	if endpoint == OpenAIImageEndpointGenerations {
		if mediaType != "application/json" || !gjson.ValidBytes(body) {
			return nil, fmt.Errorf("image generations requires a valid application/json body")
		}
		parsed.Prompt = strings.TrimSpace(gjson.GetBytes(body, "prompt").String())
		parsed.Size = strings.TrimSpace(gjson.GetBytes(body, "size").String())
		parsed.Quality = strings.TrimSpace(gjson.GetBytes(body, "quality").String())
		parsed.Background = strings.TrimSpace(gjson.GetBytes(body, "background").String())
		parsed.OutputFormat = strings.TrimSpace(gjson.GetBytes(body, "output_format").String())
		parsed.ResponseFormat = strings.TrimSpace(gjson.GetBytes(body, "response_format").String())
		parsed.Moderation = strings.TrimSpace(gjson.GetBytes(body, "moderation").String())
		parsed.Style = strings.TrimSpace(gjson.GetBytes(body, "style").String())
		if n := gjson.GetBytes(body, "n"); n.Exists() {
			parsed.N = int(n.Int())
		}
		parsed.OutputCompression = optionalOpenAIImageInt(gjson.GetBytes(body, "output_compression"))
		parsed.PartialImages = optionalOpenAIImageInt(gjson.GetBytes(body, "partial_images"))
	} else {
		if endpoint != OpenAIImageEndpointEdits || mediaType != "multipart/form-data" || params["boundary"] == "" {
			return nil, fmt.Errorf("image edits requires multipart/form-data")
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				return nil, fmt.Errorf("invalid multipart body")
			}
			name := strings.TrimSpace(part.FormName())
			fileName := part.FileName()
			partContentType := part.Header.Get("Content-Type")
			value, readErr := io.ReadAll(part)
			_ = part.Close()
			if readErr != nil {
				return nil, fmt.Errorf("read multipart field %q: %w", name, readErr)
			}
			if name == "image" || name == "image[]" {
				parsed.Images = append(parsed.Images, openAIImageOAuthUpload{
					FileName: fileName, ContentType: partContentType, Data: value,
				})
				continue
			}
			if name == "mask" {
				parsed.Mask = &openAIImageOAuthUpload{
					FileName: fileName, ContentType: partContentType, Data: value,
				}
				continue
			}
			setOpenAIImageOAuthFormField(parsed, name, strings.TrimSpace(string(value)))
		}
	}
	if parsed.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if parsed.N <= 0 || parsed.N > 10 {
		return nil, fmt.Errorf("n must be between 1 and 10")
	}
	if endpoint == OpenAIImageEndpointEdits && len(parsed.Images) == 0 {
		return nil, fmt.Errorf("image input is required")
	}
	return parsed, nil
}

func optionalOpenAIImageInt(value gjson.Result) *int {
	if !value.Exists() || value.Type != gjson.Number {
		return nil
	}
	parsed := int(value.Int())
	return &parsed
}

func setOpenAIImageOAuthFormField(parsed *openAIImageOAuthRequest, name, value string) {
	if parsed == nil {
		return
	}
	switch name {
	case "prompt":
		parsed.Prompt = value
	case "n":
		if n, err := strconv.Atoi(value); err == nil {
			parsed.N = n
		} else {
			parsed.N = 0
		}
	case "size":
		parsed.Size = value
	case "quality":
		parsed.Quality = value
	case "background":
		parsed.Background = value
	case "output_format":
		parsed.OutputFormat = value
	case "response_format":
		parsed.ResponseFormat = value
	case "moderation":
		parsed.Moderation = value
	case "style":
		parsed.Style = value
	case "input_fidelity":
		parsed.InputFidelity = value
	case "output_compression":
		if n, err := strconv.Atoi(value); err == nil {
			parsed.OutputCompression = &n
		}
	case "partial_images":
		if n, err := strconv.Atoi(value); err == nil {
			parsed.PartialImages = &n
		}
	}
}

func buildOpenAIImageOAuthResponsesRequest(parsed *openAIImageOAuthRequest, toolModel, endpoint string) ([]byte, error) {
	if parsed == nil {
		return nil, fmt.Errorf("parsed images request is required")
	}
	content := []any{map[string]any{"type": "input_text", "text": parsed.Prompt}}
	for _, upload := range parsed.Images {
		dataURL, err := openAIImageOAuthDataURL(upload)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]any{"type": "input_image", "image_url": dataURL})
	}
	action := "generate"
	if endpoint == OpenAIImageEndpointEdits {
		action = "edit"
	}
	tool := map[string]any{"type": "image_generation", "action": action, "model": strings.TrimSpace(toolModel)}
	if shouldPassOpenAIImageOAuthN(toolModel, parsed.N) {
		tool["n"] = parsed.N
	}
	for key, value := range map[string]string{
		"size": parsed.Size, "quality": parsed.Quality, "background": parsed.Background,
		"output_format": parsed.OutputFormat, "moderation": parsed.Moderation, "style": parsed.Style,
	} {
		if value = strings.TrimSpace(value); value != "" {
			tool[key] = value
		}
	}
	if parsed.OutputCompression != nil {
		tool["output_compression"] = *parsed.OutputCompression
	}
	if parsed.PartialImages != nil {
		tool["partial_images"] = *parsed.PartialImages
	}
	if parsed.Mask != nil {
		dataURL, err := openAIImageOAuthDataURL(*parsed.Mask)
		if err != nil {
			return nil, err
		}
		tool["input_image_mask"] = map[string]any{"image_url": dataURL}
	}
	payload := map[string]any{
		"instructions": "", "stream": true, "store": false, "parallel_tool_calls": true,
		"include":   []string{"reasoning.encrypted_content"},
		"reasoning": map[string]any{"effort": "medium", "summary": "auto"},
		"model":     openAIImageOAuthMainModel,
		"input":     []any{map[string]any{"type": "message", "role": "user", "content": content}},
		"tools":     []any{tool}, "tool_choice": map[string]any{"type": "image_generation"},
	}
	return json.Marshal(payload)
}

func shouldPassOpenAIImageOAuthN(model string, n int) bool {
	return n > 1 && !strings.EqualFold(strings.TrimSpace(model), "dall-e-3")
}

func openAIImageOAuthDataURL(upload openAIImageOAuthUpload) (string, error) {
	if len(upload.Data) == 0 {
		return "", fmt.Errorf("upload %q is empty", strings.TrimSpace(upload.FileName))
	}
	contentType := strings.TrimSpace(upload.ContentType)
	if contentType == "" {
		contentType = http.DetectContentType(upload.Data)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(upload.Data), nil
}

func parseOpenAIImageOAuthSSE(body []byte) openAIImageOAuthParsedResponse {
	parsed := openAIImageOAuthParsedResponse{CreatedAt: time.Now().Unix()}
	fallback := make([]openAIImageOAuthResult, 0, 1)
	forEachOpenAIImageSSEPayload(body, func(payload []byte) {
		if !gjson.ValidBytes(payload) {
			return
		}
		eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
		response := gjson.GetBytes(payload, "response")
		mergeOpenAIImageOAuthLifecycleMeta(&parsed.Meta, response)
		switch eventType {
		case "response.created":
			if created := response.Get("created_at").Int(); created > 0 {
				parsed.CreatedAt = created
			}
		case "response.output_item.done":
			if result, ok := parseOpenAIImageOAuthOutput(responseOrItem(payload)); ok {
				fallback = appendOpenAIImageOAuthResultUnique(fallback, result)
			}
		case "response.completed", "response.done":
			if created := response.Get("created_at").Int(); created > 0 {
				parsed.CreatedAt = created
			}
			parsed.Usage, parsed.UsageRaw = parseOpenAIImageOAuthUsage(response)
			for _, item := range response.Get("output").Array() {
				if result, ok := parseOpenAIImageOAuthOutput(item); ok {
					parsed.Results = appendOpenAIImageOAuthResultUnique(parsed.Results, result)
				}
			}
		case "error":
			setOpenAIImageOAuthError(&parsed, gjson.GetBytes(payload, "error"))
		case "response.failed":
			setOpenAIImageOAuthError(&parsed, response.Get("error"))
		case "response.incomplete":
			setOpenAIImageOAuthError(&parsed, response.Get("error"))
			if parsed.ErrorType == "" && parsed.ErrorCode == "" && parsed.ErrorText == "" {
				setOpenAIImageOAuthIncompleteError(&parsed, response.Get("incomplete_details.reason"))
			}
		}
	})
	if len(parsed.Results) == 0 {
		parsed.Results = fallback
	}
	for i := range parsed.Results {
		fillOpenAIImageOAuthResultMeta(&parsed.Results[i], parsed.Meta)
	}
	if len(parsed.Results) > 0 {
		mergeOpenAIImageOAuthResultMeta(&parsed.Meta, parsed.Results[0])
	}
	return parsed
}

func responseOrItem(payload []byte) gjson.Result {
	return gjson.GetBytes(payload, "item")
}

func parseOpenAIImageOAuthOutput(item gjson.Result) (openAIImageOAuthResult, bool) {
	if !item.Exists() || item.Get("type").String() != "image_generation_call" {
		return openAIImageOAuthResult{}, false
	}
	result := strings.TrimSpace(item.Get("result").String())
	if result == "" {
		return openAIImageOAuthResult{}, false
	}
	return openAIImageOAuthResult{
		Result: result, RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
		OutputFormat: strings.TrimSpace(item.Get("output_format").String()),
		Size:         strings.TrimSpace(item.Get("size").String()), Background: strings.TrimSpace(item.Get("background").String()),
		Quality: strings.TrimSpace(item.Get("quality").String()), Model: strings.TrimSpace(item.Get("model").String()),
	}, true
}

func appendOpenAIImageOAuthResultUnique(results []openAIImageOAuthResult, candidate openAIImageOAuthResult) []openAIImageOAuthResult {
	for _, existing := range results {
		if existing.Result == candidate.Result {
			return results
		}
	}
	return append(results, candidate)
}

func mergeOpenAIImageOAuthLifecycleMeta(meta *openAIImageOAuthResult, response gjson.Result) {
	if meta == nil || !response.Exists() {
		return
	}
	for _, tool := range response.Get("tools").Array() {
		if tool.Get("type").String() != "image_generation" {
			continue
		}
		mergeOpenAIImageOAuthResultMeta(meta, openAIImageOAuthResult{
			Model: strings.TrimSpace(tool.Get("model").String()), Size: strings.TrimSpace(tool.Get("size").String()),
			Quality: strings.TrimSpace(tool.Get("quality").String()), Background: strings.TrimSpace(tool.Get("background").String()),
			OutputFormat: strings.TrimSpace(tool.Get("output_format").String()),
		})
		break
	}
}

func mergeOpenAIImageOAuthResultMeta(dst *openAIImageOAuthResult, src openAIImageOAuthResult) {
	if dst == nil {
		return
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.Size != "" {
		dst.Size = src.Size
	}
	if src.Quality != "" {
		dst.Quality = src.Quality
	}
	if src.Background != "" {
		dst.Background = src.Background
	}
	if src.OutputFormat != "" {
		dst.OutputFormat = src.OutputFormat
	}
}

func fillOpenAIImageOAuthResultMeta(dst *openAIImageOAuthResult, src openAIImageOAuthResult) {
	if dst == nil {
		return
	}
	if dst.Model == "" {
		dst.Model = src.Model
	}
	if dst.Size == "" {
		dst.Size = src.Size
	}
	if dst.Quality == "" {
		dst.Quality = src.Quality
	}
	if dst.Background == "" {
		dst.Background = src.Background
	}
	if dst.OutputFormat == "" {
		dst.OutputFormat = src.OutputFormat
	}
}

func parseOpenAIImageOAuthUsage(response gjson.Result) (openAIImageUsage, map[string]any) {
	usageResult := response.Get("usage")
	toolUsage := response.Get("tool_usage.image_gen")
	selected := usageResult
	usingToolUsage := toolUsage.Exists() && toolUsage.IsObject()
	if usingToolUsage {
		selected = toolUsage
	}

	textInput, _ := boundedOpenAIImageJSONNonNegativeInt(selected.Get("input_tokens_details.text_tokens"))
	imageInput, imageInputOK := boundedOpenAIImageJSONNonNegativeInt(selected.Get("input_tokens_details.image_tokens"))
	if !imageInputOK && usingToolUsage {
		imageInput, imageInputOK = boundedOpenAIImageJSONNonNegativeInt(usageResult.Get("input_tokens_details.image_tokens"))
	}
	if totalInput, ok := boundedOpenAIImageJSONNonNegativeInt(selected.Get("input_tokens")); ok {
		if !imageInputOK || imageInput > totalInput {
			imageInput = 0
		}
		// The tool aggregate is authoritative. Any image-token split observed on
		// the lifecycle usage is subtracted so the same input is never billed twice.
		textInput = totalInput - imageInput
	}
	imageOutput, ok := boundedOpenAIImageJSONNonNegativeInt(selected.Get("output_tokens_details.image_tokens"))
	if !ok {
		imageOutput, _ = boundedOpenAIImageJSONNonNegativeInt(selected.Get("output_tokens"))
	}
	usage := openAIImageUsage{
		TextInputTokens: textInput, ImageInputTokens: imageInput, ImageOutputTokens: imageOutput,
	}
	var raw map[string]any
	if selected.Exists() && selected.IsObject() {
		_ = json.Unmarshal([]byte(selected.Raw), &raw)
	}
	return usage, raw
}

// boundedOpenAIImageJSONNonNegativeInt accepts integral JSON numbers, including
// bounded exponent notation, without allocating an arbitrary-precision value
// from upstream-controlled input.
func boundedOpenAIImageJSONNonNegativeInt(value gjson.Result) (int, bool) {
	if !value.Exists() || value.Type != gjson.Number {
		return 0, false
	}
	raw := value.Raw
	if len(raw) == 0 || len(raw) > 64 || raw[0] == '-' {
		return 0, false
	}

	mantissaEnd := len(raw)
	for i, c := range raw {
		if c == 'e' || c == 'E' {
			mantissaEnd = i
			break
		}
	}
	digits := raw[:mantissaEnd]
	fractionDigits := 0
	digitCount := 0
	dotSeen := false
	mantissaIsZero := true
	for _, c := range digits {
		switch {
		case c == '.' && !dotSeen:
			dotSeen = true
		case c >= '0' && c <= '9':
			digitCount++
			mantissaIsZero = mantissaIsZero && c == '0'
			if dotSeen {
				fractionDigits++
			}
		default:
			return 0, false
		}
	}

	exponent := 0
	if mantissaEnd < len(raw) {
		exponentRaw := raw[mantissaEnd+1:]
		negative := false
		if len(exponentRaw) > 0 && (exponentRaw[0] == '+' || exponentRaw[0] == '-') {
			negative = exponentRaw[0] == '-'
			exponentRaw = exponentRaw[1:]
		}
		if len(exponentRaw) == 0 {
			return 0, false
		}
		for len(exponentRaw) > 1 && exponentRaw[0] == '0' {
			exponentRaw = exponentRaw[1:]
		}
		for _, digit := range exponentRaw {
			if digit < '0' || digit > '9' {
				return 0, false
			}
		}
		if mantissaIsZero {
			return 0, true
		}
		if len(exponentRaw) > 3 {
			return 0, false
		}
		for _, digit := range exponentRaw {
			exponent = exponent*10 + int(digit-'0')
		}
		if exponent > 100 {
			return 0, false
		}
		if negative {
			exponent = -exponent
		}
	}

	trailingZeros := exponent - fractionDigits
	scaleReduction := 0
	if trailingZeros < 0 {
		scaleReduction = -trailingZeros
		remaining := scaleReduction
		allZeros := true
		for i := len(digits) - 1; i >= 0; i-- {
			if digits[i] == '.' {
				continue
			}
			if digits[i] != '0' {
				allZeros = false
				if remaining > 0 {
					return 0, false
				}
			}
			if remaining > 0 {
				remaining--
			}
		}
		if remaining > 0 {
			if allZeros {
				return 0, true
			}
			return 0, false
		}
	}

	maxInt := int(^uint(0) >> 1)
	parsed := 0
	digitsToAccumulate := digitCount - scaleReduction
	for _, c := range digits {
		if c == '.' {
			continue
		}
		if digitsToAccumulate <= 0 {
			break
		}
		if parsed > (maxInt-int(c-'0'))/10 {
			return 0, false
		}
		parsed = parsed*10 + int(c-'0')
		digitsToAccumulate--
	}
	if trailingZeros < 0 {
		return parsed, true
	}
	for ; trailingZeros > 0; trailingZeros-- {
		if parsed > maxInt/10 {
			return 0, false
		}
		parsed *= 10
	}
	return parsed, true
}

func setOpenAIImageOAuthError(parsed *openAIImageOAuthParsedResponse, value gjson.Result) {
	if parsed == nil || !value.Exists() {
		return
	}
	parsed.ErrorType = strings.TrimSpace(value.Get("type").String())
	parsed.ErrorCode = strings.TrimSpace(value.Get("code").String())
	parsed.ErrorText = strings.TrimSpace(value.Get("message").String())
}

func setOpenAIImageOAuthIncompleteError(parsed *openAIImageOAuthParsedResponse, reason gjson.Result) {
	if parsed == nil {
		return
	}
	value := strings.TrimSpace(reason.String())
	parsed.ErrorType = "incomplete_error"
	parsed.ErrorCode = "response_incomplete"
	parsed.ErrorText = "Upstream did not complete image generation"
	if value != "" {
		parsed.ErrorText += ": " + value
	}
	normalized := strings.ToLower(value)
	if strings.Contains(normalized, "content_filter") || strings.Contains(normalized, "moderation") {
		parsed.ErrorType = "image_generation_user_error"
	}
}

func isOpenAIImageOAuthUserError(errorType, code string) bool {
	normalized := strings.ToLower(strings.TrimSpace(errorType + " " + code))
	return strings.Contains(normalized, "user_error") || strings.Contains(normalized, "moderation") ||
		strings.Contains(normalized, "content_policy") || strings.Contains(normalized, "invalid_request")
}

func buildOpenAIImageOAuthAPIResponse(parsed openAIImageOAuthParsedResponse, responseFormat string) ([]byte, error) {
	format := strings.ToLower(strings.TrimSpace(responseFormat))
	if format == "" {
		format = "b64_json"
	}
	data := make([]map[string]any, 0, len(parsed.Results))
	for _, result := range parsed.Results {
		item := make(map[string]any)
		if format == "url" {
			item["url"] = "data:" + openAIImageOAuthMIMEType(result.OutputFormat) + ";base64," + result.Result
		} else {
			item["b64_json"] = result.Result
		}
		if result.RevisedPrompt != "" {
			item["revised_prompt"] = result.RevisedPrompt
		}
		data = append(data, item)
	}
	out := map[string]any{"created": parsed.CreatedAt, "data": data}
	if parsed.Meta.Background != "" {
		out["background"] = parsed.Meta.Background
	}
	if parsed.Meta.OutputFormat != "" {
		out["output_format"] = parsed.Meta.OutputFormat
	}
	if parsed.Meta.Quality != "" {
		out["quality"] = parsed.Meta.Quality
	}
	if parsed.Meta.Size != "" {
		out["size"] = parsed.Meta.Size
	}
	if parsed.Meta.Model != "" {
		out["model"] = parsed.Meta.Model
	}
	if len(parsed.UsageRaw) > 0 {
		out["usage"] = parsed.UsageRaw
	}
	return json.Marshal(out)
}

func openAIImageOAuthMIMEType(outputFormat string) string {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpg", "jpeg", "image/jpeg":
		return "image/jpeg"
	case "webp", "image/webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func forEachOpenAIImageSSEPayload(body []byte, visit func([]byte)) {
	if visit == nil {
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), len(body)+1)
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if payload != "" && payload != "[DONE]" {
			visit([]byte(payload))
		}
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
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
