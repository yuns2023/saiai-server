//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func restartExtra(start, reset time.Time, source string, baseline any) map[string]any {
	entry := map[string]any{"start": float64(start.Unix()), "reset": float64(reset.Unix()), "source": source}
	if baseline != nil {
		entry["baseline_utilization"] = baseline
	}
	return map[string]any{QuotaWindowRestartsExtraKey: map[string]any{"seven_day": entry}}
}

func TestParseQuotaWindowRestarts(t *testing.T) {
	start := time.Unix(1794500000, 0)
	reset := time.Unix(1794574800, 0)

	auto := parseQuotaWindowRestarts(restartExtra(start, reset, "auto", 0.02))["seven_day"]
	require.NotNil(t, auto)
	require.True(t, start.Equal(auto.Start))
	require.True(t, reset.Equal(auto.Reset))
	require.InDelta(t, 0.02, *auto.BaselineUtilization, 1e-12)

	manual := parseQuotaWindowRestarts(restartExtra(start, reset, "manual", 0.02))["seven_day"]
	require.NotNil(t, manual)
	require.Nil(t, manual.BaselineUtilization, "manual restarts never carry a baseline")

	noBaseline := parseQuotaWindowRestarts(restartExtra(start, reset, "auto", "0.02"))["seven_day"]
	require.NotNil(t, noBaseline)
	require.Nil(t, noBaseline.BaselineUtilization, "a non-numeric baseline is unknown, not zero")

	for name, extra := range map[string]map[string]any{
		"nil extra":       nil,
		"not an object":   {QuotaWindowRestartsExtraKey: "x"},
		"window not obj":  {QuotaWindowRestartsExtraKey: map[string]any{"seven_day": 1.0}},
		"unknown source":  restartExtra(start, reset, "other", nil),
		"string start":    {QuotaWindowRestartsExtraKey: map[string]any{"seven_day": map[string]any{"start": "1794500000", "reset": float64(reset.Unix()), "source": "manual"}}},
		"missing reset":   {QuotaWindowRestartsExtraKey: map[string]any{"seven_day": map[string]any{"start": float64(start.Unix()), "source": "manual"}}},
		"null start":      {QuotaWindowRestartsExtraKey: map[string]any{"seven_day": map[string]any{"start": nil, "reset": float64(reset.Unix()), "source": "manual"}}},
		"five_hour entry": {QuotaWindowRestartsExtraKey: map[string]any{"five_hour": map[string]any{"start": float64(start.Unix()), "reset": float64(reset.Unix()), "source": "manual"}}},
	} {
		require.Nil(t, parseQuotaWindowRestarts(extra)["seven_day"], name)
		require.Nil(t, parseQuotaWindowRestarts(extra)["five_hour"], name)
	}
}

func TestQuotaCostWindowWithRestart(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	reset := now.Add(9 * time.Hour)
	base, ok := resolveQuotaCostWindow("seven_day", &reset, now)
	require.True(t, ok)
	restartAt := now.Add(-4 * 24 * time.Hour)

	w := base.withRestart(&QuotaWindowRestart{Start: restartAt, Reset: reset, Source: "manual"}, now)
	require.True(t, restartAt.Equal(w.Start))
	require.NotNil(t, w.Restart)

	w = base.withRestart(&QuotaWindowRestart{Start: restartAt, Reset: reset.Add(60 * time.Second), Source: "manual"}, now)
	require.True(t, restartAt.Equal(w.Start), "reset within the 1 minute tolerance")

	for name, r := range map[string]*QuotaWindowRestart{
		"nil":                 nil,
		"other window":        {Start: restartAt, Reset: reset.Add(61 * time.Second), Source: "manual"},
		"before window start": {Start: base.Start.Add(-time.Hour), Reset: reset, Source: "manual"},
		"at window start":     {Start: base.Start, Reset: reset, Source: "manual"},
		"in the future":       {Start: now.Add(time.Second), Reset: reset, Source: "manual"},
	} {
		w = base.withRestart(r, now)
		require.Equal(t, base.Start, w.Start, name)
		require.Nil(t, w.Restart, name)
	}

	// Samples taken before the restart cannot back an estimate.
	restarted := base.withRestart(&QuotaWindowRestart{Start: restartAt, Reset: reset, Source: "manual"}, now)
	before := restartAt.Add(-time.Second)
	require.Nil(t, restarted.withSample(50, &before, now).SampledAt)
	at := restartAt
	require.NotNil(t, restarted.withSample(50, &at, now).SampledAt, "a second-precision sample at the restart second is inside")
}

