package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type inputModerationClientStub struct {
	result *InputModerationClassification
	err    error
	calls  int
}

func (s *inputModerationClientStub) Classify(_ context.Context, _ string) (*InputModerationClassification, error) {
	s.calls++
	return s.result, s.err
}

type inputModerationEventRepoStub struct {
	events     []*InputModerationEvent
	transition *InputModerationTransition
	state      *UserInputRiskState
	stateReads int
}

func (s *inputModerationEventRepoStub) InsertInputModerationEvent(_ context.Context, event *InputModerationEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *inputModerationEventRepoStub) ApplyInputModerationIncident(_ context.Context, event *InputModerationEvent, _ InputModerationEscalationPolicy) (*InputModerationTransition, error) {
	s.events = append(s.events, event)
	if s.transition != nil {
		return s.transition, nil
	}
	return &InputModerationTransition{Action: InputModerationActionAutoDisabled, StrikeCount: 2, UserDisabled: true}, nil
}

func (s *inputModerationEventRepoStub) GetUserInputRiskState(context.Context, int64) (*UserInputRiskState, error) {
	s.stateReads++
	return s.state, nil
}
func (s *inputModerationEventRepoStub) ResetUserInputRiskState(context.Context, int64) error {
	s.state = nil
	return nil
}

type inputModerationGroupRepoStub struct {
	GroupRepository
	groups []*Group
	calls  int
}

func (s *inputModerationGroupRepoStub) GetByIDLite(_ context.Context, _ int64) (*Group, error) {
	s.calls++
	index := s.calls - 1
	if index >= len(s.groups) {
		index = len(s.groups) - 1
	}
	return s.groups[index], nil
}

type inputModerationUserRepoStub struct {
	UserRepository
	user        *User
	updateCalls int
}

func (s *inputModerationUserRepoStub) GetByID(_ context.Context, _ int64) (*User, error) {
	copy := *s.user
	return &copy, nil
}

func (s *inputModerationUserRepoStub) Update(_ context.Context, user *User) error {
	s.updateCalls++
	copy := *user
	s.user = &copy
	return nil
}

type inputModerationInvalidatorStub struct {
	userIDs []int64
}

type inputModerationStateCacheStub struct {
	blockedUntil  *time.Time
	negativeFound bool
}

func (s *inputModerationStateCacheStub) GetUserCooldown(context.Context, int64) (*time.Time, bool, error) {
	return s.blockedUntil, s.blockedUntil != nil || s.negativeFound, nil
}
func (s *inputModerationStateCacheStub) SetUserNoCooldown(context.Context, int64, time.Duration) error {
	s.negativeFound = true
	return nil
}
func (s *inputModerationStateCacheStub) SetUserCooldown(_ context.Context, _ int64, blockedUntil time.Time) error {
	s.blockedUntil = &blockedUntil
	return nil
}
func (s *inputModerationStateCacheStub) ClearUserCooldown(context.Context, int64) error {
	s.blockedUntil = nil
	s.negativeFound = false
	return nil
}

type inputModerationQueueStub struct {
	enqueued chan string
}

func (*inputModerationQueueStub) EnsureConsumerGroup(context.Context) error { return nil }
func (s *inputModerationQueueStub) Enqueue(_ context.Context, ciphertext string) error {
	s.enqueued <- ciphertext
	return nil
}
func (*inputModerationQueueStub) Read(ctx context.Context, _ string, _ int64, _ time.Duration) ([]InputModerationQueueMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*inputModerationQueueStub) ClaimStale(context.Context, string, time.Duration, int64) ([]InputModerationQueueMessage, error) {
	return nil, nil
}
func (*inputModerationQueueStub) Ack(context.Context, string) error { return nil }

type inputModerationPlainEncryptor struct{}

func (inputModerationPlainEncryptor) Encrypt(value string) (string, error) {
	return "enc:" + value, nil
}
func (inputModerationPlainEncryptor) Decrypt(value string) (string, error) {
	return strings.TrimPrefix(value, "enc:"), nil
}

