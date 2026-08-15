package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) ImagesGenerations(c *gin.Context) {
	h.images(c, service.OpenAIImageEndpointGenerations)
}

func (h *OpenAIGatewayHandler) ImagesEdits(c *gin.Context) {
	h.images(c, service.OpenAIImageEndpointEdits)
}

func (h *OpenAIGatewayHandler) images(c *gin.Context, endpoint string) {
	startedAt := time.Now()
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(c, "handler.openai_gateway.images",
		zap.Int64("user_id", subject.UserID), zap.Int64("api_key_id", apiKey.ID), zap.String("image_endpoint", endpoint))
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}
	if apiKey.Group == nil || apiKey.Group.Platform != service.PlatformOpenAI {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "OpenAI Image API requires an OpenAI group")
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	model, imageSize, err := parseOpenAIImageRequest(endpoint, c.GetHeader("Content-Type"), body)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	reqLog = reqLog.With(zap.String("model", model))
	if err := h.gatewayService.ValidateOpenAIImagePricing(model); err != nil {
		reqLog.Warn("openai.image_pricing_unavailable", zap.Error(err))
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Image billing is unavailable for the requested model")
		return
	}
	// Image edits can contain user-provided binary data. Keep only routing metadata
	// in operational context; never enqueue the request body for capture.
	setOpsRequestContext(c, model, false, nil)

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	streamStarted := false
	userRelease, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userRelease != nil {
		defer userRelease()
	}
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		status, code, message := billingErrorDetails(err)
		h.errorResponse(c, status, code, message)
		return
	}

	failedAccountIDs := make(map[int64]struct{})
	var lastFailoverErr *service.UpstreamFailoverError
	switchCount := 0
	for {
		selection, _, err := h.gatewayService.SelectAccountWithScheduler(
			c.Request.Context(), apiKey.GroupID, "", "", model, failedAccountIDs, service.OpenAIUpstreamTransportHTTPSSE)
		if err != nil || selection == nil || selection.Account == nil {
			if lastFailoverErr != nil {
				h.handleFailoverExhausted(c, lastFailoverErr, false)
			} else {
				h.writeError(c, errEnvelopeNoAvailableAccount, false)
			}
			return
		}
		account := selection.Account
		if !account.IsOpenAIApiKey() {
			if selection.Acquired && selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			failedAccountIDs[account.ID] = struct{}{}
			continue
		}
		setOpsSelectedAccount(c, account.ID, account.Platform)
		accountRelease, acquired := h.acquireResponsesAccountSlot(c, apiKey.GroupID, "", selection, false, &streamStarted, reqLog)
		if !acquired {
			return
		}
		result, forwardErr := h.gatewayService.ForwardImage(c.Request.Context(), c, account, endpoint, body, model, imageSize)
		if accountRelease != nil {
			accountRelease()
		}
		if forwardErr != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(forwardErr, &failoverErr) {
				failedAccountIDs[account.ID] = struct{}{}
				lastFailoverErr = failoverErr
				if switchCount >= h.maxAccountSwitches {
					h.handleFailoverExhausted(c, failoverErr, false)
					return
				}
				switchCount++
				continue
			}
			h.ensureForwardErrorResponse(c, false)
			reqLog.Warn("openai.image_forward_failed", zap.Int64("account_id", account.ID), zap.Error(forwardErr))
			return
		}

		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		payloadHash := service.HashUsageRequestPayload(body)
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
		h.submitUsageRecordTask(func(ctx context.Context) {
			if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
				Result: result, APIKey: apiKey, User: apiKey.User, Account: account, Subscription: subscription,
				InboundEndpoint: inboundEndpoint, UpstreamEndpoint: upstreamEndpoint,
				UserAgent: userAgent, IPAddress: clientIP, RequestPayloadHash: payloadHash, APIKeyService: h.apiKeyService,
			}); err != nil {
				logger.L().Warn("openai.image_usage_record_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			}
		})
		reqLog.Info("openai.image_request_succeeded", zap.Int64("account_id", account.ID), zap.Int("image_count", result.ImageCount), zap.Int64("duration_ms", time.Since(startedAt).Milliseconds()))
		return
	}
}

func parseOpenAIImageRequest(endpoint, contentType string, body []byte) (model string, imageSize string, err error) {
	if len(body) == 0 {
		return "", "", fmt.Errorf("request body is empty")
	}
	mediaType, params, parseErr := mime.ParseMediaType(contentType)
	if parseErr != nil {
		return "", "", fmt.Errorf("invalid Content-Type")
	}
	if endpoint == service.OpenAIImageEndpointGenerations {
		if mediaType != "application/json" || !gjson.ValidBytes(body) {
			return "", "", fmt.Errorf("image generations requires a valid application/json body")
		}
		modelValue := gjson.GetBytes(body, "model")
		if modelValue.Type != gjson.String || strings.TrimSpace(modelValue.String()) == "" {
			return "", "", fmt.Errorf("model is required")
		}
		if stream := gjson.GetBytes(body, "stream"); stream.Exists() {
			if stream.Type != gjson.True && stream.Type != gjson.False {
				return "", "", fmt.Errorf("invalid stream field type")
			}
			if stream.Bool() {
				return "", "", fmt.Errorf("streaming is not supported for the direct Image API")
			}
		}
		return strings.TrimSpace(modelValue.String()), strings.TrimSpace(gjson.GetBytes(body, "size").String()), nil
	}
	if endpoint != service.OpenAIImageEndpointEdits || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return "", "", fmt.Errorf("image edits requires multipart/form-data")
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return "", "", fmt.Errorf("invalid multipart body")
		}
		name := part.FormName()
		if name == "model" || name == "size" || name == "stream" {
			value, readErr := io.ReadAll(io.LimitReader(part, 1025))
			if readErr != nil || len(value) > 1024 {
				_ = part.Close()
				return "", "", fmt.Errorf("invalid %s field", name)
			}
			switch name {
			case "model":
				model = strings.TrimSpace(string(value))
			case "size":
				imageSize = strings.TrimSpace(string(value))
			case "stream":
				if strings.EqualFold(strings.TrimSpace(string(value)), "true") {
					_ = part.Close()
					return "", "", fmt.Errorf("streaming is not supported for the direct Image API")
				}
			}
		}
		_ = part.Close()
	}
	if model == "" {
		return "", "", fmt.Errorf("model is required")
	}
	return model, imageSize, nil
}
