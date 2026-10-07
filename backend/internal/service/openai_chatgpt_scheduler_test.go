package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type chatGPTReservationCache struct {
	stubConcurrencyCache
	acquired map[int64]int
	active   map[string]int64
}

func (c *chatGPTReservationCache) AcquireAccountSlot(_ context.Context, id int64, max int, requestID string) (bool, error) {
	if c.acquired == nil {
		c.acquired = make(map[int64]int)
		c.active = make(map[string]int64)
	}
	c.acquired[id]++
	count := 0
	for _, activeID := range c.active {
		if activeID == id {
			count++
		}
	}
	if count >= max {
		return false, nil
	}
	c.active[requestID] = id
	return true, nil
}

func (c *chatGPTReservationCache) ReleaseAccountSlot(_ context.Context, _ int64, requestID string) error {
	delete(c.active, requestID)
	return nil
}

func TestSelectChatGPTOAuthAccountRejectsAPIKeysBeforeReservation(t *testing.T) {
	groupID := int64(1)
	key := Account{ID: 18, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10}
	for _, sticky := range []bool{false, true} {
		t.Run(map[bool]string{false: "load_balance", true: "existing_key_sticky"}[sticky], func(t *testing.T) {
			cache := &chatGPTReservationCache{}
			bindings := &stubGatewayCache{}
			if sticky {
				bindings.sessionBindings = map[string]int64{"openai:desktop-session": key.ID}
			}
			svc := &OpenAIGatewayService{accountRepo: stubOpenAIAccountRepo{accounts: []Account{key}}, cache: bindings, cfg: &config.Config{}, concurrencyService: NewConcurrencyService(cache)}
			for i := 0; i < 100; i++ {
				selection, _, err := svc.SelectChatGPTOAuthAccount(context.Background(), &groupID, "desktop-session", "")
				require.Error(t, err)
				require.Nil(t, selection)
			}
			require.Empty(t, cache.acquired, "an ineligible key must never acquire even a temporary slot")
			require.Empty(t, cache.active)
			if sticky {
				require.Equal(t, key.ID, bindings.sessionBindings["openai:desktop-session"], "a failed Chat request must preserve the Responses binding")
			} else {
				require.Empty(t, bindings.sessionBindings)
			}

			// The same account remains eligible for Codex Responses after all
			// native Chat failures, rather than being left at its slot limit.
			selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportHTTPSSE)
			require.NoError(t, err)
			require.True(t, selection.Acquired)
			require.Equal(t, key.ID, selection.Account.ID)
			selection.ReleaseFunc()
			require.Empty(t, cache.active)
		})
	}
}

func TestSelectChatGPTOAuthAccountMixedPoolAndFreshType(t *testing.T) {
	groupID := int64(1)
	key := Account{ID: 18, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10, Priority: 0}
	oauth := Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10}
	for _, freshKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "eligible_oauth", true: "snapshot_oauth_changed_to_key"}[freshKey], func(t *testing.T) {
			cache := &chatGPTReservationCache{}
			fresh := oauth
			if freshKey {
				fresh.Type = AccountTypeAPIKey
			}
			snapshot := &openAISnapshotCacheStub{snapshotAccounts: []*Account{&key, &oauth}, accountsByID: map[int64]*Account{key.ID: &key, oauth.ID: &fresh}}
			svc := &OpenAIGatewayService{
				accountRepo: stubOpenAIAccountRepo{accounts: []Account{key, fresh}}, cfg: &config.Config{},
				schedulerSnapshot: &SchedulerSnapshotService{cache: snapshot}, concurrencyService: NewConcurrencyService(cache),
			}
			selection, _, err := svc.SelectChatGPTOAuthAccount(context.Background(), &groupID, "", "")
			if freshKey {
				require.Error(t, err)
				require.Nil(t, selection)
				require.Empty(t, cache.acquired)
				return
			}
			require.NoError(t, err)
			require.True(t, selection.Acquired)
			require.Equal(t, oauth.ID, selection.Account.ID)
			require.Zero(t, cache.acquired[key.ID], "a higher-priority API key is ineligible for native Chat")
			require.Len(t, cache.active, 1)
			selection.ReleaseFunc()
			require.Empty(t, cache.active)
		})
	}
}

type chatGPTInvalidScheduler struct {
	OpenAIAccountScheduler
	selection *AccountSelectionResult
	err       error
}

func (s *chatGPTInvalidScheduler) Select(_ context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	if req.RequiredAccountType != AccountTypeOAuth {
		return nil, OpenAIAccountScheduleDecision{}, errors.New("missing OAuth filter")
	}
	return s.selection, OpenAIAccountScheduleDecision{}, s.err
}

func TestSelectChatGPTOAuthAccountReleasesRejectedCustomReservations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account *Account
		err     error
	}{
		{"key", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, nil},
		{"missing_account", nil, nil},
		{"selection_with_error", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, errors.New("scheduler failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			releases := 0
			svc := &OpenAIGatewayService{openaiScheduler: &chatGPTInvalidScheduler{
				selection: &AccountSelectionResult{Account: tc.account, Acquired: true, ReleaseFunc: func() { releases++ }}, err: tc.err,
			}}
			selection, _, err := svc.SelectChatGPTOAuthAccount(context.Background(), nil, "", "")
			require.Error(t, err)
			require.Nil(t, selection)
			require.Equal(t, 1, releases)
		})
	}
}

func TestOpenAIPreviousResponseReleasesTransportIneligibleReservation(t *testing.T) {
	groupID := int64(1)
	key := Account{ID: 18, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10}
	oauth := Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Extra: map[string]any{"openai_oauth_responses_websockets_v2_enabled": true},
	}
	cache := &chatGPTReservationCache{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	svc := &OpenAIGatewayService{accountRepo: stubOpenAIAccountRepo{accounts: []Account{key, oauth}}, cfg: cfg, concurrencyService: NewConcurrencyService(cache)}
	require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(context.Background(), groupID, "resp_transport", key.ID, time.Hour))
	selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "resp_transport", "", "gpt-5.1", nil, OpenAIUpstreamTransportResponsesWebsocketV2)
	require.NoError(t, err)
	require.Equal(t, oauth.ID, selection.Account.ID)
	require.Len(t, cache.active, 1)
	for _, id := range cache.active {
		require.Equal(t, oauth.ID, id, "rejected previous-response reservation must be released")
	}
	selection.ReleaseFunc()
	require.Empty(t, cache.active)
}