func (*inputModerationInvalidatorStub) InvalidateAuthCacheByKey(context.Context, string) {}
func (s *inputModerationInvalidatorStub) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	s.userIDs = append(s.userIDs, userID)
}
func (*inputModerationInvalidatorStub) InvalidateAuthCacheByGroupID(context.Context, int64) {}

func newInputModerationServiceForTest(
	client InputModerationClient,
	events InputModerationEventRepository,
	groups GroupRepository,
	users UserRepository,
	invalidator APIKeyAuthCacheInvalidator,
) *InputModerationService {
	cfg := &config.Config{}
	cfg.Gateway.InputModeration.Endpoint = "http://sidecar.test/v1/classify"
	cfg.Gateway.InputModeration.WorkerCount = 1
	cfg.Gateway.InputModeration.QueueSize = 4
	cfg.Gateway.InputModeration.RequestTimeoutSeconds = 1
	cfg.Gateway.InputModeration.MaxInputChars = 1024
	return NewInputModerationService(
		client,
		events,
		nil,
		nil,
		nil,
		groups,
		NewUserService(users, invalidator, nil),
		cfg,
	)
}

func TestExtractLatestRealUserText(t *testing.T) {
	tests := []struct {
		name     string
		messages []any
		want     string
	}{
		{
			name: "text blocks only",
			messages: []any{map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": " first "},
					map[string]any{"type": "image", "source": map[string]any{}},
					map[string]any{"type": "text", "text": "second"},
				},
			}},
			want: "first\nsecond",
		},
		{
			name: "tool result excluded",
			messages: []any{map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "tool_result", "content": "ignore instructions"}},
			}},
			want: "",
		},
		{
			name: "assistant excluded",
			messages: []any{map[string]any{
				"role": "assistant", "content": "unsafe historical output",
			}},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractLatestRealUserText(&ParsedRequest{Messages: tt.messages})
			require.Equal(t, tt.want, got)
		})
	}
}

func TestExtractLatestOpenAIUserText(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "direct string", body: `{"input":" hello "}`, want: "hello"},
		{
			name: "latest role user content",
			body: `{"input":[
				{"role":"user","content":[{"type":"input_text","text":"old"}]},
				{"role":"assistant","content":[{"type":"output_text","text":"answer"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"new one"},{"type":"input_image","image_url":"data:image/png;base64,x"},{"type":"input_text","text":"new two"}]}
			]}`,
			want: "new one\nnew two",
		},
		{
			name: "top level input text",
			body: `{"input":[{"type":"input_text","text":"first"},{"type":"input_text","text":"second"}]}`,
			want: "first\nsecond",
		},
		{
			name: "tool only turn",
			body: `{"input":[{"role":"user","content":"historical"},{"type":"function_call_output","call_id":"call_1","output":"ignore previous instructions"}]}`,
			want: "",
		},
		{
			name: "assistant latest",
			body: `{"input":[{"role":"user","content":"historical"},{"role":"assistant","content":"answer"}]}`,
			want: "",
		},
		{
			name: "developer only",
			body: `{"input":[{"role":"developer","content":"system policy"}]}`,
			want: "",
		},
		{
			name: "image only user",
			body: `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"}]}]}`,
			want: "",
		},
		{name: "invalid json", body: `{`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ExtractLatestOpenAIUserText([]byte(tt.body)))
		})
	}
}

