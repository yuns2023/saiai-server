package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	coderws "github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

const openAIWSMaxAccountFailovers = 3

// OpenAIWSAccountFailoverError is returned only when an upstream account
// reports a quota/rate-limit error before the current turn emitted any client
// frame and the turn can be reconstructed without account-bound state.
type OpenAIWSAccountFailoverError struct {
	accountID   int64
	turn        int
	messageType coderws.MessageType
	payload     []byte
	cause       error
}

func (e *OpenAIWSAccountFailoverError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause == nil {
		return fmt.Sprintf("openai websocket account exhausted: account_id=%d turn=%d", e.accountID, e.turn)
	}
	return fmt.Sprintf("openai websocket account exhausted: account_id=%d turn=%d: %v", e.accountID, e.turn, e.cause)
}

func (e *OpenAIWSAccountFailoverError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *OpenAIWSAccountFailoverError) AccountID() int64 {
	if e == nil {
		return 0
	}
	return e.accountID
}

func (e *OpenAIWSAccountFailoverError) Turn() int {
	if e == nil || e.turn <= 0 {
		return 1
	}
	return e.turn
}

func (e *OpenAIWSAccountFailoverError) MessageType() coderws.MessageType {
	if e == nil || e.messageType != coderws.MessageBinary {
		return coderws.MessageText
	}
	return coderws.MessageBinary
}

func (e *OpenAIWSAccountFailoverError) ReplayPayload() []byte {
	if e == nil {
		return nil
	}
	return cloneOpenAIWSPayloadBytes(e.payload)
}

// OpenAIWSFailoverTarget is supplied by the ingress handler after it excludes
// the exhausted account and acquires a compatible replacement account.
type OpenAIWSFailoverTarget struct {
	Account *Account
	Token   string
}

type openAIWSPassthroughReplayTurn struct {
	turn               int
	messageType        coderws.MessageType
	payload            []byte
	previousResponseID string
	fullInput          []json.RawMessage
	replayReady        bool
	responseID         string
}

// openAIWSPassthroughReplayTracker keeps only the canonical Responses input
// sequence needed to migrate a not-yet-started turn. It never logs content.
type openAIWSPassthroughReplayTracker struct {
	mu         sync.Mutex
	store      OpenAIWSStateStore
	userID     int64
	pending    []*openAIWSPassthroughReplayTurn
	byResponse map[string]*openAIWSPassthroughReplayTurn
}

func newOpenAIWSPassthroughReplayTracker(store OpenAIWSStateStore, userID int64) *openAIWSPassthroughReplayTracker {
	return &openAIWSPassthroughReplayTracker{
		store:      store,
		userID:     userID,
		pending:    make([]*openAIWSPassthroughReplayTurn, 0, 2),
		byResponse: make(map[string]*openAIWSPassthroughReplayTurn, 2),
	}
}

func (t *openAIWSPassthroughReplayTracker) RegisterTurn(turn int, messageType coderws.MessageType, payload []byte) {
	if t == nil || turn <= 0 || strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "response.create" {
		return
	}
	previousResponseID := strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String())
	var previousInput []json.RawMessage
	previousInputExists := false
	replayReady := previousResponseID == ""
	if previousResponseID != "" && t.store != nil {
		previousInput, previousInputExists = t.store.GetResponseReplayForUser(t.userID, previousResponseID)
		replayReady = previousInputExists
	}
	fullInput, fullInputExists, err := buildOpenAIWSReplayInputSequence(
		previousInput,
		previousInputExists,
		payload,
		previousResponseID != "",
	)
	if err != nil {
		replayReady = false
		fullInput = nil
		fullInputExists = false
	}
	if previousResponseID == "" {
		// A first/full create can be retried even when input is intentionally absent.
		replayReady = true
	}
	if previousResponseID != "" && !fullInputExists {
		replayReady = false
	}

	entry := &openAIWSPassthroughReplayTurn{
		turn:               turn,
		messageType:        messageType,
		payload:            cloneOpenAIWSPayloadBytes(payload),
		previousResponseID: previousResponseID,
		fullInput:          cloneOpenAIWSRawMessages(fullInput),
		replayReady:        replayReady,
	}
	t.mu.Lock()
	t.pending = append(t.pending, entry)
	t.mu.Unlock()
}

func (t *openAIWSPassthroughReplayTracker) ObserveUpstreamFrame(payload []byte) {
	if t == nil || len(payload) == 0 {
		return
	}
	eventType, responseID, response := parseOpenAIWSEventEnvelope(payload)
	if eventType == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.bindResponseLocked(responseID)
	if entry == nil || !isOpenAIWSReplaySuccessfulTerminal(eventType, response) {
		return
	}
	output, outputExists, err := openAIWSExtractResponseOutputSequence(payload)
	if err != nil || !outputExists || !entry.replayReady {
		t.removePendingLocked(entry)
		return
	}
	combined := make([]json.RawMessage, 0, len(entry.fullInput)+len(output))
	combined = append(combined, cloneOpenAIWSRawMessages(entry.fullInput)...)
	combined = append(combined, cloneOpenAIWSRawMessages(output)...)
	sanitized, sanitizeErr := sanitizeOpenAIWSReplayInput(combined)
	if sanitizeErr == nil && len(sanitized) > 0 && t.store != nil {
		t.store.BindResponseReplayForUser(t.userID, responseID, sanitized, openaiStickySessionTTL)
	}
	t.removePendingLocked(entry)
}

