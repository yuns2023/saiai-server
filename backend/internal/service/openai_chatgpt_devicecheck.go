package service

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const chatGPTDeviceCookieContextKey = "native_chat_verified_device_cookie"

var ErrChatGPTDeviceCookie = errors.New("native Chat device registration is unavailable")
var ErrChatGPTDeviceCookieStore = errors.New("native Chat device ownership is unavailable")

// Keep attachment/upload claims bounded to one hour. Only the exact scoped
// device-cookie digest shape can carry the provider proof's longer lifetime.
func ValidChatGPTUploadClaimTTL(key string, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	if ttl <= ChatGPTUploadTTL {
		return true
	}
	if ttl > 30*24*time.Hour {
		return false
	}
	prefix, digest, found := strings.Cut(key, "}:device-cookie:")
	if !found || !strings.HasPrefix(prefix, "chatgpt:upload:{") {
		return false
	}
	namespace := strings.TrimPrefix(prefix, "chatgpt:upload:{")
	for _, part := range []string{namespace, digest} {
		if len(part) != 64 {
			return false
		}
		if _, err := hex.DecodeString(part); err != nil {
			return false
		}
	}
	return true
}

type chatGPTVerifiedDeviceCookie struct {
	value string
	owner int64
}

// Native Mac uses oai-did; other official clients use OAI-Device-ID.
// Prefer the native cookie manager's identity without changing either wire
// header. These two headers need not represent the same identity.
func ChatGPTDeviceID(headers http.Header) (string, error) {
	var device string
	for _, name := range []string{"OAI-DID", "OAI-Device-ID"} {
		var identity string
		for _, value := range headers.Values(name) {
			if !ValidChatGPTMetadataValue(value, 512) || (identity != "" && value != identity) {
				return "", ErrChatGPTDeviceCookie
			}
			identity = value
		}
		if device == "" {
			device = identity
		}
	}
	return device, nil
}

func (s ChatGPTTurnScope) DeviceCookieKey(device, value string) string {
	return s.uploadKey("device-cookie", device+"\x00"+value)
}

func chatGPTDeviceCookieValue(request *http.Request) (string, error) {
	value := ""
	for _, cookie := range request.Cookies() {
		if cookie.Name != "_devicecheck" {
			continue
		}
		if value != "" || cookie.Value == "" || len(cookie.Value) > 16*1024 || cookie.Value == "saiai-local-proxy" || cookie.Quoted {
			return "", ErrChatGPTDeviceCookie
		}
		for _, ch := range cookie.Value {
			if ch < 0x21 || ch > 0x7e || strings.ContainsRune("\";,\\", ch) {
				return "", ErrChatGPTDeviceCookie
			}
		}
		value = cookie.Value
	}
	return value, nil
}

// Only a proof issued by the provider to this user/Key/group/device may return
// to its original OAuth owner. Redis contains the scoped digest, never a Cookie.
func (s *OpenAIGatewayService) ValidateChatGPTDeviceCookie(c *gin.Context, scope ChatGPTTurnScope) (int64, error) {
	c.Set(chatGPTDeviceCookieContextKey, chatGPTVerifiedDeviceCookie{})
	value, err := chatGPTDeviceCookieValue(c.Request)
	if err != nil || value == "" {
		return 0, err
	}
	device, err := ChatGPTDeviceID(c.Request.Header)
	if err != nil || device == "" {
		return 0, ErrChatGPTDeviceCookie
	}
	cache, err := s.ChatGPTUploadCache()
	if err != nil {
		return 0, ErrChatGPTDeviceCookieStore
	}
	owner, err := cache.GetChatGPTUploadOwner(c.Request.Context(), scope.DeviceCookieKey(device, value))
	if err != nil {
		return 0, ErrChatGPTDeviceCookieStore
	}
	if owner <= 0 {
		return 0, ErrChatGPTDeviceCookie
	}
	c.Set(chatGPTDeviceCookieContextKey, chatGPTVerifiedDeviceCookie{value: value, owner: owner})
	return owner, nil
}

// This is a narrow selected-account-owned proof relay, not a general Cookie
// jar. Auth/session/browser cookies remain excluded on every native path.
func copyChatGPTDeviceCookie(c *gin.Context, account *Account, headers http.Header) error {
	value, err := chatGPTDeviceCookieValue(c.Request)
	if err != nil || value == "" {
		return err
	}
	stored, _ := c.Get(chatGPTDeviceCookieContextKey)
	proof, ok := stored.(chatGPTVerifiedDeviceCookie)
	if !ok || proof.value != value || proof.owner != account.ID {
		return ErrChatGPTDeviceCookie
	}
	headers.Set("Cookie", "_devicecheck="+value)
	return nil
}

// Honor the real proof's lifetime, bounded to 30 days. Session cookies have a
// 24-hour ownership lease. An expired lease requires a real registration again.
func ChatGPTDeviceCookieTTL(cookie *http.Cookie, now time.Time) (time.Duration, error) {
	// The native cookie manager consumes the real proof and its expiry. Keep
	// the provider's Path/Secure/HttpOnly attributes unchanged, rather than
	// requiring a guessed response shape. Its issuer and scoped owner provide
	// the credential boundary; this does not widen which cookies are relayed.
	if cookie.Name != "_devicecheck" ||
		(cookie.Domain != "" && strings.TrimPrefix(strings.ToLower(cookie.Domain), ".") != "chatgpt.com") ||
		cookie.MaxAge < 0 {
		return 0, ErrChatGPTDeviceCookie
	}
	request := &http.Request{Header: http.Header{"Cookie": []string{"_devicecheck=" + cookie.Value}}}
	value, err := chatGPTDeviceCookieValue(request)
	if err != nil || value != cookie.Value {
		return 0, ErrChatGPTDeviceCookie
	}
	ttl := 24 * time.Hour
	if cookie.MaxAge > 0 {
		ttl = time.Duration(min(cookie.MaxAge, 30*24*60*60)) * time.Second
	} else if !cookie.Expires.IsZero() {
		ttl = min(cookie.Expires.Sub(now), 30*24*time.Hour)
	}
	if ttl <= 0 {
		return 0, ErrChatGPTDeviceCookie
	}
	return ttl, nil
}