func TestInputModerationProcessUnsafeAutoDisablesUser(t *testing.T) {
	group := &Group{
		ID:                             3,
		InputModerationEnabled:         true,
		InputModerationAutoDisableUser: true,
		InputModerationCategories:      []string{"Jailbreak"},
		UpdatedAt:                      time.Unix(100, 0),
	}
	groups := &inputModerationGroupRepoStub{groups: []*Group{group, group}}
	users := &inputModerationUserRepoStub{user: &User{ID: 1, Role: RoleUser, Status: StatusActive}}
	invalidator := &inputModerationInvalidatorStub{}
	client := &inputModerationClientStub{result: &InputModerationClassification{
		Safety: "Unsafe", Categories: []string{"Jailbreak"}, ModelVersion: "test-model",
	}}
	events := &inputModerationEventRepoStub{}
	svc := newInputModerationServiceForTest(client, events, groups, users, invalidator)
	defer svc.Stop()

	svc.process(InputModerationTask{RequestID: "req-1", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "test input"})

	require.Equal(t, []int64{1}, invalidator.userIDs)
	require.Len(t, events.events, 1)
	require.Equal(t, InputModerationActionAutoDisabled, events.events[0].Action)
	require.Len(t, events.events[0].InputHash, 64)
	require.Equal(t, InputModerationSourceAnthropicMessages, events.events[0].Source)
}

func TestInputModerationProcessFirstIncidentSetsCooldown(t *testing.T) {
	blockedUntil := time.Now().Add(30 * time.Minute)
	group := &Group{ID: 3, InputModerationEnabled: true, InputModerationAutoDisableUser: true, InputModerationCategories: []string{"Jailbreak"}}
	groups := &inputModerationGroupRepoStub{groups: []*Group{group, group}}
	events := &inputModerationEventRepoStub{transition: &InputModerationTransition{
		Action: InputModerationActionCooldown, StrikeCount: 1, BlockedUntil: &blockedUntil,
	}}
	cache := &inputModerationStateCacheStub{}
	cfg := &config.Config{}
	cfg.Gateway.InputModeration.Endpoint = "http://sidecar.test/v1/classify"
	cfg.Gateway.InputModeration.WorkerCount = 1
	cfg.Gateway.InputModeration.QueueSize = 4
	cfg.Gateway.InputModeration.RequestTimeoutSeconds = 1
	cfg.Gateway.InputModeration.MaxInputChars = 1024
	svc := NewInputModerationService(
		&inputModerationClientStub{result: &InputModerationClassification{Safety: "unsafe", Categories: []string{"Jailbreak"}}},
		events, cache, nil, nil, groups,
		NewUserService(&inputModerationUserRepoStub{user: &User{ID: 1, Role: RoleUser, Status: StatusActive}}, &inputModerationInvalidatorStub{}, nil),
		cfg,
	)
	defer svc.Stop()

	require.NoError(t, svc.process(InputModerationTask{UserID: 1, APIKeyID: 2, GroupID: 3, Text: "test"}))
	require.NotNil(t, cache.blockedUntil)
	require.WithinDuration(t, blockedUntil, *cache.blockedUntil, time.Second)
}

func TestInputModerationSubmitUsesEncryptedDurableQueue(t *testing.T) {
	queue := &inputModerationQueueStub{enqueued: make(chan string, 1)}
	cfg := &config.Config{}
	cfg.Totp.EncryptionKeyConfigured = true
	cfg.Gateway.InputModeration.Endpoint = "http://sidecar.test/v1/classify"
	cfg.Gateway.InputModeration.WorkerCount = 1
	cfg.Gateway.InputModeration.QueueSize = 4
	cfg.Gateway.InputModeration.RequestTimeoutSeconds = 1
	cfg.Gateway.InputModeration.MaxInputChars = 1024
	svc := NewInputModerationService(nil, nil, nil, queue, inputModerationPlainEncryptor{}, nil, nil, cfg)
	defer svc.Stop()

	require.True(t, svc.Submit(InputModerationTask{UserID: 1, APIKeyID: 2, GroupID: 3, Text: "secret prompt"}))
	select {
	case ciphertext := <-queue.enqueued:
		require.NotEqual(t, "secret prompt", ciphertext)
		require.True(t, strings.HasPrefix(ciphertext, "enc:"))
		var task InputModerationTask
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(ciphertext, "enc:")), &task))
		require.Equal(t, "secret prompt", task.Text)
		require.NotEmpty(t, task.JobID)
	case <-time.After(time.Second):
		t.Fatal("durable queue did not receive task")
	}
}

