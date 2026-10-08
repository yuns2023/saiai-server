package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	pkgopenai "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

type chatGPTBillingSettingsReader interface {
	GetOpenAIChatGPTBillingSettings(context.Context) (*service.OpenAIChatGPTBillingSettings, error)
}

// OpenAIGatewayHandler handles OpenAI API gateway requests.
type OpenAIGatewayHandler struct {
	gatewayService            *service.OpenAIGatewayService
	chatGPTBillingSettings    chatGPTBillingSettingsReader
	billingCacheService       *service.BillingCacheService
	apiKeyService             *service.APIKeyService
	usageRecordWorkerPool     *service.UsageRecordWorkerPool
	inputModerationService    *service.InputModerationService
	errorPassthroughService   *service.ErrorPassthroughService
	concurrencyHelper         *ConcurrencyHelper
	maxAccountSwitches        int
	cfg                       *config.Config
	openAIChatModelRequests   atomic.Int64
	chatGPTUpdatesMu          sync.Mutex
	chatGPTUpdatesConnections map[string]int
	chatGPTUpdatesActive      int
}

// NewOpenAIGatewayHandler creates a new OpenAIGatewayHandler
func NewOpenAIGatewayHandler(
	gatewayService *service.OpenAIGatewayService,
	concurrencyService *service.ConcurrencyService,
	billingCacheService *service.BillingCacheService,
	apiKeyService *service.APIKeyService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	inputModerationService *service.InputModerationService,
	errorPassthroughService *service.ErrorPassthroughService,
	cfg *config.Config,
	settingService *service.SettingService,
) *OpenAIGatewayHandler {
	pingInterval := time.Duration(0)
	maxAccountSwitches := 3
	if cfg != nil {
		pingInterval = time.Duration(cfg.Concurrency.PingInterval) * time.Second
		if cfg.Gateway.MaxAccountSwitches > 0 {
			maxAccountSwitches = cfg.Gateway.MaxAccountSwitches
		}
	}
	h := &OpenAIGatewayHandler{
		gatewayService:          gatewayService,
		billingCacheService:     billingCacheService,
		apiKeyService:           apiKeyService,
		usageRecordWorkerPool:   usageRecordWorkerPool,
		inputModerationService:  inputModerationService,
		errorPassthroughService: errorPassthroughService,
		concurrencyHelper:       NewConcurrencyHelper(concurrencyService, SSEPingFormatComment, pingInterval),
		maxAccountSwitches:      maxAccountSwitches,
		cfg:                     cfg,
	}
	if settingService != nil {
		h.chatGPTBillingSettings = settingService
	}
	return h
}

