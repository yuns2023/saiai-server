package handler

import (
	"errors"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// chatGPTSentinelAffinityOwner picks the account that issues a Sentinel
// prepare token: the device's short affinity (UploadSessionKey, shared with
// uploads and Mac integrity), claiming one when absent. Errors are answered
// here (ok=false).
func (h *OpenAIGatewayHandler) chatGPTSentinelAffinityOwner(c *gin.Context, groupID *int64, scope service.ChatGPTTurnScope) (int64, bool) {
	device, err := service.ChatGPTDeviceID(c.Request.Header)
	if err != nil {
		h.errorResponse(c, 400, "invalid_request_error", "Invalid Chat device identity")
		return 0, false
	}
	cache, err := h.gatewayService.ChatGPTUploadCache()
	var owner int64
	sessionKey := scope.UploadSessionKey(device)
	if err == nil {
		owner, err = cache.GetChatGPTUploadOwner(c.Request.Context(), sessionKey)
	}
	if err == nil {
		var ok bool
		if owner, ok = h.chatGPTSkipStaleAffinity(c, groupID, cache, sessionKey, owner); !ok {
			return 0, false
		}
	}
	if err == nil && owner == 0 {
		candidateID, ok := h.chatGPTAffinityCandidate(c, groupID, scope)
		if !ok {
			return 0, false
		}
		owner, err = cache.ClaimChatGPTUploadOwner(c.Request.Context(), sessionKey, candidateID, service.ChatGPTUploadSessionTTL)
	}
	if err != nil || owner == 0 {
		h.errorResponse(c, 503, "integrity_context_unavailable", "Chat integrity ownership is unavailable")
		return 0, false
	}
	return owner, true
}

// preferChatGPTDeviceAffinity points the device affinity at the account the
// device's conversation or turn is routed to, so the next Sentinel prepare
// issues its token there. Best effort: a later write may win, and the model
// request re-checks the token owner.
func (h *OpenAIGatewayHandler) preferChatGPTDeviceAffinity(c *gin.Context, scope service.ChatGPTTurnScope, accountID int64) {
	if scope == (service.ChatGPTTurnScope{}) || accountID <= 0 {
		return
	}
	device, err := service.ChatGPTDeviceID(c.Request.Header)
	if err != nil {
		return
	}
	cache, err := h.gatewayService.ChatGPTUploadCache()
	if err == nil {
		err = cache.SetChatGPTUploadOwner(c.Request.Context(), scope.UploadSessionKey(device), accountID, service.ChatGPTUploadSessionTTL)
	}
	if err != nil {
		logger.L().Warn("openai.chatgpt_device_affinity_update_failed", zap.Int64("account_id", accountID), zap.Error(err))
	}
}

// relayChatGPTSentinelPrepare binds a successful prepare token to the account
// that issued it before the client can use it.
func (h *OpenAIGatewayHandler) relayChatGPTSentinelPrepare(c *gin.Context, resp *http.Response, scope service.ChatGPTTurnScope, accountID int64) {
	var buffered []byte
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, service.MaxChatGPTSentinelResponseBytes+1))
		var decoded []byte
		if err == nil && len(raw) <= service.MaxChatGPTSentinelResponseBytes {
			decoded, err = decodeChatGPTBody(raw, resp.Header.Get("Content-Encoding"), service.MaxChatGPTSentinelResponseBytes)
		} else if err == nil {
			err = errors.New("too large")
		}
		if err != nil {
			h.errorResponse(c, 502, "upstream_error", "Chat integrity response is unreadable")
			return
		}
		token, err := service.ChatGPTSentinelPrepareResponse(decoded)
		if err != nil {
			h.errorResponse(c, 502, "upstream_error", "Chat integrity response has no usable prepare token")
			return
		}
		cache, cacheErr := h.gatewayService.ChatGPTUploadCache()
		if cacheErr == nil {
			cacheErr = cache.BindChatGPTUploadOwner(c.Request.Context(), []string{scope.SentinelKey(token)}, accountID, service.ChatGPTSentinelTTL)
		}
		if cacheErr != nil {
			h.errorResponse(c, 503, "integrity_context_unavailable", "Chat integrity ownership is unavailable")
			return
		}
		buffered = raw
	}
	for name, values := range resp.Header {
		if shouldCopyChatGPTResponseHeader(name) {
			for _, value := range values {
				c.Writer.Header().Add(name, value)
			}
		}
	}
	c.Header("Cache-Control", "no-store")
	c.Status(resp.StatusCode)
	if buffered != nil {
		_, _ = c.Writer.Write(buffered)
	} else {
		_, _ = io.Copy(c.Writer, resp.Body)
	}
}