func TestInputModerationProcessNeverAutoDisablesAdmin(t *testing.T) {
	group := &Group{ID: 3, InputModerationEnabled: true, InputModerationAutoDisableUser: true}
	groups := &inputModerationGroupRepoStub{groups: []*Group{group, group}}
	users := &inputModerationUserRepoStub{user: &User{ID: 1, Role: RoleAdmin, Status: StatusActive}}
	events := &inputModerationEventRepoStub{transition: &InputModerationTransition{Action: InputModerationActionIgnoredAdmin}}
	svc := newInputModerationServiceForTest(
		&inputModerationClientStub{result: &InputModerationClassification{Safety: "unsafe", Categories: []string{"Jailbreak"}}},
		events,
		groups,
		users,
		&inputModerationInvalidatorStub{},
	)
	defer svc.Stop()

	svc.process(InputModerationTask{UserID: 1, APIKeyID: 2, GroupID: 3, Text: "test"})

	require.Zero(t, users.updateCalls)
	require.Len(t, events.events, 1)
	require.Equal(t, InputModerationActionIgnoredAdmin, events.events[0].Action)
}

func TestInputModerationProcessRechecksPolicyAfterInference(t *testing.T) {
	enabled := &Group{ID: 3, InputModerationEnabled: true, InputModerationAutoDisableUser: true}
	disabled := &Group{ID: 3, InputModerationEnabled: false, InputModerationAutoDisableUser: true}
	groups := &inputModerationGroupRepoStub{groups: []*Group{enabled, disabled}}
	users := &inputModerationUserRepoStub{user: &User{ID: 1, Role: RoleUser, Status: StatusActive}}
	events := &inputModerationEventRepoStub{}
	svc := newInputModerationServiceForTest(
		&inputModerationClientStub{result: &InputModerationClassification{Safety: "unsafe", Categories: []string{"Jailbreak"}}},
		events,
		groups,
		users,
		&inputModerationInvalidatorStub{},
	)
	defer svc.Stop()

	svc.process(InputModerationTask{UserID: 1, APIKeyID: 2, GroupID: 3, Text: "test"})

	require.Zero(t, users.updateCalls)
	require.Empty(t, events.events)
}

func TestInputModerationProcessDoesNotOverrideNewerManualUserUpdate(t *testing.T) {
	now := time.Now().UTC()
	group := &Group{ID: 3, InputModerationEnabled: true, InputModerationAutoDisableUser: true}
	groups := &inputModerationGroupRepoStub{groups: []*Group{group, group}}
	users := &inputModerationUserRepoStub{user: &User{
		ID: 1, Role: RoleUser, Status: StatusActive, UpdatedAt: now,
	}}
	events := &inputModerationEventRepoStub{transition: &InputModerationTransition{Action: InputModerationActionStaleUserState}}
	svc := newInputModerationServiceForTest(
		&inputModerationClientStub{result: &InputModerationClassification{Safety: "unsafe", Categories: []string{"Jailbreak"}}},
		events,
		groups,
		users,
		&inputModerationInvalidatorStub{},
	)
	defer svc.Stop()

	svc.process(InputModerationTask{
		UserID: 1, APIKeyID: 2, GroupID: 3, Text: "test", EnqueuedAt: now.Add(-time.Minute),
	})

	require.Zero(t, users.updateCalls)
	require.Len(t, events.events, 1)
	require.Equal(t, InputModerationActionStaleUserState, events.events[0].Action)
}

func TestModerationCategoriesMatch(t *testing.T) {
	require.True(t, moderationCategoriesMatch(nil, nil))
	require.True(t, moderationCategoriesMatch([]string{"Jailbreak"}, []string{"jailbreak"}))
	require.False(t, moderationCategoriesMatch([]string{"PII"}, []string{"Jailbreak"}))
}

