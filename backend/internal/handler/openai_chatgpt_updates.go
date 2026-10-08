package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) chatGPTDeliveryContext(c *gin.Context) (*service.APIKey, service.ChatGPTTurnCache, service.ChatGPTTurnScope, bool) {
	var scope service.ChatGPTTurnScope
	if h.cfg == nil || !h.cfg.Gateway.OpenAIChatEnabled || !h.cfg.Gateway.OpenAIChatUpdatesEnabled {
		h.errorResponse(c, http.StatusNotImplemented, "native_chat_updates_unsupported", "Background Chat updates are not enabled")
		return nil, nil, scope, false
	}
	key, ok := middleware.GetAPIKeyFromContext(c)
	subject, subjectOK := middleware.GetAuthSubjectFromContext(c)
	if !ok || !subjectOK || key.Group == nil || key.Group.Platform != service.PlatformOpenAI || key.User == nil || subject.UserID != key.User.ID {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "Native Chat updates require an authenticated OpenAI group")
		return nil, nil, scope, false
	}
	scope = service.ChatGPTTurnScope{UserID: subject.UserID, APIKeyID: key.ID, GroupID: key.Group.ID}
	cache, err := h.gatewayService.ChatGPTTurnCache()
	if err != nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "accounting_unavailable", "Native Chat update ownership is unavailable")
		return nil, nil, scope, false
	}
	return key, cache, scope, true
}

// The returned URL contains no provider credentials. The local proxy replaces
// authentication again on the WebSocket handshake. Bootstrap selects no account.
func (h *OpenAIGatewayHandler) ChatGPTUpdatesBootstrap(c *gin.Context) {
	if _, _, _, ok := h.chatGPTDeliveryContext(c); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"websocket_url": "wss://chatgpt.com/backend-api/saiai/chat-updates"})
}