// ChatGPTConversation handles the experimental native ChatGPT conversation
// protocol. It is intentionally separate from Responses and disabled unless
// Gateway.OpenAIChatEnabled is explicitly enabled.
func (h *OpenAIGatewayHandler) ChatGPTConversation(c *gin.Context) {
	requestStart := time.Now()
	if h == nil || h.cfg == nil || !h.cfg.Gateway.OpenAIChatEnabled {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
			"type": "not_found_error", "message": "Native ChatGPT Chat is disabled",
		}})
		return
	}
	isModelRequest := c.Request.URL.Path == "/chatgpt/backend-api/f/conversation"
	isResumeRequest := c.Request.URL.Path == "/chatgpt/backend-api/f/conversation/resume"
	isTurnStream := isModelRequest || isResumeRequest
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformOpenAI {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
			"type": "permission_error", "message": "Native ChatGPT Chat requires an OpenAI group",
		}})
		return
	}
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil || len(body) == 0 || !gjson.ValidBytes(body) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request_error", "message": "Invalid ChatGPT conversation body",
		}})
		return
	}
	model, thinkingEffort := service.ChatGPTRequestMetadata(body)
	if isTurnStream && (!service.ValidChatGPTMetadataValue(model, 100) || !service.ValidChatGPTMetadataValue(thinkingEffort, 32)) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Invalid ChatGPT model metadata")
		return
	}
	setOpsRequestContext(c, model, isTurnStream, body)
	var turnCache service.ChatGPTTurnCache
	var turn *service.ChatGPTTurnSnapshot
	var turnScope service.ChatGPTTurnScope
	resumeConversationID := ""
	var conversationOwnerID int64
	if isResumeRequest {
		subject, ok := middleware2.GetAuthSubjectFromContext(c)
		if !ok {
			h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
			return
		}
		turnScope = service.ChatGPTTurnScope{UserID: subject.UserID, APIKeyID: apiKey.ID, GroupID: apiKey.Group.ID}
	}
	if isResumeRequest {
		resumeConversationID, err = service.ResolveChatGPTResumeConversation(body)
		if err != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Invalid ChatGPT resume body")
			return
		}
		turnCache, err = h.gatewayService.ChatGPTTurnCache()
		if err == nil {
			turn, err = turnCache.GetChatGPTResume(c.Request.Context(), turnScope.ResumeKey(resumeConversationID))
		}
		if err != nil {
			h.errorResponse(c, http.StatusServiceUnavailable, "accounting_unavailable", "Native ChatGPT Chat accounting context is unavailable; retry later")
			return
		}
		if turn == nil {
			h.errorResponse(c, http.StatusConflict, "resume_context_unavailable", "ChatGPT resume context is unavailable; start a new turn")
			return
		}
		model, thinkingEffort = turn.RequestedModel, turn.ThinkingEffort
		setOpsRequestContext(c, model, true, body)
	}
	// Snapshot the current price once, before provider traffic. An admin change
	// during a long-running stream must not reprice that in-flight turn.
	fixedTurnPriceUSD := h.cfg.Gateway.OpenAIChatSuccessTurnPriceUSD
	var imagePricesUSD map[string]float64
	if isModelRequest && h.chatGPTBillingSettings != nil {
		settings, priceErr := h.chatGPTBillingSettings.GetOpenAIChatGPTBillingSettings(c.Request.Context())
		if priceErr != nil || settings == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
				"type": "accounting_unavailable", "message": "Native ChatGPT Chat billing settings are unavailable",
			}})
			return
		}
		fixedTurnPriceUSD = settings.PriceFor(model, thinkingEffort)
		imagePricesUSD = service.CloneChatGPTImagePrices(settings.ImagePricesUSD)
	}
	if isResumeRequest {
		fixedTurnPriceUSD = turn.BasePriceUSD
		imagePricesUSD = service.CloneChatGPTImagePrices(turn.ImagePricesUSD)
	}
	fixedTurnBillingEnabled := service.IsValidOpenAIChatGPTTurnPrice(fixedTurnPriceUSD)
	boundedStaging := h.cfg.Gateway.OpenAIChatUnaccountedAllowed &&
		h.cfg.Gateway.OpenAIProviderAttemptBudget.Enabled && h.cfg.Gateway.OpenAIChatModelRequestCap > 0
	if isTurnStream && strings.TrimSpace(h.cfg.Gateway.OpenAIChatUpstreamBaseURL) == "" &&
		!fixedTurnBillingEnabled && !boundedStaging {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "service_unavailable", "message": "Native ChatGPT Chat requires fixed-turn billing or an explicit bounded staging session",
		}})
		return
	}
	if isTurnStream && !fixedTurnBillingEnabled && !h.cfg.Gateway.OpenAIChatUnaccountedAllowed {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "accounting_unavailable", "message": "Native ChatGPT Chat accounting is not enabled",
		}})
		return
	}
	streamStarted := false
	forwardCtx := c.Request.Context()
	cancelForward := func() {}
	if isTurnStream {
		if forwardCtx.Err() != nil {
			return
		}
		turnTimeout := time.Duration(h.cfg.Gateway.OpenAIChatTurnTimeoutSeconds) * time.Second
		if turnTimeout <= 0 {
			turnTimeout = 10 * time.Minute
		}
		forwardCtx, cancelForward = context.WithTimeout(context.WithoutCancel(forwardCtx), turnTimeout)
		// Native stream leases follow the bounded drain, including after the
		// downstream disconnects. Provider-facing bytes and headers are unchanged.
		c.Request = c.Request.WithContext(forwardCtx)
	}
	defer cancelForward()
	var reqLog *zap.Logger
	var subscription *service.UserSubscription
	if isTurnStream {
		subject, subjectOK := middleware2.GetAuthSubjectFromContext(c)
		if !subjectOK {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
				"type": "api_error", "message": "User context not found",
			}})
			return
		}
		turnScope = service.ChatGPTTurnScope{UserID: subject.UserID, APIKeyID: apiKey.ID, GroupID: apiKey.Group.ID}
		reqLog = requestLogger(
			c,
			"handler.openai_gateway.chatgpt_conversation",
			zap.Int64("user_id", subject.UserID),
			zap.Int64("api_key_id", apiKey.ID),
		)
		if fixedTurnBillingEnabled {
			if h.billingCacheService == nil || apiKey.User == nil ||
				((apiKey.Quota > 0 || apiKey.HasRateLimits()) && h.apiKeyService == nil) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
					"type": "accounting_unavailable", "message": "Native ChatGPT Chat billing dependencies are unavailable",
				}})
				return
			}
			subscription, _ = middleware2.GetSubscriptionFromContext(c)
			if isModelRequest {
				if err := h.billingCacheService.CheckBillingEligibility(
					c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription,
				); err != nil {
					status, code, message := billingErrorDetails(err)
					h.errorResponse(c, status, code, message)
					return
				}
			}
		}
		if h.concurrencyHelper != nil && h.concurrencyHelper.concurrencyService != nil {
			userRelease, acquired := h.acquireResponsesUserSlot(
				c, subject.UserID, subject.Concurrency, true, &streamStarted, reqLog,
			)
			if !acquired {
				return
			}
			if userRelease != nil {
				defer userRelease()
			}
		}
	}
	billingIdentity := service.ResolveChatGPTTurnBillingIdentity(body, service.ResolveUsageBillingRequestID(c.Request.Context(), ""))
	if isModelRequest {
		turnCache, err = h.gatewayService.ChatGPTTurnCache()
		if err != nil && fixedTurnBillingEnabled {
			h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "accounting_unavailable", "Native ChatGPT Chat accounting context is unavailable", streamStarted)
			return
		}
		if turnCache != nil {
			turn, err = turnCache.GetChatGPTTurn(forwardCtx, turnScope.TurnKey(billingIdentity))
			if err != nil {
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "accounting_unavailable", "Native ChatGPT Chat accounting context is unavailable", streamStarted)
				return
			}
			if turn != nil && (turn.RequestedModel != model || turn.ThinkingEffort != thinkingEffort) {
				h.handleStreamingAwareError(c, http.StatusConflict, "turn_identity_conflict", "ChatGPT turn metadata changed; use a new message identity", streamStarted)
				return
			}
			if conversationID := strings.TrimSpace(gjson.GetBytes(body, "conversation_id").String()); conversationID != "" {
				active, activeErr := turnCache.GetChatGPTResume(forwardCtx, turnScope.ResumeKey(conversationID))
				if activeErr != nil {
					h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "accounting_unavailable", "Native ChatGPT Chat accounting context is unavailable", streamStarted)
					return
				}
				if active != nil && !active.Completed && !active.TerminalSeen && active.Identity != billingIdentity {
					h.handleStreamingAwareError(c, http.StatusConflict, "turn_pending", "A ChatGPT turn is still pending in this conversation", streamStarted)
					return
				}
				if active == nil {
					h.handleStreamingAwareError(c, http.StatusConflict, "conversation_context_unavailable", "ChatGPT conversation ownership is unavailable; start a new conversation", streamStarted)
					return
				}
				conversationOwnerID = active.AccountID
			}
		}
	}
	sessionHash := service.ResolveChatGPTConversationSessionHash(body)
	if sessionHash == "" {
		sessionHash = h.gatewayService.GenerateSessionHash(c, body)
	}
	var selection *service.AccountSelectionResult
	var selectErr error
	if turn != nil {
		selection, selectErr = h.gatewayService.SelectChatGPTBoundAccount(forwardCtx, apiKey.GroupID, turn.AccountID)
	} else if conversationOwnerID > 0 {
		selection, selectErr = h.gatewayService.SelectChatGPTBoundAccount(forwardCtx, apiKey.GroupID, conversationOwnerID)
	} else {
		selection, _, selectErr = h.gatewayService.SelectChatGPTOAuthAccount(forwardCtx, apiKey.GroupID, sessionHash, model)
	}
	if selectErr != nil || selection == nil || selection.Account == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "service_unavailable", "message": "No available OpenAI OAuth account",
		}})
		return
	}
	if isModelRequest && turnCache != nil {
		var subscriptionID int64
		if subscription != nil {
			subscriptionID = subscription.ID
		}
		turn, err = turnCache.PutChatGPTTurnIfAbsent(forwardCtx, turnScope.TurnKey(billingIdentity), &service.ChatGPTTurnSnapshot{
			Identity: billingIdentity, AccountID: selection.Account.ID, BasePriceUSD: fixedTurnPriceUSD,
			ImagePricesUSD:         imagePricesUSD,
			UserMessageHash:        service.ChatGPTUserMessageHash(body),
			BillingSnapshotVersion: 1, SubscriptionID: subscriptionID,
			RequestedModel: model, ThinkingEffort: thinkingEffort, StartedAt: requestStart,
		}, service.ChatGPTTurnContextTTL)
		if err != nil || turn == nil || turn.RequestedModel != model || turn.ThinkingEffort != thinkingEffort {
			releaseChatGPTControlSelection(selection)
			h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "accounting_unavailable", "Native ChatGPT Chat accounting context is unavailable", streamStarted)
			return
		}
		if selection.Account.ID != turn.AccountID {
			releaseChatGPTControlSelection(selection)
			selection, selectErr = h.gatewayService.SelectChatGPTBoundAccount(forwardCtx, apiKey.GroupID, turn.AccountID)
			if selectErr != nil || selection == nil {
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "service_unavailable", "ChatGPT turn account is unavailable", streamStarted)
				return
			}
		}
	}
	account := selection.Account
	setOpsSelectedAccount(c, account.ID, service.PlatformOpenAI)
	if !isTurnStream {
		// The shared scheduler opportunistically acquires an account slot even
		// for short native-Chat control-plane requests. They are not model
		// turns, so release that reservation immediately instead of leaking it
		// until the final /f/conversation request blocks.
		releaseChatGPTControlSelection(selection)
	} else if h.concurrencyHelper != nil && h.concurrencyHelper.concurrencyService != nil {
		accountRelease, acquired := h.acquireResponsesAccountSlot(
			c, apiKey.GroupID, "", selection, true, &streamStarted, reqLog,
		)
		if !acquired {
			return
		}
		if accountRelease != nil {
			defer accountRelease()
		}
	} else {
		defer releaseChatGPTControlSelection(selection)
	}
	path := c.Request.URL.RequestURI()
	if isModelRequest {
		if cap := h.cfg.Gateway.OpenAIChatModelRequestCap; cap > 0 {
			attempt := h.openAIChatModelRequests.Add(1)
			if attempt > cap {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": gin.H{
					"type": "request_cap_exceeded", "message": "Native ChatGPT staging request cap exceeded",
				}})
				return
			}
		}
	}
	resp, err := h.gatewayService.ForwardChatGPTConversation(forwardCtx, c, account, body, path)
	if err != nil {
		if service.WriteOpenAIProviderAttemptBudgetError(c, err) {
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"type": "upstream_error", "message": "Upstream ChatGPT conversation request failed",
		}})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for key, values := range resp.Header {
		if !shouldCopyChatGPTResponseHeader(key) {
			continue
		}
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Accel-Buffering", "no")
	}
	c.Status(resp.StatusCode)
	buffer := make([]byte, 32*1024)
	var streamObserver *service.ChatGPTConversationStreamObserver
	responseShapeCapture := false
	if isTurnStream {
		responseShapeCapture = h.cfg.Gateway.OpenAIChatResponseShapeCapture
		if responseShapeCapture {
			streamObserver = service.NewChatGPTConversationStreamShapeObserver()
		} else {
			streamObserver = service.NewChatGPTConversationStreamObserver()
		}
	}
	var streamObserverErr error
	mergeTurnImages := func() error {
		if streamObserver == nil || turnCache == nil || turn == nil {
			return nil
		}
		evidence := streamObserver.ImageEvidence()
		if !evidence.GenerationSeen {
			return nil
		}
		updated, err := service.MergeChatGPTDeliveryImages(forwardCtx, turnCache, turnScope, turn, evidence)
		if err != nil {
			logger.L().Warn("openai.chatgpt_image_metadata_merge_failed", zap.Error(err))
			return err
		}
		turn = updated
		return nil
	}
	clientDisconnected := false
	boundConversation := ""
	boundTopics := make(map[string]bool)
	lastAsyncStatus := 0
	mergeAsyncStatus := func(signals service.ChatGPTConversationStreamSummary) error {
		if !signals.AsyncStatusSeen || signals.AsyncStatus == lastAsyncStatus || turnCache == nil || turn == nil {
			return nil
		}
		var statusErr error
		turn, statusErr = turnCache.SetChatGPTTurnAsyncStatus(forwardCtx, turnScope.TurnKey(turn.Identity), signals.AsyncStatus)
		if statusErr == nil {
			lastAsyncStatus = signals.AsyncStatus
		}
		return statusErr
	}
	bindDelivery := func(signals service.ChatGPTConversationStreamSummary) error {
		if turnCache == nil || turn == nil {
			return errors.New("ChatGPT delivery accounting context unavailable")
		}
		if signals.ConversationID != "" {
			requestedConversation := strings.TrimSpace(gjson.GetBytes(body, "conversation_id").String())
			if (boundConversation != "" && boundConversation != signals.ConversationID) ||
				(requestedConversation != "" && requestedConversation != signals.ConversationID) ||
				(isResumeRequest && signals.ConversationID != resumeConversationID) {
				return errors.New("ChatGPT delivery identity changed")
			}
			if boundConversation == "" {
				if err := turnCache.BindChatGPTUpdates(forwardCtx, turnScope.ResumeKey(signals.ConversationID), turnScope.TurnKey(turn.Identity), turnScope.UpdatesKey()); err != nil {
					return err
				}
				boundConversation = signals.ConversationID
			}
		}
		for _, topic := range signals.UpdateTopics {
			if !boundTopics[topic] {
				if err := turnCache.BindChatGPTResume(forwardCtx, turnScope.TopicKey(topic), turnScope.TurnKey(turn.Identity)); err != nil {
					return err
				}
				boundTopics[topic] = true
			}
		}
		return mergeAsyncStatus(signals)
	}
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if streamObserver != nil && streamObserverErr == nil {
				streamObserverErr = streamObserver.Observe(buffer[:n])
				signals := streamObserver.Snapshot()
				if streamObserverErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && !signals.ProviderErrorSeen {
					streamObserverErr = bindDelivery(signals)
					if streamObserverErr == nil {
						streamObserverErr = mergeTurnImages()
					}
					if streamObserverErr != nil {
						h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "accounting_unavailable", "ChatGPT delivery accounting context is unavailable", c.Writer.Written())
						return
					}
				}
			}
			if !clientDisconnected {
				if _, writeErr := c.Writer.Write(buffer[:n]); writeErr != nil {
					// Continue draining the upstream response so completion and any
					// future accounting evidence are not lost solely because the
					// Desktop client disconnected.
					clientDisconnected = true
				} else if flusher, ok := c.Writer.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) && streamObserver != nil {
				summary, finishErr := streamObserver.Finish()
				if streamObserverErr == nil {
					streamObserverErr = finishErr
				}
				if streamObserverErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && !summary.ProviderErrorSeen {
					streamObserverErr = bindDelivery(summary)
				}
				if streamObserverErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && !summary.ProviderErrorSeen {
					streamObserverErr = mergeTurnImages()
					if streamObserverErr == nil && turn != nil {
						images := service.MergeChatGPTImageEvidence(turn.Images, streamObserver.ImageEvidence())
						summary.ImageGenerationSeen = images.GenerationSeen
						summary.ImageCount = len(images.AssetHashes)
					}
				}
				if reqLog != nil {
					fields := []zap.Field{
						zap.Int64("account_id", account.ID),
						zap.Bool("completion_seen", summary.CompletionSeen),
						zap.Bool("async_pending", turn != nil && turn.AsyncPending),
						zap.Bool("handoff_seen", summary.HandoffSeen),
						zap.Bool("async_status_seen", summary.AsyncStatusSeen),
						zap.Int("async_status", summary.AsyncStatus),
						zap.Bool("done_sentinel_seen", summary.DoneSentinelSeen),
						zap.Bool("provider_error_seen", summary.ProviderErrorSeen),
						zap.String("observed_model", summary.ObservedModel),
						zap.Bool("image_generation_seen", summary.ImageGenerationSeen),
						zap.Int("observed_image_count", summary.ImageCount),
					}
					if responseShapeCapture {
						fields = append(fields,
							zap.Strings("event_types", summary.EventTypes),
							zap.Strings("top_level_fields", summary.TopLevelFields),
							zap.Strings("message_metadata_fields", summary.MessageMetadataFields),
							zap.Strings("usage_like_field_paths", summary.UsageLikeFieldPaths),
						)
					}
					if streamObserverErr != nil {
						reqLog.Warn("openai.chatgpt_stream_observer_failed", append(fields, zap.Error(streamObserverErr))...)
					} else if responseShapeCapture {
						reqLog.Info("openai.chatgpt_response_shape_captured", fields...)
					} else if h.cfg.Gateway.OpenAIChatUpdatesEnabled {
						reqLog.Info("openai.chatgpt_stream_observed", fields...)
					} else {
						reqLog.Debug("openai.chatgpt_stream_observed", fields...)
					}
				}
				if streamObserverErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 &&
					summary.CompletionSeen && !summary.ProviderErrorSeen &&
					(turn == nil || !turn.AsyncPending) &&
					(!isResumeRequest || summary.ConversationID == resumeConversationID) {
					if err := h.settleChatGPTTurn(forwardCtx, turnCache, turnScope, turn, &service.OpenAIChatGPTTurnUsageInput{
						ObservedModel: summary.ObservedModel, APIKey: apiKey, User: apiKey.User, Account: account,
						Subscription: subscription, InboundEndpoint: c.Request.URL.Path,
						UpstreamEndpoint: strings.TrimPrefix(c.Request.URL.Path, "/chatgpt"),
						UserAgent:        c.GetHeader("User-Agent"), IPAddress: ip.GetClientIP(c),
					}); err != nil && !errors.Is(err, service.ErrChatGPTTurnPending) {
						logger.L().Error("openai.chatgpt_seal_billing_failed", zap.Error(err))
						return
					}
					if stickyHash := service.ChatGPTConversationSessionHash(summary.ConversationID); stickyHash != "" {
						_ = h.gatewayService.BindStickySession(forwardCtx, apiKey.GroupID, stickyHash, account.ID)
					}
				}
			}
			return
		}
	}
}