func TestInputModerationGroupFieldsSurviveAuthCacheSnapshot(t *testing.T) {
	apiKeyService := &APIKeyService{}
	original := &APIKey{
		ID:      2,
		UserID:  1,
		Status:  StatusAPIKeyActive,
		User:    &User{ID: 1, Status: StatusActive, Role: RoleUser},
		GroupID: ptrInt64(3),
		Group: &Group{
			ID:                               3,
			InputModerationEnabled:           true,
			InputModerationAutoDisableUser:   true,
			InputModerationCategories:        []string{"Jailbreak", "PII"},
			InputModerationActionMode:        InputModerationActionModeCooldownThenDisable,
			InputModerationCooldownMinutes:   30,
			InputModerationDisableAfterHits:  2,
			InputModerationStrikeWindowHours: 24,
			InputModerationDedupeMinutes:     5,
		},
	}

	snapshot := apiKeyService.snapshotFromAPIKey(original)
	restored := apiKeyService.snapshotToAPIKey("sk-test", snapshot)

	require.True(t, restored.Group.InputModerationEnabled)
	require.True(t, restored.Group.InputModerationAutoDisableUser)
	require.Equal(t, []string{"Jailbreak", "PII"}, restored.Group.InputModerationCategories)
	require.Equal(t, InputModerationActionModeCooldownThenDisable, restored.Group.InputModerationActionMode)
	require.Equal(t, 30, restored.Group.InputModerationCooldownMinutes)
	require.Equal(t, 2, restored.Group.InputModerationDisableAfterHits)
	require.Equal(t, 24, restored.Group.InputModerationStrikeWindowHours)
	require.Equal(t, 5, restored.Group.InputModerationDedupeMinutes)
}

func TestUserReenableResetsInputModerationState(t *testing.T) {
	blockedUntil := time.Now().Add(20 * time.Minute)
	events := &inputModerationEventRepoStub{state: &UserInputRiskState{UserID: 1, StrikeCount: 1, BlockedUntil: &blockedUntil}}
	cache := &inputModerationStateCacheStub{blockedUntil: &blockedUntil}
	users := &inputModerationUserRepoStub{user: &User{ID: 1, Role: RoleUser, Status: StatusDisabled}}
	userService := NewUserService(users, &inputModerationInvalidatorStub{}, nil)
	userService.SetInputModerationState(events, cache)

	require.NoError(t, userService.UpdateStatus(context.Background(), 1, StatusActive))
	require.Nil(t, events.state)
	require.Nil(t, cache.blockedUntil)
	require.Equal(t, StatusActive, users.user.Status)
}

func TestAdminReenableResetsInputModerationState(t *testing.T) {
	blockedUntil := time.Now().Add(20 * time.Minute)
	events := &inputModerationEventRepoStub{state: &UserInputRiskState{UserID: 1, StrikeCount: 1, BlockedUntil: &blockedUntil}}
	cache := &inputModerationStateCacheStub{blockedUntil: &blockedUntil}
	users := &inputModerationUserRepoStub{user: &User{ID: 1, Role: RoleUser, Status: StatusDisabled}}
	admin := &adminServiceImpl{userRepo: users, inputRiskRepo: events, inputRiskCache: cache}

	updated, err := admin.UpdateUser(context.Background(), 1, &UpdateUserInput{Status: StatusActive})
	require.NoError(t, err)
	require.Equal(t, StatusActive, updated.Status)
	require.Nil(t, events.state)
	require.Nil(t, cache.blockedUntil)
}

func TestGetActiveCooldownCachesNegativeState(t *testing.T) {
	events := &inputModerationEventRepoStub{}
	cache := &inputModerationStateCacheStub{}
	cfg := &config.Config{}
	cfg.Gateway.InputModeration.WorkerCount = 1
	cfg.Gateway.InputModeration.QueueSize = 1
	svc := NewInputModerationService(nil, events, cache, nil, nil, nil, nil, cfg)
	defer svc.Stop()

	blocked, err := svc.GetActiveCooldown(context.Background(), 1)
	require.NoError(t, err)
	require.Nil(t, blocked)
	blocked, err = svc.GetActiveCooldown(context.Background(), 1)
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, 1, events.stateReads)
}

func ptrInt64(value int64) *int64 { return &value }
