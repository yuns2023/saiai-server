//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestResolveQuotaCostWindowValidity(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }

	_, ok := resolveQuotaCostWindow("five_hour", nil, now)
	require.False(t, ok, "missing reset")
	_, ok = resolveQuotaCostWindow("five_hour", at(0), now)
	require.False(t, ok, "reset equal to now is expired")
	_, ok = resolveQuotaCostWindow("seven_day", at(-time.Second), now)
	require.False(t, ok, "expired reset")
	_, ok = resolveQuotaCostWindow("five_hour", at(5*time.Hour+time.Second), now)
	require.False(t, ok, "reset further away than the window length")
	_, ok = resolveQuotaCostWindow("seven_day_sonnet", at(time.Hour), now)
	require.False(t, ok, "model-scoped windows have no cost window")

	w, ok := resolveQuotaCostWindow("five_hour", at(5*time.Hour), now)
	require.True(t, ok, "start exactly at now is valid")
	require.Equal(t, now, w.Start)
	w, ok = resolveQuotaCostWindow("seven_day", at(48*time.Hour), now)
	require.True(t, ok)
	require.Equal(t, now.Add(48*time.Hour-7*24*time.Hour), w.Start)
	require.Equal(t, now.Add(48*time.Hour), w.Reset)

}

func TestQuotaCostWindowSampleAndEstimate(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	base, ok := resolveQuotaCostWindow("five_hour", &reset, now)
	require.True(t, ok)
	fresh := now.Add(-time.Minute)

	for name, tc := range map[string]struct {
		util      float64
		sampledAt *time.Time
	}{
		"zero":           {0, &fresh},
		"below ten":      {9.99, &fresh},
		"NaN":            {math.NaN(), &fresh},
		"infinite":       {math.Inf(1), &fresh},
		"no sample time": {50, nil},
		"future sample":  {50, ptrTime(now.Add(time.Second))},
		"before window":  {50, ptrTime(base.Start.Add(-time.Second))},
	} {
		w := base.withSample(tc.util, tc.sampledAt, now)
		require.Nil(t, w.SampledAt, name)
		require.Nil(t, w.estimate(12), name)
		q := w.query(7, now)
		require.Equal(t, q.Start, q.SampleEnd, name+": no sample range without a usable sample")
	}

	w := base.withSample(10, &fresh, now)
	require.NotNil(t, w.SampledAt, "10% is the inclusive threshold")
	q := w.query(7, now)
	require.Equal(t, AccountWindowCostQuery{AccountID: 7, Start: base.Start, SampleEnd: fresh, End: now}, q)
	require.InDelta(t, 120.0, *w.estimate(12), 1e-9)

	w = base.withSample(40, &fresh, now)
	require.InDelta(t, 25.0, *w.estimate(10), 1e-9)
	require.Nil(t, w.estimate(0), "no platform usage gives no estimate, not $0")
	require.Nil(t, w.estimate(math.NaN()))

	old := now.Add(-2 * time.Hour)
	w = base.withSample(40, &old, now)
	require.Equal(t, &old, w.SampledAt, "an old sample inside the window still backs an estimate")
	require.Equal(t, old, w.query(7, now).SampleEnd)
	require.InDelta(t, 25.0, *w.estimate(10), 1e-9)

	newReset := now.Add(5*time.Hour - 5*time.Minute)
	fresh5h, ok := resolveQuotaCostWindow("five_hour", &newReset, now)
	require.True(t, ok)
	edge := fresh5h.withSample(40, &fresh5h.Start, now)
	require.NotNil(t, edge.SampledAt, "sample exactly at window start is inside the window")
}

func TestCodexWindowStatsStartFallsBackOnlyWithoutReset(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	start, rolling, ok := codexWindowStatsStart("five_hour", &reset, now)
	require.True(t, ok)
	require.False(t, rolling)
	require.Equal(t, now.Add(-3*time.Hour), start)
	start, rolling, ok = codexWindowStatsStart("seven_day", nil, now)
	require.True(t, ok)
	require.True(t, rolling)
	require.Equal(t, now.Add(-7*24*time.Hour), start)
	for _, invalid := range []time.Time{now.Add(-time.Minute), now, now.Add(5*time.Hour + time.Second)} {
		_, _, ok = codexWindowStatsStart("five_hour", &invalid, now)
		require.False(t, ok, "an elapsed or too-far reset gives no stats rather than a rolling window")
	}
}