// ChatGPTFileDownload resolves the short-lived download URL for a native
// ChatGPT file/image pointer (for example sediment://file_...). It is a
// control-plane request: preserve the provider JSON and do not count it as a
// model turn. When the Desktop supplies conversation_id, use the same
// namespaced sticky identity as the conversation stream so the file belongs to
// the account that produced it.
func (h *OpenAIGatewayHandler) ChatGPTFileDownload(c *gin.Context) {
	fileID := strings.TrimSpace(c.Param("file_id"))
	if !isSafeChatGPTFileID(fileID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request_error", "message": "Invalid ChatGPT file id",
		}})
		return
	}
	h.chatGPTAssetDownload(c)
}

// ChatGPTEstuaryContent forwards the signed native ChatGPT asset bytes. The
// Desktop first resolves a sediment:// file through ChatGPTFileDownload, then
// follows the returned /backend-api/estuary/content URL.
func (h *OpenAIGatewayHandler) ChatGPTEstuaryContent(c *gin.Context) {
	if !isSafeChatGPTFileID(strings.TrimSpace(c.Query("id"))) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request_error", "message": "Invalid ChatGPT asset id",
		}})
		return
	}
	h.chatGPTAssetDownload(c)
}

func (h *OpenAIGatewayHandler) chatGPTAssetDownload(c *gin.Context) {
	if h.cfg != nil && h.cfg.Gateway.OpenAIChatUpdatesEnabled {
		h.chatGPTScopedAssetDownload(c)
		return
	}
	// Estuary cid is an opaque provider asset identifier, not a conversation
	// UUID. Preserve it in the query without inventing a conversation binding.
	conversationID := strings.TrimSpace(c.Query("conversation_id"))
	sessionHash := ""
	if conversationID != "" {
		sessionHash = service.ChatGPTConversationSessionHash(conversationID)
	}
	h.chatGPTControlGET(c, sessionHash, "Upstream ChatGPT asset download request failed")
}

// ChatGPTModels forwards the ordinary Chat catalog without translating it to
// a Codex catalog or synthesizing model names.
func (h *OpenAIGatewayHandler) ChatGPTModels(c *gin.Context) {
	h.chatGPTControlGET(c, "", "Upstream ChatGPT model catalog request failed")
}

func (h *OpenAIGatewayHandler) chatGPTControlGET(c *gin.Context, sessionHash, failureMessage string) {
	setOpsRequestContext(c, "", false, nil)
	if h == nil || h.cfg == nil || !h.cfg.Gateway.OpenAIChatEnabled {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
			"type": "not_found_error", "message": "Native ChatGPT Chat is disabled",
		}})
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformOpenAI {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
			"type": "permission_error", "message": "Native ChatGPT Chat requires an OpenAI group",
		}})
		return
	}
	if sessionHash == "" {
		sessionHash = h.gatewayService.GenerateSessionHash(c, nil)
	}
	selection, _, selectErr := h.gatewayService.SelectChatGPTOAuthAccount(
		c.Request.Context(), apiKey.GroupID, sessionHash, "",
	)
	if selectErr != nil || selection == nil || selection.Account == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "service_unavailable", "message": "No available OpenAI OAuth account",
		}})
		return
	}
	account := selection.Account
	setOpsSelectedAccount(c, account.ID, service.PlatformOpenAI)
	releaseChatGPTControlSelection(selection)
	path := c.Request.URL.RequestURI()
	resp, err := h.gatewayService.ForwardChatGPTControl(c.Request.Context(), c, account, path)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"type": "upstream_error", "message": failureMessage,
		}})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for key, values := range resp.Header {
		if !shouldCopyChatGPTResponseHeader(key) {
			continue
		}
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}
	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}

func isSafeChatGPTFileID(fileID string) bool {
	if len(fileID) < 6 || len(fileID) > 256 || !strings.HasPrefix(fileID, "file_") {
		return false
	}
	for _, r := range fileID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func releaseChatGPTControlSelection(selection *service.AccountSelectionResult) {
	if selection != nil && selection.Acquired && selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func shouldCopyChatGPTResponseHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "content-length", "set-cookie":
		return false
	default:
		return true
	}
}

