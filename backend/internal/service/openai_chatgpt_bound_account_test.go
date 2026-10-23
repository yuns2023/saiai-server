package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func chatGPTBoundTestAccount(groupID int64) Account {
	return Account{ID: 248, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID}}
}

func TestClassifyChatGPTBoundAccountStates(t *testing.T) {
	now := time.Date(2026, 10, 23, 6, 14, 0, 0, time.UTC)
	past, soon, later := now.Add(-time.Minute), now.Add(10*time.Minute), now.Add(3*time.Hour)
	const gid = int64(48)
	cases := []struct {
		name    string
		mutate  func(*Account)
		kind    ChatGPTBoundAccountUnavailableKind
		reason  string
		retryAt time.Time
	}{
		{name: "healthy", mutate: func(*Account) {}},
		{name: "expired cooldowns are healthy", mutate: func(a *Account) { a.RateLimitResetAt, a.OverloadUntil, a.TempUnschedulableUntil = &past, &past, &past }},
		{name: "rate limited", mutate: func(a *Account) { a.RateLimitResetAt = &later }, kind: ChatGPTBoundAccountTemporary, reason: ChatGPTBoundReasonRateLimited, retryAt: later},
		{name: "overloaded", mutate: func(a *Account) { a.OverloadUntil = &soon }, kind: ChatGPTBoundAccountTemporary, reason: ChatGPTBoundReasonOverloaded, retryAt: soon},
		{name: "temp unschedulable", mutate: func(a *Account) { a.TempUnschedulableUntil = &soon }, kind: ChatGPTBoundAccountTemporary, reason: ChatGPTBoundReasonTempUnschedulable, retryAt: soon},
		{name: "latest cooldown wins", mutate: func(a *Account) { a.OverloadUntil, a.RateLimitResetAt = &soon, &later }, kind: ChatGPTBoundAccountTemporary, reason: ChatGPTBoundReasonRateLimited, retryAt: later},
		{name: "disabled", mutate: func(a *Account) { a.Status = StatusDisabled; a.RateLimitResetAt = &later }, kind: ChatGPTBoundAccountPermanent, reason: ChatGPTBoundReasonInactive},
		{name: "error status", mutate: func(a *Account) { a.Status = StatusError }, kind: ChatGPTBoundAccountPermanent, reason: ChatGPTBoundReasonInactive},
		{name: "admin unschedulable", mutate: func(a *Account) { a.Schedulable = false; a.RateLimitResetAt = &later }, kind: ChatGPTBoundAccountPermanent, reason: ChatGPTBoundReasonAdminUnschedulable},
		{name: "removed from group", mutate: func(a *Account) { a.GroupIDs = []int64{gid + 1} }, kind: ChatGPTBoundAccountPermanent, reason: ChatGPTBoundReasonNotInGroup},
		{name: "api key account", mutate: func(a *Account) { a.Type = AccountTypeAPIKey }, kind: ChatGPTBoundAccountPermanent, reason: ChatGPTBoundReasonNotOpenAIOAuth},
		{name: "platform mismatch", mutate: func(a *Account) { a.Platform = PlatformAnthropic }, kind: ChatGPTBoundAccountPermanent, reason: ChatGPTBoundReasonNotOpenAIOAuth},
		{name: "auto paused after expiry", mutate: func(a *Account) { a.AutoPauseOnExpired, a.ExpiresAt = true, &past }, kind: ChatGPTBoundAccountPermanent, reason: ChatGPTBoundReasonExpired},
		{name: "expiry without auto pause", mutate: func(a *Account) { a.ExpiresAt = &past }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := chatGPTBoundTestAccount(gid)
			tc.mutate(&account)
			got := ClassifyChatGPTBoundAccount(&account, gid, now)
			if tc.kind == "" {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tc.kind, got.Kind)
			require.Equal(t, tc.reason, got.Reason)
			require.Equal(t, tc.retryAt, got.RetryAt)
			require.EqualValues(t, 248, got.AccountID)
			require.ErrorIs(t, got, ErrNoAvailableAccounts)
		})
	}
	missing := ClassifyChatGPTBoundAccount(nil, gid, now)
	require.Equal(t, ChatGPTBoundAccountPermanent, missing.Kind)
	require.Equal(t, ChatGPTBoundReasonNotFound, missing.Reason)
}

// chatGPTBoundRepo mirrors the production group query: only schedulable,
// grouped accounts are listed while GetByID sees every non-deleted account.
type chatGPTBoundRepo struct {
	AccountRepository
	accounts []Account
	listErr  error
	getErr   error
}

func (r *chatGPTBoundRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]Account, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	var out []Account
	for _, account := range r.accounts {
		if account.Platform == platform && account.IsSchedulable() && ClassifyChatGPTBoundAccount(&account, groupID, time.Now()) == nil {
			out = append(out, account)
		}
	}
	return out, nil
}