func TestQuotaCostWindowEstimateAfterRestart(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	reset := now.Add(9 * time.Hour)
	base, _ := resolveQuotaCostWindow("seven_day", &reset, now)
	restartAt := now.Add(-2 * 24 * time.Hour)
	sampled := now.Add(-time.Minute)
	baseline := 0.10

	auto := base.withRestart(&QuotaWindowRestart{Start: restartAt, Reset: reset, Source: "auto", BaselineUtilization: &baseline}, now).withSample(40, &sampled, now)
	require.InDelta(t, 100.0, *auto.estimate(30), 1e-9, "$30 since the restart for 40%-10% = 30 points")

	small := base.withRestart(&QuotaWindowRestart{Start: restartAt, Reset: reset, Source: "auto", BaselineUtilization: &baseline}, now).withSample(19.9, &sampled, now)
	require.Nil(t, small.estimate(30), "less than 10 points since the restart")

	// 38 − 0.28×100 is 9.999999999999996 in float64: still exactly 10 points.
	edgeBaseline := 0.28
	edge := base.withRestart(&QuotaWindowRestart{Start: restartAt, Reset: reset, Source: "auto", BaselineUtilization: &edgeBaseline}, now).withSample(38, &sampled, now)
	require.NotNil(t, edge.estimate(30), "exactly 10 points since the restart gives an estimate")

	manual := base.withRestart(&QuotaWindowRestart{Start: restartAt, Reset: reset, Source: "manual"}, now).withSample(40, &sampled, now)
	require.NotNil(t, manual.SampledAt)
	require.Nil(t, manual.estimate(30), "manual restarts have no baseline: no estimate")

	plain := base.withSample(40, &sampled, now)
	require.InDelta(t, 75.0, *plain.estimate(30), 1e-9, "without a restart the whole utilization is used")
}

func TestAttachQuotaWindowEstimatesUsesRestart(t *testing.T) {
	now := time.Now()
	account := hostedCostFixture(now, 4, []int64{10})
	resetUnix, ok := account.Extra["passive_usage_7d_reset"].(int64)
	require.True(t, ok)
	weekReset := time.Unix(resetUnix, 0)
	account.Extra["passive_usage_7d_utilization"] = 0.40
	restartAt := now.Add(-time.Hour).Truncate(time.Second)
	for k, v := range restartExtra(restartAt, weekReset, "auto", 0.20) {
		account.Extra[k] = v
	}
	costs := &windowCostUsageStub{}
	svc := &AccountUsageService{usageLogRepo: costs}
	usage := &UsageInfo{SevenDay: &UsageProgress{Utilization: 40, ResetsAt: &weekReset}}
	svc.attachQuotaWindowEstimates(context.Background(), &account, usage, now)
	require.Len(t, costs.queries, 1)
	require.True(t, restartAt.Equal(costs.queries[0].Start), "platform usage counted from the restart")
	require.NotNil(t, usage.SevenDay.WindowRestart)
	require.Equal(t, "auto", usage.SevenDay.WindowRestart.Source)
	// Stub: sampled cost = id/2 = $2 over 40%-20% = 20 points.
	require.InDelta(t, 10.0, *usage.SevenDay.EstimatedCapacityUSD, 1e-9)

	// A restart for an earlier window (different reset) is ignored.
	for k, v := range restartExtra(restartAt, weekReset.Add(-7*24*time.Hour), "manual", nil) {
		account.Extra[k] = v
	}
	usage = &UsageInfo{SevenDay: &UsageProgress{Utilization: 40, ResetsAt: &weekReset}}
	svc.attachQuotaWindowEstimates(context.Background(), &account, usage, now)
	require.True(t, weekReset.Add(-7*24*time.Hour).Equal(costs.queries[0].Start))
	require.Nil(t, usage.SevenDay.WindowRestart)
	require.InDelta(t, 5.0, *usage.SevenDay.EstimatedCapacityUSD, 1e-9)
}