// Responses handles OpenAI Responses API endpoint
// POST /openai/v1/responses
func (h *OpenAIGatewayHandler) Responses(c *gin.Context) {
	// 局部兜底：确保该 handler 内部任何 panic 都不会击穿到进程级。
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)
	compactStartedAt := time.Now()
	defer h.logOpenAIRemoteCompactOutcome(c, compactStartedAt)
	setOpenAIClientTransportHTTP(c)

	requestStart := time.Now()

	// Get apiKey and user from context (set by ApiKeyAuth middleware)
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
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.responses",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	// Read request body
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}
	wireBody := body
	requestContentEncoding := strings.TrimSpace(c.GetHeader("Content-Encoding"))
	bodyWasEncoded := false
	if requestContentEncoding != "" && !strings.EqualFold(requestContentEncoding, "identity") {
		body, bodyWasEncoded, err = decodeOpenAIRequestBody(body, requestContentEncoding, h.openAIRequestBodyDecodeLimit())
		if err != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to decode request body")
			return
		}
	}

	setOpsRequestContext(c, "", false, body)
	sessionHashBody := body
	if service.IsOpenAIResponsesCompactPathForTest(c) {
		if compactSeed := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); compactSeed != "" {
			c.Set(service.OpenAICompactSessionSeedKeyForTest(), compactSeed)
		}
		normalizedCompactBody, normalizedCompact, compactErr := service.NormalizeOpenAICompactRequestBodyForTest(body)
		if compactErr != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to normalize compact request body")
			return
		}
		if normalizedCompact {
			body = normalizedCompactBody
			if bodyWasEncoded {
				wireBody = body
				bodyWasEncoded = false
				requestContentEncoding = ""
				c.Request.Header.Del("Content-Encoding")
			}
		}
	}

	// 校验请求体 JSON 合法性
	if !gjson.ValidBytes(body) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	if bodyWasEncoded {
		var parsedBody map[string]any
		if err := json.Unmarshal(body, &parsedBody); err != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
			return
		}
		c.Set(service.OpenAIParsedRequestBodyKey, parsedBody)
	}

	// 使用 gjson 只读提取字段做校验，避免完整 Unmarshal
	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	if apiKey.Group != nil && apiKey.Group.IsModelBlocked(reqModel) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", fmt.Sprintf("Model %s is not allowed for this group", reqModel))
		return
	}
	usageSessionID := service.ResolveOpenAIUsageSessionID(
		c.GetHeader("session_id"),
		c.GetHeader("conversation_id"),
		gjson.GetBytes(sessionHashBody, "prompt_cache_key").String(),
	)

	streamResult := gjson.GetBytes(body, "stream")
	if streamResult.Exists() && streamResult.Type != gjson.True && streamResult.Type != gjson.False {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "invalid stream field type")
		return
	}
	reqStream := streamResult.Bool()
	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))
	previousResponseID := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if previousResponseID != "" {
		previousResponseIDKind := service.ClassifyOpenAIPreviousResponseIDKind(previousResponseID)
		reqLog = reqLog.With(
			zap.Bool("has_previous_response_id", true),
			zap.String("previous_response_id_kind", previousResponseIDKind),
			zap.Int("previous_response_id_len", len(previousResponseID)),
		)
		if previousResponseIDKind == service.OpenAIPreviousResponseIDKindMessageID {
			reqLog.Warn("openai.request_validation_failed",
				zap.String("reason", "previous_response_id_looks_like_message_id"),
			)
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "previous_response_id must be a response.id (resp_*), not a message id")
			return
		}
	}

	setOpsRequestContext(c, reqModel, reqStream, body)
	if !h.validateCodexClientPolicyHTTP(c, apiKey.Group) {
		return
	}

	// 提前校验 function_call_output 是否具备可关联上下文，避免上游 400。
	if !h.validateFunctionCallOutputRequest(c, body, reqLog) {
		return
	}
	requestID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
	h.submitOpenAIInputModeration(apiKey, requestID, body, service.InputModerationSourceOpenAIResponsesHTTP, 1)

	// 绑定错误透传服务，允许 service 层在非 failover 错误场景复用规则。
	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	// Get subscription info (may be nil)
	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	// 确保请求取消时也会释放槽位，避免长连接被动中断造成泄漏
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// 2. Re-check billing eligibility after wait
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		reqLog.Info("openai.billing_eligibility_check_failed", zap.Error(err))
		status, code, message := billingErrorDetails(err)
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	// Generate session hash (header first; fallback to prompt_cache_key)
	sessionHash := h.gatewayService.GenerateSessionHash(c, sessionHashBody)

	maxAccountSwitches := h.maxAccountSwitches
	switchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError

	for {
		// Select account supporting the requested model
		reqLog.Debug("openai.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		selection, scheduleDecision, err := h.gatewayService.SelectAccountForNativeCodexRequest(
			c,
			apiKey.GroupID,
			subject.UserID,
			previousResponseID,
			sessionHash,
			reqModel,
			failedAccountIDs,
			service.OpenAIUpstreamTransportAny,
			sessionHashBody,
		)
		if err != nil {
			reqLog.Warn("openai.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if len(failedAccountIDs) == 0 {
				h.writeOpenAISelectionError(c, err, streamStarted)
				return
			}
			if lastFailoverErr != nil {
				h.handleFailoverExhausted(c, lastFailoverErr, streamStarted)
			} else {
				h.handleFailoverExhaustedSimple(c, 502, streamStarted)
			}
			return
		}
		if selection == nil || selection.Account == nil {
			h.writeError(c, errEnvelopeNoAvailableAccount, streamStarted)
			return
		}
		if previousResponseID != "" && selection != nil && selection.Account != nil {
			reqLog.Debug("openai.account_selected_with_previous_response_id", zap.Int64("account_id", selection.Account.ID))
		}
		reqLog.Debug("openai.account_schedule_decision",
			zap.String("layer", scheduleDecision.Layer),
			zap.Bool("sticky_previous_hit", scheduleDecision.StickyPreviousHit),
			zap.Bool("sticky_session_hit", scheduleDecision.StickySessionHit),
			zap.Int("candidate_count", scheduleDecision.CandidateCount),
			zap.Int("top_k", scheduleDecision.TopK),
			zap.Int64("latency_ms", scheduleDecision.LatencyMs),
			zap.Float64("load_skew", scheduleDecision.LoadSkew),
		)
		account := selection.Account
		if enforceCodexContinuationAccountBoundary(c, account) && previousResponseID != "" {
			continuationAccountID, ownerErr := h.gatewayService.OpenAIContinuationAccountID(c.Request.Context(), subject.UserID, previousResponseID)
			if ownerErr != nil {
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				h.errorResponse(c, http.StatusServiceUnavailable, "continuation_lookup_failed", "Unable to verify the conversation account")
				return
			}
			if !service.OpenAIContinuationAccountMatches(previousResponseID, continuationAccountID, account.ID) {
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				h.errorResponse(c, http.StatusConflict, "continuation_account_unavailable", "This conversation cannot continue on a different upstream account. Start a new conversation and retry.")
				return
			}
		}
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		setOpsSelectedAccount(c, account.ID, account.Platform)

		accountReleaseFunc, acquired := h.acquireResponsesAccountSlot(c, apiKey.GroupID, sessionHash, selection, reqStream, &streamStarted, reqLog)
		if !acquired {
			return
		}

		// Forward request
		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		forwardStart := time.Now()
		forwardCtx := service.WithAccountSwitchCount(c.Request.Context(), switchCount, false)
		forwardBody := body
		if bodyWasEncoded && shouldPreserveOpenAIEncodedWireBody(c, account) {
			c.Request.Header.Set("Content-Encoding", requestContentEncoding)
			forwardBody = wireBody
		} else if bodyWasEncoded {
			c.Request.Header.Del("Content-Encoding")
		}
		result, err := h.gatewayService.Forward(forwardCtx, c, account, forwardBody)
		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)
		if err == nil && result != nil && result.FirstTokenMs != nil {
			service.SetOpsLatencyMs(c, service.OpsTimeToFirstTokenMsKey, int64(*result.FirstTokenMs))
		}
		if err != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
				// 池模式：同账号重试
				if failoverErr.RetryableOnSameAccount {
					retryLimit := account.GetPoolModeRetryCount()
					if sameAccountRetryCount[account.ID] < retryLimit {
						sameAccountRetryCount[account.ID]++
						reqLog.Warn("openai.pool_mode_same_account_retry",
							zap.Int64("account_id", account.ID),
							zap.Int("upstream_status", failoverErr.StatusCode),
							zap.Int("retry_limit", retryLimit),
							zap.Int("retry_count", sameAccountRetryCount[account.ID]),
						)
						select {
						case <-c.Request.Context().Done():
							return
						case <-time.After(sameAccountRetryDelay):
						}
						continue
					}
				}
				h.gatewayService.RecordOpenAIAccountSwitch()
				failedAccountIDs[account.ID] = struct{}{}
				lastFailoverErr = failoverErr
				if switchCount >= maxAccountSwitches {
					h.handleFailoverExhausted(c, failoverErr, streamStarted)
					return
				}
				switchCount++
				reqLog.Warn("openai.upstream_failover_switching",
					zap.Int64("account_id", account.ID),
					zap.Int("upstream_status", failoverErr.StatusCode),
					zap.Int("switch_count", switchCount),
					zap.Int("max_switches", maxAccountSwitches),
				)
				continue
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
			wroteFallback := h.ensureForwardErrorResponse(c, streamStarted)
			fields := []zap.Field{
				zap.Int64("account_id", account.ID),
				zap.Bool("fallback_error_response_written", wroteFallback),
				zap.Error(err),
			}
			if shouldLogOpenAIForwardFailureAsWarn(c, wroteFallback) {
				reqLog.Warn("openai.forward_failed", fields...)
				return
			}
			reqLog.Error("openai.forward_failed", fields...)
			return
		}
		if result != nil {
			if account.Type == service.AccountTypeOAuth {
				h.gatewayService.UpdateCodexUsageSnapshotFromHeaders(c.Request.Context(), account.ID, result.ResponseHeaders)
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, result.FirstTokenMs)
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, nil)
		}

		// 捕获请求信息（用于异步记录，避免在 goroutine 中访问 gin.Context）
		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		requestPayloadHash := service.HashUsageRequestPayload(body)

		// 使用量记录通过有界 worker 池提交，避免请求热路径创建无界 goroutine。
		h.submitUsageRecordTask(func(ctx context.Context) {
			if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
				Result:             result,
				APIKey:             apiKey,
				User:               apiKey.User,
				Account:            account,
				Subscription:       subscription,
				SessionID:          usageSessionID,
				InboundEndpoint:    GetInboundEndpoint(c),
				UpstreamEndpoint:   GetUpstreamEndpoint(c, account.Platform),
				UserAgent:          userAgent,
				IPAddress:          clientIP,
				RequestPayloadHash: requestPayloadHash,
				APIKeyService:      h.apiKeyService,
			}); err != nil {
				logger.L().With(
					zap.String("component", "handler.openai_gateway.responses"),
					zap.Int64("user_id", subject.UserID),
					zap.Int64("api_key_id", apiKey.ID),
					zap.Any("group_id", apiKey.GroupID),
					zap.String("model", reqModel),
					zap.Int64("account_id", account.ID),
				).Error("openai.record_usage_failed", zap.Error(err))
			}
		})
		reqLog.Debug("openai.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

func (h *OpenAIGatewayHandler) writeOpenAISelectionError(c *gin.Context, err error, streamStarted bool) {
	if service.IsOpenAITurnStateAccountMismatch(err) {
		h.writeError(c, gatewayErrorEnvelope{
			Status: http.StatusConflict, Type: "invalid_request_error", Code: "turn_state_account_mismatch",
			Message: "This conversation's upstream account is unavailable or its state cannot be verified. Start a new conversation and retry.",
		}, streamStarted)
		return
	}
	var quotaGuardErr *service.OpenAINewSessionQuotaGuardError
	if !errors.As(err, &quotaGuardErr) {
		h.writeError(c, errEnvelopeNoAvailableAccount, streamStarted)
		return
	}

	envelope := gatewayErrorEnvelope{
		Status:  http.StatusServiceUnavailable,
		Type:    "gateway_error",
		Code:    "openai_new_session_quota_guard",
		Message: "OpenAI accounts in this group are temporarily reserved for existing sessions because their 5-hour quota is near its limit. Please retry later.",
	}
	if quotaGuardErr.RetryAfter > 0 {
		envelope.RetryAfter = max(1, int(math.Ceil(quotaGuardErr.RetryAfter.Seconds())))
	}
	h.writeError(c, envelope, streamStarted)
}

func shouldPreserveOpenAIEncodedWireBody(c *gin.Context, account *service.Account) bool {
	if c == nil || account == nil {
		return false
	}
	if account.Type != service.AccountTypeOAuth && !account.IsOpenAICodexNativeRelay() {
		return false
	}
	return pkgopenai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator"))
}

func (h *OpenAIGatewayHandler) openAIRequestBodyDecodeLimit() int64 {
	const defaultLimit = int64(256 * 1024 * 1024)
	if h == nil || h.cfg == nil {
		return defaultLimit
	}
	if h.cfg.Server.MaxRequestBodySize > 0 {
		return h.cfg.Server.MaxRequestBodySize
	}
	if h.cfg.Gateway.MaxBodySize > 0 {
		return h.cfg.Gateway.MaxBodySize
	}
	return defaultLimit
}

func decodeOpenAIRequestBody(body []byte, contentEncoding string, maxDecodedBytes int64) ([]byte, bool, error) {
	encoding := strings.ToLower(strings.TrimSpace(contentEncoding))
	if encoding == "" || encoding == "identity" {
		return body, false, nil
	}
	if encoding != "zstd" {
		return nil, false, fmt.Errorf("unsupported request content encoding: %s", encoding)
	}
	decoder, err := zstd.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("initialize zstd request decoder: %w", err)
	}
	defer decoder.Close()
	if maxDecodedBytes <= 0 {
		maxDecodedBytes = 256 * 1024 * 1024
	}
	decoded, err := io.ReadAll(io.LimitReader(decoder, maxDecodedBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("decode zstd request body: %w", err)
	}
	if int64(len(decoded)) > maxDecodedBytes {
		return nil, false, fmt.Errorf("decoded request body exceeds %d bytes", maxDecodedBytes)
	}
	return decoded, true, nil
}

func (h *OpenAIGatewayHandler) submitOpenAIInputModeration(
	apiKey *service.APIKey,
	requestID string,
	body []byte,
	source string,
	turnNumber int,
) {
	if h == nil || h.inputModerationService == nil || apiKey == nil || apiKey.User == nil ||
		apiKey.Group == nil || apiKey.GroupID == nil ||
		!h.inputModerationService.EnabledForGroup(apiKey.Group) {
		return
	}
	text := service.ExtractLatestOpenAIUserText(body)
	if text == "" {
		return
	}
	h.inputModerationService.Submit(service.InputModerationTask{
		RequestID:  strings.TrimSpace(requestID),
		UserID:     apiKey.UserID,
		APIKeyID:   apiKey.ID,
		GroupID:    *apiKey.GroupID,
		Text:       text,
		Source:     source,
		TurnNumber: turnNumber,
	})
}

func codexClientPolicyMatched(c *gin.Context, policy string) bool {
	policy = strings.ToLower(strings.TrimSpace(policy))
	if policy == "" || policy == "off" {
		return true
	}
	userAgent, originator := "", ""
	if c != nil {
		userAgent = c.GetHeader("User-Agent")
		originator = strings.ToLower(strings.TrimSpace(c.GetHeader("originator")))
	}
	switch policy {
	case "official_clients":
		return pkgopenai.IsCodexOfficialClientByHeaders(userAgent, originator)
	case "cli_only":
		return pkgopenai.IsCodexOfficialClientByHeaders(userAgent, originator) &&
			pkgopenai.IsCodexTerminalRequest(userAgent)
	case "local_proxy_only":
		return codexLocalProxyRequestMatched(c, userAgent, originator)
	default:
		return false
	}
}

// codexLocalProxyRequestMatched identifies the request shape emitted by the
// SAIAI local-proxy OAuth path. The client-generated `version` and
// `chatgpt-account-id` fields are intentionally used as compatibility signals;
// this is a migration/accidental-configuration gate, not a cryptographic
// attestation boundary. Base-URL + API-key requests lack both fields, while
// the observed Base-URL + OAuth hybrid lacks `version`.
func codexLocalProxyRequestMatched(c *gin.Context, userAgent, originator string) bool {
	if !pkgopenai.IsCodexOfficialClientByHeaders(userAgent, originator) || c == nil {
		return false
	}
	return strings.TrimSpace(c.GetHeader("chatgpt-account-id")) != "" &&
		strings.TrimSpace(c.GetHeader("version")) != ""
}

func enforceCodexContinuationAccountBoundary(c *gin.Context, account *service.Account) bool {
	if account == nil || !account.IsOpenAIOAuth() || c == nil {
		return false
	}
	return codexLocalProxyRequestMatched(c, c.GetHeader("User-Agent"), strings.ToLower(strings.TrimSpace(c.GetHeader("originator"))))
}

// codexLocalProxyModelsRequestMatched intentionally omits the `version`
// requirement. Model discovery is a control-plane compatibility request and
// some official surfaces do not attach the model-request version header to
// every discovery attempt; the strict version check remains on Responses
// model ingress.
func codexLocalProxyModelsRequestMatched(c *gin.Context) bool {
	if c == nil || !pkgopenai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) {
		return false
	}
	return strings.TrimSpace(c.GetHeader("chatgpt-account-id")) != ""
}