func safeChatGPTConversationID(id string) bool {
	if id == "" || len(id) > 512 {
		return false
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Single-conversation reads are allowed only for the same user/Key/group and
// original OAuth owner. They never select a fallback account or generate a turn.
func (h *OpenAIGatewayHandler) ChatGPTConversationRead(c *gin.Context) {
	key, cache, scope, ok := h.chatGPTDeliveryContext(c)
	if !ok {
		return
	}
	id := c.Param("conversation_id")
	if !safeChatGPTConversationID(id) {
		h.errorResponse(c, 400, "invalid_request_error", "Invalid Chat conversation identity")
		return
	}
	turn, err := cache.GetChatGPTResume(c.Request.Context(), scope.ResumeKey(id))
	if err != nil {
		h.errorResponse(c, 503, "accounting_unavailable", "Chat conversation ownership is unavailable")
		return
	}
	if turn == nil {
		h.errorResponse(c, 404, "conversation_context_unavailable", "Chat conversation ownership is unavailable")
		return
	}
	account, err := h.gatewayService.GetChatGPTBoundAccount(c.Request.Context(), key.GroupID, turn.AccountID)
	if err != nil {
		h.errorResponse(c, 503, "service_unavailable", "Chat conversation account is unavailable")
		return
	}
	setOpsSelectedAccount(c, account.ID, service.PlatformOpenAI)
	response, err := h.gatewayService.ForwardChatGPTControl(c.Request.Context(), c, account, c.Request.URL.RequestURI())
	if err != nil {
		h.errorResponse(c, 502, "upstream_error", "Chat conversation read failed")
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		if shouldCopyChatGPTResponseHeader(name) {
			for _, value := range values {
				c.Writer.Header().Add(name, value)
			}
		}
	}
	c.Status(response.StatusCode)
	var captured bytes.Buffer
	reader := io.LimitReader(response.Body, service.MaxChatGPTConversationSnapshotBytes+1)
	_, readErr := io.Copy(io.MultiWriter(c.Writer, &captured), reader)
	if readErr == nil && captured.Len() > service.MaxChatGPTConversationSnapshotBytes {
		_, _ = io.Copy(c.Writer, response.Body)
		return
	}
	if readErr == nil && response.StatusCode >= 200 && response.StatusCode < 300 && response.Header.Get("Content-Encoding") == "" {
		h.observeChatGPTSnapshot(c.Request.Context(), c, key, cache, scope, account, turn, id, captured.Bytes())
	}
}

func (h *OpenAIGatewayHandler) observeChatGPTSnapshot(ctx context.Context, c *gin.Context, key *service.APIKey, cache service.ChatGPTTurnCache, scope service.ChatGPTTurnScope, account *service.Account, turn *service.ChatGPTTurnSnapshot, id string, raw []byte) {
	if turn.Completed {
		return
	}
	summary, images, completed, err := service.InspectChatGPTConversationSnapshot(raw, id, turn.UserMessageHash)
	if err != nil || !completed {
		logger.L().Debug("openai.chatgpt_async_snapshot_unverified", zap.Int64("api_key_id", key.ID), zap.Int64("account_id", account.ID), zap.Bool("observer_error", err != nil), zap.Int("snapshot_bytes", len(raw)))
		return
	}
	turnKey := scope.TurnKey(turn.Identity)
	turn, err = service.MergeChatGPTDeliveryImages(ctx, cache, scope, turn, images)
	if err == nil {
		turn, err = cache.SetChatGPTTurnAsyncStatus(ctx, turnKey, summary.AsyncStatus)
	}
	if err == nil {
		subscription, _ := middleware.GetSubscriptionFromContext(c)
		err = h.settleChatGPTTurn(ctx, cache, scope, turn, &service.OpenAIChatGPTTurnUsageInput{
			ObservedModel: summary.ObservedModel, APIKey: key, User: key.User, Account: account, Subscription: subscription,
			InboundEndpoint: "/chatgpt/backend-api/f/conversation", UpstreamEndpoint: "/backend-api/f/conversation",
			UserAgent: c.GetHeader("User-Agent"), IPAddress: ip.GetClientIP(c),
		})
	}
	if err != nil && !errors.Is(err, service.ErrChatGPTTurnPending) {
		logger.L().Error("openai.chatgpt_async_settlement_failed", zap.Int64("api_key_id", key.ID), zap.Int64("account_id", account.ID))
	}
}

func (h *OpenAIGatewayHandler) chatGPTScopedAssetDownload(c *gin.Context) {
	key, cache, scope, ok := h.chatGPTDeliveryContext(c)
	if !ok {
		return
	}
	id := c.Param("file_id")
	if id == "" {
		id = c.Query("id")
	}
	var turn *service.ChatGPTTurnSnapshot
	for _, digest := range service.ChatGPTAssetLookupHashes(id) {
		var err error
		turn, err = cache.GetChatGPTResume(c.Request.Context(), scope.AssetKey(digest))
		if err != nil {
			h.errorResponse(c, 503, "accounting_unavailable", "Chat asset ownership is unavailable")
			return
		}
		if turn != nil {
			break
		}
	}
	if turn == nil {
		h.errorResponse(c, 404, "asset_context_unavailable", "Chat asset ownership is unavailable")
		return
	}
	account, err := h.gatewayService.GetChatGPTBoundAccount(c.Request.Context(), key.GroupID, turn.AccountID)
	if err != nil {
		h.errorResponse(c, 503, "service_unavailable", "Chat asset account is unavailable")
		return
	}
	setOpsSelectedAccount(c, account.ID, service.PlatformOpenAI)
	response, err := h.gatewayService.ForwardChatGPTControl(c.Request.Context(), c, account, c.Request.URL.RequestURI())
	if err != nil {
		h.errorResponse(c, 502, "upstream_error", "Chat asset download failed")
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		if shouldCopyChatGPTResponseHeader(name) {
			for _, value := range values {
				c.Writer.Header().Add(name, value)
			}
		}
	}
	c.Status(response.StatusCode)
	_, _ = io.Copy(c.Writer, response.Body)
}

type chatGPTSubscription struct {
	Offset   string
	Revision uint64
}
type chatGPTSubscriptions struct {
	sync.RWMutex
	Topics   map[string]chatGPTSubscription
	Revision uint64
	Presence string
}

func (s *chatGPTSubscriptions) contains(topic string) bool {
	s.RLock()
	defer s.RUnlock()
	_, ok := s.Topics[topic]
	return ok
}

type chatGPTProviderFrame struct {
	AccountID int64
	Raw       []byte
	Failed    bool
}

func (h *OpenAIGatewayHandler) reserveChatGPTUpdates(scope service.ChatGPTTurnScope) (func(), bool) {
	h.chatGPTUpdatesMu.Lock()
	defer h.chatGPTUpdatesMu.Unlock()
	if h.chatGPTUpdatesConnections == nil {
		h.chatGPTUpdatesConnections = make(map[string]int)
	}
	id := scope.UpdatesKey()
	if h.chatGPTUpdatesConnections[id] >= 2 || h.chatGPTUpdatesActive >= 64 {
		return nil, false
	}
	h.chatGPTUpdatesConnections[id]++
	h.chatGPTUpdatesActive++
	return func() {
		h.chatGPTUpdatesMu.Lock()
		defer h.chatGPTUpdatesMu.Unlock()
		h.chatGPTUpdatesConnections[id]--
		h.chatGPTUpdatesActive--
		if h.chatGPTUpdatesConnections[id] == 0 {
			delete(h.chatGPTUpdatesConnections, id)
		}
	}, true
}

func (h *OpenAIGatewayHandler) ChatGPTUpdatesWebSocket(c *gin.Context) {
	key, cache, scope, ok := h.chatGPTDeliveryContext(c)
	if !ok {
		return
	}
	release, ok := h.reserveChatGPTUpdates(scope)
	if !ok {
		h.errorResponse(c, 429, "concurrency_limit", "Too many Chat update connections")
		return
	}
	defer release()
	conn, err := coderws.Accept(c.Writer, c.Request, &coderws.AcceptOptions{InsecureSkipVerify: true, CompressionMode: coderws.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 * 1024)
	ctx, cancel := context.WithTimeout(c.Request.Context(), service.ChatGPTTurnContextTTL)
	defer cancel()
	subscriptions := &chatGPTSubscriptions{Topics: make(map[string]chatGPTSubscription), Presence: "foreground"}
	output := make(chan []byte, 8)
	frames := make(chan chatGPTProviderFrame)
	failed := make(chan struct{}, 2)
	go func() {
		defer func() {
			select {
			case failed <- struct{}{}:
			default:
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case raw := <-output:
				writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(writeCtx, coderws.MessageText, raw)
				stop()
				if err != nil {
					return
				}
			}
		}
	}()
	go func() {
		defer func() {
			select {
			case failed <- struct{}{}:
			default:
			}
		}()
		h.readChatGPTCommands(ctx, conn, cache, scope, subscriptions, output)
	}()
	workers := make(map[int64]context.CancelFunc)
	snapshotAttempts := make(map[string]int)
	defer func() {
		for _, stop := range workers {
			stop()
		}
	}()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-failed:
			return
		case <-ticker.C:
			accounts, err := cache.ListChatGPTUpdateAccounts(ctx, scope.UpdatesKey())
			if err != nil {
				return
			}
			present := make(map[int64]bool)
			for _, id := range accounts {
				present[id] = true
				if workers[id] != nil {
					continue
				}
				workerCtx, stop := context.WithCancel(ctx)
				workers[id] = stop
				copy := chatGPTControlContext(c)
				go h.runChatGPTUpdatesAccount(workerCtx, copy, key, cache, scope, id, subscriptions, frames)
			}
			for id, stop := range workers {
				if !present[id] {
					stop()
					delete(workers, id)
				}
			}
		case frame := <-frames:
			if frame.Failed {
				_ = conn.Close(coderws.StatusInternalError, "Chat update provider connection failed")
				return
			}
			updates, err := service.FilterChatGPTUpdates(ctx, cache, scope, frame.AccountID, frame.Raw, subscriptions.contains)
			if err != nil {
				return
			}
			for _, update := range updates {
				wire, _ := json.Marshal([]json.RawMessage{update.Raw})
				select {
				case output <- wire:
				case <-ctx.Done():
					return
				}
				if update.CompletionHint && safeChatGPTConversationID(update.ConversationID) && snapshotAttempts[update.ConversationID] < 3 {
					if len(snapshotAttempts) >= 4096 {
						return
					}
					snapshotAttempts[update.ConversationID]++
					h.readChatGPTCompletedSnapshot(ctx, c, key, cache, scope, frame.AccountID, update.ConversationID)
				}
			}
		}
	}
}

func chatGPTControlContext(c *gin.Context) *gin.Context {
	copy := c.Copy()
	copy.Request = c.Request.Clone(c.Request.Context())
	for name := range copy.Request.Header {
		if strings.HasPrefix(strings.ToLower(name), "sec-websocket-") {
			copy.Request.Header.Del(name)
		}
	}
	copy.Request.Header.Del("Upgrade")
	copy.Request.Header.Del("Connection")
	copy.Request.Header.Set("Accept-Encoding", "identity")
	return copy
}

// Completion hints trigger one read, never generation or model retries. The
// snapshot still has to prove inactivity, branch ownership and final success.
func (h *OpenAIGatewayHandler) readChatGPTCompletedSnapshot(ctx context.Context, c *gin.Context, key *service.APIKey, cache service.ChatGPTTurnCache, scope service.ChatGPTTurnScope, accountID int64, id string) {
	turn, err := cache.GetChatGPTResume(ctx, scope.ResumeKey(id))
	if err != nil || turn == nil || turn.AccountID != accountID || turn.Completed {
		return
	}
	account, err := h.gatewayService.GetChatGPTBoundAccount(ctx, key.GroupID, accountID)
	if err != nil {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	copy := chatGPTControlContext(c)
	response, err := h.gatewayService.ForwardChatGPTDeliveryControl(readCtx, copy, account, "/chatgpt/backend-api/conversation/"+url.PathEscape(id))
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, service.MaxChatGPTConversationSnapshotBytes+1))
	if err == nil {
		h.observeChatGPTSnapshot(readCtx, copy, key, cache, scope, account, turn, id, raw)
	}
}

