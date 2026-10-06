package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Connection may nominate additional hop-by-hop headers. Apply this at each
// boundary, rather than treating unknown application headers as disposable.
func openAIConnectionHeaders(headers http.Header) map[string]bool {
	result := make(map[string]bool)
	for _, value := range headers.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			result[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	return result
}

func (s *OpenAIGatewayService) writeOpenAIResponseHeaders(dst, src http.Header, account *Account) {
	if account != nil && (account.IsOpenAIOAuth() || account.IsOpenAICodexNativeRelay()) {
		s.writeOpenAINativeResponseHeaders(dst, src)
		return
	}
	responseheaders.WriteFilteredHeaders(dst, src, s.responseHeaderFilter)
}

func copyOpenAINativeRequestHeaders(dst, src http.Header, websocket bool) {
	hop := openAIConnectionHeaders(src)
	for name, values := range src {
		allowed := shouldCopyOpenAIRequestHeader(name)
		if websocket {
			allowed = shouldCopyOpenAIWSRequestHeader(name)
		}
		if !allowed || hop[strings.ToLower(name)] {
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}

// Native Codex controls must survive a response so the next client request
// retains its provider-issued state. Credentials and transport-owned fields
// remain private to their own leg. API-key compatibility keeps its old filter.
func (s *OpenAIGatewayService) writeOpenAINativeResponseHeaders(dst, src http.Header) {
	hop := openAIConnectionHeaders(src)
	if s != nil && s.cfg != nil && s.cfg.Security.ResponseHeaders.Enabled {
		for _, name := range s.cfg.Security.ResponseHeaders.ForceRemove {
			hop[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range src {
		lower := strings.ToLower(name)
		switch lower {
		case "authorization", "cookie", "set-cookie", "chatgpt-account-id", "x-api-key", "x-goog-api-key",
			"host", "content-length", "connection", "keep-alive", "proxy-authenticate",
			"proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
			continue
		}
		if hop[lower] || strings.HasPrefix(lower, "cf-") || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-envoy-") ||
			lower == "x-real-ip" || lower == "forwarded" || lower == "via" || lower == strings.ToLower(openAINativeRelayChainHeader) {
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}

// WriteNativeCodexResponseHeaders applies the same boundary policy to model
// discovery as to native Responses traffic.
func (s *OpenAIGatewayService) WriteNativeCodexResponseHeaders(dst, src http.Header) {
	s.writeOpenAINativeResponseHeaders(dst, src)
}

var errOpenAITurnStateAccountMismatch = errors.New("Codex turn state cannot be verified for the selected account; start a fresh conversation")

const openAINativeTurnStateSessionHashKey = "openai_native_turn_state_session_hash"

func (s *OpenAIGatewayService) openAINativeResponseSessionHash(c *gin.Context) string {
	if c != nil {
		if hash := c.GetString(openAINativeTurnStateSessionHashKey); hash != "" {
			return hash
		}
	}
	return s.openAISessionHashForTurnState(c, "")
}

// An opaque token must not be guessed, replaced with a cached token, or sent
// to another pooled account. Preserve a verified supplied token byte-for-byte;
// fail before provider I/O when ownership cannot be established. Absence stays
// absent. This intentionally fails closed after process-local state expires.
func (s *OpenAIGatewayService) validateOpenAINativeTurnState(account *Account, userID int64, sessionHash string, incoming http.Header) error {
	values := incoming.Values(openAIWSTurnStateHeader)
	if len(values) == 0 {
		return nil
	}
	if len(values) != 1 || values[0] == "" || sessionHash == "" || s == nil {
		return errOpenAITurnStateAccountMismatch
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return errOpenAITurnStateAccountMismatch
	}
	stored, ok := store.GetSessionTurnState(0, openAIWSUserTurnStateSessionHash(userID, account.ID, sessionHash))
	if ok && subtle.ConstantTimeCompare([]byte(stored), []byte(values[0])) == 1 {
		return nil
	}
	stored, ok = store.GetSessionTurnState(0, openAINativeTurnStateKey(userID, account.ID, sessionHash, values[0]))
	if ok && subtle.ConstantTimeCompare([]byte(stored), []byte(values[0])) == 1 {
		return nil
	}
	return errOpenAITurnStateAccountMismatch
}

func openAINativeTurnStateKey(userID, accountID int64, sessionHash, state string) string {
	return fmt.Sprintf("%s:observed-state:%x", openAIWSUserTurnStateSessionHash(userID, accountID, sessionHash), sha256.Sum256([]byte(state)))
}

func (s *OpenAIGatewayService) bindOpenAINativeTurnState(account *Account, userID int64, sessionHash, state string) {
	if state == "" || sessionHash == "" {
		return
	}
	store := s.getOpenAIWSStateStore()
	store.BindSessionTurnState(0, openAIWSUserTurnStateSessionHash(userID, account.ID, sessionHash), state, s.openAIWSSessionStickyTTL())
	// Concurrent turns can legitimately have different tokens. Ownership is a
	// set of observed tokens, rather than only the last value for the session.
	store.BindSessionTurnState(0, openAINativeTurnStateKey(userID, account.ID, sessionHash, state), state, s.openAIWSSessionStickyTTL())
}

func (s *OpenAIGatewayService) validateOpenAINativeFrameTurnState(c *gin.Context, account *Account, payload []byte) error {
	state := gjson.GetBytes(payload, "client_metadata.x-codex-turn-state")
	if !state.Exists() {
		return nil
	}
	if state.Type != gjson.String {
		return errOpenAITurnStateAccountMismatch
	}
	headers := make(http.Header)
	headers.Set(openAIWSTurnStateHeader, state.String())
	sessionHash := s.openAISessionHashForTurnState(c, gjson.GetBytes(payload, "prompt_cache_key").String())
	return s.validateOpenAINativeTurnState(account, getOpenAIUserIDFromContext(c), sessionHash, headers)
}

// Codex consumes the same opaque token from response.metadata event headers.
// Observe it without rewriting the event, so a later HTTP/WS request can be
// verified even when the token was not in the initial handshake.
func (s *OpenAIGatewayService) observeOpenAINativeMetadataTurnState(c *gin.Context, account *Account, sessionHash string, payload []byte) {
	if account == nil || !account.IsOpenAIOAuth() || gjson.GetBytes(payload, "type").String() != "response.metadata" {
		return
	}
	gjson.GetBytes(payload, "headers").ForEach(func(name, value gjson.Result) bool {
		if !strings.EqualFold(name.String(), openAIWSTurnStateHeader) {
			return true
		}
		if value.IsArray() && len(value.Array()) > 0 {
			value = value.Array()[0]
		}
		if value.Type == gjson.String {
			s.bindOpenAINativeTurnState(account, getOpenAIUserIDFromContext(c), sessionHash, value.String())
		}
		return false
	})
}