func (h *OpenAIGatewayHandler) validateCodexClientPolicyHTTP(c *gin.Context, group *service.Group) bool {
	if group == nil || codexClientPolicyMatched(c, group.CodexClientPolicy) {
		return true
	}
	logCodexClientPolicyRejection(c, group)
	if strings.EqualFold(strings.TrimSpace(group.CodexClientPolicy), "local_proxy_only") {
		h.errorResponse(c, http.StatusForbidden, "saiai_local_proxy_required", "This group requires SAIAI local proxy mode")
		return false
	}
	h.errorResponse(c, http.StatusForbidden, "official_client_required", "This group only allows approved Codex clients")
	return false
}

func logCodexClientPolicyRejection(c *gin.Context, group *service.Group) {
	reason := "invalid_client_headers"
	if pkgopenai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) &&
		strings.EqualFold(strings.TrimSpace(group.CodexClientPolicy), "local_proxy_only") {
		reason = "required_proxy_headers_missing"
	}
	// Never include raw headers, account IDs or request bodies in this event.
	requestLogger(c, "handler.openai_gateway.client_policy",
		zap.Int64("group_id", group.ID),
		zap.String("policy", group.CodexClientPolicy),
		zap.String("reason", reason),
	).Warn("openai.codex_client_policy_rejected")
}

func isOpenAIRemoteCompactPath(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	normalizedPath := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	return strings.HasSuffix(normalizedPath, "/responses/compact")
}

func (h *OpenAIGatewayHandler) logOpenAIRemoteCompactOutcome(c *gin.Context, startedAt time.Time) {
	if !isOpenAIRemoteCompactPath(c) {
		return
	}

	var (
		ctx    = context.Background()
		path   string
		status int
	)
	if c != nil {
		if c.Request != nil {
			ctx = c.Request.Context()
			if c.Request.URL != nil {
				path = strings.TrimSpace(c.Request.URL.Path)
			}
		}
		if c.Writer != nil {
			status = c.Writer.Status()
		}
	}

	outcome := "failed"
	if status >= 200 && status < 300 {
		outcome = "succeeded"
	}
	latencyMs := time.Since(startedAt).Milliseconds()
	if latencyMs < 0 {
		latencyMs = 0
	}

	fields := []zap.Field{
		zap.String("component", "handler.openai_gateway.responses"),
		zap.Bool("remote_compact", true),
		zap.String("compact_outcome", outcome),
		zap.Int("status_code", status),
		zap.Int64("latency_ms", latencyMs),
		zap.String("path", path),
	}

	if c != nil {
		if userAgent := strings.TrimSpace(c.GetHeader("User-Agent")); userAgent != "" {
			fields = append(fields, zap.String("request_user_agent", userAgent))
		}
		if v, ok := c.Get(opsModelKey); ok {
			if model, ok := v.(string); ok && strings.TrimSpace(model) != "" {
				fields = append(fields, zap.String("request_model", strings.TrimSpace(model)))
			}
		}
		if v, ok := c.Get(opsAccountIDKey); ok {
			if accountID, ok := v.(int64); ok && accountID > 0 {
				fields = append(fields, zap.Int64("account_id", accountID))
			}
		}
		if c.Writer != nil {
			if upstreamRequestID := strings.TrimSpace(c.Writer.Header().Get("x-request-id")); upstreamRequestID != "" {
				fields = append(fields, zap.String("upstream_request_id", upstreamRequestID))
			} else if upstreamRequestID := strings.TrimSpace(c.Writer.Header().Get("X-Request-Id")); upstreamRequestID != "" {
				fields = append(fields, zap.String("upstream_request_id", upstreamRequestID))
			}
		}
	}

	log := logger.FromContext(ctx).With(fields...)
	if outcome == "succeeded" {
		log.Info("codex.remote_compact.succeeded")
		return
	}
	log.Warn("codex.remote_compact.failed")
}

func (h *OpenAIGatewayHandler) validateFunctionCallOutputRequest(c *gin.Context, body []byte, reqLog *zap.Logger) bool {
	if !gjson.GetBytes(body, `input.#(type=="function_call_output")`).Exists() {
		return true
	}

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		// 保持原有容错语义：解析失败时跳过预校验，沿用后续上游校验结果。
		return true
	}

	c.Set(service.OpenAIParsedRequestBodyKey, reqBody)
	validation := service.ValidateFunctionCallOutputContext(reqBody)
	if !validation.HasFunctionCallOutput {
		return true
	}

	previousResponseID, _ := reqBody["previous_response_id"].(string)
	if strings.TrimSpace(previousResponseID) != "" || validation.HasToolCallContext {
		return true
	}

	if validation.HasFunctionCallOutputMissingCallID {
		reqLog.Warn("openai.request_validation_failed",
			zap.String("reason", "function_call_output_missing_call_id"),
		)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "function_call_output requires call_id or previous_response_id; if relying on history, ensure store=true and reuse previous_response_id")
		return false
	}
	if validation.HasItemReferenceForAllCallIDs {
		return true
	}

	reqLog.Warn("openai.request_validation_failed",
		zap.String("reason", "function_call_output_missing_item_reference"),
	)
	h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "function_call_output requires item_reference ids matching each call_id, or previous_response_id/tool_call context; if relying on history, ensure store=true and reuse previous_response_id")
	return false
}

func (h *OpenAIGatewayHandler) acquireResponsesUserSlot(
	c *gin.Context,
	userID int64,
	userConcurrency int,
	reqStream bool,
	streamStarted *bool,
	reqLog *zap.Logger,
) (func(), bool) {
	ctx := c.Request.Context()
	userReleaseFunc, userAcquired, err := h.concurrencyHelper.TryAcquireUserSlot(ctx, userID, userConcurrency)
	if err != nil {
		reqLog.Warn("openai.user_slot_acquire_failed", zap.Error(err))
		h.handleConcurrencyError(c, err, "user", *streamStarted)
		return nil, false
	}
	if userAcquired {
		return wrapReleaseOnDone(ctx, userReleaseFunc), true
	}

	maxWait := service.CalculateMaxWait(userConcurrency)
	canWait, waitErr := h.concurrencyHelper.IncrementWaitCount(ctx, userID, maxWait)
	if waitErr != nil {
		reqLog.Warn("openai.user_wait_counter_increment_failed", zap.Error(waitErr))
		// 按现有降级语义：等待计数异常时放行后续抢槽流程
	} else if !canWait {
		reqLog.Info("openai.user_wait_queue_full", zap.Int("max_wait", maxWait))
		h.errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later")
		return nil, false
	}

	waitCounted := waitErr == nil && canWait
	defer func() {
		if waitCounted {
			h.concurrencyHelper.DecrementWaitCount(ctx, userID)
		}
	}()

	userReleaseFunc, err = h.concurrencyHelper.AcquireUserSlotWithWait(c, userID, userConcurrency, reqStream, streamStarted)
	if err != nil {
		reqLog.Warn("openai.user_slot_acquire_failed_after_wait", zap.Error(err))
		h.handleConcurrencyError(c, err, "user", *streamStarted)
		return nil, false
	}

	// 槽位获取成功后，立刻退出等待计数。
	if waitCounted {
		h.concurrencyHelper.DecrementWaitCount(ctx, userID)
		waitCounted = false
	}
	return wrapReleaseOnDone(ctx, userReleaseFunc), true
}

