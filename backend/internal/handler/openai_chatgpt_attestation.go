package handler

import (
	"io"
	"net/http"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// macOS Desktop 26.930 uses this native integrity GET instead of the web
// Sentinel prepare POST. Never synthesize a challenge or bypass DeviceCheck.
// The provider response and client integrity headers remain unchanged.
func (h *OpenAIGatewayHandler) ChatGPTAttestationChallenge(c *gin.Context) {
	setOpsRequestContext(c, "", false, nil)
	if h == nil || h.cfg == nil || !h.cfg.Gateway.OpenAIChatEnabled {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Native ChatGPT Chat is disabled")
		return
	}
	key, ok := middleware.GetAPIKeyFromContext(c)
	subject, subjectOK := middleware.GetAuthSubjectFromContext(c)
	if !ok || !subjectOK || key.Group == nil || key.Group.Platform != service.PlatformOpenAI || key.User == nil || key.User.ID != subject.UserID {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "Native Chat integrity requires an authenticated OpenAI group")
		return
	}
	scope := service.ChatGPTTurnScope{UserID: subject.UserID, APIKeyID: key.ID, GroupID: key.Group.ID}
	cache, err := h.gatewayService.ChatGPTUploadCache()
	if err != nil {
		h.errorResponse(c, 503, "integrity_context_unavailable", "Chat integrity ownership is unavailable")
		return
	}
	device, err := service.ChatGPTDeviceID(c.Request.Header)
	if err != nil {
		h.errorResponse(c, 400, "invalid_request_error", "Invalid Chat device identity")
		return
	}
	cookieOwner, ok := h.chatGPTDeviceCookieOwner(c, scope)
	if !ok {
		return
	}
	// Share the existing short upload affinity when present, so creating an
	// attachment and preparing integrity cannot silently use different accounts.
	sessionKey := scope.UploadSessionKey(device)
	owner, err := cache.GetChatGPTUploadOwner(c.Request.Context(), sessionKey)
	if err == nil && cookieOwner != 0 {
		if owner != 0 && owner != cookieOwner {
			h.errorResponse(c, 409, "integrity_context_unavailable", "Chat device and upload accounts differ")
			return
		}
		owner = cookieOwner
	}
	if err == nil && owner == 0 {
		// The integrity GET has no conversation ID. When this scope has exactly
		// one live conversation account, retain it after the short upload lease
		// expires instead of scheduling its next challenge onto another account.
		turns, turnErr := h.gatewayService.ChatGPTTurnCache()
		var accounts []int64
		if turnErr == nil {
			accounts, turnErr = turns.ListChatGPTUpdateAccounts(c.Request.Context(), scope.UpdatesKey())
		}
		if turnErr != nil {
			h.errorResponse(c, 503, "integrity_context_unavailable", "Chat integrity ownership is unavailable")
			return
		}
		var candidateID int64
		if len(accounts) == 1 {
			candidateID = accounts[0]
		} else {
			selection, _, selectErr := h.gatewayService.SelectChatGPTOAuthAccount(c.Request.Context(), key.GroupID, h.gatewayService.GenerateSessionHash(c, nil), "")
			if selectErr != nil || selection == nil || selection.Account == nil {
				h.errorResponse(c, 503, "service_unavailable", "No available OpenAI OAuth account")
				return
			}
			candidateID = selection.Account.ID
			releaseChatGPTControlSelection(selection)
		}
		owner, err = cache.ClaimChatGPTUploadOwner(c.Request.Context(), sessionKey, candidateID, service.ChatGPTUploadSessionTTL)
	}
	if err != nil || owner == 0 {
		h.errorResponse(c, 503, "integrity_context_unavailable", "Chat integrity ownership is unavailable")
		return
	}
	account, err := h.gatewayService.GetChatGPTBoundAccount(c.Request.Context(), key.GroupID, owner)
	if err != nil {
		h.errorResponse(c, 503, "service_unavailable", "Chat integrity account is unavailable")
		return
	}
	setOpsSelectedAccount(c, owner, service.PlatformOpenAI)
	response, err := h.gatewayService.ForwardChatGPTControl(c.Request.Context(), c, account, c.Request.URL.RequestURI())
	if err != nil {
		h.errorResponse(c, 502, "upstream_error", "Chat integrity request failed")
		return
	}
	defer func() { _ = response.Body.Close() }()
	var buffered []byte
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		buffered, err = io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
		if err != nil || len(buffered) > 64*1024 {
			h.errorResponse(c, 502, "upstream_error", "Chat integrity metadata is invalid")
			return
		}
		metadata, decodeErr := decodeChatGPTUploadMetadata(buffered, response.Header.Get("Content-Encoding"))
		challenge, observeErr := service.ChatGPTAttestationResponse(metadata)
		if decodeErr != nil || observeErr != nil {
			h.errorResponse(c, 502, "upstream_error", "Chat integrity metadata is invalid")
			return
		}
		if err = cache.BindChatGPTUploadOwner(c.Request.Context(), []string{scope.AttestationKey(challenge)}, owner, service.ChatGPTAttestationTTL); err != nil {
			h.errorResponse(c, 503, "integrity_context_unavailable", "Chat integrity ownership is unavailable")
			return
		}
	}
	for name, values := range response.Header {
		if shouldCopyChatGPTResponseHeader(name) {
			for _, value := range values {
				c.Writer.Header().Add(name, value)
			}
		}
	}
	c.Header("Cache-Control", "no-store")
	c.Status(response.StatusCode)
	if buffered != nil {
		_, _ = c.Writer.Write(buffered)
	} else {
		_, _ = io.Copy(c.Writer, response.Body)
	}
}
