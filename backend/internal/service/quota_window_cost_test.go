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

	_, ok := resolveQuotaCostWindow("five_hour", nil, nil, now)
	require.False(t, ok, "missing reset")
	_, ok = resolveQuotaCostWindow("five_hour", nil, at(0), now)
	require.False(t, ok, "reset equal to now is expired")
	_, ok = resolveQuotaCostWindow("seven_day", nil, at(-time.Second), now)
	require.False(t, ok, "expired reset")
	_, ok = resolveQuotaCostWindow("five_hour", nil, at(5*time.Hour+time.Second), now)
	require.False(t, ok, "reset further away than the window length")
	_, ok = resolveQuotaCostWindow("seven_day_sonnet", nil, at(time.Hour), now)
	require.False(t, ok, "model-scoped windows have no cost window")

	w, ok := resolveQuotaCostWindow("five_hour", nil, at(5*time.Hour), now)
	require.True(t, ok, "start exactly at now is valid")
	require.Equal(t, now, w.Start)
	w, ok = resolveQuotaCostWindow("seven_day", nil, at(48*time.Hour), now)
	require.True(t, ok)
	require.Equal(t, now.Add(48*time.Hour-7*24*time.Hour), w.Start)
	require.Equal(t, now.Add(48*time.Hour), w.Reset)

	// A consistent recorded start wins; inconsistent ones fall back to reset-length.
	w, _ = resolveQuotaCostWindow("five_hour", at(-90*time.Minute), at(3*time.Hour), now)
	require.Equal(t, now.Add(-90*time.Minute), w.Start)
	for _, recorded := range []*time.Time{at(time.Minute), at(-3 * time.Hour), at(3 * time.Hour)} {
		w, _ = resolveQuotaCostWindow("five_hour", recorded, at(3*time.Hour), now)
		require.Equal(t, now.Add(-2*time.Hour), w.Start)
	}
}

func TestQuotaCostWindowSampleAndEstimate(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	base, ok := resolveQuotaCostWindow("five_hour", nil, &reset, now)
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
		"stale":          {50, ptrTime(now.Add(-hostedQuotaFreshness))},
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

	newReset := now.Add(5*time.Hour - 5*time.Minute)
	fresh5h, ok := resolveQuotaCostWindow("five_hour", nil, &newReset, now)
	require.True(t, ok)
	edge := fresh5h.withSample(40, &fresh5h.Start, now)
	require.NotNil(t, edge.SampledAt, "sample exactly at window start is inside the window")
}

func TestCodexWindowStatsStartFallsBackOnlyWithoutReset(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	start, rolling := codexWindowStatsStart("five_hour", &reset, now)
	require.False(t, rolling)
	require.Equal(t, now.Add(-3*time.Hour), start)
	start, rolling = codexWindowStatsStart("seven_day", nil, now)
	require.True(t, rolling)
	require.Equal(t, now.Add(-7*24*time.Hour), start)
	expired := now.Add(-time.Minute)
	_, rolling = codexWindowStatsStart("five_hour", &expired, now)
	require.True(t, rolling)
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
	fiveStart := fiveReset.Add(-5 * time.Hour)
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
		five := windowByKey(rows[0], "five_hour")
		require.InDelta(t, 4.0, *five.UsedCostUSD, 1e-9)
		require.InDelta(t, 4.0, *five.EstimatedCapacityUSD, 1e-9, "sampled $2 at 50%")
		seven := windowByKey(rows[0], "seven_day")
		require.InDelta(t, 4.0, *seven.UsedCostUSD, 1e-9)
		require.Nil(t, seven.EstimatedCapacityUSD, "utilization 5% is below the estimate threshold")
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
