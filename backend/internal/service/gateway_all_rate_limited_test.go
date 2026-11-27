package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func allLimitedAccount(id int64, mutate func(*Account)) Account {
	acc := Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
	if mutate != nil {
		mutate(&acc)
	}
	return acc
}

func allLimitedTimePtr(t time.Time) *time.Time { return &t }

func TestEarliestTimeBoundRelease(t *testing.T) {
	now := time.Now()
	in := func(d time.Duration) *time.Time { return allLimitedTimePtr(now.Add(d)) }
	model := "claude-sonnet-4-5"
	rateLimited := func(d time.Duration) func(*Account) { return func(a *Account) { a.RateLimitResetAt = in(d) } }

	cases := []struct {
		name     string
		accounts []Account
		want     *time.Time
	}{
		{
			name:     "single weekly-limited account",
			accounts: []Account{allLimitedAccount(1, rateLimited(22*time.Hour))},
			want:     in(22 * time.Hour),
		},
		{
			name: "earliest over rate limit, overload, temp unschedulable and model limit",
			accounts: []Account{
				allLimitedAccount(1, rateLimited(5*time.Hour)),
				allLimitedAccount(2, func(a *Account) { a.OverloadUntil = in(3 * time.Hour) }),
				allLimitedAccount(3, func(a *Account) { a.TempUnschedulableUntil = in(4 * time.Hour) }),
				allLimitedAccount(4, func(a *Account) {
					a.Extra = map[string]any{modelRateLimitsKey: map[string]any{
						model: map[string]any{"rate_limit_reset_at": now.Add(2 * time.Hour).UTC().Format(time.RFC3339)},
					}}
				}),
			},
			want: in(2 * time.Hour),
		},
		{
			name: "one account blocked by several limits counts the latest",
			accounts: []Account{allLimitedAccount(1, func(a *Account) {
				a.RateLimitResetAt = in(time.Hour)
				a.OverloadUntil = in(3 * time.Hour)
			})},
			want: in(3 * time.Hour),
		},
		{
			name: "accounts that can never serve are not candidates",
			accounts: []Account{
				allLimitedAccount(1, rateLimited(time.Hour)),
				allLimitedAccount(2, func(a *Account) { a.Platform = PlatformOpenAI }),
				allLimitedAccount(3, func(a *Account) {
					a.Type = AccountTypeAPIKey
					a.Credentials = map[string]any{"model_mapping": map[string]any{"claude-haiku-4-5": "claude-haiku-4-5"}}
				}),
			},
			want: in(time.Hour),
		},
		{name: "no candidates", accounts: nil},
		{
			name:     "a schedulable account means the failure has another cause",
			accounts: []Account{allLimitedAccount(1, rateLimited(time.Hour)), allLimitedAccount(2, nil)},
		},
		{
			name:     "expired limit is not a block",
			accounts: []Account{allLimitedAccount(1, rateLimited(-time.Minute))},
		},
		{
			name:     "error status has no known end",
			accounts: []Account{allLimitedAccount(1, rateLimited(time.Hour)), allLimitedAccount(2, func(a *Account) { a.Status = StatusError })},
		},
		{
			name:     "disabled status has no known end",
			accounts: []Account{allLimitedAccount(1, rateLimited(time.Hour)), allLimitedAccount(2, func(a *Account) { a.Status = StatusDisabled })},
		},
		{
			name: "paused scheduling has no known end even with a reset time",
			accounts: []Account{allLimitedAccount(1, func(a *Account) {
				a.Schedulable = false
				a.RateLimitResetAt = in(time.Hour)
			})},
		},
		{
			name: "owner pause has no known end",
			accounts: []Account{allLimitedAccount(1, func(a *Account) {
				a.RateLimitResetAt = in(time.Hour)
				a.OwnerPausedAt = allLimitedTimePtr(now.Add(-time.Hour))
			})},
		},
		{
			name: "auto-paused expiry has no known end",
			accounts: []Account{allLimitedAccount(1, func(a *Account) {
				a.RateLimitResetAt = in(time.Hour)
				a.AutoPauseOnExpired = true
				a.ExpiresAt = allLimitedTimePtr(now.Add(-time.Minute))
			})},
		},
		{
			name: "account expiring before its reset has no known end",
			accounts: []Account{allLimitedAccount(1, func(a *Account) {
				a.RateLimitResetAt = in(time.Hour)
				a.AutoPauseOnExpired = true
				a.ExpiresAt = in(30 * time.Minute)
			})},
		},
		{
			name: "account expiring after its reset still counts",
			accounts: []Account{allLimitedAccount(1, func(a *Account) {
				a.RateLimitResetAt = in(time.Hour)
				a.AutoPauseOnExpired = true
				a.ExpiresAt = in(2 * time.Hour)
			})},
			want: in(time.Hour),
		},
		{
			name: "exhausted API key quota outlives the reset",
			accounts: []Account{allLimitedAccount(1, func(a *Account) {
				a.Type = AccountTypeAPIKey
				a.RateLimitResetAt = in(time.Hour)
				a.Extra = map[string]any{"quota_limit": 10.0, "quota_used": 10.0}
			})},
		},
	}

	svc := &GatewayService{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := svc.earliestTimeBoundRelease(context.Background(), tc.accounts, PlatformAnthropic, true, model, now)
			if tc.want == nil {
				require.False(t, ok, "got %s", got)
				return
			}
			require.True(t, ok)
			require.WithinDuration(t, *tc.want, got, time.Second)
		})
	}
}

