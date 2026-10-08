package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/extracerts"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// OpenChatGPTUpdates keeps the short-lived provider URL in memory. Never use
// the Responses trace dialer here: the URL itself may contain a credential.
func (s *OpenAIGatewayService) OpenChatGPTUpdates(ctx context.Context, c *gin.Context, account *Account) (*coderws.Conn, error) {
	response, err := s.ForwardChatGPTDeliveryControl(ctx, c, account, "/chatgpt/backend-api/celsius/ws/user")
	if err != nil {
		return nil, errors.New("native Chat updates bootstrap failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("native Chat updates bootstrap rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, errors.New("native Chat updates bootstrap exceeds limit")
	}
	var bootstrap struct {
		URL string `json:"websocket_url"`
	}
	if json.Unmarshal(raw, &bootstrap) != nil || !s.validChatGPTUpdatesURL(bootstrap.URL) {
		return nil, errors.New("invalid native Chat updates destination")
	}
	transport := &http.Transport{TLSClientConfig: extracerts.TLSClientConfig(), TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second}
	if account.ProxyID != nil && account.Proxy != nil {
		proxy, err := url.Parse(account.Proxy.URL())
		if err != nil {
			return nil, errors.New("invalid native Chat updates proxy")
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	headers := make(http.Header)
	for name, values := range c.Request.Header {
		if shouldCopyOpenAIRequestHeader(name) && !strings.HasPrefix(strings.ToLower(name), "sec-websocket-") {
			for _, value := range values {
				headers.Add(name, value)
			}
		}
	}
	conn, dialResponse, err := coderws.Dial(ctx, bootstrap.URL, &coderws.DialOptions{HTTPClient: client, HTTPHeader: headers, CompressionMode: coderws.CompressionDisabled})
	if err != nil {
		transport.CloseIdleConnections()
		if dialResponse != nil && dialResponse.Body != nil {
			_ = dialResponse.Body.Close()
		}
		// Dial errors can embed the credentialed URL. Return a fixed error.
		return nil, errors.New("native Chat updates transport failed")
	}
	conn.SetReadLimit(maxChatGPTStreamEventBytes)
	return conn, nil
}

func (s *OpenAIGatewayService) validChatGPTUpdatesURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.Fragment != "" || len(value) > 16384 {
		return false
	}
	if s.cfg != nil && s.cfg.Gateway.OpenAIChatUpstreamBaseURL != "" {
		base, err := url.Parse(s.cfg.Gateway.OpenAIChatUpstreamBaseURL)
		if err == nil && u.Host == base.Host && ((base.Scheme == "http" && u.Scheme == "ws") || (base.Scheme == "https" && u.Scheme == "wss")) {
			return true
		}
	}
	host := strings.ToLower(u.Hostname())
	return u.Scheme == "wss" && (u.Port() == "" || u.Port() == "443") &&
		(strings.HasSuffix(host, ".chatgpt.com") || host == "chatgpt.com" || strings.HasSuffix(host, ".webpubsub.azure.com"))
}

type ChatGPTUpdate struct {
	Raw            json.RawMessage
	ConversationID string
	CompletionHint bool
}

// FilterChatGPTUpdates drops unknown account-wide events and command replies.
// Catchups are subjected to the same ownership test as live messages. Raw
// native payloads are preserved; offsets on the multiplexed global topic are
// omitted because different accounts do not share a cursor.
func FilterChatGPTUpdates(ctx context.Context, cache ChatGPTTurnCache, scope ChatGPTTurnScope, accountID int64, raw []byte, subscribed func(string) bool) ([]ChatGPTUpdate, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > maxChatGPTStreamEventBytes || !json.Valid(raw) {
		return nil, errors.New("invalid native Chat update frame")
	}
	var entries []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' {
		if json.Unmarshal(raw, &entries) != nil || len(entries) > 256 {
			return nil, errors.New("native Chat update batch exceeds limit")
		}
	} else {
		entries = []json.RawMessage{raw}
	}
	var result []ChatGPTUpdate
	for _, entry := range entries {
		var envelope struct {
			Type    string          `json:"type"`
			Topic   string          `json:"topic_id"`
			Payload json.RawMessage `json:"payload"`
			Reply   *struct {
				Catchups []json.RawMessage `json:"catchups"`
			} `json:"reply"`
		}
		if json.Unmarshal(entry, &envelope) != nil {
			return nil, errors.New("invalid native Chat update envelope")
		}
		if envelope.Reply != nil {
			if len(envelope.Reply.Catchups) > 256 {
				return nil, errors.New("native Chat catchups exceed limit")
			}
			for _, catchup := range envelope.Reply.Catchups {
				// A catchup must be a message, never another nested reply.
				var kind struct {
					Type  string          `json:"type"`
					Reply json.RawMessage `json:"reply"`
				}
				_ = json.Unmarshal(catchup, &kind)
				if kind.Type != "message" || len(kind.Reply) > 0 {
					continue
				}
				filtered, err := FilterChatGPTUpdates(ctx, cache, scope, accountID, catchup, subscribed)
				if err != nil {
					return nil, err
				}
				result = append(result, filtered...)
			}
			continue
		}
		topic, payload := envelope.Topic, envelope.Payload
		if envelope.Type != "message" {
			topic, payload = "conversations", entry
		}
		if !subscribed(topic) {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Payload struct {
				ConversationID string `json:"conversation_id"`
				Type           string `json:"type"`
				UpdateType     string `json:"update_type"`
				Content        struct {
					Message     json.RawMessage `json:"message"`
					AsyncStatus *int            `json:"conversation_async_status"`
					Options     []struct {
						Type  string `json:"type"`
						Topic string `json:"topic_id"`
					} `json:"options"`
				} `json:"update_content"`
			} `json:"payload"`
		}
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		id := event.Payload.ConversationID
		var owner *ChatGPTTurnSnapshot
		var err error
		if topic == "conversations" {
			if id == "" || len(id) > 512 || (event.Type != "conversation-update" && event.Type != "conversation-history-update") {
				continue
			}
			owner, err = cache.GetChatGPTResume(ctx, scope.ResumeKey(id))
		} else {
			if !ValidChatGPTUpdateTopic(topic) || event.Type != "conversation-turn-stream" {
				continue
			}
			owner, err = cache.GetChatGPTResume(ctx, scope.TopicKey(topic))
			if err == nil && owner != nil && id != "" {
				conversation, readErr := cache.GetChatGPTResume(ctx, scope.ResumeKey(id))
				if readErr != nil {
					return nil, readErr
				}
				if conversation == nil || conversation.Identity != owner.Identity {
					continue
				}
			}
		}
		if err != nil {
			return nil, err
		}
		if owner == nil || owner.AccountID != accountID {
			continue
		}
		if len(event.Payload.Content.Message) > 0 {
			var observer chatGPTImageObserver
			observer.observeMessage(event.Payload.Content.Message)
			if images := observer.evidence(); images.GenerationSeen {
				if err := BindChatGPTDeliveryAssets(ctx, cache, scope, owner, images); err != nil {
					return nil, err
				}
			}
		}
		if event.Payload.UpdateType == "stream-handoff" {
			for _, option := range event.Payload.Content.Options {
				if option.Type == "subscribe_ws_topic" && ValidChatGPTUpdateTopic(option.Topic) {
					if err := cache.BindChatGPTResume(ctx, scope.TopicKey(option.Topic), scope.TurnKey(owner.Identity)); err != nil {
						return nil, err
					}
				}
			}
		}
		wire := entry
		if topic == "conversations" {
			wire, _ = json.Marshal(struct {
				Type    string          `json:"type"`
				Topic   string          `json:"topic_id"`
				Payload json.RawMessage `json:"payload"`
			}{"message", topic, payload})
		}
		hint := event.Payload.UpdateType == "async-task-completed" || event.Payload.Type == "done" ||
			(event.Payload.Content.AsyncStatus != nil && *event.Payload.Content.AsyncStatus == 4)
		result = append(result, ChatGPTUpdate{Raw: wire, ConversationID: id, CompletionHint: hint})
	}
	return result, nil
}
