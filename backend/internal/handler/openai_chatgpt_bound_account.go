package handler

import (
	"math"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// clearChatGPTDeviceCookieHeader removes the local provider proof so the
// official cookie manager performs a real registration on its next request.
const clearChatGPTDeviceCookieHeader = "_devicecheck=; Domain=.chatgpt.com; Path=/; Max-Age=0; Secure; HttpOnly; SameSite=Lax"

// opsGatewayErrorDetailKey carries an operator-only explanation that the Ops
// error logger appends to the recorded message. It never reaches the client.
const opsGatewayErrorDetailKey = "ops_gateway_error_detail"

// Retry-After bounds for a bound account in cooldown. The client-facing
// message carries the exact recovery time; the header stays sane.
const (
	chatGPTBoundRetryAfterMin = time.Second
	chatGPTBoundRetryAfterMax = 6 * time.Hour
)

// chatGPTBoundContext names what is bound to the owner account. It selects
// the existing 409 type whose client remedy discards that binding.
type chatGPTBoundContext int

const (
	chatGPTBoundDevice chatGPTBoundContext = iota
	chatGPTBoundConversation
	chatGPTBoundResume
	chatGPTBoundAttachment
	chatGPTBoundIntegrity
	chatGPTBoundUpload
	// Read-only delivery of history that exists only on the owner account.
	chatGPTBoundConversationRead
	chatGPTBoundAsset
)

func (b chatGPTBoundContext) String() string {
	switch b {
	case chatGPTBoundConversation:
		return "conversation"
	case chatGPTBoundResume:
		return "resume"
	case chatGPTBoundAttachment:
		return "attachment"
	case chatGPTBoundIntegrity:
		return "integrity"
	case chatGPTBoundUpload:
		return "upload"
	case chatGPTBoundConversationRead:
		return "conversation_read"
	case chatGPTBoundAsset:
		return "asset"
	default:
		return "device"
	}
}

// A stale binding cannot be repaired by retrying. Each context keeps the
// type its existing ownership-mismatch branch already returns.
func (b chatGPTBoundContext) staleBinding() (int, string, string) {
	switch b {
	case chatGPTBoundConversation:
		return http.StatusConflict, "conversation_context_unavailable", "ChatGPT conversation account is no longer available; start a new conversation"
	case chatGPTBoundResume:
		return http.StatusConflict, "resume_context_unavailable", "ChatGPT resume account is no longer available; start a new turn"
	case chatGPTBoundAttachment:
		return http.StatusConflict, "attachment_context_unavailable", "Chat attachment account is no longer available; upload the attachment again"
	case chatGPTBoundIntegrity:
		return http.StatusConflict, "integrity_context_unavailable", "Chat integrity account is no longer available; refresh integrity before retrying"
	case chatGPTBoundUpload:
		return http.StatusConflict, "upload_context_unavailable", "Chat upload account is no longer available; upload the file again"
	case chatGPTBoundConversationRead:
		return http.StatusNotFound, "conversation_context_unavailable", "Chat conversation account is no longer available"
	case chatGPTBoundAsset:
		return http.StatusNotFound, "asset_context_unavailable", "Chat asset account is no longer available"
	default:
		return http.StatusConflict, "device_registration_required", "Chat device registration belongs to an account that is no longer available; register this device again"
	}
}

func chatGPTBoundReasonPhrase(reason string) string {
	switch reason {
	case service.ChatGPTBoundReasonRateLimited:
		return "rate limited"
	case service.ChatGPTBoundReasonOverloaded:
		return "overloaded"
	default:
		return "unavailable"
	}
}

func chatGPTBoundRetryAfter(retryAt, now time.Time) int {
	wait := retryAt.Sub(now)
	if wait < chatGPTBoundRetryAfterMin {
		wait = chatGPTBoundRetryAfterMin
	}
	if wait > chatGPTBoundRetryAfterMax {
		wait = chatGPTBoundRetryAfterMax
	}
	return int(math.Ceil(wait.Seconds()))
}

// writeChatGPTBoundAccountUnavailable answers a request whose bound owner
// account cannot serve it. Temporary cooldowns keep every binding and return
// 429 with Retry-After. Stale bindings return the context's 409; a device
// proof bound to that owner is released and cleared so the client registers
// again on an available account. Conversations are never moved silently.
// Errors that are not classified fall back to the caller's previous response.
func (h *OpenAIGatewayHandler) writeChatGPTBoundAccountUnavailable(
	c *gin.Context,
	err error,
	bound chatGPTBoundContext,
	scope service.ChatGPTTurnScope,
	streamStarted bool,
	fallback gatewayErrorEnvelope,
) {
	unavailable, ok := service.AsChatGPTBoundAccountUnavailable(err)
	if !ok {
		h.writeError(c, fallback, streamStarted)
		return
	}
	setOpsSelectedAccount(c, unavailable.AccountID, service.PlatformOpenAI)
	c.Set(opsGatewayErrorDetailKey, unavailable.Error()+"; bound context "+bound.String())
	log := requestLogger(c, "handler.openai_gateway.chatgpt_bound_account",
		zap.Int64("account_id", unavailable.AccountID),
		zap.String("kind", string(unavailable.Kind)),
		zap.String("reason", unavailable.Reason),
		zap.String("bound_context", bound.String()),
		zap.String("path", c.Request.URL.Path),
	)
	if unavailable.Status != "" {
		log = log.With(zap.String("account_status", unavailable.Status))
	}
	if !unavailable.RetryAt.IsZero() {
		log = log.With(zap.Time("retry_at", unavailable.RetryAt))
	}
	switch unavailable.Kind {
	case service.ChatGPTBoundAccountTemporary:
		log.Info("openai.chatgpt_bound_account_unavailable")
		now := time.Now()
		h.writeError(c, gatewayErrorEnvelope{
			Status: http.StatusTooManyRequests,
			Type:   "rate_limit_error",
			Code:   "bound_account_unavailable",
			Message: "Bound ChatGPT account is temporarily " + chatGPTBoundReasonPhrase(unavailable.Reason) +
				"; retry after " + unavailable.RetryAt.UTC().Format(time.RFC3339),
			RetryAfter: chatGPTBoundRetryAfter(unavailable.RetryAt, now),
		}, streamStarted)
	case service.ChatGPTBoundAccountPermanent:
		released, releaseErr := h.gatewayService.ReleaseChatGPTDeviceCookie(c, scope, unavailable.AccountID)
		if releaseErr != nil {
			log = log.With(zap.Error(releaseErr))
		}
		log.Warn("openai.chatgpt_bound_account_unavailable", zap.Bool("device_proof_released", released))
		if (released || bound == chatGPTBoundDevice) && !streamStarted {
			c.Header("Set-Cookie", clearChatGPTDeviceCookieHeader)
		}
		status, errType, message := bound.staleBinding()
		h.writeError(c, gatewayErrorEnvelope{Status: status, Type: errType, Message: message}, streamStarted)
	default:
		log.Warn("openai.chatgpt_bound_account_unavailable")
		h.writeError(c, gatewayErrorEnvelope{
			Status:  http.StatusServiceUnavailable,
			Type:    "service_unavailable",
			Message: "Bound ChatGPT account state is temporarily unavailable; retry later",
		}, streamStarted)
	}
}

// chatGPTNoOAuthAccountEnvelope is the native Chat answer when no OAuth
// account can be selected and no bound owner was classified.
var chatGPTNoOAuthAccountEnvelope = gatewayErrorEnvelope{
	Status:  http.StatusServiceUnavailable,
	Type:    "service_unavailable",
	Message: "No available OpenAI OAuth account",
}

// chatGPTAffinityOwnerUsable reports whether a live-conversation account may
// still serve as the registration/integrity affinity. Only a permanently
// unavailable owner is skipped, so a stale owner cannot pull every new
// registration back to itself; a cooldown or store error keeps the affinity
// and is answered by the bound-account check that follows.
func (h *OpenAIGatewayHandler) chatGPTAffinityOwnerUsable(c *gin.Context, groupID *int64, accountID int64) bool {
	_, err := h.gatewayService.GetChatGPTBoundAccount(c.Request.Context(), groupID, accountID)
	unavailable, typed := service.AsChatGPTBoundAccountUnavailable(err)
	return !typed || unavailable.Kind != service.ChatGPTBoundAccountPermanent
}
