//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type pointerGatewayCacheStub struct {
	mockGatewayCacheForPlatform
	pointers  map[string]int64
	setTTLs   map[string]time.Duration
	getErr    error
	readCalls int
}

func pointerStubKey(userID int64, digest string) string {
	return fmt.Sprintf("%d:%s", userID, digest)
}

func (m *pointerGatewayCacheStub) GetUserSessionAccountID(_ context.Context, userID int64, digest string) (int64, error) {
	m.readCalls++
	if m.getErr != nil {
		return 0, m.getErr
	}
	if id, ok := m.pointers[pointerStubKey(userID, digest)]; ok {
		return id, nil
	}
	return 0, ErrGatewaySessionCacheMiss
}

func (m *pointerGatewayCacheStub) SetUserSessionAccountID(_ context.Context, userID int64, digest string, accountID int64, ttl time.Duration) error {
	if m.pointers == nil {
		m.pointers = map[string]int64{}
	}
	if m.setTTLs == nil {
		m.setTTLs = map[string]time.Duration{}
	}
	m.pointers[pointerStubKey(userID, digest)] = accountID
	m.setTTLs[pointerStubKey(userID, digest)] = ttl
	return nil
}

const pointerTestMetadata = `{"device_id":"dev-a","account_uuid":"","session_id":"conv-1"}`

func pointerTestCtx(t *testing.T, userID int64, metadata string) (context.Context, userSessionPointer) {
	t.Helper()
	ctx := WithUserSessionPointer(context.Background(), userID, PlatformAnthropic, metadata)
	p, ok := userSessionPointerFromContext(ctx)
	require.True(t, ok)
	return ctx, p
}

func pointerTestRepo(accounts ...Account) *mockAccountRepoForPlatform {
	repo := &mockAccountRepoForPlatform{accounts: accounts, accountsByID: map[int64]*Account{}}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	return repo
}

func pointerTestLoadAwareService(repo *mockAccountRepoForPlatform, cache GatewayCache) *GatewayService {
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	return &GatewayService{
		accountRepo:        repo,
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(&mockConcurrencyCache{}),
	}
}

func TestWithUserSessionPointer_Identity(t *testing.T) {
	_, base := pointerTestCtx(t, 7, pointerTestMetadata)

	_, otherUser := pointerTestCtx(t, 8, pointerTestMetadata)
	require.Equal(t, base.digest, otherUser.digest, "user isolation comes from the key, not the digest")
	require.NotEqual(t, base.userID, otherUser.userID)

	_, otherDevice := pointerTestCtx(t, 7, `{"device_id":"dev-b","account_uuid":"","session_id":"conv-1"}`)
	require.NotEqual(t, base.digest, otherDevice.digest, "another device must not inherit the conversation account")

	_, otherSession := pointerTestCtx(t, 7, `{"device_id":"dev-a","account_uuid":"","session_id":"conv-2"}`)
	require.NotEqual(t, base.digest, otherSession.digest)

	for name, tc := range map[string]struct {
		userID   int64
		platform string
		metadata string
	}{
		"no user":          {0, PlatformAnthropic, pointerTestMetadata},
		"openai platform":  {7, PlatformOpenAI, pointerTestMetadata},
		"gemini platform":  {7, PlatformGemini, pointerTestMetadata},
		"empty metadata":   {7, PlatformAnthropic, ""},
		"malformed":        {7, PlatformAnthropic, "{not json"},
		"blank session":    {7, PlatformAnthropic, `{"device_id":"dev-a","account_uuid":"","session_id":"   "}`},
		"oversize session": {7, PlatformAnthropic, `{"device_id":"dev-a","account_uuid":"","session_id":"` + strings.Repeat("x", maxUserSessionPointerSessionIDLen+1) + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := WithUserSessionPointer(context.Background(), tc.userID, tc.platform, tc.metadata)
			_, ok := userSessionPointerFromContext(ctx)
			require.False(t, ok)
		})
	}
}

func TestRecordUserSessionPointer_WritesServedAccount(t *testing.T) {
	ctx, p := pointerTestCtx(t, 7, pointerTestMetadata)
	cache := &pointerGatewayCacheStub{}
	svc := &GatewayService{cache: cache, cfg: testConfig()}

	svc.RecordUserSessionPointer(ctx, &Account{ID: 41, Platform: PlatformAnthropic, Type: AccountTypeAPIKey})
	require.Equal(t, int64(41), cache.pointers[pointerStubKey(p.userID, p.digest)])
	require.Equal(t, defaultStickySessionTTL, cache.setTTLs[pointerStubKey(p.userID, p.digest)])

	// Without the request context nothing is written.
	cache.pointers = nil
	svc.RecordUserSessionPointer(context.Background(), &Account{ID: 41})
	require.Empty(t, cache.pointers)
}

