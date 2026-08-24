package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/alitto/pond/v2"
	"github.com/google/uuid"
)

const (
	InputModerationSafetySafe          = "safe"
	InputModerationSafetyControversial = "controversial"
	InputModerationSafetyUnsafe        = "unsafe"

	InputModerationActionAudit               = "audit"
	InputModerationActionAutoDisabled        = "auto_disabled"
	InputModerationActionAlreadyInactive     = "already_inactive"
	InputModerationActionCategoryNotSelected = "category_not_selected"
	InputModerationActionIgnoredAdmin        = "ignored_admin"
	InputModerationActionStaleUserState      = "stale_user_state"
	InputModerationActionCooldown            = "cooldown"
	InputModerationActionDuplicateIgnored    = "duplicate_ignored"

	InputModerationActionModeCooldownThenDisable = "cooldown_then_disable"
	InputModerationActionModeImmediateDisable    = "immediate_disable"

	InputModerationSourceAnthropicMessages   = "anthropic_messages"
	InputModerationSourceOpenAIResponsesHTTP = "openai_responses_http"
	InputModerationSourceOpenAIResponsesWS   = "openai_responses_ws"
)

// InputModerationClassification is the sidecar's normalized output.
type InputModerationClassification struct {
	Safety       string
	Categories   []string
	ModelVersion string
}

// InputModerationClient classifies raw text without receiving site identity.
type InputModerationClient interface {
	Classify(ctx context.Context, text string) (*InputModerationClassification, error)
}

// InputModerationEvent is a metadata-only audit record. Raw user text must not
// be added to this structure.
type InputModerationEvent struct {
	JobID           string
	RequestID       string
	UserID          int64
	Username        string
	DeviceRef       string
	APIKeyID        int64
	GroupID         int64
	InputHash       string
	Safety          string
	Categories      []string
	Action          string
	ModelVersion    string
	PolicyVersion   string
	Source          string
	TurnNumber      int
	CountedAsStrike bool
	EnqueuedAt      time.Time
	CreatedAt       time.Time
}

type InputModerationEventRepository interface {
	InsertInputModerationEvent(ctx context.Context, event *InputModerationEvent) error
	ApplyInputModerationIncident(ctx context.Context, event *InputModerationEvent, policy InputModerationEscalationPolicy) (*InputModerationTransition, error)
	GetUserInputRiskState(ctx context.Context, userID int64) (*UserInputRiskState, error)
	ResetUserInputRiskState(ctx context.Context, userID int64) error
}

type InputModerationEscalationPolicy struct {
	ActionMode       string
	Cooldown         time.Duration
	DisableAfterHits int
	StrikeWindow     time.Duration
	DedupeWindow     time.Duration
}

type InputModerationTransition struct {
	Action       string
	StrikeCount  int
	BlockedUntil *time.Time
	Duplicate    bool
	UserDisabled bool
}

type UserInputRiskState struct {
	UserID                int64
	StrikeCount           int
	StrikeWindowStartedAt *time.Time
	BlockedUntil          *time.Time
	LastIncidentAt        *time.Time
	ResetAt               *time.Time
	UpdatedAt             time.Time
}

type InputModerationStateCache interface {
	GetUserCooldown(ctx context.Context, userID int64) (blockedUntil *time.Time, found bool, err error)
	SetUserCooldown(ctx context.Context, userID int64, blockedUntil time.Time) error
	SetUserNoCooldown(ctx context.Context, userID int64, ttl time.Duration) error
	ClearUserCooldown(ctx context.Context, userID int64) error
}

type InputModerationQueueMessage struct {
	ID         string
	Ciphertext string
}

type InputModerationTaskQueue interface {
	EnsureConsumerGroup(ctx context.Context) error
	Enqueue(ctx context.Context, ciphertext string) error
	Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]InputModerationQueueMessage, error)
	ClaimStale(ctx context.Context, consumer string, minIdle time.Duration, count int64) ([]InputModerationQueueMessage, error)
	Ack(ctx context.Context, messageID string) error
}