type restartDetectorStub struct {
	syncPassiveAccountsStub
	detectCalls []WeeklyRestartObservation
	detected    *QuotaWindowRestart
	set         []*QuotaWindowRestart
	setWindows  []string
}

func (r *restartDetectorStub) UpdateExtraDetectingWeeklyRestart(_ context.Context, _ int64, updates map[string]any, obs WeeklyRestartObservation) (*QuotaWindowRestart, error) {
	r.updates = append(r.updates, updates)
	r.detectCalls = append(r.detectCalls, obs)
	return r.detected, nil
}

func (r *restartDetectorStub) SetQuotaWindowRestart(_ context.Context, _ int64, window string, restart *QuotaWindowRestart) error {
	r.setWindows = append(r.setWindows, window)
	r.set = append(r.set, restart)
	return nil
}

func TestUpdateExtraDetectingWeeklyRestartRouting(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 500_000_000, time.UTC)
	reset := now.Add(48 * time.Hour).Unix()
	anthropic := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Extra: map[string]any{}}

	repo := &restartDetectorStub{}
	require.NoError(t, updateExtraDetectingWeeklyRestart(context.Background(), repo, anthropic,
		map[string]any{"passive_usage_7d_utilization": 0.02, "passive_usage_7d_reset": reset}, now))
	require.Len(t, repo.detectCalls, 1)
	require.Equal(t, now.Truncate(time.Second), repo.detectCalls[0].ObservedAt, "restart start uses the second-precision sample time")
	require.InDelta(t, 0.02, repo.detectCalls[0].Utilization, 1e-12)

	for name, updates := range map[string]map[string]any{
		"utilization only": {"passive_usage_7d_utilization": 0.02},
		"reset only":       {"passive_usage_7d_reset": reset},
		"sonnet only":      {"passive_usage_7d_sonnet_utilization": 0.0, "passive_usage_7d_sonnet_reset": reset},
		"null utilization": {"passive_usage_7d_utilization": nil, "passive_usage_7d_reset": reset},
	} {
		repo = &restartDetectorStub{}
		require.NoError(t, updateExtraDetectingWeeklyRestart(context.Background(), repo, anthropic, updates, now), name)
		require.Empty(t, repo.detectCalls, name)
		require.Len(t, repo.updates, 1, name+": the sample is still written")
	}

	repo = &restartDetectorStub{}
	openai := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.NoError(t, updateExtraDetectingWeeklyRestart(context.Background(), repo, openai,
		map[string]any{"passive_usage_7d_utilization": 0.02, "passive_usage_7d_reset": reset}, now))
	require.Empty(t, repo.detectCalls, "only Anthropic OAuth/setup-token accounts detect")

	// Repositories without the detector keep the plain merge.
	plain := &syncPassiveAccountsStub{}
	require.NoError(t, updateExtraDetectingWeeklyRestart(context.Background(), plain, anthropic,
		map[string]any{"passive_usage_7d_utilization": 0.02, "passive_usage_7d_reset": reset}, now))
	require.Len(t, plain.updates, 1)

	// A detected restart is merged into updates (callers merge them into the account).
	start := now.Truncate(time.Second)
	baseline := 0.02
	repo = &restartDetectorStub{detected: &QuotaWindowRestart{Start: start, Reset: time.Unix(reset, 0), Source: "auto", BaselineUtilization: &baseline}}
	updates := map[string]any{"passive_usage_7d_utilization": 0.02, "passive_usage_7d_reset": reset}
	require.NoError(t, updateExtraDetectingWeeklyRestart(context.Background(), repo, anthropic, updates, now))
	merged := &Account{Extra: map[string]any{}}
	mergeAccountExtra(merged, updates)
	got := parseQuotaWindowRestarts(merged.Extra)["seven_day"]
	require.NotNil(t, got)
	require.True(t, start.Equal(got.Start))
}