func (r *chatGPTBoundRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	for _, account := range r.accounts {
		if account.ID == id {
			copy := account
			return &copy, nil
		}
	}
	return nil, ErrAccountNotFound
}

func TestGetChatGPTBoundAccountClassifiesOwner(t *testing.T) {
	gid := int64(48)
	reset := time.Now().Add(3 * time.Hour)
	limited := chatGPTBoundTestAccount(gid)
	limited.RateLimitResetAt = &reset
	disabled := chatGPTBoundTestAccount(gid)
	disabled.ID, disabled.Schedulable = 249, false
	healthy := chatGPTBoundTestAccount(gid)
	healthy.ID = 245
	repo := &chatGPTBoundRepo{accounts: []Account{limited, disabled, healthy}}
	svc := &OpenAIGatewayService{accountRepo: repo}

	account, err := svc.GetChatGPTBoundAccount(context.Background(), &gid, 245)
	require.NoError(t, err)
	require.EqualValues(t, 245, account.ID)

	_, err = svc.GetChatGPTBoundAccount(context.Background(), &gid, 248)
	unavailable, ok := AsChatGPTBoundAccountUnavailable(err)
	require.True(t, ok)
	require.Equal(t, ChatGPTBoundAccountTemporary, unavailable.Kind)
	require.Equal(t, ChatGPTBoundReasonRateLimited, unavailable.Reason)
	require.WithinDuration(t, reset, unavailable.RetryAt, 0)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Contains(t, err.Error(), "bound account 248")

	_, err = svc.GetChatGPTBoundAccount(context.Background(), &gid, 249)
	unavailable, _ = AsChatGPTBoundAccountUnavailable(err)
	require.Equal(t, ChatGPTBoundAccountPermanent, unavailable.Kind)
	require.Equal(t, ChatGPTBoundReasonAdminUnschedulable, unavailable.Reason)

	_, err = svc.GetChatGPTBoundAccount(context.Background(), &gid, 999)
	unavailable, _ = AsChatGPTBoundAccountUnavailable(err)
	require.Equal(t, ChatGPTBoundAccountPermanent, unavailable.Kind)
	require.Equal(t, ChatGPTBoundReasonNotFound, unavailable.Reason)

	other := gid + 1
	_, err = svc.GetChatGPTBoundAccount(context.Background(), &other, 245)
	unavailable, _ = AsChatGPTBoundAccountUnavailable(err)
	require.Equal(t, ChatGPTBoundReasonNotInGroup, unavailable.Reason)

	storeErr := errors.New("TEST_ONLY_DB_DOWN")
	repo.getErr = storeErr
	_, err = svc.GetChatGPTBoundAccount(context.Background(), &gid, 245)
	unavailable, _ = AsChatGPTBoundAccountUnavailable(err)
	require.Equal(t, ChatGPTBoundAccountStore, unavailable.Kind)
	require.ErrorIs(t, err, storeErr)
	repo.getErr, repo.listErr = nil, storeErr
	_, err = svc.GetChatGPTBoundAccount(context.Background(), &gid, 245)
	unavailable, _ = AsChatGPTBoundAccountUnavailable(err)
	require.Equal(t, ChatGPTBoundAccountStore, unavailable.Kind)

	// Listed state and row state disagree (cooldown just ended): short retry.
	repo.listErr = nil
	repo.accounts = []Account{healthy}
	svc.accountRepo = &chatGPTBoundEmptyListRepo{repo}
	_, err = svc.GetChatGPTBoundAccount(context.Background(), &gid, 245)
	unavailable, _ = AsChatGPTBoundAccountUnavailable(err)
	require.Equal(t, ChatGPTBoundAccountTemporary, unavailable.Kind)
	require.Equal(t, ChatGPTBoundReasonScheduleRace, unavailable.Reason)
	require.WithinDuration(t, time.Now().Add(chatGPTBoundScheduleRaceRetry), unavailable.RetryAt, time.Second)
}

type chatGPTBoundEmptyListRepo struct{ *chatGPTBoundRepo }

func (r *chatGPTBoundEmptyListRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return nil, nil
}

func TestChatGPTMemoryUploadReleaseIsCompareAndDelete(t *testing.T) {
	cache := NewChatGPTMemoryTurnCache().(ChatGPTUploadCache)
	scope := ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
	key := scope.DeviceCookieKey("TEST_ONLY_DEVICE", "TEST_ONLY_PROOF")
	_, err := cache.ClaimChatGPTUploadOwner(context.Background(), key, 17, time.Hour)
	require.NoError(t, err)
	require.NoError(t, cache.ReleaseChatGPTUploadOwner(context.Background(), key, 19))
	owner, _ := cache.GetChatGPTUploadOwner(context.Background(), key)
	require.EqualValues(t, 17, owner)
	require.NoError(t, cache.ReleaseChatGPTUploadOwner(context.Background(), key, 17))
	owner, _ = cache.GetChatGPTUploadOwner(context.Background(), key)
	require.Zero(t, owner)
}