func TestAttachQuotaWindowEstimatesRequiresMatchingReset(t *testing.T) {
	now := time.Now()
	account := hostedCostFixture(now, 4, []int64{10})
	fiveReset := *account.SessionWindowEnd
	costs := &windowCostUsageStub{}
	svc := &AccountUsageService{usageLogRepo: costs}

	usage := &UsageInfo{FiveHour: &UsageProgress{Utilization: 50, ResetsAt: &fiveReset}}
	svc.attachQuotaWindowEstimates(context.Background(), &account, usage, now)
	require.Equal(t, fiveReset.Add(-5*time.Hour), costs.queries[0].Start)
	require.InDelta(t, 4.0, *usage.FiveHour.StandardCostUSD, 1e-9)
	require.InDelta(t, 4.0, *usage.FiveHour.EstimatedCapacityUSD, 1e-9)
	sampledAt, err := time.Parse(time.RFC3339, account.Extra["passive_usage_sampled_at"].(string))
	require.NoError(t, err)
	require.Equal(t, sampledAt, *usage.FiveHour.EstimateSampledAt)

	// An idle account keeps its estimate while the sample is inside the window.
	idle := hostedCostFixture(now, 4, []int64{10})
	idleAt := now.Add(-2 * time.Hour).UTC().Truncate(time.Second)
	idle.Extra["passive_usage_sampled_at"] = idleAt.Format(time.RFC3339)
	usage = &UsageInfo{FiveHour: &UsageProgress{Utilization: 50, ResetsAt: &fiveReset}}
	svc.attachQuotaWindowEstimates(context.Background(), &idle, usage, now)
	require.InDelta(t, 4.0, *usage.FiveHour.EstimatedCapacityUSD, 1e-9)
	require.True(t, idleAt.Equal(*usage.FiveHour.EstimateSampledAt))
	require.True(t, idleAt.Equal(costs.queries[0].SampleEnd), "platform usage is counted only up to the old sample")
	idle.Extra["passive_usage_sampled_at"] = fiveReset.Add(-5*time.Hour - time.Second).UTC().Format(time.RFC3339)
	usage = &UsageInfo{FiveHour: &UsageProgress{Utilization: 50, ResetsAt: &fiveReset}}
	svc.attachQuotaWindowEstimates(context.Background(), &idle, usage, now)
	require.Nil(t, usage.FiveHour.EstimatedCapacityUSD, "a sample from the previous window gives no estimate")
	require.Nil(t, usage.FiveHour.EstimateSampledAt)

	near := fiveReset.Add(59 * time.Second)
	usage = &UsageInfo{FiveHour: &UsageProgress{Utilization: 50, ResetsAt: &near}}
	svc.attachQuotaWindowEstimates(context.Background(), &account, usage, now)
	require.NotNil(t, usage.FiveHour.EstimatedCapacityUSD, "within the 1 minute tolerance")

	moved := fiveReset.Add(61 * time.Second)
	usage = &UsageInfo{FiveHour: &UsageProgress{Utilization: 50, ResetsAt: &moved}}
	svc.attachQuotaWindowEstimates(context.Background(), &account, usage, now)
	require.NotNil(t, usage.FiveHour.StandardCostUSD, "standard cost follows the displayed valid window")
	require.Nil(t, usage.FiveHour.EstimatedCapacityUSD, "displayed window differs from the sampled one")

	for name, reset := range map[string]*time.Time{"missing": nil, "elapsed": ptrTime(now.Add(-time.Minute)), "too far": ptrTime(now.Add(6 * time.Hour))} {
		calls := costs.calls
		usage = &UsageInfo{FiveHour: &UsageProgress{Utilization: 50, ResetsAt: reset}, SevenDay: &UsageProgress{Utilization: 5}}
		svc.attachQuotaWindowEstimates(context.Background(), &account, usage, now)
		require.Nil(t, usage.FiveHour.StandardCostUSD, name)
		require.Nil(t, usage.SevenDay.StandardCostUSD, name)
		require.Equal(t, calls, costs.calls, name+": no query without a valid window")
	}
}