// The production case: the conversation lived on account 1 in another group;
// the new group has no binding and account 1 is above the 5h new-session gate.
func TestUserSessionPointer_LoadAwareKeepsAccountAboveFiveHourGate(t *testing.T) {
	resetAt := time.Now().Add(2 * time.Hour)
	repo := pointerTestRepo(
		Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5, SessionWindowEnd: &resetAt, Extra: map[string]any{"session_window_utilization": 0.92}},
		Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 5, SessionWindowEnd: &resetAt, Extra: map[string]any{"session_window_utilization": 0.08}},
	)
	model := "claude-3-5-sonnet-20241022"

	// Baseline: without a pointer the new session is moved off account 1.
	baseCache := &pointerGatewayCacheStub{mockGatewayCacheForPlatform: mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}}}
	result, err := pointerTestLoadAwareService(repo, baseCache).SelectAccountWithLoadAwareness(context.Background(), nil, "group-b-hash", model, nil, "")
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Account.ID)

	ctx, p := pointerTestCtx(t, 7, pointerTestMetadata)
	cache := &pointerGatewayCacheStub{
		mockGatewayCacheForPlatform: mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}},
		pointers:                    map[string]int64{pointerStubKey(p.userID, p.digest): 1},
	}
	result, err = pointerTestLoadAwareService(repo, cache).SelectAccountWithLoadAwareness(ctx, nil, "group-b-hash", model, nil, "")
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Account.ID, "pointer must keep the conversation on its account")
	require.Empty(t, cache.deletedSessions)
}

func TestUserSessionPointer_CreatesGroupBindingForOrdinaryAccount(t *testing.T) {
	repo := pointerTestRepo(
		Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5},
		Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Priority: 5, Status: StatusActive, Schedulable: true, Concurrency: 5},
	)
	ctx, p := pointerTestCtx(t, 7, pointerTestMetadata)
	cache := &pointerGatewayCacheStub{
		mockGatewayCacheForPlatform: mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}},
		pointers:                    map[string]int64{pointerStubKey(p.userID, p.digest): 2},
	}
	result, err := pointerTestLoadAwareService(repo, cache).SelectAccountWithLoadAwareness(ctx, nil, "group-b-hash", "claude-3-5-sonnet-20241022", nil, "")
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Account.ID, "pointer outranks priority like any sticky binding")
	require.Equal(t, int64(2), cache.sessionBindings["group-b-hash"], "the new group gets its own binding")
	require.Zero(t, cache.refreshed["group-b-hash"], "a pointer hit has no group key to refresh")
}

func TestUserSessionPointer_GroupBindingWins(t *testing.T) {
	repo := pointerTestRepo(
		Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5},
		Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Priority: 5, Status: StatusActive, Schedulable: true, Concurrency: 5},
	)
	ctx, p := pointerTestCtx(t, 7, pointerTestMetadata)
	cache := &pointerGatewayCacheStub{
		mockGatewayCacheForPlatform: mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"h": 1}},
		pointers:                    map[string]int64{pointerStubKey(p.userID, p.digest): 2},
	}
	result, err := pointerTestLoadAwareService(repo, cache).SelectAccountWithLoadAwareness(ctx, nil, "h", "claude-3-5-sonnet-20241022", nil, "")
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Account.ID)
	require.Zero(t, cache.readCalls, "the pointer is read only when the group has no binding")
}

func TestUserSessionPointer_RejectedCandidateNeverDeletes(t *testing.T) {
	future := time.Now().Add(time.Hour)
	for name, pointed := range map[string]int64{
		"not in group":       99,
		"temp unschedulable": 3,
	} {
		t.Run(name, func(t *testing.T) {
			repo := pointerTestRepo(
				Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5},
				Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5, TempUnschedulableUntil: &future},
			)
			ctx, p := pointerTestCtx(t, 7, pointerTestMetadata)
			cache := &pointerGatewayCacheStub{
				mockGatewayCacheForPlatform: mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}},
				pointers:                    map[string]int64{pointerStubKey(p.userID, p.digest): pointed},
			}
			result, err := pointerTestLoadAwareService(repo, cache).SelectAccountWithLoadAwareness(ctx, nil, "h", "claude-3-5-sonnet-20241022", nil, "")
			require.NoError(t, err)
			require.Equal(t, int64(2), result.Account.ID)
			require.Empty(t, cache.deletedSessions, "a rejected pointer must not delete any group binding")
		})
	}
}

func TestUserSessionPointer_CacheErrorFallsBack(t *testing.T) {
	repo := pointerTestRepo(
		Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5},
	)
	ctx, _ := pointerTestCtx(t, 7, pointerTestMetadata)
	cache := &pointerGatewayCacheStub{
		mockGatewayCacheForPlatform: mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}},
		getErr:                      errors.New("redis down"),
	}
	result, err := pointerTestLoadAwareService(repo, cache).SelectAccountWithLoadAwareness(ctx, nil, "h", "claude-3-5-sonnet-20241022", nil, "")
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Account.ID)
}