func (h *OpenAIGatewayHandler) acquireResponsesAccountSlot(
	c *gin.Context,
	groupID *int64,
	sessionHash string,
	selection *service.AccountSelectionResult,
	reqStream bool,
	streamStarted *bool,
	reqLog *zap.Logger,
) (func(), bool) {
	if selection == nil || selection.Account == nil {
		h.writeError(c, errEnvelopeNoAvailableAccount, *streamStarted)
		return nil, false
	}

	ctx := c.Request.Context()
	account := selection.Account
	if selection.Acquired {
		return wrapReleaseOnDone(ctx, selection.ReleaseFunc), true
	}
	if selection.WaitPlan == nil {
		h.writeError(c, errEnvelopeNoAvailableAccount, *streamStarted)
		return nil, false
	}

	fastReleaseFunc, fastAcquired, err := h.concurrencyHelper.TryAcquireAccountSlot(
		ctx,
		account.ID,
		selection.WaitPlan.MaxConcurrency,
	)
	if err != nil {
		reqLog.Warn("openai.account_slot_quick_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		h.handleConcurrencyError(c, err, "account", *streamStarted)
		return nil, false
	}
	if fastAcquired {
		if err := h.gatewayService.BindStickySession(ctx, groupID, sessionHash, account.ID); err != nil {
			reqLog.Warn("openai.bind_sticky_session_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		}
		return wrapReleaseOnDone(ctx, fastReleaseFunc), true
	}

	canWait, waitErr := h.concurrencyHelper.IncrementAccountWaitCount(ctx, account.ID, selection.WaitPlan.MaxWaiting)
	if waitErr != nil {
		reqLog.Warn("openai.account_wait_counter_increment_failed", zap.Int64("account_id", account.ID), zap.Error(waitErr))
	} else if !canWait {
		reqLog.Info("openai.account_wait_queue_full",
			zap.Int64("account_id", account.ID),
			zap.Int("max_waiting", selection.WaitPlan.MaxWaiting),
		)
		h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", *streamStarted)
		return nil, false
	}

	accountWaitCounted := waitErr == nil && canWait
	releaseWait := func() {
		if accountWaitCounted {
			h.concurrencyHelper.DecrementAccountWaitCount(ctx, account.ID)
			accountWaitCounted = false
		}
	}
	defer releaseWait()

	accountReleaseFunc, err := h.concurrencyHelper.AcquireAccountSlotWithWaitTimeout(
		c,
		account.ID,
		selection.WaitPlan.MaxConcurrency,
		selection.WaitPlan.Timeout,
		reqStream,
		streamStarted,
	)
	if err != nil {
		reqLog.Warn("openai.account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		h.handleConcurrencyError(c, err, "account", *streamStarted)
		return nil, false
	}

	// Slot acquired: no longer waiting in queue.
	releaseWait()
	if err := h.gatewayService.BindStickySession(ctx, groupID, sessionHash, account.ID); err != nil {
		reqLog.Warn("openai.bind_sticky_session_failed", zap.Int64("account_id", account.ID), zap.Error(err))
	}
	return wrapReleaseOnDone(ctx, accountReleaseFunc), true
}

// ResponsesWebSocket handles OpenAI Responses API WebSocket ingress endpoint
// GET /openai/v1/responses (Upgrade: websocket)
func (h *OpenAIGatewayHandler) ResponsesWebSocket(c *gin.Context) {
	if !isOpenAIWSUpgradeRequest(c.Request) {
		h.errorResponse(c, http.StatusUpgradeRequired, "invalid_request_error", "WebSocket upgrade required (Upgrade: websocket)")
		return
	}
	setOpenAIClientTransportWS(c)

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

	reqLog := requestLogger(
		c,
		"handler.openai_gateway.responses_ws",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.Bool("openai_ws_mode", true),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}
	reqLog.Info("openai.websocket_ingress_started")
	clientIP := ip.GetClientIP(c)
	userAgent := strings.TrimSpace(c.GetHeader("User-Agent"))

	wsConn, err := coderws.Accept(c.Writer, c.Request, &coderws.AcceptOptions{
		CompressionMode: coderws.CompressionContextTakeover,
	})
	if err != nil {
		reqLog.Warn("openai.websocket_accept_failed",
			zap.Error(err),
			zap.String("client_ip", clientIP),
			zap.String("request_user_agent", userAgent),
			zap.String("upgrade_header", strings.TrimSpace(c.GetHeader("Upgrade"))),
			zap.String("connection_header", strings.TrimSpace(c.GetHeader("Connection"))),
			zap.String("sec_websocket_version", strings.TrimSpace(c.GetHeader("Sec-WebSocket-Version"))),
			zap.Bool("has_sec_websocket_key", strings.TrimSpace(c.GetHeader("Sec-WebSocket-Key")) != ""),
		)
		return
	}
	defer func() {
		_ = wsConn.CloseNow()
	}()
	wsConn.SetReadLimit(16 * 1024 * 1024)

	ctx := c.Request.Context()
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	msgType, firstMessage, err := wsConn.Read(readCtx)
	cancel()
	if err != nil {
		closeStatus, closeReason := summarizeWSCloseErrorForLog(err)
		reqLog.Warn("openai.websocket_read_first_message_failed",
			zap.Error(err),
			zap.String("client_ip", clientIP),
			zap.String("close_status", closeStatus),
			zap.String("close_reason", closeReason),
			zap.Duration("read_timeout", 30*time.Second),
		)
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusPolicyViolation, "missing first response.create message")
		return
	}
	if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusPolicyViolation, "unsupported websocket message type")
		return
	}
	if !gjson.ValidBytes(firstMessage) {
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusPolicyViolation, "invalid JSON payload")
		return
	}
	originalFirstMessage := append([]byte(nil), firstMessage...)

	reqModel := strings.TrimSpace(gjson.GetBytes(firstMessage, "model").String())
	setOpsRequestContext(c, reqModel, true, firstMessage)
	usageSessionID := service.ResolveOpenAIUsageSessionID(
		c.GetHeader("session_id"),
		c.GetHeader("conversation_id"),
		gjson.GetBytes(firstMessage, "prompt_cache_key").String(),
	)
	if reqModel == "" {
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusPolicyViolation, "model is required in first response.create payload")
		return
	}
	if apiKey.Group != nil && apiKey.Group.IsModelBlocked(reqModel) {
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusPolicyViolation, fmt.Sprintf("model %s is not allowed for this group", reqModel))
		return
	}
	if apiKey.Group != nil && !codexClientPolicyMatched(c, apiKey.Group.CodexClientPolicy) {
		logCodexClientPolicyRejection(c, apiKey.Group)
		reason := "approved Codex client required"
		if strings.EqualFold(strings.TrimSpace(apiKey.Group.CodexClientPolicy), "local_proxy_only") {
			reason = "SAIAI local proxy required"
		}
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusPolicyViolation, reason)
		return
	}
	previousResponseID := strings.TrimSpace(gjson.GetBytes(firstMessage, "previous_response_id").String())
	previousResponseIDKind := service.ClassifyOpenAIPreviousResponseIDKind(previousResponseID)
	if previousResponseID != "" && previousResponseIDKind == service.OpenAIPreviousResponseIDKindMessageID {
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusPolicyViolation, "previous_response_id must be a response.id (resp_*), not a message id")
		return
	}
	reqLog = reqLog.With(
		zap.Bool("ws_ingress", true),
		zap.String("model", reqModel),
		zap.Bool("has_previous_response_id", previousResponseID != ""),
		zap.String("previous_response_id_kind", previousResponseIDKind),
	)

	var currentUserRelease func()
	var currentAccountRelease func()
	releaseTurnSlots := func() {
		if currentAccountRelease != nil {
			currentAccountRelease()
			currentAccountRelease = nil
		}
		if currentUserRelease != nil {
			currentUserRelease()
			currentUserRelease = nil
		}
	}
	// 必须尽早注册，确保任何 early return 都能释放已获取的并发槽位。
	defer releaseTurnSlots()

	userReleaseFunc, userAcquired, err := h.concurrencyHelper.TryAcquireUserSlot(ctx, subject.UserID, subject.Concurrency)
	if err != nil {
		reqLog.Warn("openai.websocket_user_slot_acquire_failed", zap.Error(err))
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusInternalError, "failed to acquire user concurrency slot")
		return
	}
	if !userAcquired {
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusTryAgainLater, "too many concurrent requests, please retry later")
		return
	}
	currentUserRelease = wrapReleaseOnDone(ctx, userReleaseFunc)

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(ctx, apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		reqLog.Info("openai.websocket_billing_eligibility_check_failed", zap.Error(err))
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusPolicyViolation, "billing check failed")
		return
	}

	sessionHash := h.gatewayService.GenerateSessionHashWithFallback(
		c,
		firstMessage,
		openAIWSIngressFallbackSessionSeed(subject.UserID, apiKey.ID, apiKey.GroupID),
	)
	excludedAccountIDs := make(map[int64]struct{}, 4)
	selection, scheduleDecision, err := h.gatewayService.SelectAccountForNativeCodexRequest(
		c,
		apiKey.GroupID,
		subject.UserID,
		previousResponseID,
		sessionHash,
		reqModel,
		excludedAccountIDs,
		service.OpenAIUpstreamTransportResponsesWebsocketV2,
		firstMessage,
	)
	if err != nil {
		reqLog.Warn("openai.websocket_account_select_failed", zap.Error(err))
		if service.IsOpenAITurnStateAccountMismatch(err) {
			closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusPolicyViolation, "conversation turn state cannot be verified for an available account")
			return
		}
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusTryAgainLater, "no available account")
		return
	}
	if selection == nil || selection.Account == nil {
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusTryAgainLater, "no available account")
		return
	}

	account := selection.Account
	setOpsSelectedAccount(c, account.ID, account.Platform)
	preflightContinuationMigrated := false
	if enforceCodexContinuationAccountBoundary(c, account) && previousResponseID != "" {
		continuationAccountID, ownerErr := h.gatewayService.OpenAIContinuationAccountID(ctx, subject.UserID, previousResponseID)
		if ownerErr != nil {
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusTryAgainLater, "unable to verify conversation account")
			return
		}
		if !service.OpenAIContinuationAccountMatches(previousResponseID, continuationAccountID, account.ID) {
			migratedPayload, migrated, migrateErr := h.gatewayService.PrepareOpenAIWSContinuationFailoverPayload(
				ctx,
				subject.UserID,
				continuationAccountID,
				previousResponseID,
				firstMessage,
			)
			if migrateErr != nil {
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusTryAgainLater, "unable to rebuild conversation context for account failover")
				return
			}
			if !migrated {
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusPolicyViolation, "conversation cannot continue on a different upstream account; replay context is unavailable")
				return
			}
			firstMessage = migratedPayload
			preflightContinuationMigrated = true
			h.gatewayService.RecordOpenAIAccountSwitch()
			reqLog.Info("openai.websocket_continuation_account_failover_prepared",
				zap.Int64("exhausted_account_id", continuationAccountID),
				zap.Int64("replacement_account_id", account.ID),
			)
		}
	}
	accountMaxConcurrency := account.Concurrency
	if selection.WaitPlan != nil && selection.WaitPlan.MaxConcurrency > 0 {
		accountMaxConcurrency = selection.WaitPlan.MaxConcurrency
	}
	accountReleaseFunc := selection.ReleaseFunc
	if !selection.Acquired {
		if selection.WaitPlan == nil {
			closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusTryAgainLater, "account is busy, please retry later")
			return
		}
		fastReleaseFunc, fastAcquired, err := h.concurrencyHelper.TryAcquireAccountSlot(
			ctx,
			account.ID,
			selection.WaitPlan.MaxConcurrency,
		)
		if err != nil {
			reqLog.Warn("openai.websocket_account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusInternalError, "failed to acquire account concurrency slot")
			return
		}
		if !fastAcquired {
			closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusTryAgainLater, "account is busy, please retry later")
			return
		}
		accountReleaseFunc = fastReleaseFunc
	}
	currentAccountRelease = wrapReleaseOnDone(ctx, accountReleaseFunc)
	if err := h.gatewayService.BindStickySession(ctx, apiKey.GroupID, sessionHash, account.ID); err != nil {
		reqLog.Warn("openai.websocket_bind_sticky_session_failed", zap.Int64("account_id", account.ID), zap.Error(err))
	}

	token, _, err := h.gatewayService.GetAccessToken(ctx, account)
	if err != nil {
		reqLog.Warn("openai.websocket_get_access_token_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusInternalError, "failed to get access token")
		return
	}

	reqLog.Debug("openai.websocket_account_selected",
		zap.Int64("account_id", account.ID),
		zap.String("account_name", account.Name),
		zap.String("schedule_layer", scheduleDecision.Layer),
		zap.Int("candidate_count", scheduleDecision.CandidateCount),
	)

	hooks := &service.OpenAIWSIngressHooks{
		OnClientTurn: func(turn int, rawPayload []byte) error {
			policyPayload := rawPayload
			if turn == 1 && preflightContinuationMigrated {
				policyPayload = originalFirstMessage
			}
			turnModel := strings.TrimSpace(gjson.GetBytes(policyPayload, "model").String())
			if apiKey.Group != nil && apiKey.Group.IsModelBlocked(turnModel) {
				return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, fmt.Sprintf("model %s is not allowed for this group", turnModel), nil)
			}
			if err := h.revalidateOpenAIWSTurn(ctx, apiKey, turn); err != nil {
				return err
			}
			requestID, _ := ctx.Value(ctxkey.RequestID).(string)
			h.submitOpenAIInputModeration(apiKey, requestID, policyPayload, service.InputModerationSourceOpenAIResponsesWS, turn)
			return nil
		},
		BeforeTurn: func(turn int) error {
			if currentUserRelease != nil && currentAccountRelease != nil {
				return nil
			}
			// 防御式清理：避免异常路径下旧槽位覆盖导致泄漏。
			releaseTurnSlots()
			// 非首轮 turn 需要重新抢占并发槽位，避免长连接空闲占槽。
			userReleaseFunc, userAcquired, err := h.concurrencyHelper.TryAcquireUserSlot(ctx, subject.UserID, subject.Concurrency)
			if err != nil {
				return service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to acquire user concurrency slot", err)
			}
			if !userAcquired {
				return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "too many concurrent requests, please retry later", nil)
			}
			accountReleaseFunc, accountAcquired, err := h.concurrencyHelper.TryAcquireAccountSlot(ctx, account.ID, accountMaxConcurrency)
			if err != nil {
				if userReleaseFunc != nil {
					userReleaseFunc()
				}
				return service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to acquire account concurrency slot", err)
			}
			if !accountAcquired {
				if userReleaseFunc != nil {
					userReleaseFunc()
				}
				return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "account is busy, please retry later", nil)
			}
			currentUserRelease = wrapReleaseOnDone(ctx, userReleaseFunc)
			currentAccountRelease = wrapReleaseOnDone(ctx, accountReleaseFunc)
			return nil
		},
		AfterTurn: func(turn int, result *service.OpenAIForwardResult, turnErr error) {
			releaseTurnSlots()
			if turnErr != nil || result == nil {
				return
			}
			turnAccount := account
			if turnAccount.Type == service.AccountTypeOAuth {
				h.gatewayService.UpdateCodexUsageSnapshotFromHeaders(ctx, turnAccount.ID, result.ResponseHeaders)
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(turnAccount.ID, true, result.FirstTokenMs)
			requestPayloadHash := strings.TrimSpace(result.RequestPayloadHash)
			if requestPayloadHash == "" {
				requestPayloadHash = service.HashUsageRequestPayload(originalFirstMessage)
			}
			h.submitUsageRecordTask(func(taskCtx context.Context) {
				if err := h.gatewayService.RecordUsage(taskCtx, &service.OpenAIRecordUsageInput{
					Result:             result,
					APIKey:             apiKey,
					User:               apiKey.User,
					Account:            turnAccount,
					Subscription:       subscription,
					SessionID:          usageSessionID,
					InboundEndpoint:    GetInboundEndpoint(c),
					UpstreamEndpoint:   GetUpstreamEndpoint(c, turnAccount.Platform),
					UserAgent:          userAgent,
					IPAddress:          clientIP,
					RequestPayloadHash: requestPayloadHash,
					APIKeyService:      h.apiKeyService,
				}); err != nil {
					reqLog.Error("openai.websocket_record_usage_failed",
						zap.Int64("account_id", turnAccount.ID),
						zap.String("request_id", result.RequestID),
						zap.Error(err),
					)
				}
			})
		},
		OnAccountExhausted: func(failure *service.OpenAIWSAccountFailoverError) (*service.OpenAIWSFailoverTarget, error) {
			if failure == nil || failure.AccountID() <= 0 {
				return nil, service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "invalid upstream account failover state", nil)
			}
			excludedAccountIDs[failure.AccountID()] = struct{}{}
			h.gatewayService.ReportOpenAIAccountScheduleResult(failure.AccountID(), false, nil)
			releaseTurnSlots()

			userReleaseFunc, userAcquired, acquireUserErr := h.concurrencyHelper.TryAcquireUserSlot(ctx, subject.UserID, subject.Concurrency)
			if acquireUserErr != nil {
				return nil, service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to acquire user concurrency slot during account failover", acquireUserErr)
			}
			if !userAcquired {
				return nil, service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "too many concurrent requests, please retry later", nil)
			}
			currentUserRelease = wrapReleaseOnDone(ctx, userReleaseFunc)

			failoverPayload := failure.ReplayPayload()
			failoverModel := strings.TrimSpace(gjson.GetBytes(failoverPayload, "model").String())
			nextSelection, nextDecision, selectErr := h.gatewayService.SelectAccountWithSchedulerForUser(
				ctx,
				apiKey.GroupID,
				subject.UserID,
				"",
				sessionHash,
				failoverModel,
				excludedAccountIDs,
				service.OpenAIUpstreamTransportResponsesWebsocketV2,
			)
			if selectErr != nil || nextSelection == nil || nextSelection.Account == nil {
				releaseTurnSlots()
				return nil, service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "all compatible upstream accounts are currently unavailable", selectErr)
			}
			nextAccount := nextSelection.Account
			nextAccountMaxConcurrency := nextAccount.Concurrency
			if nextSelection.WaitPlan != nil && nextSelection.WaitPlan.MaxConcurrency > 0 {
				nextAccountMaxConcurrency = nextSelection.WaitPlan.MaxConcurrency
			}
			nextAccountRelease := nextSelection.ReleaseFunc
			if !nextSelection.Acquired {
				if nextSelection.WaitPlan == nil {
					releaseTurnSlots()
					return nil, service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "replacement upstream account is busy", nil)
				}
				fastRelease, fastAcquired, acquireAccountErr := h.concurrencyHelper.TryAcquireAccountSlot(
					ctx,
					nextAccount.ID,
					nextSelection.WaitPlan.MaxConcurrency,
				)
				if acquireAccountErr != nil {
					releaseTurnSlots()
					return nil, service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to acquire replacement account concurrency slot", acquireAccountErr)
				}
				if !fastAcquired {
					releaseTurnSlots()
					return nil, service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "replacement upstream account is busy", nil)
				}
				nextAccountRelease = fastRelease
			}
			currentAccountRelease = wrapReleaseOnDone(ctx, nextAccountRelease)

			nextToken, _, tokenErr := h.gatewayService.GetAccessToken(ctx, nextAccount)
			if tokenErr != nil {
				releaseTurnSlots()
				return nil, service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to get replacement account access token", tokenErr)
			}
			account = nextAccount
			accountMaxConcurrency = nextAccountMaxConcurrency
			if bindErr := h.gatewayService.BindStickySession(ctx, apiKey.GroupID, sessionHash, nextAccount.ID); bindErr != nil {
				reqLog.Warn("openai.websocket_failover_bind_sticky_session_failed", zap.Int64("account_id", nextAccount.ID), zap.Error(bindErr))
			}
			h.gatewayService.RecordOpenAIAccountSwitch()
			setOpsSelectedAccount(c, nextAccount.ID, nextAccount.Platform)
			reqLog.Info("openai.websocket_account_failover_selected",
				zap.Int("turn", failure.Turn()),
				zap.Int64("exhausted_account_id", failure.AccountID()),
				zap.Int64("replacement_account_id", nextAccount.ID),
				zap.String("schedule_layer", nextDecision.Layer),
				zap.Int("candidate_count", nextDecision.CandidateCount),
			)
			return &service.OpenAIWSFailoverTarget{Account: nextAccount, Token: nextToken}, nil
		},
	}

	if err := h.gatewayService.ProxyResponsesWebSocketFromClient(ctx, c, wsConn, account, token, msgType, firstMessage, hooks); err != nil {
		h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
		closeStatus, closeReason := summarizeWSCloseErrorForLog(err)
		reqLog.Warn("openai.websocket_proxy_failed",
			zap.Int64("account_id", account.ID),
			zap.Error(err),
			zap.String("close_status", closeStatus),
			zap.String("close_reason", closeReason),
		)
		var closeErr *service.OpenAIWSClientCloseError
		if errors.As(err, &closeErr) {
			closeOpenAIClientWSWithOps(c, wsConn, closeErr.StatusCode(), closeErr.Reason())
			return
		}
		closeOpenAIClientWSWithOps(c, wsConn, coderws.StatusInternalError, "upstream websocket proxy failed")
		return
	}
	reqLog.Info("openai.websocket_ingress_closed", zap.Int64("account_id", account.ID))
}