type hostedAudienceUsersStub struct {
	hostedUsersStub
	audience []int64
	err      error
}

func (r *hostedAudienceUsersStub) ListGroupAudienceUserIDs(_ context.Context, _ int64, limit int) ([]int64, error) {
	if len(r.audience) > limit {
		return r.audience[:limit], r.err
	}
	return r.audience, r.err
}

type windowCostUsageStub struct {
	UsageLogRepository
	calls   int
	queries []AccountWindowCostQuery
	err     error
}

func (r *windowCostUsageStub) GetAccountWindowCosts(_ context.Context, queries []AccountWindowCostQuery) ([]AccountWindowCost, error) {
	r.calls++
	r.queries = queries
	if r.err != nil {
		return nil, r.err
	}
	out := make([]AccountWindowCost, len(queries))
	for i, q := range queries {
		// Used = $1 per account id unit; sampled = half of it.
		out[i] = AccountWindowCost{UsedCost: float64(q.AccountID), SampledCost: float64(q.AccountID) / 2}
	}
	return out, nil
}

func hostedCostFixture(now time.Time, id int64, groups []int64) Account {
	fiveReset := now.Add(2 * time.Hour)
	// The scheduler's recorded start (hour-truncated prediction) must not move the cost window.
	fiveStart := fiveReset.Add(-4 * time.Hour)
	return Account{ID: id, Name: "Hosted", Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		GroupIDs: groups, SessionWindowStart: &fiveStart, SessionWindowEnd: &fiveReset,
		Extra: map[string]any{
			"session_window_utilization":          0.5,
			"passive_usage_7d_utilization":        0.05,
			"passive_usage_7d_reset":              now.Add(24 * time.Hour).Unix(),
			"passive_usage_7d_sonnet_utilization": 0.5,
			"passive_usage_7d_sonnet_reset":       now.Add(24 * time.Hour).Unix(),
			"passive_usage_sampled_at":            now.Add(-time.Minute).UTC().Format(time.RFC3339),
		}}
}

