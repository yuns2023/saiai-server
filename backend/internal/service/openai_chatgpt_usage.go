package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

var (
	// ErrChatGPTThreadUsagePending means the provider did not return a usage
	// snapshot for the requested conversation yet. The Desktop client treats
	// this as an eventually-consistent condition and retries separately.
	ErrChatGPTThreadUsagePending = errors.New("ChatGPT thread usage is not available yet")
)

const maxChatGPTStreamEventBytes = 2 * 1024 * 1024

// ChatGPTTurnBillingIdentity is a content-hiding, retry-stable identity for
// one native Chat user message.
type ChatGPTTurnBillingIdentity struct {
	RequestID   string
	PayloadHash string
}

// ResolveChatGPTTurnBillingIdentity hashes the conversation identity and the
// last message JSON. Desktop retries keep that message stable even when outer
// preparation fields change. A changed message or conversation produces a new
// billing identity, while raw IDs and content never enter the usage log.
func ResolveChatGPTTurnBillingIdentity(body []byte, fallbackRequestID string) ChatGPTTurnBillingIdentity {
	fallback := ChatGPTTurnBillingIdentity{
		RequestID:   strings.TrimSpace(fallbackRequestID),
		PayloadHash: HashUsageRequestPayload(body),
	}
	if len(body) == 0 {
		return fallback
	}
	var envelope struct {
		ConversationID string            `json:"conversation_id"`
		Messages       []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Messages) == 0 {
		return fallback
	}
	message := bytes.TrimSpace(envelope.Messages[len(envelope.Messages)-1])
	var identity struct {
		ID string `json:"id"`
	}
	if len(message) == 0 || json.Unmarshal(message, &identity) != nil || strings.TrimSpace(identity.ID) == "" {
		return fallback
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("chatgpt-native-turn-v1\x00"))
	_, _ = hash.Write([]byte(strings.TrimSpace(envelope.ConversationID)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(message)
	digest := hex.EncodeToString(hash.Sum(nil))
	return ChatGPTTurnBillingIdentity{
		RequestID:   "chatgpt-turn:" + digest,
		PayloadHash: digest,
	}
}

// ChatGPTConversationStreamSummary contains only non-content accounting and
// affinity signals observed in a native ChatGPT SSE response. The inspected
// Desktop terminal schema does not define token usage, so this type must not
// be treated as billable usage evidence.
type ChatGPTConversationStreamSummary struct {
	ConversationID        string
	ObservedModel         string
	CompletionSeen        bool
	DoneSentinelSeen      bool
	ProviderErrorSeen     bool
	EventTypes            []string
	TopLevelFields        []string
	MessageMetadataFields []string
	UsageLikeFieldPaths   []string
}

// ChatGPTConversationStreamObserver incrementally inspects a native ChatGPT
// SSE stream without retaining message content. It recognizes the terminal
// message_stream_complete event used by Desktop 26.901.51231 and keeps enough
// state for conversation affinity and future accounting research.
type ChatGPTConversationStreamObserver struct {
	lineBuffer            []byte
	eventData             []byte
	summary               ChatGPTConversationStreamSummary
	captureShape          bool
	eventTypes            map[string]struct{}
	topLevelFields        map[string]struct{}
	messageMetadataFields map[string]struct{}
	usageLikeFieldPaths   map[string]struct{}
	err                   error
	finished              bool
}

// NewChatGPTConversationStreamObserver creates a bounded, content-discarding
// native ChatGPT stream observer.
func NewChatGPTConversationStreamObserver() *ChatGPTConversationStreamObserver {
	return newChatGPTConversationStreamObserver(false)
}

// NewChatGPTConversationStreamShapeObserver enables schema-only capture for an
// explicitly authorized staging window. It records names, never field values.
func NewChatGPTConversationStreamShapeObserver() *ChatGPTConversationStreamObserver {
	return newChatGPTConversationStreamObserver(true)
}

func newChatGPTConversationStreamObserver(captureShape bool) *ChatGPTConversationStreamObserver {
	return &ChatGPTConversationStreamObserver{
		lineBuffer:            make([]byte, 0, 4*1024),
		eventData:             make([]byte, 0, 4*1024),
		captureShape:          captureShape,
		eventTypes:            make(map[string]struct{}),
		topLevelFields:        make(map[string]struct{}),
		messageMetadataFields: make(map[string]struct{}),
		usageLikeFieldPaths:   make(map[string]struct{}),
	}
}

// Observe consumes an arbitrary response chunk. Message text is discarded as
// soon as the containing SSE event has been inspected.
func (o *ChatGPTConversationStreamObserver) Observe(p []byte) error {
	if o == nil {
		return errors.New("nil ChatGPT stream observer")
	}
	if o.finished {
		return errors.New("ChatGPT stream observer is already finished")
	}
	if o.err != nil {
		return o.err
	}

	for len(p) > 0 {
		newline := bytes.IndexByte(p, '\n')
		if newline < 0 {
			if len(o.lineBuffer)+len(p) > maxChatGPTStreamEventBytes {
				o.err = errors.New("ChatGPT SSE line exceeds observer limit")
				return o.err
			}
			o.lineBuffer = append(o.lineBuffer, p...)
			break
		}

		if len(o.lineBuffer)+newline > maxChatGPTStreamEventBytes {
			o.err = errors.New("ChatGPT SSE line exceeds observer limit")
			return o.err
		}
		o.lineBuffer = append(o.lineBuffer, p[:newline]...)
		if err := o.processLine(o.lineBuffer); err != nil {
			o.err = err
			return err
		}
		clear(o.lineBuffer)
		o.lineBuffer = o.lineBuffer[:0]
		p = p[newline+1:]
	}
	return nil
}

// Finish flushes the final unterminated line/event and returns a copy of the
// observed non-content summary.
func (o *ChatGPTConversationStreamObserver) Finish() (ChatGPTConversationStreamSummary, error) {
	if o == nil {
		return ChatGPTConversationStreamSummary{}, errors.New("nil ChatGPT stream observer")
	}
	if o.err != nil {
		return o.summary, o.err
	}
	if !o.finished {
		if len(o.lineBuffer) > 0 {
			if err := o.processLine(o.lineBuffer); err != nil {
				o.err = err
				return o.summary, err
			}
			clear(o.lineBuffer)
			o.lineBuffer = o.lineBuffer[:0]
		}
		if err := o.dispatchEvent(); err != nil {
			o.err = err
			return o.summary, err
		}
		o.finished = true
	}
	if o.captureShape {
		o.summary.EventTypes = sortedChatGPTSchemaNames(o.eventTypes)
		o.summary.TopLevelFields = sortedChatGPTSchemaNames(o.topLevelFields)
		o.summary.MessageMetadataFields = sortedChatGPTSchemaNames(o.messageMetadataFields)
		o.summary.UsageLikeFieldPaths = sortedChatGPTSchemaNames(o.usageLikeFieldPaths)
	}
	return o.summary, nil
}

func (o *ChatGPTConversationStreamObserver) processLine(line []byte) error {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 {
		return o.dispatchEvent()
	}
	if bytes.HasPrefix(line, []byte("event:")) {
		eventType := strings.TrimSpace(string(line[len("event:"):]))
		if o.captureShape && safeChatGPTSchemaName(eventType) {
			o.eventTypes[eventType] = struct{}{}
		}
		return nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	payload := line[len("data:"):]
	payload = bytes.TrimLeft(payload, " \t")
	additional := len(payload)
	if len(o.eventData) > 0 {
		additional++
	}
	if len(o.eventData)+additional > maxChatGPTStreamEventBytes {
		return errors.New("ChatGPT SSE event exceeds observer limit")
	}
	if len(o.eventData) > 0 {
		o.eventData = append(o.eventData, '\n')
	}
	o.eventData = append(o.eventData, payload...)
	return nil
}

func (o *ChatGPTConversationStreamObserver) dispatchEvent() error {
	if len(o.eventData) == 0 {
		return nil
	}
	eventBytes := o.eventData
	payload := bytes.TrimSpace(eventBytes)
	defer func() {
		clear(eventBytes)
		o.eventData = o.eventData[:0]
	}()
	if bytes.Equal(payload, []byte("[DONE]")) {
		o.summary.DoneSentinelSeen = true
		return nil
	}

	var rawEvent map[string]json.RawMessage
	if err := json.Unmarshal(payload, &rawEvent); err != nil {
		// The native protocol also supports encoded delta events. They are
		// opaque here and are forwarded unchanged; only JSON control events are
		// relevant to the current completion/accounting contract.
		return nil
	}
	if o.captureShape {
		o.captureResponseShape(rawEvent)
	}

	var event struct {
		Type           string          `json:"type"`
		ConversationID string          `json:"conversation_id"`
		Error          json.RawMessage `json:"error"`
		Message        *struct {
			Author *struct {
				Role string `json:"role"`
			} `json:"author"`
			Metadata *struct {
				ModelSlug string `json:"model_slug"`
			} `json:"metadata"`
		} `json:"message"`
	}
	_ = json.Unmarshal(payload, &event)
	if o.captureShape && safeChatGPTSchemaName(event.Type) {
		o.eventTypes[event.Type] = struct{}{}
	}
	if conversationID := strings.TrimSpace(event.ConversationID); conversationID != "" {
		o.summary.ConversationID = conversationID
		if event.Type == "message_stream_complete" {
			o.summary.CompletionSeen = true
		}
	}
	if event.Message != nil && event.Message.Metadata != nil {
		role := ""
		if event.Message.Author != nil {
			role = strings.TrimSpace(event.Message.Author.Role)
		}
		if role == "" || role == "assistant" {
			if model := strings.TrimSpace(event.Message.Metadata.ModelSlug); model != "" {
				o.summary.ObservedModel = model
			}
		}
	}
	if raw := bytes.TrimSpace(event.Error); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		o.summary.ProviderErrorSeen = true
	}
	return nil
}

const (
	maxChatGPTCapturedSchemaNames = 256
	maxChatGPTSchemaDepth         = 8
)

func (o *ChatGPTConversationStreamObserver) captureResponseShape(event map[string]json.RawMessage) {
	if o == nil || !o.captureShape {
		return
	}
	for key, value := range event {
		if !safeChatGPTSchemaComponent(key) {
			continue
		}
		addChatGPTSchemaName(o.topLevelFields, key)
		if key != "message" {
			if chatGPTUsageLikeSchemaName(key) {
				addChatGPTSchemaName(o.usageLikeFieldPaths, key)
				collectChatGPTSchemaPaths(value, key, o.usageLikeFieldPaths, 0)
			} else {
				findChatGPTUsageLikePaths(value, key, o.usageLikeFieldPaths, 0)
			}
		}
	}

	messageRaw, ok := event["message"]
	if !ok {
		return
	}
	var message map[string]json.RawMessage
	if json.Unmarshal(messageRaw, &message) != nil {
		return
	}
	metadataRaw, ok := message["metadata"]
	if !ok {
		return
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(metadataRaw, &metadata) != nil {
		return
	}
	for key, value := range metadata {
		if !safeChatGPTSchemaComponent(key) {
			continue
		}
		addChatGPTSchemaName(o.messageMetadataFields, key)
		findChatGPTUsageLikePaths(value, "message.metadata."+key, o.usageLikeFieldPaths, 0)
		if chatGPTUsageLikeSchemaName(key) {
			addChatGPTSchemaName(o.usageLikeFieldPaths, "message.metadata."+key)
			collectChatGPTSchemaPaths(value, "message.metadata."+key, o.usageLikeFieldPaths, 0)
		}
	}
}

func findChatGPTUsageLikePaths(raw json.RawMessage, prefix string, output map[string]struct{}, depth int) {
	if depth >= maxChatGPTSchemaDepth || len(output) >= maxChatGPTCapturedSchemaNames {
		return
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		for key, child := range object {
			if !safeChatGPTSchemaComponent(key) {
				continue
			}
			path := prefix + "." + key
			if chatGPTUsageLikeSchemaName(key) {
				addChatGPTSchemaName(output, path)
				collectChatGPTSchemaPaths(child, path, output, depth+1)
			} else {
				findChatGPTUsageLikePaths(child, path, output, depth+1)
			}
		}
		return
	}
	var array []json.RawMessage
	if json.Unmarshal(raw, &array) == nil {
		for _, child := range array {
			findChatGPTUsageLikePaths(child, prefix+".[]", output, depth+1)
		}
	}
}

func collectChatGPTSchemaPaths(raw json.RawMessage, prefix string, output map[string]struct{}, depth int) {
	if depth >= maxChatGPTSchemaDepth || len(output) >= maxChatGPTCapturedSchemaNames {
		return
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		for key, child := range object {
			if !safeChatGPTSchemaComponent(key) {
				continue
			}
			path := prefix + "." + key
			addChatGPTSchemaName(output, path)
			collectChatGPTSchemaPaths(child, path, output, depth+1)
		}
		return
	}
	var array []json.RawMessage
	if json.Unmarshal(raw, &array) == nil {
		arrayPath := prefix + ".[]"
		addChatGPTSchemaName(output, arrayPath)
		for _, child := range array {
			collectChatGPTSchemaPaths(child, arrayPath, output, depth+1)
		}
	}
}

func chatGPTUsageLikeSchemaName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(name, "usage") || strings.Contains(name, "token") || strings.Contains(name, "credit")
}

func safeChatGPTSchemaName(name string) bool {
	if len(name) == 0 || len(name) > 512 {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' ||
			char == '[' || char == ']' {
			continue
		}
		return false
	}
	return true
}

func safeChatGPTSchemaComponent(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func addChatGPTSchemaName(output map[string]struct{}, name string) {
	if len(output) >= maxChatGPTCapturedSchemaNames || !safeChatGPTSchemaName(name) {
		return
	}
	output[name] = struct{}{}
}

func sortedChatGPTSchemaNames(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// ChatGPTThreadUsageGroup is one model/dimension bucket from ChatGPT's
// cumulative thread-usage response. It is deliberately not OpenAIUsage: these
// values cover the entire conversation observed by the provider, not one
// Gateway request, and therefore must never be passed directly to RecordUsage.
type ChatGPTThreadUsageGroup struct {
	Model                 string
	ReasoningEffort       string
	Speed                 string
	NetNewInputTokens     int64
	CachedInputTokens     int64
	OutputTokens          int64
	EstimatedCreditMicros *int64
}

// ChatGPTThreadUsageSnapshot is the cumulative provider snapshot for one
// conversation. A future billing implementation must persist and atomically
// settle a monotonic delta between snapshots; billing this object directly
// would charge all previous turns again.
type ChatGPTThreadUsageSnapshot struct {
	ThreadID              string
	EstimatedCreditMicros *int64
	EstimatedUSDMicros    *int64
	Groups                []ChatGPTThreadUsageGroup
}

// ChatGPTThreadUsageDelta is a monotonic difference between two provider
// snapshots of the same conversation. It remains separate from OpenAIUsage so
// callers cannot accidentally skip the required persistence/dedup step.
type ChatGPTThreadUsageDelta struct {
	ThreadID              string
	EstimatedCreditMicros *int64
	EstimatedUSDMicros    *int64
	Groups                []ChatGPTThreadUsageGroup
}

type chatGPTThreadUsageEnvelope struct {
	Threads json.RawMessage `json:"threads"`
}

type chatGPTThreadUsageRaw struct {
	ThreadID              string                       `json:"thread_id"`
	EstimatedCreditMicros json.RawMessage              `json:"estimated_usage_credits_micros"`
	EstimatedUSDMicros    json.RawMessage              `json:"estimated_usage_usd_micros"`
	Groups                []chatGPTThreadUsageRawGroup `json:"groups"`
}

type chatGPTThreadUsageRawGroup struct {
	Model                 string          `json:"model"`
	ReasoningEffort       *string         `json:"reasoning_effort"`
	Speed                 *string         `json:"speed"`
	NetNewInputTokens     json.RawMessage `json:"net_new_input_tokens"`
	CachedInputTokens     json.RawMessage `json:"cached_input_tokens"`
	OutputTokens          json.RawMessage `json:"output_tokens"`
	EstimatedCreditMicros json.RawMessage `json:"estimated_usage_credits_micros"`
}

// ParseChatGPTThreadUsageSnapshot parses the cumulative response shape used by
// ChatGPT Desktop 26.901.51231 for /wham/usage/thread_usage/query. Unknown
// fields are intentionally tolerated, while every token field used for future
// billing must be a present, non-negative base-10 integer.
//
// This function only validates provider evidence. It does not make the result
// safe for per-request billing; callers still need a durable, atomic delta
// settlement contract and an exact model-price mapping.
func ParseChatGPTThreadUsageSnapshot(body []byte, threadID string) (*ChatGPTThreadUsageSnapshot, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, fmt.Errorf("ChatGPT thread usage requires a conversation id")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("parse ChatGPT thread usage: empty response")
	}

	var envelope chatGPTThreadUsageEnvelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("parse ChatGPT thread usage: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("parse ChatGPT thread usage: %w", err)
	}

	if len(bytes.TrimSpace(envelope.Threads)) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Threads), []byte("null")) {
		return nil, fmt.Errorf("parse ChatGPT thread usage: threads is required")
	}
	var threads []chatGPTThreadUsageRaw
	if err := json.Unmarshal(envelope.Threads, &threads); err != nil {
		return nil, fmt.Errorf("parse ChatGPT thread usage: threads must be an array: %w", err)
	}

	var matched *chatGPTThreadUsageRaw
	for i := range threads {
		candidate := &threads[i]
		if strings.TrimSpace(candidate.ThreadID) != threadID {
			continue
		}
		if matched != nil {
			return nil, fmt.Errorf("parse ChatGPT thread usage: duplicate thread %q", threadID)
		}
		matched = candidate
	}
	if matched == nil {
		return nil, ErrChatGPTThreadUsagePending
	}
	if len(matched.Groups) == 0 {
		return nil, fmt.Errorf("parse ChatGPT thread usage: thread %q has no usage groups", threadID)
	}

	snapshot := &ChatGPTThreadUsageSnapshot{ThreadID: threadID}
	var err error
	if snapshot.EstimatedCreditMicros, err = parseOptionalChatGPTUsageInt(
		matched.EstimatedCreditMicros, "estimated_usage_credits_micros",
	); err != nil {
		return nil, err
	}
	if snapshot.EstimatedUSDMicros, err = parseOptionalChatGPTUsageInt(
		matched.EstimatedUSDMicros, "estimated_usage_usd_micros",
	); err != nil {
		return nil, err
	}

	snapshot.Groups = make([]ChatGPTThreadUsageGroup, 0, len(matched.Groups))
	seenDimensions := make(map[string]struct{}, len(matched.Groups))
	for i, raw := range matched.Groups {
		model := strings.TrimSpace(raw.Model)
		if model == "" {
			return nil, fmt.Errorf("parse ChatGPT thread usage: groups[%d].model is required", i)
		}
		netNew, err := parseRequiredChatGPTUsageInt(raw.NetNewInputTokens, fmt.Sprintf("groups[%d].net_new_input_tokens", i))
		if err != nil {
			return nil, err
		}
		cached, err := parseRequiredChatGPTUsageInt(raw.CachedInputTokens, fmt.Sprintf("groups[%d].cached_input_tokens", i))
		if err != nil {
			return nil, err
		}
		output, err := parseRequiredChatGPTUsageInt(raw.OutputTokens, fmt.Sprintf("groups[%d].output_tokens", i))
		if err != nil {
			return nil, err
		}
		credits, err := parseOptionalChatGPTUsageInt(raw.EstimatedCreditMicros, fmt.Sprintf("groups[%d].estimated_usage_credits_micros", i))
		if err != nil {
			return nil, err
		}
		group := ChatGPTThreadUsageGroup{
			Model:                 model,
			NetNewInputTokens:     netNew,
			CachedInputTokens:     cached,
			OutputTokens:          output,
			EstimatedCreditMicros: credits,
		}
		if raw.ReasoningEffort != nil {
			group.ReasoningEffort = strings.TrimSpace(*raw.ReasoningEffort)
		}
		if raw.Speed != nil {
			group.Speed = strings.TrimSpace(*raw.Speed)
		}
		dimension := chatGPTThreadUsageDimension(group)
		if _, exists := seenDimensions[dimension]; exists {
			return nil, fmt.Errorf("parse ChatGPT thread usage: duplicate usage dimension at groups[%d]", i)
		}
		seenDimensions[dimension] = struct{}{}
		snapshot.Groups = append(snapshot.Groups, group)
	}
	return snapshot, nil
}

// DiffChatGPTThreadUsageSnapshots computes a non-negative cumulative delta.
// Both snapshots must already have been durably associated with the same
// selected provider account by the caller. This pure function does not persist
// a baseline, reserve quota, choose prices, or apply billing.
func DiffChatGPTThreadUsageSnapshots(previous, current *ChatGPTThreadUsageSnapshot) (*ChatGPTThreadUsageDelta, error) {
	if previous == nil || current == nil {
		return nil, errors.New("diff ChatGPT thread usage: both snapshots are required")
	}
	previousThreadID := strings.TrimSpace(previous.ThreadID)
	currentThreadID := strings.TrimSpace(current.ThreadID)
	if previousThreadID == "" || currentThreadID == "" || previousThreadID != currentThreadID {
		return nil, errors.New("diff ChatGPT thread usage: snapshots must have the same conversation id")
	}

	previousGroups, err := indexChatGPTThreadUsageGroups(previous.Groups, "previous")
	if err != nil {
		return nil, err
	}
	currentGroups, err := indexChatGPTThreadUsageGroups(current.Groups, "current")
	if err != nil {
		return nil, err
	}
	for dimension, group := range previousGroups {
		if _, exists := currentGroups[dimension]; !exists && chatGPTThreadUsageGroupHasTokens(group) {
			return nil, fmt.Errorf("diff ChatGPT thread usage: current snapshot dropped a non-zero usage dimension")
		}
	}

	delta := &ChatGPTThreadUsageDelta{ThreadID: currentThreadID}
	if delta.EstimatedCreditMicros, err = diffOptionalChatGPTUsageInt(
		previous.EstimatedCreditMicros, current.EstimatedCreditMicros, "estimated_usage_credits_micros",
	); err != nil {
		return nil, err
	}
	if delta.EstimatedUSDMicros, err = diffOptionalChatGPTUsageInt(
		previous.EstimatedUSDMicros, current.EstimatedUSDMicros, "estimated_usage_usd_micros",
	); err != nil {
		return nil, err
	}

	delta.Groups = make([]ChatGPTThreadUsageGroup, 0, len(current.Groups))
	for _, currentGroup := range current.Groups {
		previousGroup, existed := previousGroups[chatGPTThreadUsageDimension(currentGroup)]
		if currentGroup.NetNewInputTokens < previousGroup.NetNewInputTokens ||
			currentGroup.CachedInputTokens < previousGroup.CachedInputTokens ||
			currentGroup.OutputTokens < previousGroup.OutputTokens {
			return nil, fmt.Errorf("diff ChatGPT thread usage: token counters regressed for model %q", currentGroup.Model)
		}
		groupDelta := ChatGPTThreadUsageGroup{
			Model:             currentGroup.Model,
			ReasoningEffort:   currentGroup.ReasoningEffort,
			Speed:             currentGroup.Speed,
			NetNewInputTokens: currentGroup.NetNewInputTokens - previousGroup.NetNewInputTokens,
			CachedInputTokens: currentGroup.CachedInputTokens - previousGroup.CachedInputTokens,
			OutputTokens:      currentGroup.OutputTokens - previousGroup.OutputTokens,
		}
		if !existed {
			if currentGroup.EstimatedCreditMicros != nil {
				credits := *currentGroup.EstimatedCreditMicros
				groupDelta.EstimatedCreditMicros = &credits
			}
		} else {
			groupDelta.EstimatedCreditMicros, err = diffOptionalChatGPTUsageInt(
				previousGroup.EstimatedCreditMicros,
				currentGroup.EstimatedCreditMicros,
				fmt.Sprintf("estimated_usage_credits_micros for model %q", currentGroup.Model),
			)
			if err != nil {
				return nil, err
			}
		}
		if chatGPTThreadUsageGroupHasTokens(groupDelta) ||
			(groupDelta.EstimatedCreditMicros != nil && *groupDelta.EstimatedCreditMicros > 0) {
			delta.Groups = append(delta.Groups, groupDelta)
		}
	}
	return delta, nil
}

func indexChatGPTThreadUsageGroups(groups []ChatGPTThreadUsageGroup, label string) (map[string]ChatGPTThreadUsageGroup, error) {
	indexed := make(map[string]ChatGPTThreadUsageGroup, len(groups))
	for i, group := range groups {
		if strings.TrimSpace(group.Model) == "" {
			return nil, fmt.Errorf("diff ChatGPT thread usage: %s groups[%d].model is required", label, i)
		}
		if group.NetNewInputTokens < 0 || group.CachedInputTokens < 0 || group.OutputTokens < 0 {
			return nil, fmt.Errorf("diff ChatGPT thread usage: %s groups[%d] has a negative token counter", label, i)
		}
		if group.EstimatedCreditMicros != nil && *group.EstimatedCreditMicros < 0 {
			return nil, fmt.Errorf("diff ChatGPT thread usage: %s groups[%d] has a negative credit counter", label, i)
		}
		dimension := chatGPTThreadUsageDimension(group)
		if _, exists := indexed[dimension]; exists {
			return nil, fmt.Errorf("diff ChatGPT thread usage: %s snapshot has a duplicate usage dimension", label)
		}
		indexed[dimension] = group
	}
	return indexed, nil
}

func chatGPTThreadUsageDimension(group ChatGPTThreadUsageGroup) string {
	return strings.Join([]string{
		strings.TrimSpace(group.Model),
		strings.TrimSpace(group.ReasoningEffort),
		strings.TrimSpace(group.Speed),
	}, "\x00")
}

func chatGPTThreadUsageGroupHasTokens(group ChatGPTThreadUsageGroup) bool {
	return group.NetNewInputTokens != 0 || group.CachedInputTokens != 0 || group.OutputTokens != 0
}

func diffOptionalChatGPTUsageInt(previous, current *int64, field string) (*int64, error) {
	if previous == nil && current == nil {
		return nil, nil
	}
	if previous == nil || current == nil {
		return nil, fmt.Errorf("diff ChatGPT thread usage: %s availability changed", field)
	}
	if *previous < 0 || *current < 0 || *current < *previous {
		return nil, fmt.Errorf("diff ChatGPT thread usage: %s regressed", field)
	}
	delta := *current - *previous
	return &delta, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func parseRequiredChatGPTUsageInt(raw json.RawMessage, field string) (int64, error) {
	value, err := parseOptionalChatGPTUsageInt(raw, field)
	if err != nil {
		return 0, err
	}
	if value == nil {
		return 0, fmt.Errorf("parse ChatGPT thread usage: %s is required", field)
	}
	return *value, nil
}

func parseOptionalChatGPTUsageInt(raw json.RawMessage, field string) (*int64, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse ChatGPT thread usage: %s must be an integer", field)
	}
	if parsed < 0 {
		return nil, fmt.Errorf("parse ChatGPT thread usage: %s must be non-negative", field)
	}
	return &parsed, nil
}