func TestUserSessionPointer_LegacySelectionKeepsAccountAboveFiveHourGate(t *testing.T) {
	resetAt := time.Now().Add(2 * time.Hour)
	repo := pointerTestRepo(
		Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 0, Status: StatusActive, Schedulable: true, SessionWindowEnd: &resetAt, Extra: map[string]any{"session_window_utilization": 0.95}},
		Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 5, Status: StatusActive, Schedulable: true, SessionWindowEnd: &resetAt, Extra: map[string]any{"session_window_utilization": 0.50}},
	)
	ctx, p := pointerTestCtx(t, 7, pointerTestMetadata)
	cache := &pointerGatewayCacheStub{
		mockGatewayCacheForPlatform: mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}},
		pointers:                    map[string]int64{pointerStubKey(p.userID, p.digest): 1},
	}
	svc := &GatewayService{accountRepo: repo, cache: cache, cfg: testConfig()}
	selected, err := svc.selectAccountForModelWithPlatform(ctx, nil, "h", "claude-3-5-sonnet-20241022", nil, PlatformAnthropic)
	require.NoError(t, err)
	require.Equal(t, int64(1), selected.ID)
	require.Empty(t, cache.deletedSessions)
}

// pendingReadFailStub makes the group's pending lookup fail with a real error
// (not a miss); the pointer must not replace an unreadable group state.
type pendingReadFailStub struct {
	pointerGatewayCacheStub
}

func (m *pendingReadFailStub) GetPendingSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, errors.New("redis timeout")
}
func (m *pendingReadFailStub) SetPendingSessionAccountIDNX(context.Context, int64, string, int64, time.Duration) (bool, error) {
	return true, nil
}
func (m *pendingReadFailStub) DeletePendingSessionAccountID(context.Context, int64, string) error {
	return nil
}

func TestUserSessionPointer_GroupReadFailureDoesNotUsePointer(t *testing.T) {
	ctx, p := pointerTestCtx(t, 7, pointerTestMetadata)
	cache := &pendingReadFailStub{pointerGatewayCacheStub{
		mockGatewayCacheForPlatform: mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}},
		pointers:                    map[string]int64{pointerStubKey(p.userID, p.digest): 2},
	}}
	svc := &GatewayService{cache: cache, cfg: testConfig()}
	id, fromPointer, err := svc.getStickySessionAccountIDForSelection(ctx, nil, "h")
	require.Error(t, err)
	require.Zero(t, id)
	require.False(t, fromPointer)
	require.Zero(t, cache.readCalls)
}

type pointerOwnershipStub struct {
	*stickyOwnershipStub
	pointers map[string]int64
}

func (c *pointerOwnershipStub) GetUserSessionAccountID(_ context.Context, userID int64, digest string) (int64, error) {
	if id, ok := c.pointers[pointerStubKey(userID, digest)]; ok {
		return id, nil
	}
	return 0, ErrGatewaySessionCacheMiss
}
func (c *pointerOwnershipStub) SetUserSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}

// Claude Code ownership path: the new group has no owner; the pointer account
// is selected and claimed with previous="" like any new owner.
func TestUserSessionPointer_ClaudeCodeOwnershipClaimsPointerAccount(t *testing.T) {
	groupID := int64(31)
	resetAt := time.Now().Add(2 * time.Hour)
	repo := pointerTestRepo(
		Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5, SessionWindowEnd: &resetAt, Extra: map[string]any{"session_window_utilization": 0.08}, AccountGroups: []AccountGroup{{GroupID: groupID}}},
		Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5, SessionWindowEnd: &resetAt, Extra: map[string]any{"session_window_utilization": 0.92}, AccountGroups: []AccountGroup{{GroupID: groupID}}},
	)
	ctx, p := pointerTestCtx(t, 7, pointerTestMetadata)
	ctx = SetClaudeCodeClient(ctx, true)
	cache := &pointerOwnershipStub{
		stickyOwnershipStub: &stickyOwnershipStub{mockGatewayCacheForPlatform: &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{}}},
		pointers:            map[string]int64{pointerStubKey(p.userID, p.digest): 2},
	}
	group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
	svc := pointerTestLoadAwareService(repo, cache)
	svc.groupRepo = &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}}

	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "h", "claude-3-5-sonnet-20241022", nil, "")
	require.NoError(t, err)
	if result.ReleaseFunc != nil {
		defer result.ReleaseFunc()
	}
	require.Equal(t, int64(2), result.Account.ID, "pointer keeps the account despite the 5h new-session gate")
	require.NotEmpty(t, result.StickyOwnership)
	require.Equal(t, 1, cache.claims)
	require.Equal(t, int64(2), cache.sessionBindings["h"])
}