func TestHostedAccountWindowCostsDisclosureGuard(t *testing.T) {
	newService := func(audience []int64, accounts []Account, costs *windowCostUsageStub) *HostedAccountService {
		users := &hostedAudienceUsersStub{hostedUsersStub: hostedUsersStub{user: &User{ID: 1, Status: StatusActive, AllowedGroups: []int64{10}}}, audience: audience}
		groups := &hostedGroupsStub{group: &Group{ID: 10, Status: StatusActive, IsExclusive: true, ShowAccountQuota: true}}
		return NewHostedAccountService(users, groups, &hostedAccountsStub{rows: accounts}, costs)
	}
	params := pagination.PaginationParams{Page: 1, PageSize: 20}
	windowByKey := func(row HostedAccount, key string) HostedQuotaWindow {
		for _, w := range row.Windows {
			if w.Key == key {
				return w
			}
		}
		t.Fatalf("window %s missing", key)
		return HostedQuotaWindow{}
	}

	t.Run("sole user sees cost and estimate with one query", func(t *testing.T) {
		now := time.Now()
		costs := &windowCostUsageStub{}
		accounts := []Account{hostedCostFixture(now, 4, []int64{10}), hostedCostFixture(now, 8, []int64{10})}
		rows, _, err := newService([]int64{1}, accounts, costs).ListAccounts(context.Background(), 1, 10, params)
		require.NoError(t, err)
		require.Equal(t, 1, costs.calls)
		require.Len(t, costs.queries, 4, "five_hour and seven_day for both accounts; never sub-windows")
		require.Equal(t, accounts[0].SessionWindowEnd.Add(-5*time.Hour), costs.queries[0].Start, "cost window is exactly reset - 5h")
		five := windowByKey(rows[0], "five_hour")
		require.InDelta(t, 4.0, *five.UsedCostUSD, 1e-9)
		require.InDelta(t, 4.0, *five.EstimatedCapacityUSD, 1e-9, "sampled $2 at 50%")
		seven := windowByKey(rows[0], "seven_day")
		require.InDelta(t, 4.0, *seven.UsedCostUSD, 1e-9)
		require.Nil(t, seven.EstimatedCapacityUSD, "utilization 5% is below the estimate threshold")
		require.Nil(t, seven.EstimateSampledAt)
		require.NotNil(t, five.EstimateSampledAt)
		require.Equal(t, accounts[0].Extra["passive_usage_sampled_at"], five.EstimateSampledAt.UTC().Format(time.RFC3339))
		sonnet := windowByKey(rows[0], "seven_day_sonnet")
		require.Nil(t, sonnet.UsedCostUSD)
		require.Nil(t, sonnet.EstimatedCapacityUSD)
		require.InDelta(t, 8.0, *windowByKey(rows[1], "five_hour").UsedCostUSD, 1e-9)
	})

	t.Run("costs hidden unless the viewer is the only user and the account has one group", func(t *testing.T) {
		now := time.Now()
		for name, tc := range map[string]struct {
			audience []int64
			account  Account
		}{
			"second allowed user":   {[]int64{1, 2}, hostedCostFixture(now, 4, []int64{10})},
			"other user only":       {[]int64{2}, hostedCostFixture(now, 4, []int64{10})},
			"no audience rows":      {nil, hostedCostFixture(now, 4, []int64{10})},
			"account in two groups": {[]int64{1}, hostedCostFixture(now, 4, []int64{10, 11})},
			"groups not loaded":     {[]int64{1}, hostedCostFixture(now, 4, nil)},
			"carpool user":          {[]int64{1}, func() Account { a := hostedCostFixture(now, 4, []int64{10}); a.CarpoolUserIDs = []int64{3}; return a }()},
			"carpool key of other user": {[]int64{1}, func() Account {
				a := hostedCostFixture(now, 4, []int64{10})
				a.CarpoolAPIKeys = []CarpoolAPIKeyRef{{ID: 30, UserID: 1}, {ID: 31, UserID: 3}}
				return a
			}()},
		} {
			costs := &windowCostUsageStub{}
			rows, _, err := newService(tc.audience, []Account{tc.account}, costs).ListAccounts(context.Background(), 1, 10, params)
			require.NoError(t, err, name)
			require.Zero(t, costs.calls, name)
			for _, w := range rows[0].Windows {
				require.Nil(t, w.UsedCostUSD, name)
				require.Nil(t, w.EstimatedCapacityUSD, name)
			}
		}
	})

	t.Run("query failure leaves costs unknown", func(t *testing.T) {
		now := time.Now()
		costs := &windowCostUsageStub{err: errors.New("db down")}
		rows, _, err := newService([]int64{1}, []Account{hostedCostFixture(now, 4, []int64{10})}, costs).ListAccounts(context.Background(), 1, 10, params)
		require.NoError(t, err)
		require.Nil(t, windowByKey(rows[0], "five_hour").UsedCostUSD)
	})

	t.Run("missing reset gives unknown cost, not zero", func(t *testing.T) {
		now := time.Now()
		a := hostedCostFixture(now, 4, []int64{10})
		delete(a.Extra, "passive_usage_7d_reset")
		costs := &windowCostUsageStub{}
		rows, _, err := newService([]int64{1}, []Account{a}, costs).ListAccounts(context.Background(), 1, 10, params)
		require.NoError(t, err)
		require.Len(t, costs.queries, 1)
		require.NotNil(t, windowByKey(rows[0], "five_hour").UsedCostUSD)
		require.Nil(t, windowByKey(rows[0], "seven_day").UsedCostUSD)
	})

	t.Run("services without readers never disclose", func(t *testing.T) {
		now := time.Now()
		users := &hostedUsersStub{user: &User{ID: 1, Status: StatusActive, AllowedGroups: []int64{10}}}
		groups := &hostedGroupsStub{group: &Group{ID: 10, Status: StatusActive, IsExclusive: true, ShowAccountQuota: true}}
		svc := NewHostedAccountService(users, groups, &hostedAccountsStub{rows: []Account{hostedCostFixture(now, 4, []int64{10})}}, &windowCostUsageStub{})
		rows, _, err := svc.ListAccounts(context.Background(), 1, 10, params)
		require.NoError(t, err)
		require.Nil(t, windowByKey(rows[0], "five_hour").UsedCostUSD)
	})
}