func (h *OpenAIGatewayHandler) revalidateOpenAIWSTurn(ctx context.Context, apiKey *service.APIKey, turn int) error {
	if turn <= 1 {
		return nil
	}
	if h == nil || h.apiKeyService == nil || apiKey == nil || strings.TrimSpace(apiKey.Key) == "" {
		return service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to revalidate user account", nil)
	}
	freshAPIKey, err := h.apiKeyService.GetByKey(ctx, apiKey.Key)
	if err != nil {
		return service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to revalidate user account", err)
	}
	if freshAPIKey == nil || !freshAPIKey.IsActive() || freshAPIKey.User == nil || !freshAPIKey.User.IsActive() {
		return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "user account is not active", nil)
	}
	if h.inputModerationService != nil && !freshAPIKey.User.IsAdmin() {
		blockedUntil, err := h.inputModerationService.GetActiveCooldown(ctx, freshAPIKey.User.ID)
		if err != nil {
			return service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to revalidate user risk state", err)
		}
		if blockedUntil != nil {
			return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "user access is temporarily suspended", nil)
		}
	}
	return nil
}

func (h *OpenAIGatewayHandler) recoverResponsesPanic(c *gin.Context, streamStarted *bool) {
	recovered := recover()
	if recovered == nil {
		return
	}

	started := false
	if streamStarted != nil {
		started = *streamStarted
	}
	wroteFallback := h.ensureForwardErrorResponse(c, started)
	requestLogger(c, "handler.openai_gateway.responses").Error(
		"openai.responses_panic_recovered",
		zap.Bool("fallback_error_response_written", wroteFallback),
		zap.Any("panic", recovered),
		zap.ByteString("stack", debug.Stack()),
	)
}

func (h *OpenAIGatewayHandler) ensureResponsesDependencies(c *gin.Context, reqLog *zap.Logger) bool {
	missing := h.missingResponsesDependencies()
	if len(missing) == 0 {
		return true
	}

	if reqLog == nil {
		reqLog = requestLogger(c, "handler.openai_gateway.responses")
	}
	reqLog.Error("openai.handler_dependencies_missing", zap.Strings("missing_dependencies", missing))

	if c != nil && c.Writer != nil && !c.Writer.Written() {
		h.writeError(c, errEnvelopeGatewayDependenciesMissing, false)
	}
	return false
}

func (h *OpenAIGatewayHandler) missingResponsesDependencies() []string {
	missing := make([]string, 0, 5)
	if h == nil {
		return append(missing, "handler")
	}
	if h.gatewayService == nil {
		missing = append(missing, "gatewayService")
	}
	if h.billingCacheService == nil {
		missing = append(missing, "billingCacheService")
	}
	if h.apiKeyService == nil {
		missing = append(missing, "apiKeyService")
	}
	if h.concurrencyHelper == nil || h.concurrencyHelper.concurrencyService == nil {
		missing = append(missing, "concurrencyHelper")
	}
	return missing
}

func getContextInt64(c *gin.Context, key string) (int64, bool) {
	if c == nil || key == "" {
		return 0, false
	}
	v, ok := c.Get(key)
	if !ok {
		return 0, false
	}
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case int32:
		return int64(t), true
	case float64:
		return int64(t), true
	default:
		return 0, false
	}
}

func (h *OpenAIGatewayHandler) submitUsageRecordTask(task service.UsageRecordTask) {
	if task == nil {
		return
	}
	if h.usageRecordWorkerPool != nil {
		h.usageRecordWorkerPool.Submit(task)
		return
	}
	h.runUsageRecordTask(task)
}

// Native Chat has no provider token usage with which to reconstruct a dropped
// charge. A full/stopped usage queue must settle synchronously instead.
func (h *OpenAIGatewayHandler) submitChatGPTUsageRecordTask(task service.UsageRecordTask) {
	if task == nil {
		return
	}
	if h.usageRecordWorkerPool != nil && h.usageRecordWorkerPool.Submit(task) != service.UsageRecordSubmitModeDropped {
		return
	}
	h.runUsageRecordTask(task)
}

