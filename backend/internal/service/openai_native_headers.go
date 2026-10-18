package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
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
		if isOpenAICodexQuotaHeader(lower) {
			continue
		}
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

var errOpenAITurnStateAccountMismatch = errors.New("codex turn state cannot be verified for the selected account; start a fresh conversation")

func IsOpenAITurnStateAccountMismatch(err error) bool {
	return errors.Is(err, errOpenAITurnStateAccountMismatch)
}

const openAINativeTurnStateSessionHashKey = "openai_native_turn_state_session_hash"

func (s *OpenAIGatewayService) openAINativeResponseSessionHash(c *gin.Context) string {
	if c != nil {
		if hash := c.GetString(openAINativeTurnStateSessionHashKey); hash != "" {
			return hash
		}
	}
	return s.openAISessionHashForTurnState(c, "")
}

// Rejections log only bounded classifications, internal IDs and a user-scoped
// digest. Never include the supplied state, session, body or cache error text.
func rejectOpenAINativeTurnState(ctx context.Context, userID, accountID, ownerID int64, state, reason, source string) error {
	logger.FromContext(ctx).Warn("openai.turn_state_rejected",
		zap.Int64("user_id", userID), zap.Int64("account_id", accountID),
		zap.Int64("turn_state_owner_account_id", ownerID),
		zap.String("turn_state_digest", openAITurnStateAccountKey(userID, state)),
		zap.String("turn_state_reason", reason), zap.String("turn_state_cache_source", source))
	return errOpenAITurnStateAccountMismatch
}

func lookupOpenAINativeTurnState(ctx context.Context, store OpenAIWSStateStore, userID int64, state string) (int64, string, error) {
	if concrete, ok := store.(*defaultOpenAIWSStateStore); ok {
		owner, source := concrete.lookupTurnStateAccount(ctx, userID, state)
		return owner, source, nil
	}
	owner, err := store.GetTurnStateAccountForUser(ctx, userID, state)
	return owner, "ownership_store", err
}

// An opaque token must not be guessed, replaced with a cached token, or sent
// to another pooled account. Preserve a verified supplied token byte-for-byte;
// fail before provider I/O when ownership cannot be established. Absence stays
// absent. Ownership is shared through Redis; a local-only token cache is a
// compatibility fallback for already observed in-flight state.
func (s *OpenAIGatewayService) validateOpenAINativeTurnState(ctx context.Context, account *Account, userID int64, sessionHash string, incoming http.Header) error {
	values := incoming.Values(openAIWSTurnStateHeader)
	if len(values) == 0 {
		return nil
	}
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}
	state := ""
	if len(values) == 1 {
		state = values[0]
	}
	reject := func(reason, source string, owner int64) error {
		return rejectOpenAINativeTurnState(ctx, userID, accountID, owner, state, reason, source)
	}
	if len(values) != 1 {
		return reject("multiple_values", "not_checked", 0)
	}
	if state == "" {
		return reject("empty_value", "not_checked", 0)
	}
	if account == nil || s == nil {
		return reject("missing_validation_context", "not_checked", 0)
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return reject("missing_store", "not_checked", 0)
	}
	owner, source, err := lookupOpenAINativeTurnState(ctx, store, userID, state)
	if err != nil {
		_ = reject("ownership_lookup_error", source, 0)
		return err
	}
	if owner > 0 {
		if owner == account.ID {
			return nil
		}
		return reject("owner_mismatch", source, owner)
	}
	stored, ok := store.GetSessionTurnState(0, openAIWSUserTurnStateSessionHash(userID, account.ID, sessionHash))
	if ok && subtle.ConstantTimeCompare([]byte(stored), []byte(state)) == 1 {
		return nil
	}
	stored, ok = store.GetSessionTurnState(0, openAINativeTurnStateKey(userID, account.ID, sessionHash, state))
	if ok && subtle.ConstantTimeCompare([]byte(stored), []byte(state)) == 1 {
		return nil
	}
	return reject("ownership_unverified", source+"_local_fallback_miss", 0)
}

func openAINativeTurnStateKey(userID, accountID int64, sessionHash, state string) string {
	return fmt.Sprintf("%s:observed-state:%x", openAIWSUserTurnStateSessionHash(userID, accountID, sessionHash), sha256.Sum256([]byte(state)))
}

func (s *OpenAIGatewayService) bindOpenAINativeTurnState(account *Account, userID int64, sessionHash, state string) {
	if state == "" || account == nil || s == nil {
		return
	}
	store := s.getOpenAIWSStateStore()
	// Persist only a user-scoped digest and owner. Keep the bounded local
	// entries even if Redis is temporarily unavailable.
	if err := store.BindTurnStateAccountForUser(context.Background(), userID, state, account.ID, s.openAIWSSessionStickyTTL()); err != nil {
		logOpenAIWSV2Passthrough("turn_state_owner_bind_failed account_id=%d", account.ID)
	}
	if sessionHash == "" {
		return
	}
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
		var accountID int64
		if account != nil {
			accountID = account.ID
		}
		return rejectOpenAINativeTurnState(c.Request.Context(), getOpenAIUserIDFromContext(c), accountID, 0, "", "non_string_frame_metadata", "not_checked")
	}
	headers := make(http.Header)
	headers.Set(openAIWSTurnStateHeader, state.String())
	sessionHash := s.openAISessionHashForTurnState(c, gjson.GetBytes(payload, "prompt_cache_key").String())
	return s.validateOpenAINativeTurnState(c.Request.Context(), account, getOpenAIUserIDFromContext(c), sessionHash, headers)
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