func TestPassiveSamplingDetectsRestart(t *testing.T) {
	reset := time.Now().Add(48 * time.Hour).Unix()
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.02")
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(reset, 10))
	headers.Set("anthropic-ratelimit-unified-7d-status", "allowed")
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Extra: map[string]any{}}

	repo := &restartDetectorStub{}
	svc := &RateLimitService{accountRepo: repo}
	svc.UpdateSessionWindow(context.Background(), account, headers)
	require.Len(t, repo.detectCalls, 1, "success responses (incl. Ultra without 5h headers) run the detection")
	require.Equal(t, reset, repo.detectCalls[0].Reset.Unix())

	repo = &restartDetectorStub{}
	svc = &RateLimitService{accountRepo: repo}
	svc.sampleAnthropicRateLimitUsage(context.Background(), account, headers)
	require.Len(t, repo.detectCalls, 1, "429 samples run the detection too")
}

func TestSetManualQuotaWindowRestart(t *testing.T) {
	now := time.Now()
	reset := now.Add(48 * time.Hour).Truncate(time.Second)
	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Extra: map[string]any{
		"passive_usage_7d_utilization": 0.2,
		"passive_usage_7d_reset":       float64(reset.Unix()),
	}}
	newSvc := func() (*AccountUsageService, *restartDetectorStub) {
		repo := &restartDetectorStub{syncPassiveAccountsStub: syncPassiveAccountsStub{account: account}}
		return &AccountUsageService{accountRepo: repo}, repo
	}

	svc, repo := newSvc()
	info, err := svc.SetManualQuotaWindowRestart(context.Background(), 7, SetQuotaWindowRestartInput{Window: "seven_day", ExpectedReset: reset.Add(30 * time.Second)})
	require.NoError(t, err)
	require.Equal(t, "manual", info.Source)
	require.WithinDuration(t, now, info.Start, 2*time.Second)
	require.Len(t, repo.set, 1)
	require.True(t, reset.Equal(repo.set[0].Reset), "the restart is tied to the current window's reset")
	require.Nil(t, repo.set[0].BaselineUtilization)

	chosen := reset.Add(-5 * 24 * time.Hour).Add(1500 * time.Millisecond)
	svc, repo = newSvc()
	info, err = svc.SetManualQuotaWindowRestart(context.Background(), 7, SetQuotaWindowRestartInput{Window: "seven_day", ExpectedReset: reset, Start: &chosen})
	require.NoError(t, err)
	require.True(t, chosen.Truncate(time.Second).Equal(info.Start))
	require.Len(t, repo.set, 1)

	conflict := func(err error) bool {
		var app *infraerrors.ApplicationError
		return errors.As(err, &app) && app.Code == http.StatusConflict
	}
	badRequest := func(err error) bool {
		var app *infraerrors.ApplicationError
		return errors.As(err, &app) && app.Code == http.StatusBadRequest
	}

	svc, repo = newSvc()
	_, err = svc.SetManualQuotaWindowRestart(context.Background(), 7, SetQuotaWindowRestartInput{Window: "seven_day", ExpectedReset: reset.Add(-7 * 24 * time.Hour)})
	require.True(t, conflict(err), "a stale page (previous window) gets 409: %v", err)
	require.Empty(t, repo.set)

	for name, start := range map[string]time.Time{
		"before window start": reset.Add(-7*24*time.Hour - time.Second),
		"at window start":     reset.Add(-7 * 24 * time.Hour),
		"future":              now.Add(time.Minute),
	} {
		s := start
		svc, repo = newSvc()
		_, err = svc.SetManualQuotaWindowRestart(context.Background(), 7, SetQuotaWindowRestartInput{Window: "seven_day", ExpectedReset: reset, Start: &s})
		require.True(t, badRequest(err), name+": %v", err)
		require.Empty(t, repo.set, name)
	}

	svc, _ = newSvc()
	_, err = svc.SetManualQuotaWindowRestart(context.Background(), 7, SetQuotaWindowRestartInput{Window: "five_hour", ExpectedReset: reset})
	require.True(t, badRequest(err))

	expired := &Account{ID: 8, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{
		"passive_usage_7d_utilization": 0.2, "passive_usage_7d_reset": float64(now.Add(-time.Hour).Unix()),
	}}
	repo = &restartDetectorStub{syncPassiveAccountsStub: syncPassiveAccountsStub{account: expired}}
	svc = &AccountUsageService{accountRepo: repo}
	_, err = svc.SetManualQuotaWindowRestart(context.Background(), 8, SetQuotaWindowRestartInput{Window: "seven_day", ExpectedReset: now.Add(-time.Hour)})
	require.True(t, conflict(err), "the window the admin saw has elapsed: same 409 as a moved window")

	svc, repo = newSvc()
	require.NoError(t, svc.ClearQuotaWindowRestart(context.Background(), 7, " seven_day ", reset))
	require.Equal(t, []string{"seven_day"}, repo.setWindows, "the trimmed window is removed")
	require.Nil(t, repo.set[0])
	require.True(t, badRequest(svc.ClearQuotaWindowRestart(context.Background(), 7, "five_hour", reset)))
	svc, repo = newSvc()
	require.True(t, conflict(svc.ClearQuotaWindowRestart(context.Background(), 7, "seven_day", reset.Add(-7*24*time.Hour))),
		"a stale dialog cannot remove the next window's restart")
	require.Empty(t, repo.set)
}