func (h *OpenAIGatewayHandler) runUsageRecordTask(task service.UsageRecordTask) {
	// 回退路径：worker 池未注入时同步执行，避免退回到无界 goroutine 模式。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.responses"),
				zap.Any("panic", recovered),
			).Error("openai.usage_record_task_panic_recovered")
		}
	}()
	task(ctx)
}

// handleConcurrencyError handles concurrency-related errors with proper 429 response
func (h *OpenAIGatewayHandler) handleConcurrencyError(c *gin.Context, err error, slotType string, streamStarted bool) {
	h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error",
		fmt.Sprintf("Concurrency limit exceeded for %s, please retry later", slotType), streamStarted)
}

func (h *OpenAIGatewayHandler) handleFailoverExhausted(c *gin.Context, failoverErr *service.UpstreamFailoverError, streamStarted bool) {
	statusCode := failoverErr.StatusCode
	responseBody := failoverErr.ResponseBody

	// 先检查透传规则
	if h.errorPassthroughService != nil && len(responseBody) > 0 {
		if rule := h.errorPassthroughService.MatchRule("openai", statusCode, responseBody); rule != nil {
			// 确定响应状态码
			respCode := statusCode
			if !rule.PassthroughCode && rule.ResponseCode != nil {
				respCode = *rule.ResponseCode
			}

			// 确定响应消息
			msg := service.ExtractUpstreamErrorMessage(responseBody)
			if !rule.PassthroughBody && rule.CustomMessage != nil {
				msg = *rule.CustomMessage
			}

			if rule.SkipMonitoring {
				c.Set(service.OpsSkipPassthroughKey, true)
			}

			h.handleStreamingAwareError(c, respCode, "upstream_error", msg, streamStarted)
			return
		}
	}

	// 记录原始上游状态码，以便 ops 错误日志捕获真实的上游错误
	upstreamMsg := service.ExtractUpstreamErrorMessage(responseBody)
	service.SetOpsUpstreamError(c, statusCode, upstreamMsg, "")

	// 使用默认的错误映射
	status, errType, errMsg := h.mapUpstreamError(statusCode)
	h.handleStreamingAwareError(c, status, errType, errMsg, streamStarted)
}

// handleFailoverExhaustedSimple 简化版本，用于没有响应体的情况
func (h *OpenAIGatewayHandler) handleFailoverExhaustedSimple(c *gin.Context, statusCode int, streamStarted bool) {
	status, errType, errMsg := h.mapUpstreamError(statusCode)
	service.SetOpsUpstreamError(c, statusCode, errMsg, "")
	h.handleStreamingAwareError(c, status, errType, errMsg, streamStarted)
}

func (h *OpenAIGatewayHandler) mapUpstreamError(statusCode int) (int, string, string) {
	switch statusCode {
	case 401:
		return http.StatusBadGateway, "upstream_error", "Upstream authentication failed, please contact administrator"
	case 403:
		return http.StatusBadGateway, "upstream_error", "Upstream access forbidden, please contact administrator"
	case 429:
		return http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit exceeded, please retry later"
	case 529:
		return http.StatusServiceUnavailable, "upstream_error", "Upstream service overloaded, please retry later"
	case 500, 502, 503, 504:
		return http.StatusBadGateway, "upstream_error", "Upstream service temporarily unavailable"
	default:
		return http.StatusBadGateway, "upstream_error", "Upstream request failed"
	}
}

// handleStreamingAwareError handles errors that may occur after streaming has started.
// Legacy entry preserved for callers that don't need code/Retry-After. New
// callers should construct a gatewayErrorEnvelope and call writeError directly.
func (h *OpenAIGatewayHandler) handleStreamingAwareError(c *gin.Context, status int, errType, message string, streamStarted bool) {
	h.writeError(c, gatewayErrorEnvelope{Status: status, Type: errType, Message: message}, streamStarted)
}

// ensureForwardErrorResponse 在 Forward 返回错误但尚未写响应时补写统一错误响应。
func (h *OpenAIGatewayHandler) ensureForwardErrorResponse(c *gin.Context, streamStarted bool) bool {
	if c == nil || c.Writer == nil || c.Writer.Written() {
		return false
	}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed", streamStarted)
	return true
}

func shouldLogOpenAIForwardFailureAsWarn(c *gin.Context, wroteFallback bool) bool {
	if wroteFallback {
		return false
	}
	if c == nil || c.Writer == nil {
		return false
	}
	return c.Writer.Written()
}

// errorResponse returns OpenAI API format error response.
// Legacy entry preserved for callers that don't need code/Retry-After. New
// callers should construct a gatewayErrorEnvelope and call writeError directly.
func (h *OpenAIGatewayHandler) errorResponse(c *gin.Context, status int, errType, message string) {
	h.writeError(c, gatewayErrorEnvelope{Status: status, Type: errType, Message: message}, false)
}

// gatewayErrorEnvelope describes an error to return to OpenAI / Anthropic compatible clients.
//
// Type follows the OpenAI error envelope convention:
//   - "gateway_error"        — saiai gateway-side failure (no candidate account, missing handler dependency)
//   - "upstream_unreachable" — outbound request failed before any upstream response header was seen
//     (socks reset, proxy unreachable, connection RST during body send)
//   - "upstream_error"       — upstream returned 4xx/5xx (see mapUpstreamError)
//   - "rate_limit_error"     — rate limit
//   - "api_error"            — fallback when no finer classification fits
//
// Code is an optional OpenAI-style sub-classifier ("no_available_account",
// "upstream_request_failed", ...). Only emitted in the OpenAI-format response
// body. Anthropic-format responses omit Code to preserve protocol compatibility
// with strict client SDKs.
//
// RetryAfter > 0 emits a Retry-After header (in seconds) when a JSON response
// body is being written. Suppressed once a stream has begun, since headers can
// no longer be set on a flushed response.
type gatewayErrorEnvelope struct {
	Status     int
	Type       string
	Code       string
	Message    string
	RetryAfter int
}

// upstreamUnreachableRetryAfterSeconds is the Retry-After hint emitted with
// upstream_unreachable. Picked at 30s so it sits well outside the 1-3s
// "transient blip" retry window most LLM SDKs use; this is what lets a
// codex-tui style client break out of an infinite same-turn-id retry loop
// when the upstream backend silently rejects a replay.
const upstreamUnreachableRetryAfterSeconds = 30

// Reusable envelope templates. writeError takes its argument by value so
// callers cannot mutate the templates; if a callsite needs a per-request
// override (e.g. richer Message), copy first then mutate the local copy.
var (
	// errEnvelopeNoAvailableAccount: gateway could not pick any account for the
	// request (group bound to zero accounts, every account unschedulable, etc.).
	errEnvelopeNoAvailableAccount = gatewayErrorEnvelope{
		Status:  http.StatusServiceUnavailable,
		Type:    "gateway_error",
		Code:    "no_available_account",
		Message: "No available accounts",
	}

	// errEnvelopeGatewayDependenciesMissing: handler's required services are
	// not wired (gatewayService / billingCacheService nil, etc.).
	errEnvelopeGatewayDependenciesMissing = gatewayErrorEnvelope{
		Status:  http.StatusServiceUnavailable,
		Type:    "gateway_error",
		Code:    "gateway_dependencies_missing",
		Message: "Service temporarily unavailable",
	}

	// errEnvelopeUpstreamUnreachable: outbound request failed without ever
	// receiving an upstream response header (socks RST, proxy unreachable,
	// connection reset mid-body). Use only when the callsite can prove transport
	// failure.
	errEnvelopeUpstreamUnreachable = gatewayErrorEnvelope{
		Status:     http.StatusBadGateway,
		Type:       "upstream_unreachable",
		Code:       "upstream_request_failed",
		Message:    "Upstream request failed",
		RetryAfter: upstreamUnreachableRetryAfterSeconds,
	}
)

// writeError emits an OpenAI-format error response. When streamStarted is true,
// the error is delivered as an SSE error event so the half-written stream can
// terminate cleanly; Retry-After is suppressed in that branch.
func (h *OpenAIGatewayHandler) writeError(c *gin.Context, e gatewayErrorEnvelope, streamStarted bool) {
	e.Message = service.ClientSafeUpstreamErrorMessage(e.Message)
	if streamStarted {
		setOpsLocalFailure(c, opsLocalFailure{status: e.Status, errType: e.Type, code: e.Code, message: e.Message})
		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			return
		}
		var event strings.Builder
		_, _ = event.WriteString(`event: error` + "\n" + `data: {"error":{"type":`)
		_, _ = event.WriteString(strconv.Quote(e.Type))
		if e.Code != "" {
			_, _ = event.WriteString(`,"code":`)
			_, _ = event.WriteString(strconv.Quote(e.Code))
		}
		_, _ = event.WriteString(`,"message":`)
		_, _ = event.WriteString(strconv.Quote(e.Message))
		_, _ = event.WriteString(`}}` + "\n\n")
		if _, err := fmt.Fprint(c.Writer, event.String()); err != nil {
			_ = c.Error(err)
		}
		flusher.Flush()
		return
	}
	if e.RetryAfter > 0 {
		c.Header("Retry-After", strconv.Itoa(e.RetryAfter))
	}
	body := gin.H{
		"type":    e.Type,
		"message": e.Message,
	}
	if e.Code != "" {
		body["code"] = e.Code
	}
	c.JSON(e.Status, gin.H{"error": body})
}

func setOpenAIClientTransportHTTP(c *gin.Context) {
	service.SetOpenAIClientTransport(c, service.OpenAIClientTransportHTTP)
}

func setOpenAIClientTransportWS(c *gin.Context) {
	service.SetOpenAIClientTransport(c, service.OpenAIClientTransportWS)
}

func ensureOpenAIPoolModeSessionHash(sessionHash string, account *service.Account) string {
	if sessionHash != "" || account == nil || !account.IsPoolMode() {
		return sessionHash
	}
	// 为当前请求生成一次性粘性会话键，确保同账号重试不会重新负载均衡到其他账号。
	return "openai-pool-retry-" + uuid.NewString()
}

func openAIWSIngressFallbackSessionSeed(userID, apiKeyID int64, groupID *int64) string {
	gid := int64(0)
	if groupID != nil {
		gid = *groupID
	}
	return fmt.Sprintf("openai_ws_ingress:%d:%d:%d", gid, userID, apiKeyID)
}

func isOpenAIWSUpgradeRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(r.Header.Get("Connection"))), "upgrade")
}

func closeOpenAIClientWS(conn *coderws.Conn, status coderws.StatusCode, reason string) {
	if conn == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 120 {
		reason = reason[:120]
	}
	_ = conn.Close(status, reason)
	_ = conn.CloseNow()
}

func summarizeWSCloseErrorForLog(err error) (string, string) {
	if err == nil {
		return "-", "-"
	}
	statusCode := coderws.CloseStatus(err)
	if statusCode == -1 {
		return "-", "-"
	}
	closeStatus := fmt.Sprintf("%d(%s)", int(statusCode), statusCode.String())
	closeReason := "-"
	var closeErr coderws.CloseError
	if errors.As(err, &closeErr) {
		reason := strings.TrimSpace(closeErr.Reason)
		if reason != "" {
			closeReason = reason
		}
	}
	return closeStatus, closeReason
}