// InputModerationTask contains the authenticated identity held by the Gateway.
// Only Text is sent to the classifier sidecar.
type InputModerationTask struct {
	JobID      string    `json:"job_id"`
	RequestID  string    `json:"request_id"`
	UserID     int64     `json:"user_id"`
	Username   string    `json:"username,omitempty"`
	DeviceRef  string    `json:"device_ref,omitempty"`
	APIKeyID   int64     `json:"api_key_id"`
	GroupID    int64     `json:"group_id"`
	Text       string    `json:"text"`
	EnqueuedAt time.Time `json:"enqueued_at"`
	Source     string    `json:"source"`
	TurnNumber int       `json:"turn_number"`
}

// InputModerationService uses an encrypted Redis Stream when a stable key is
// configured and otherwise falls back to a bounded in-memory queue. Request
// handlers never wait for classification.
type InputModerationService struct {
	client      InputModerationClient
	eventRepo   InputModerationEventRepository
	stateCache  InputModerationStateCache
	taskQueue   InputModerationTaskQueue
	encryptor   SecretEncryptor
	groupRepo   GroupRepository
	userService *UserService
	cfg         config.GatewayInputModerationConfig
	pool        pond.Pool
	stopOnce    sync.Once
	dropped     atomic.Uint64
	consumer    string
	queueCancel context.CancelFunc
	queueWG     sync.WaitGroup
}

func NewInputModerationService(
	client InputModerationClient,
	eventRepo InputModerationEventRepository,
	stateCache InputModerationStateCache,
	taskQueue InputModerationTaskQueue,
	encryptor SecretEncryptor,
	groupRepo GroupRepository,
	userService *UserService,
	cfg *config.Config,
) *InputModerationService {
	moderationCfg := config.GatewayInputModerationConfig{}
	if cfg != nil {
		moderationCfg = cfg.Gateway.InputModeration
	}
	workerCount := moderationCfg.WorkerCount
	if workerCount <= 0 {
		workerCount = 2
	}
	queueSize := moderationCfg.QueueSize
	if queueSize <= 0 {
		queueSize = 256
	}
	service := &InputModerationService{
		client:      client,
		eventRepo:   eventRepo,
		stateCache:  stateCache,
		taskQueue:   taskQueue,
		encryptor:   encryptor,
		groupRepo:   groupRepo,
		userService: userService,
		cfg:         moderationCfg,
		pool:        pond.NewPool(workerCount, pond.WithQueueSize(queueSize)),
		consumer:    "input-moderation-" + uuid.NewString(),
	}
	durableQueueEnabled := service.taskQueue != nil && service.encryptor != nil && cfg != nil &&
		cfg.Totp.EncryptionKeyConfigured && strings.TrimSpace(moderationCfg.Endpoint) != ""
	if durableQueueEnabled {
		service.startQueueConsumers(workerCount)
	} else if service.taskQueue != nil && strings.TrimSpace(moderationCfg.Endpoint) != "" {
		slog.Warn("input_moderation_durable_queue_disabled", "reason", "totp.encryption_key must be explicitly configured")
		service.taskQueue = nil
	}
	return service
}

func (s *InputModerationService) EnabledForGroup(group *Group) bool {
	return s != nil && group != nil && group.ID > 0 && group.InputModerationEnabled &&
		strings.TrimSpace(s.cfg.Endpoint) != ""
}