func TestSyncActiveToPassiveDetectsOnlyFreshResults(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{}}
	repo := &restartDetectorStub{syncPassiveAccountsStub: syncPassiveAccountsStub{account: account}}
	svc := &AccountUsageService{accountRepo: repo}
	weekReset := time.Now().Add(48 * time.Hour)
	usage := &UsageInfo{SevenDay: &UsageProgress{Utilization: 2, ResetsAt: &weekReset}}

	svc.syncActiveToPassive(context.Background(), account, usage, false)
	require.Empty(t, repo.detectCalls, "a cached (up to 3 minutes old) result is not a new observation")
	require.Len(t, repo.updates, 1)

	svc.syncActiveToPassive(context.Background(), account, usage, true)
	require.Len(t, repo.detectCalls, 1)
	require.InDelta(t, 0.02, repo.detectCalls[0].Utilization, 1e-12, "active percent is stored as a ratio")
}

type windowStatsRecorder struct {
	windowCostUsageStub
	statsStarts []time.Time
}

func (r *windowStatsRecorder) GetAccountWindowStats(_ context.Context, _ int64, start time.Time) (*usagestats.AccountStats, error) {
	r.statsStarts = append(r.statsStarts, start)
	return &usagestats.AccountStats{Requests: 1, Cost: 2}, nil
}

func TestAddWindowStatsSevenDayStartsAtRestart(t *testing.T) {
	now := time.Now()
	weekReset := now.Add(48 * time.Hour).Truncate(time.Second)
	restartAt := now.Add(-time.Hour).Truncate(time.Second)
	account := &Account{ID: 5, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Extra: restartExtra(restartAt, weekReset, "manual", nil)}
	repo := &windowStatsRecorder{}
	svc := &AccountUsageService{usageLogRepo: repo, cache: NewUsageCache()}
	usage := &UsageInfo{SevenDay: &UsageProgress{Utilization: 20, ResetsAt: &weekReset}}
	svc.addWindowStats(context.Background(), account, usage)
	require.NotEmpty(t, repo.statsStarts)
	require.True(t, restartAt.Equal(repo.statsStarts[len(repo.statsStarts)-1]), "7d A/U stats start at the restart")
	require.NotNil(t, usage.SevenDay.WindowStats)
	require.NotNil(t, usage.SevenDay.WindowRestart)
	require.Equal(t, "manual", usage.SevenDay.WindowRestart.Source)
}

func TestCodexWindowStatsStartUsesRestart(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	reset := now.Add(24 * time.Hour)
	restartAt := now.Add(-time.Hour)
	start, rolling, restart, ok := codexWindowStatsStart("seven_day", &reset, &QuotaWindowRestart{Start: restartAt, Reset: reset, Source: "manual"}, now)
	require.True(t, ok)
	require.False(t, rolling)
	require.Equal(t, restartAt, start)
	require.NotNil(t, restart)
	// A rolling fallback (reset unknown) never applies a restart.
	start, rolling, restart, ok = codexWindowStatsStart("seven_day", nil, &QuotaWindowRestart{Start: restartAt, Reset: reset, Source: "manual"}, now)
	require.True(t, ok)
	require.True(t, rolling)
	require.Nil(t, restart)
	require.Equal(t, now.Add(-7*24*time.Hour), start)
}