type syncPassiveAccountsStub struct {
	AccountRepository
	updates []map[string]any
	account *Account
}

func (r *syncPassiveAccountsStub) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.updates = append(r.updates, updates)
	return nil
}

func (r *syncPassiveAccountsStub) GetByID(context.Context, int64) (*Account, error) {
	copied := *r.account
	return &copied, nil
}

func (r *syncPassiveAccountsStub) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.account.RateLimitResetAt = &resetAt
	return nil
}

func TestPassiveUsageSampledAfter(t *testing.T) {
	cachedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	extra := func(v any) map[string]any { return map[string]any{"passive_usage_sampled_at": v} }
	require.True(t, passiveUsageSampledAfter(extra(cachedAt.Add(time.Minute).Format(time.RFC3339)), cachedAt), "a newer passive sample must not be overwritten by a cache hit")
	require.False(t, passiveUsageSampledAfter(extra(cachedAt.Add(-time.Minute).Format(time.RFC3339)), cachedAt))
	require.True(t, passiveUsageSampledAfter(extra(cachedAt.Format(time.RFC3339)), cachedAt.Add(100*time.Millisecond)),
		"a second-precision sample in the same second may be newer than the cache")
	require.False(t, passiveUsageSampledAfter(extra(cachedAt.Add(-time.Second).Format(time.RFC3339)), cachedAt.Add(100*time.Millisecond)))
	require.False(t, passiveUsageSampledAfter(extra("bad"), cachedAt))
	require.False(t, passiveUsageSampledAfter(extra(nil), cachedAt))
	require.False(t, passiveUsageSampledAfter(nil, cachedAt))
}

func TestSyncActiveToPassiveSampleTimeAndThreshold(t *testing.T) {
	repo := &syncPassiveAccountsStub{account: &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{"claude_oauth_5h_rate_limit_threshold_percent": 90.0}}}
	svc := &AccountUsageService{accountRepo: repo}
	fiveReset := time.Now().Add(time.Hour)
	weekReset := time.Now().Add(48 * time.Hour)

	written := svc.syncActiveToPassive(context.Background(), 1, &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 50, ResetsAt: &fiveReset}, SevenDay: &UsageProgress{Utilization: 20, ResetsAt: &weekReset},
	}, true)
	require.Contains(t, written, "passive_usage_sampled_at")

	written = svc.syncActiveToPassive(context.Background(), 1, &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 50, ResetsAt: &fiveReset}, SevenDay: &UsageProgress{Utilization: 20, ResetsAt: &weekReset},
	}, false)
	require.InDelta(t, 0.2, written["passive_usage_7d_utilization"], 1e-9, "cache hits still sync values")
	require.NotContains(t, written, "passive_usage_sampled_at", "cache hits must not restamp the sample time")
	require.InDelta(t, 0.5, written["session_window_utilization"], 1e-9)

	written = svc.syncActiveToPassive(context.Background(), 1, &UsageInfo{FiveHour: &UsageProgress{Utilization: 100, ResetsAt: &fiveReset}}, true)
	require.InDelta(t, 1.0, written["session_window_utilization"], 1e-9, "5h-only results still update the 5h value")
	require.NotContains(t, written, "passive_usage_sampled_at", "no weekly window: the shared sample time must not advance")
	require.Len(t, repo.updates, 3)
	require.NotNil(t, repo.account.RateLimitResetAt, "5h threshold is still evaluated for a 5h-only result")
	require.WithinDuration(t, fiveReset, *repo.account.RateLimitResetAt, time.Second)
}