// Submit queues a moderation task without blocking on model inference.
func (s *InputModerationService) Submit(task InputModerationTask) bool {
	if s == nil || s.pool == nil || s.pool.Stopped() || task.UserID <= 0 || task.APIKeyID <= 0 || task.GroupID <= 0 {
		return false
	}
	text := strings.TrimSpace(truncateModerationText(task.Text, s.cfg.MaxInputChars))
	if text == "" {
		return false
	}
	task.Text = text
	task.Source = normalizeInputModerationSource(task.Source)
	if strings.TrimSpace(task.JobID) == "" {
		task.JobID = uuid.NewString()
	}
	if task.EnqueuedAt.IsZero() {
		task.EnqueuedAt = time.Now().UTC()
	}
	if s.taskQueue != nil && s.encryptor != nil {
		payload, err := json.Marshal(task)
		if err == nil {
			var ciphertext string
			ciphertext, err = s.encryptor.Encrypt(string(payload))
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				err = s.taskQueue.Enqueue(ctx, ciphertext)
				cancel()
			}
		}
		if err == nil {
			return true
		}
		slog.Warn("input_moderation_durable_enqueue_failed", "user_id", task.UserID, "username", task.Username, "device_ref", task.DeviceRef, "group_id", task.GroupID, "error", err)
	}
	_, ok := s.pool.TrySubmit(func() {
		_ = s.process(task)
	})
	if !ok {
		dropped := s.dropped.Add(1)
		if dropped == 1 || dropped%100 == 0 {
			slog.Warn("input_moderation_queue_full",
				"dropped", dropped,
				"user_id", task.UserID,
				"username", task.Username,
				"device_ref", task.DeviceRef,
				"api_key_id", task.APIKeyID,
				"group_id", task.GroupID,
			)
		}
	}
	return ok
}