func (t *openAIWSPassthroughReplayTracker) BuildFailoverError(accountID int64, cause error) (*OpenAIWSAccountFailoverError, bool) {
	if t == nil || accountID <= 0 {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.pending) != 1 {
		return nil, false
	}
	entry := t.pending[0]
	if entry == nil || !entry.replayReady {
		return nil, false
	}
	replayPayload, ok, err := buildOpenAIWSFailoverPayload(entry.payload, entry.fullInput, entry.previousResponseID != "")
	if err != nil || !ok {
		return nil, false
	}
	if cause == nil {
		cause = errors.New("upstream account quota exhausted")
	}
	return &OpenAIWSAccountFailoverError{
		accountID:   accountID,
		turn:        entry.turn,
		messageType: entry.messageType,
		payload:     replayPayload,
		cause:       cause,
	}, true
}

func (t *openAIWSPassthroughReplayTracker) bindResponseLocked(responseID string) *openAIWSPassthroughReplayTurn {
	responseID = strings.TrimSpace(responseID)
	if responseID != "" {
		if entry := t.byResponse[responseID]; entry != nil {
			return entry
		}
	}
	for _, entry := range t.pending {
		if entry == nil || entry.responseID != "" {
			continue
		}
		if responseID != "" {
			entry.responseID = responseID
			t.byResponse[responseID] = entry
		}
		return entry
	}
	return nil
}

func (t *openAIWSPassthroughReplayTracker) removePendingLocked(target *openAIWSPassthroughReplayTurn) {
	if target == nil {
		return
	}
	if target.responseID != "" {
		delete(t.byResponse, target.responseID)
	}
	for i, entry := range t.pending {
		if entry != target {
			continue
		}
		t.pending = append(t.pending[:i], t.pending[i+1:]...)
		return
	}
}

func openAIWSExtractResponseOutputSequence(payload []byte) ([]json.RawMessage, bool, error) {
	output := gjson.GetBytes(payload, "response.output")
	if !output.Exists() {
		return nil, false, nil
	}
	if output.Type != gjson.JSON || !strings.HasPrefix(strings.TrimSpace(output.Raw), "[") {
		return nil, true, errors.New("response.output is not an array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(output.Raw), &items); err != nil {
		return nil, true, err
	}
	return items, true, nil
}

func isOpenAIWSReplaySuccessfulTerminal(eventType string, response gjson.Result) bool {
	eventType = strings.TrimSpace(eventType)
	if eventType != "response.completed" && eventType != "response.done" {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(response.Get("status").String()))
	return status == "" || status == "completed"
}

func buildOpenAIWSFailoverPayload(currentPayload []byte, fullInput []json.RawMessage, requireReplayInput bool) ([]byte, bool, error) {
	if len(currentPayload) == 0 || !gjson.ValidBytes(currentPayload) {
		return nil, false, errors.New("invalid failover payload")
	}
	if requireReplayInput && len(fullInput) == 0 {
		return nil, false, nil
	}
	if openAIWSReplayHasItemReference(fullInput) {
		return nil, false, nil
	}
	updated, _, err := dropPreviousResponseIDFromRawPayload(currentPayload)
	if err != nil {
		return nil, false, err
	}
	if requireReplayInput {
		updated, err = setOpenAIWSPayloadInputSequence(updated, fullInput, true)
		if err != nil {
			return nil, false, err
		}
	}
	var reqBody map[string]any
	if err := json.Unmarshal(updated, &reqBody); err != nil {
		return nil, false, err
	}
	trimOpenAIEncryptedReasoningItems(reqBody)
	updated, err = json.Marshal(reqBody)
	if err != nil {
		return nil, false, err
	}
	return updated, true, nil
}

func sanitizeOpenAIWSReplayInput(input []json.RawMessage) ([]json.RawMessage, error) {
	sanitized := make([]json.RawMessage, 0, len(input))
	for _, raw := range input {
		var item any
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		next, _, keep := sanitizeEncryptedReasoningInputItem(item)
		if !keep {
			continue
		}
		encoded, err := json.Marshal(next)
		if err != nil {
			return nil, err
		}
		sanitized = append(sanitized, encoded)
	}
	return sanitized, nil
}

func openAIWSReplayHasItemReference(input []json.RawMessage) bool {
	for _, raw := range input {
		if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(raw, "type").String()), "item_reference") {
			return true
		}
	}
	return false
}

// PrepareOpenAIWSContinuationFailoverPayload migrates only a continuation
// owned by an account that is currently rate-limited and only when a complete,
// process-local replay input is available for that user/response pair.
func (s *OpenAIGatewayService) PrepareOpenAIWSContinuationFailoverPayload(
	ctx context.Context,
	userID int64,
	ownerAccountID int64,
	previousResponseID string,
	payload []byte,
) ([]byte, bool, error) {
	if s == nil || ownerAccountID <= 0 || strings.TrimSpace(previousResponseID) == "" {
		return nil, false, nil
	}
	owner, err := s.getSchedulableAccount(ctx, ownerAccountID)
	if err != nil {
		return nil, false, err
	}
	if owner == nil || !owner.IsRateLimited() {
		return nil, false, nil
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return nil, false, nil
	}
	replayInput, ok := store.GetResponseReplayForUser(userID, previousResponseID)
	if !ok {
		return nil, false, nil
	}
	fullInput, fullInputExists, buildErr := buildOpenAIWSReplayInputSequence(replayInput, true, payload, true)
	if buildErr != nil {
		return nil, false, buildErr
	}
	if !fullInputExists {
		return nil, false, nil
	}
	return buildOpenAIWSFailoverPayload(payload, fullInput, true)
}
