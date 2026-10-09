package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIGatewayHandler) chatGPTDeviceCookieOwner(c *gin.Context, scope service.ChatGPTTurnScope) (int64, bool) {
	owner, err := h.gatewayService.ValidateChatGPTDeviceCookie(c, scope)
	if err != nil {
		if errors.Is(err, service.ErrChatGPTDeviceCookieStore) {
			h.errorResponse(c, 503, "integrity_context_unavailable", "Chat device ownership is temporarily unavailable")
			return 0, false
		}
		// Removing an unusable local proof lets the official cookie manager
		// perform its real registration on the next client request. No retry,
		// challenge or success is manufactured by the Gateway.
		c.Header("Set-Cookie", "_devicecheck=; Domain=.chatgpt.com; Path=/; Max-Age=0; Secure; HttpOnly; SameSite=Lax")
		h.errorResponse(c, 409, "device_registration_required", "Chat device registration expired or belongs to another context; register this device again")
		return 0, false
	}
	return owner, true
}

// Native Mac registers an actual Apple token before its integrity GET.
// Control requests do not occupy model slots or produce a billing turn.
func (h *OpenAIGatewayHandler) ChatGPTDeviceCheck(c *gin.Context) {
	setOpsRequestContext(c, "", false, nil)
	if h.cfg == nil || !h.cfg.Gateway.OpenAIChatEnabled {
		h.errorResponse(c, 404, "not_found_error", "Native ChatGPT Chat is disabled")
		return
	}
	key, ok := middleware.GetAPIKeyFromContext(c)
	subject, subjectOK := middleware.GetAuthSubjectFromContext(c)
	if !ok || !subjectOK || key.Group == nil || key.Group.Platform != service.PlatformOpenAI || key.User == nil || key.User.ID != subject.UserID {
		h.errorResponse(c, 403, "permission_error", "Native Chat device registration requires an authenticated OpenAI group")
		return
	}
	scope := service.ChatGPTTurnScope{UserID: subject.UserID, APIKeyID: key.ID, GroupID: key.Group.ID}
	device, err := service.ChatGPTDeviceID(c.Request.Header)
	if err != nil || device == "" {
		h.errorResponse(c, 400, "invalid_request_error", "Invalid Chat device identity")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 64*1024+1))
	var metadata struct {
		BundleID string `json:"bundle_id"`
		Token    string `json:"device_token"`
	}
	if err != nil || len(body) > 64*1024 || json.Unmarshal(body, &metadata) != nil || metadata.BundleID == "" || metadata.Token == "" {
		h.errorResponse(c, 400, "invalid_request_error", "Invalid Chat device registration")
		return
	}
	cookieOwner, ok := h.chatGPTDeviceCookieOwner(c, scope)
	if !ok {
		return
	}
	cache, err := h.gatewayService.ChatGPTUploadCache()
	if err != nil {
		h.errorResponse(c, 503, "integrity_context_unavailable", "Chat integrity ownership is unavailable")
		return
	}
	owner, err := cache.GetChatGPTUploadOwner(c.Request.Context(), scope.UploadSessionKey(device))
	if err == nil && cookieOwner != 0 {
		if owner != 0 && owner != cookieOwner {
			h.errorResponse(c, 409, "integrity_context_unavailable", "Chat device and upload accounts differ")
			return
		}
		owner = cookieOwner
	}
	if err == nil && owner == 0 {
		turns, turnErr := h.gatewayService.ChatGPTTurnCache()
		var accounts []int64
		if turnErr == nil {
			accounts, turnErr = turns.ListChatGPTUpdateAccounts(c.Request.Context(), scope.UpdatesKey())
		}
		if turnErr != nil {
			h.errorResponse(c, 503, "integrity_context_unavailable", "Chat integrity ownership is unavailable")
			return
		}
		if len(accounts) == 1 {
			owner = accounts[0]
		} else {
			selection, _, selectErr := h.gatewayService.SelectChatGPTOAuthAccount(c.Request.Context(), key.GroupID, h.gatewayService.GenerateSessionHash(c, nil), "")
			if selectErr != nil || selection == nil || selection.Account == nil {
				h.errorResponse(c, 503, "service_unavailable", "No available OpenAI OAuth account")
				return
			}
			owner = selection.Account.ID
			releaseChatGPTControlSelection(selection)
		}
	}
	if err != nil || owner == 0 {
		h.errorResponse(c, 503, "integrity_context_unavailable", "Chat integrity ownership is unavailable")
		return
	}
	owner, err = cache.ClaimChatGPTUploadOwner(c.Request.Context(), scope.UploadSessionKey(device), owner, service.ChatGPTUploadSessionTTL)
	if err != nil {
		h.errorResponse(c, 503, "integrity_context_unavailable", "Chat integrity ownership is unavailable")
		return
	}
	account, err := h.gatewayService.GetChatGPTBoundAccount(c.Request.Context(), key.GroupID, owner)
	if err != nil {
		h.errorResponse(c, 503, "service_unavailable", "Chat integrity account is unavailable")
		return
	}
	setOpsSelectedAccount(c, owner, service.PlatformOpenAI)
	response, err := h.gatewayService.ForwardChatGPTConversation(c.Request.Context(), c, account, body, c.Request.URL.RequestURI())
	if err != nil {
		h.errorResponse(c, 502, "upstream_error", "Chat device registration failed")
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		var cookie *http.Cookie
		for _, item := range response.Cookies() {
			if item.Name != "_devicecheck" {
				continue
			}
			if cookie != nil {
				h.errorResponse(c, 502, "upstream_error", "Chat device registration proof is invalid")
				return
			}
			cookie = item
		}
		if cookie == nil {
			h.errorResponse(c, 502, "upstream_error", "Chat device registration returned no proof")
			return
		}
		ttl, proofErr := service.ChatGPTDeviceCookieTTL(cookie, time.Now())
		if proofErr != nil {
			h.errorResponse(c, 502, "upstream_error", "Chat device registration proof is invalid")
			return
		}
		bound, bindErr := cache.ClaimChatGPTUploadOwner(c.Request.Context(), scope.DeviceCookieKey(device, cookie.Value), owner, ttl)
		if bindErr != nil || bound != owner {
			h.errorResponse(c, 503, "integrity_context_unavailable", "Chat device proof ownership is unavailable")
			return
		}
		// Preserve the one original provider cookie header, including expiry.
		for _, raw := range response.Header.Values("Set-Cookie") {
			parsed, parseErr := http.ParseSetCookie(raw)
			if parseErr == nil && parsed.Name == "_devicecheck" {
				c.Writer.Header().Add("Set-Cookie", raw)
			}
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
	_, _ = io.Copy(c.Writer, response.Body)
}