func (h *OpenAIGatewayHandler) readChatGPTCommands(ctx context.Context, conn *coderws.Conn, cache service.ChatGPTTurnCache, scope service.ChatGPTTurnScope, subscriptions *chatGPTSubscriptions, output chan<- []byte) {
	for {
		kind, raw, err := conn.Read(ctx)
		if err != nil || kind != coderws.MessageText {
			return
		}
		var commands []struct {
			ID      int64 `json:"id"`
			Command struct {
				Type     string `json:"type"`
				Topic    string `json:"topic_id"`
				Offset   string `json:"offset"`
				State    string `json:"state"`
				Presence struct {
					State string `json:"state"`
				} `json:"presence"`
			} `json:"command"`
		}
		if json.Unmarshal(raw, &commands) != nil || len(commands) > 32 {
			return
		}
		for _, command := range commands {
			if command.ID < 1 {
				return
			}
			name, topic := command.Command.Type, command.Command.Topic
			if name == "subscribe" || name == "unsubscribe" {
				if topic != "conversations" {
					if !service.ValidChatGPTUpdateTopic(topic) {
						return
					}
					owner, err := cache.GetChatGPTResume(ctx, scope.TopicKey(topic))
					if err != nil || owner == nil {
						return
					}
				}
			} else if name != "connect" && name != "presence" {
				return
			}
			subscriptions.Lock()
			subscriptions.Revision++
			switch name {
			case "subscribe":
				if len(subscriptions.Topics) >= 32 || len(command.Command.Offset) > 512 {
					subscriptions.Unlock()
					return
				}
				subscriptions.Topics[topic] = chatGPTSubscription{command.Command.Offset, subscriptions.Revision}
			case "unsubscribe":
				delete(subscriptions.Topics, topic)
			case "connect", "presence":
				state := command.Command.State
				if name == "connect" {
					state = command.Command.Presence.State
				}
				if state == "foreground" || state == "background" {
					subscriptions.Presence = state
				}
			}
			subscriptions.Unlock()
			reply := map[string]any{"type": name}
			if name == "subscribe" {
				reply["topic_id"], reply["recovered"] = topic, false
			}
			wire, _ := json.Marshal([]any{map[string]any{"id": command.ID, "reply": reply}})
			select {
			case output <- wire:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (h *OpenAIGatewayHandler) runChatGPTUpdatesAccount(ctx context.Context, c *gin.Context, key *service.APIKey, cache service.ChatGPTTurnCache, scope service.ChatGPTTurnScope, accountID int64, subscriptions *chatGPTSubscriptions, frames chan<- chatGPTProviderFrame) {
	defer func() {
		if ctx.Err() == nil {
			select {
			case frames <- chatGPTProviderFrame{AccountID: accountID, Failed: true}:
			case <-ctx.Done():
			}
		}
	}()
	account, err := h.gatewayService.GetChatGPTBoundAccount(ctx, key.GroupID, accountID)
	if err != nil {
		return
	}
	connectCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	conn, err := h.gatewayService.OpenChatGPTUpdates(connectCtx, c, account)
	stop()
	if err != nil {
		return
	}
	defer conn.CloseNow()
	logger.L().Info("openai.chatgpt_updates_provider_connected", zap.Int64("api_key_id", key.ID), zap.Int64("account_id", accountID))
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		h.writeChatGPTSubscriptions(workerCtx, conn, key, cache, scope, accountID, subscriptions)
		cancel()
	}()
	defer func() { cancel(); <-writeDone }()
	for {
		kind, raw, err := conn.Read(workerCtx)
		if err != nil || kind != coderws.MessageText {
			return
		}
		select {
		case frames <- chatGPTProviderFrame{AccountID: accountID, Raw: raw}:
		case <-workerCtx.Done():
			return
		}
	}
}

func (h *OpenAIGatewayHandler) writeChatGPTSubscriptions(ctx context.Context, conn *coderws.Conn, key *service.APIKey, cache service.ChatGPTTurnCache, scope service.ChatGPTTurnScope, accountID int64, subscriptions *chatGPTSubscriptions) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	sent := make(map[string]uint64)
	requestID := int64(1)
	connected := false
	lastPresence := ""
	lastCheck := time.Now()
	for {
		subscriptions.RLock()
		presence := subscriptions.Presence
		topics := make(map[string]chatGPTSubscription, len(subscriptions.Topics))
		for topic, subscription := range subscriptions.Topics {
			topics[topic] = subscription
		}
		subscriptions.RUnlock()
		commands := make([]any, 0, len(topics)+1)
		if !connected {
			commands = append(commands, map[string]any{"id": requestID, "command": map[string]any{"type": "connect", "presence": map[string]any{"type": "presence", "state": presence}}})
			requestID++
			connected = true
			lastPresence = presence
		} else if presence != lastPresence {
			commands = append(commands, map[string]any{"id": requestID, "command": map[string]any{"type": "presence", "state": presence}})
			requestID++
			lastPresence = presence
		}
		for topic, subscription := range topics {
			if sent[topic] == subscription.Revision {
				continue
			}
			if topic != "conversations" {
				owner, err := cache.GetChatGPTResume(ctx, scope.TopicKey(topic))
				if err != nil {
					return
				}
				if owner == nil || owner.AccountID != accountID {
					continue
				}
			}
			offset := subscription.Offset
			if topic == "conversations" {
				offset = "0"
			}
			command := map[string]any{"type": "subscribe", "topic_id": topic}
			if offset != "" {
				command["offset"] = offset
			}
			commands = append(commands, map[string]any{"id": requestID, "command": command})
			requestID++
			sent[topic] = subscription.Revision
		}
		for topic := range sent {
			if _, exists := topics[topic]; !exists {
				commands = append(commands, map[string]any{"id": requestID, "command": map[string]any{"type": "unsubscribe", "topic_id": topic}})
				requestID++
				delete(sent, topic)
			}
		}
		if len(commands) > 0 {
			raw, _ := json.Marshal(commands)
			writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(writeCtx, coderws.MessageText, raw)
			stop()
			if err != nil {
				return
			}
		}
		if time.Since(lastCheck) >= 5*time.Second {
			if _, err := h.gatewayService.GetChatGPTBoundAccount(ctx, key.GroupID, accountID); err != nil {
				return
			}
			pingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			stop()
			if err != nil {
				return
			}
			lastCheck = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