type allLimitedListRepo struct {
	AccountRepository
	accounts []Account
	total    int64
	groupID  int64
}

func (r *allLimitedListRepo) ListWithFilters(_ context.Context, _ pagination.PaginationParams, _, _, _, _ string, groupID int64, _ AccountListSort) ([]Account, *pagination.PaginationResult, error) {
	r.groupID = groupID
	return r.accounts, &pagination.PaginationResult{Total: r.total}, nil
}

func TestAllAccountsRateLimitedUntilScope(t *testing.T) {
	resetAt := time.Now().Add(22 * time.Hour)
	repo := &allLimitedListRepo{accounts: []Account{allLimitedAccount(293, func(a *Account) { a.RateLimitResetAt = &resetAt })}, total: 1}
	groups := map[int64]*Group{
		55: {ID: 55, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true},
		56: {ID: 56, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true, MarketTier: true},
		57: {ID: 57, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true},
	}
	svc := &GatewayService{accountRepo: repo, groupRepo: &allLimitedGroupRepo{groups: groups}}
	id := func(v int64) *int64 { return &v }

	got, ok := svc.AllAccountsRateLimitedUntil(context.Background(), id(55), "claude-sonnet-4-5")
	require.True(t, ok)
	require.True(t, got.Equal(resetAt))
	require.Equal(t, int64(55), repo.groupID)

	_, ok = svc.AllAccountsRateLimitedUntil(context.Background(), nil, "claude-sonnet-4-5")
	require.False(t, ok, "ungrouped keys keep 503")
	_, ok = svc.AllAccountsRateLimitedUntil(context.Background(), id(56), "claude-sonnet-4-5")
	require.False(t, ok, "market tiers keep 503")
	_, ok = svc.AllAccountsRateLimitedUntil(context.Background(), id(57), "gpt-6")
	require.False(t, ok, "non-Anthropic groups keep 503")

	repo.total = 101
	_, ok = svc.AllAccountsRateLimitedUntil(context.Background(), id(55), "claude-sonnet-4-5")
	require.False(t, ok, "a group larger than one page keeps 503")

	repo.total = 2
	_, ok = svc.AllAccountsRateLimitedUntil(context.Background(), id(55), "claude-sonnet-4-5")
	require.False(t, ok, "a member missing from the page keeps 503")
}

type allLimitedGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *allLimitedGroupRepo) GetByID(_ context.Context, id int64) (*Group, error) {
	if g, ok := r.groups[id]; ok {
		copied := *g
		return &copied, nil
	}
	return nil, ErrGroupNotFound
}

func (r *allLimitedGroupRepo) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return r.GetByID(ctx, id)
}

func TestAllAccountsRateLimitedMessage(t *testing.T) {
	msg := AllAccountsRateLimitedMessage(time.Date(2026, 11, 28, 7, 0, 0, 0, time.UTC))
	require.Contains(t, msg, "No available accounts: all accounts in this group are rate limited, resets at 2026-11-28 ")
}

type allLimitedFailingUsageRepo struct{ UsageLogRepository }

func (allLimitedFailingUsageRepo) GetAccountWindowStats(context.Context, int64, time.Time) (*usagestats.AccountStats, error) {
	return nil, errors.New("TEST_ONLY usage read failure")
}

// Selection fails open on a window cost read error; the reset explanation
// must not treat that unknown as "no window cost block".
func TestEarliestTimeBoundReleaseWindowCostUnknown(t *testing.T) {
	now := time.Now()
	resetAt := now.Add(time.Hour)
	acc := allLimitedAccount(1, func(a *Account) {
		a.RateLimitResetAt = &resetAt
		a.Extra = map[string]any{"window_cost_limit": 50.0}
	})
	svc := &GatewayService{usageLogRepo: allLimitedFailingUsageRepo{}}
	_, ok := svc.earliestTimeBoundRelease(context.Background(), []Account{acc}, PlatformAnthropic, true, "claude-sonnet-4-5", now)
	require.False(t, ok)
}