func (s *InputModerationService) startQueueConsumers(workerCount int) {
	ctx, cancel := context.WithCancel(context.Background())
	s.queueCancel = cancel
	s.queueWG.Add(1)
	go func() {
		defer s.queueWG.Done()
		for {
			if err := s.taskQueue.EnsureConsumerGroup(ctx); err == nil {
				break
			} else if ctx.Err() == nil {
				slog.Error("input_moderation_queue_group_failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		claimTicker := time.NewTicker(30 * time.Second)
		defer claimTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			messages, err := s.taskQueue.Read(ctx, s.consumer, int64(workerCount), time.Second)
			if err != nil && ctx.Err() == nil {
				slog.Warn("input_moderation_queue_read_failed", "error", err)
			}
			s.submitQueueMessages(messages)
			select {
			case <-claimTicker.C:
				minIdle := 5 * time.Minute
				if configured := time.Duration(s.cfg.RequestTimeoutSeconds*3) * time.Second; configured > minIdle {
					minIdle = configured
				}
				claimed, claimErr := s.taskQueue.ClaimStale(ctx, s.consumer, minIdle, int64(workerCount))
				if claimErr != nil && ctx.Err() == nil {
					slog.Warn("input_moderation_queue_claim_failed", "error", claimErr)
				}
				s.submitQueueMessages(claimed)
			default:
			}
		}
	}()
}

func (s *InputModerationService) submitQueueMessages(messages []InputModerationQueueMessage) {
	for _, message := range messages {
		message := message
		_, ok := s.pool.TrySubmit(func() { s.handleQueueMessage(message) })
		if !ok {
			return // leave pending; XAUTOCLAIM will recover it
		}
	}
}

func (s *InputModerationService) handleQueueMessage(message InputModerationQueueMessage) {
	plaintext, err := s.encryptor.Decrypt(message.Ciphertext)
	if err != nil {
		slog.Error("input_moderation_queue_decrypt_failed", "message_id", message.ID, "error", err)
		_ = s.taskQueue.Ack(context.Background(), message.ID)
		return
	}
	var task InputModerationTask
	if err := json.Unmarshal([]byte(plaintext), &task); err != nil {
		slog.Error("input_moderation_queue_decode_failed", "message_id", message.ID, "error", err)
		_ = s.taskQueue.Ack(context.Background(), message.ID)
		return
	}
	if err := s.process(task); err != nil {
		slog.Warn("input_moderation_queue_process_failed", "message_id", message.ID, "job_id", task.JobID, "error", err)
		return // pending entry is retried through XAUTOCLAIM
	}
	ackCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.taskQueue.Ack(ackCtx, message.ID); err != nil {
		slog.Warn("input_moderation_queue_ack_failed", "message_id", message.ID, "error", err)
	}
}

func (s *InputModerationService) process(task InputModerationTask) error {
	if s.client == nil || s.groupRepo == nil || s.userService == nil {
		return fmt.Errorf("input moderation dependencies unavailable")
	}
	task.Source = normalizeInputModerationSource(task.Source)
	if strings.TrimSpace(task.JobID) == "" {
		task.JobID = uuid.NewString()
	}
	if task.TurnNumber < 0 {
		task.TurnNumber = 0
	}
	maxJobAge := time.Duration(s.cfg.MaxJobAgeHours) * time.Hour
	if maxJobAge <= 0 {
		maxJobAge = 24 * time.Hour
	}
	if !task.EnqueuedAt.IsZero() && time.Since(task.EnqueuedAt) > maxJobAge {
		slog.Warn("input_moderation_job_expired", "job_id", task.JobID, "user_id", task.UserID, "username", task.Username, "device_ref", task.DeviceRef)
		return nil
	}
	timeout := time.Duration(s.cfg.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	classifyCtx, cancelClassify := context.WithTimeout(context.Background(), timeout)
	defer cancelClassify()

	// Re-read the policy so disabling the switch also protects against already
	// queued work automatically disabling users.
	group, err := s.groupRepo.GetByIDLite(classifyCtx, task.GroupID)
	if err != nil {
		return err
	}
	if group == nil || !group.InputModerationEnabled {
		return nil
	}

	result, err := s.client.Classify(classifyCtx, task.Text)
	if err != nil {
		slog.Warn("input_moderation_classify_failed",
			"user_id", task.UserID,
			"username", task.Username,
			"device_ref", task.DeviceRef,
			"api_key_id", task.APIKeyID,
			"group_id", task.GroupID,
			"error", err,
		)
		return err
	}
	if result == nil {
		return fmt.Errorf("input moderation sidecar returned no result")
	}
	result.Safety = strings.ToLower(strings.TrimSpace(result.Safety))
	if result.Safety != InputModerationSafetySafe &&
		result.Safety != InputModerationSafetyControversial &&
		result.Safety != InputModerationSafetyUnsafe {
		slog.Warn("input_moderation_invalid_safety", "group_id", task.GroupID, "safety", result.Safety)
		return fmt.Errorf("input moderation sidecar returned invalid safety %q", result.Safety)
	}
	if result.Safety == InputModerationSafetySafe {
		return nil
	}
	cancelClassify()

	actionCtx, cancelAction := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelAction()
	// Re-read after inference as well. An administrator may have switched the
	// policy off while a slow model request was running.
	group, err = s.groupRepo.GetByIDLite(actionCtx, task.GroupID)
	if err != nil {
		return err
	}
	if group == nil || !group.InputModerationEnabled {
		return nil
	}

	hash := sha256.Sum256([]byte(task.Text))
	event := &InputModerationEvent{
		JobID:         task.JobID,
		RequestID:     truncateModerationText(strings.TrimSpace(task.RequestID), 128),
		UserID:        task.UserID,
		Username:      task.Username,
		DeviceRef:     task.DeviceRef,
		APIKeyID:      task.APIKeyID,
		GroupID:       task.GroupID,
		InputHash:     hex.EncodeToString(hash[:]),
		Safety:        result.Safety,
		Categories:    normalizeModerationCategories(result.Categories),
		Action:        InputModerationActionAudit,
		ModelVersion:  truncateModerationText(strings.TrimSpace(result.ModelVersion), 128),
		PolicyVersion: fmt.Sprintf("%d", group.UpdatedAt.UTC().UnixNano()),
		Source:        task.Source,
		TurnNumber:    task.TurnNumber,
		EnqueuedAt:    task.EnqueuedAt,
		CreatedAt:     time.Now().UTC(),
	}
	if result.Safety == InputModerationSafetyUnsafe && group.InputModerationAutoDisableUser &&
		!moderationCategoriesMatch(group.InputModerationCategories, result.Categories) {
		event.Action = InputModerationActionCategoryNotSelected
	}
	incidentApplied := false
	if result.Safety == InputModerationSafetyUnsafe && group.InputModerationAutoDisableUser &&
		moderationCategoriesMatch(group.InputModerationCategories, result.Categories) && s.eventRepo != nil {
		transition, err := s.eventRepo.ApplyInputModerationIncident(actionCtx, event, InputModerationEscalationPolicy{
			ActionMode:       group.InputModerationActionMode,
			Cooldown:         time.Duration(group.InputModerationCooldownMinutes) * time.Minute,
			DisableAfterHits: group.InputModerationDisableAfterHits,
			StrikeWindow:     time.Duration(group.InputModerationStrikeWindowHours) * time.Hour,
			DedupeWindow:     time.Duration(group.InputModerationDedupeMinutes) * time.Minute,
		})
		if err != nil {
			slog.Error("input_moderation_transition_failed", "user_id", event.UserID, "username", event.Username, "device_ref", event.DeviceRef, "group_id", event.GroupID, "error", err)
			return err
		}
		incidentApplied = true
		if transition != nil {
			event.Action = transition.Action
			event.CountedAsStrike = !transition.Duplicate
			if transition.BlockedUntil != nil && s.stateCache != nil {
				if err := s.stateCache.SetUserCooldown(actionCtx, event.UserID, *transition.BlockedUntil); err != nil {
					slog.Warn("input_moderation_cooldown_cache_set_failed", "user_id", event.UserID, "username", event.Username, "device_ref", event.DeviceRef, "error", err)
				}
			}
			if transition.UserDisabled {
				s.userService.InvalidateAuthCacheByUserID(actionCtx, event.UserID)
				if s.stateCache != nil {
					_ = s.stateCache.ClearUserCooldown(actionCtx, event.UserID)
				}
			}
		}
	}
	if s.eventRepo != nil && !incidentApplied {
		if err := s.eventRepo.InsertInputModerationEvent(actionCtx, event); err != nil {
			slog.Error("input_moderation_event_insert_failed",
				"job_id", event.JobID,
				"user_id", event.UserID,
				"username", event.Username,
				"device_ref", event.DeviceRef,
				"group_id", event.GroupID,
				"action", event.Action,
				"error", err,
			)
			return err
		}
	}
	slog.Info("input_moderation_decision",
		"job_id", event.JobID,
		"user_id", event.UserID,
		"username", event.Username,
		"device_ref", event.DeviceRef,
		"api_key_id", event.APIKeyID,
		"group_id", event.GroupID,
		"safety", event.Safety,
		"categories", event.Categories,
		"action", event.Action,
		"source", event.Source,
		"turn_number", event.TurnNumber,
	)
	return nil
}

// GetActiveCooldown returns a temporary user-global block. Redis is only an
// accelerator; PostgreSQL remains authoritative on cache miss.
func (s *InputModerationService) GetActiveCooldown(ctx context.Context, userID int64) (*time.Time, error) {
	if s == nil || userID <= 0 {
		return nil, nil
	}
	now := time.Now()
	if s.stateCache != nil {
		blockedUntil, found, err := s.stateCache.GetUserCooldown(ctx, userID)
		if err == nil && blockedUntil != nil && blockedUntil.After(now) {
			return blockedUntil, nil
		}
		if err == nil && found {
			return nil, nil
		}
	}
	if s.eventRepo == nil {
		return nil, nil
	}
	state, err := s.eventRepo.GetUserInputRiskState(ctx, userID)
	if err != nil || state == nil || state.BlockedUntil == nil || !state.BlockedUntil.After(now) {
		if err == nil && s.stateCache != nil {
			_ = s.stateCache.SetUserNoCooldown(ctx, userID, 15*time.Second)
		}
		return nil, err
	}
	if s.stateCache != nil {
		_ = s.stateCache.SetUserCooldown(ctx, userID, *state.BlockedUntil)
	}
	return state.BlockedUntil, nil
}

func (s *InputModerationService) Stop() {
	if s == nil || s.pool == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.queueCancel != nil {
			s.queueCancel()
		}
		s.queueWG.Wait()
		s.pool.StopAndWait()
	})
}

// ExtractLatestRealUserText returns only text blocks from the last genuine user
// message. Tool results, prior assistant content, images and system scaffolding
// are intentionally excluded.
func ExtractLatestRealUserText(parsed *ParsedRequest) string {
	if !IsRealUserMessage(parsed) || parsed == nil || len(parsed.Messages) == 0 {
		return ""
	}
	last, ok := parsed.Messages[len(parsed.Messages)-1].(map[string]any)
	if !ok {
		return ""
	}
	content, ok := last["content"]
	if !ok {
		return ""
	}
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			typeName, _ := block["type"].(string)
			if typeName != "" && typeName != "text" {
				continue
			}
			text, _ := block["text"].(string)
			if text = strings.TrimSpace(text); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// ExtractLatestOpenAIUserText extracts only the newest user-authored text from
// a native Responses request. Tool outputs, developer/system instructions,
// assistant history, reasoning and images are deliberately excluded.
func ExtractLatestOpenAIUserText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var request struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil || len(request.Input) == 0 {
		return ""
	}

	var direct string
	if err := json.Unmarshal(request.Input, &direct); err == nil {
		return strings.TrimSpace(direct)
	}

	var items []json.RawMessage
	if err := json.Unmarshal(request.Input, &items); err != nil || len(items) == 0 {
		return ""
	}

	var trailingInputTexts []string
	for index := len(items) - 1; index >= 0; index-- {
		var item struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Text    string          `json:"text"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(items[index], &item); err != nil {
			return ""
		}
		itemType := strings.ToLower(strings.TrimSpace(item.Type))
		role := strings.ToLower(strings.TrimSpace(item.Role))

		if role == "user" {
			if len(trailingInputTexts) > 0 {
				return strings.Join(reverseNonEmptyStrings(trailingInputTexts), "\n")
			}
			if text := extractOpenAIUserContentText(item.Content); text != "" {
				return text
			}
			if itemType == "input_text" || itemType == "text" {
				return strings.TrimSpace(item.Text)
			}
			return ""
		}
		if role != "" {
			return ""
		}
		if itemType == "input_text" || itemType == "text" {
			if text := strings.TrimSpace(item.Text); text != "" {
				trailingInputTexts = append(trailingInputTexts, text)
			}
			continue
		}
		if len(trailingInputTexts) > 0 {
			return strings.Join(reverseNonEmptyStrings(trailingInputTexts), "\n")
		}
		// The newest logical item is not user-authored. Do not walk backwards
		// into history past tool outputs, reasoning, function calls or unknown
		// future item types.
		return ""
	}
	return strings.Join(reverseNonEmptyStrings(trailingInputTexts), "\n")
}

func extractOpenAIUserContentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var direct string
	if err := json.Unmarshal(content, &direct); err == nil {
		return strings.TrimSpace(direct)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		partType := strings.ToLower(strings.TrimSpace(part.Type))
		if partType != "input_text" && partType != "text" {
			continue
		}
		if text := strings.TrimSpace(part.Text); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

func reverseNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for index := len(values) - 1; index >= 0; index-- {
		if value := strings.TrimSpace(values[index]); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func normalizeInputModerationSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case InputModerationSourceOpenAIResponsesHTTP:
		return InputModerationSourceOpenAIResponsesHTTP
	case InputModerationSourceOpenAIResponsesWS:
		return InputModerationSourceOpenAIResponsesWS
	default:
		return InputModerationSourceAnthropicMessages
	}
}

func truncateModerationText(text string, maxChars int) string {
	if maxChars <= 0 {
		return text
	}
	count := 0
	for index := range text {
		if count == maxChars {
			return text[:index]
		}
		count++
	}
	return text
}

func normalizeModerationCategories(categories []string) []string {
	out := make([]string, 0, len(categories))
	seen := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		category = strings.TrimSpace(category)
		if category == "" {
			continue
		}
		key := strings.ToLower(category)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, category)
	}
	return out
}

func moderationCategoriesMatch(configured, detected []string) bool {
	configured = normalizeModerationCategories(configured)
	if len(configured) == 0 {
		return true
	}
	detected = normalizeModerationCategories(detected)
	if len(detected) == 0 {
		return false
	}
	wanted := make(map[string]struct{}, len(configured))
	for _, category := range configured {
		wanted[strings.ToLower(category)] = struct{}{}
	}
	for _, category := range detected {
		if _, ok := wanted[strings.ToLower(category)]; ok {
			return true
		}
	}
	return false
}
