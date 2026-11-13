//go:build integration

package repository

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) newRestartAccount(name string, extra map[string]any) *service.Account {
	return mustCreateAccount(s.T(), s.client, &service.Account{
		Name:     name,
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeSetupToken,
		Status:   service.StatusActive,
		Extra:    extra,
	})
}

func (s *AccountRepoSuite) sevenDayRestart(id int64) map[string]any {
	got, err := s.repo.GetByID(s.ctx, id)
	s.Require().NoError(err)
	restarts, _ := got.Extra[service.QuotaWindowRestartsExtraKey].(map[string]any)
	entry, _ := restarts["seven_day"].(map[string]any)
	return entry
}

func (s *AccountRepoSuite) TestUpdateExtraDetectingWeeklyRestart() {
	reset := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	observedAt := time.Now().Truncate(time.Second)
	sample := func(util float64, r time.Time) (map[string]any, service.WeeklyRestartObservation) {
		return map[string]any{
				"passive_usage_7d_utilization": util,
				"passive_usage_7d_reset":       r.Unix(),
				"passive_usage_sampled_at":     observedAt.UTC().Format(time.RFC3339),
			},
			service.WeeklyRestartObservation{Utilization: util, Reset: r, ObservedAt: observedAt}
	}
	stored := func(util any, r any) map[string]any {
		return map[string]any{"passive_usage_7d_utilization": util, "passive_usage_7d_reset": r, "unrelated": "keep"}
	}

	cases := []struct {
		name      string
		extra     map[string]any
		util      float64
		reset     time.Time
		restarted bool
	}{
		{"real reset 0.50 -> 0.02", stored(0.50, reset.Unix()), 0.02, reset, true},
		{"exact 5 point drop 0.30 -> 0.25", stored(0.30, reset.Unix()), 0.25, reset, true},
		{"drop below 5 points 0.30 -> 0.26", stored(0.30, reset.Unix()), 0.26, reset, false},
		{"late older response 0.35 -> 0.33", stored(0.35, reset.Unix()), 0.33, reset, false},
		{"increase", stored(0.20, reset.Unix()), 0.35, reset, false},
		{"reset within 60s", stored(0.50, reset.Add(-60*time.Second).Unix()), 0.02, reset, true},
		{"new window (reset moved 61s)", stored(0.50, reset.Add(-61*time.Second).Unix()), 0.02, reset, false},
		{"new window (reset moved a week)", stored(0.90, reset.Add(-7*24*time.Hour).Unix()), 0.0, reset, false},
		{"no stored sample", map[string]any{"unrelated": "keep"}, 0.02, reset, false},
		{"stored utilization is a string", stored("0.50", reset.Unix()), 0.02, reset, false},
		{"stored reset is null", stored(0.50, nil), 0.02, reset, false},
		{"nil extra", nil, 0.02, reset, false},
		{"stored utilization is a non-numeric string", stored("bad", reset.Unix()), 0.02, reset, false},
		{"stored utilization is an object", stored(map[string]any{"x": 1}, reset.Unix()), 0.02, reset, false},
		{"stored reset is an array", stored(0.50, []any{1, 2}), 0.02, reset, false},
		{"stored reset is a non-numeric string", stored(0.50, "soon"), 0.02, reset, false},
		{"stored utilization is tiny", stored(json.Number("1e-400"), reset.Unix()), 0.02, reset, false},
		{"stored utilization is huge", stored(json.Number("1e400"), reset.Unix()), 0.02, reset, true},
	}
	for _, tc := range cases {
		account := s.newRestartAccount("acc-restart-"+tc.name, tc.extra)
		updates, obs := sample(tc.util, tc.reset)
		restart, err := s.repo.UpdateExtraDetectingWeeklyRestart(s.ctx, account.ID, updates, obs)
		s.Require().NoError(err, tc.name)
		s.Require().Equal(tc.restarted, restart != nil, tc.name)

		got, err := s.repo.GetByID(s.ctx, account.ID)
		s.Require().NoError(err, tc.name)
		s.Require().InDelta(tc.util, got.Extra["passive_usage_7d_utilization"], 1e-12, tc.name+": the sample is always written")
		if tc.extra != nil {
			s.Require().Equal("keep", got.Extra["unrelated"], tc.name)
		}
		entry := s.sevenDayRestart(account.ID)
		if !tc.restarted {
			s.Require().Nil(entry, tc.name)
			continue
		}
		s.Require().Equal(float64(observedAt.Unix()), entry["start"], tc.name)
		s.Require().Equal(float64(reset.Unix()), entry["reset"], tc.name)
		s.Require().Equal("auto", entry["source"], tc.name)
		s.Require().InDelta(tc.util, entry["baseline_utilization"], 1e-12, tc.name)
	}
}

func (s *AccountRepoSuite) TestUpdateExtraDetectingWeeklyRestart_ManualRestartWins() {
	reset := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	manualStart := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	manual := map[string]any{"start": manualStart.Unix(), "reset": reset.Unix(), "source": "manual"}
	account := s.newRestartAccount("acc-restart-manual", map[string]any{
		"passive_usage_7d_utilization":      0.60,
		"passive_usage_7d_reset":            reset.Unix(),
		service.QuotaWindowRestartsExtraKey: map[string]any{"seven_day": manual, "other": "keep"},
	})
	now := time.Now().Truncate(time.Second)
	restart, err := s.repo.UpdateExtraDetectingWeeklyRestart(s.ctx, account.ID,
		map[string]any{"passive_usage_7d_utilization": 0.01, "passive_usage_7d_reset": reset.Unix()},
		service.WeeklyRestartObservation{Utilization: 0.01, Reset: reset, ObservedAt: now})
	s.Require().NoError(err)
	s.Require().Nil(restart, "a manual restart for the same window is never replaced")
	entry := s.sevenDayRestart(account.ID)
	s.Require().Equal("manual", entry["source"])
	s.Require().Equal(float64(manualStart.Unix()), entry["start"])

	// A malformed manual entry does not break the sample write.
	odd := s.newRestartAccount("acc-restart-odd-manual", map[string]any{
		"passive_usage_7d_utilization":      0.60,
		"passive_usage_7d_reset":            reset.Unix(),
		service.QuotaWindowRestartsExtraKey: map[string]any{"seven_day": map[string]any{"source": "manual", "reset": "soon"}},
	})
	restart, err = s.repo.UpdateExtraDetectingWeeklyRestart(s.ctx, odd.ID,
		map[string]any{"passive_usage_7d_utilization": 0.01, "passive_usage_7d_reset": reset.Unix()},
		service.WeeklyRestartObservation{Utilization: 0.01, Reset: reset, ObservedAt: now})
	s.Require().NoError(err)
	s.Require().NotNil(restart, "an unreadable manual entry does not block detection")

	// A manual restart of an earlier window does not block detection; other
	// window keys survive.
	old := s.newRestartAccount("acc-restart-old-manual", map[string]any{
		"passive_usage_7d_utilization": 0.60,
		"passive_usage_7d_reset":       reset.Unix(),
		service.QuotaWindowRestartsExtraKey: map[string]any{
			"seven_day": map[string]any{"start": manualStart.Add(-7 * 24 * time.Hour).Unix(), "reset": reset.Add(-7 * 24 * time.Hour).Unix(), "source": "manual"},
			"other":     "keep",
		},
	})
	restart, err = s.repo.UpdateExtraDetectingWeeklyRestart(s.ctx, old.ID,
		map[string]any{"passive_usage_7d_utilization": 0.01, "passive_usage_7d_reset": reset.Unix()},
		service.WeeklyRestartObservation{Utilization: 0.01, Reset: reset, ObservedAt: now})
	s.Require().NoError(err)
	s.Require().NotNil(restart)
	got, err := s.repo.GetByID(s.ctx, old.ID)
	s.Require().NoError(err)
	restarts, ok := got.Extra[service.QuotaWindowRestartsExtraKey].(map[string]any)
	s.Require().True(ok)
	s.Require().Equal("keep", restarts["other"])
	s.Require().Equal("auto", s.sevenDayRestart(old.ID)["source"])

	// A second detection right after the first sees the low stored value: no new restart.
	restart, err = s.repo.UpdateExtraDetectingWeeklyRestart(s.ctx, old.ID,
		map[string]any{"passive_usage_7d_utilization": 0.01, "passive_usage_7d_reset": reset.Unix()},
		service.WeeklyRestartObservation{Utilization: 0.01, Reset: reset, ObservedAt: now.Add(time.Minute)})
	s.Require().NoError(err)
	s.Require().Nil(restart)
	s.Require().Equal(float64(now.Unix()), s.sevenDayRestart(old.ID)["start"], "the first detection's start is kept")
}

func (s *AccountRepoSuite) TestUpdateExtraDetectingWeeklyRestart_MissingAccount() {
	_, err := s.repo.UpdateExtraDetectingWeeklyRestart(s.ctx, 987654321,
		map[string]any{"passive_usage_7d_utilization": 0.01, "passive_usage_7d_reset": time.Now().Add(time.Hour).Unix()},
		service.WeeklyRestartObservation{Utilization: 0.01, Reset: time.Now().Add(time.Hour), ObservedAt: time.Now()})
	s.Require().True(errors.Is(err, service.ErrAccountNotFound), "%v", err)
}

func (s *AccountRepoSuite) TestSetQuotaWindowRestart() {
	reset := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	account := s.newRestartAccount("acc-restart-set", map[string]any{"passive_usage_7d_utilization": 0.4, "keep": "x"})

	s.Require().NoError(s.repo.SetQuotaWindowRestart(s.ctx, account.ID, "seven_day",
		&service.QuotaWindowRestart{Start: start, Reset: reset, Source: service.QuotaWindowRestartSourceManual}))
	entry := s.sevenDayRestart(account.ID)
	s.Require().Equal(float64(start.Unix()), entry["start"])
	s.Require().Equal(float64(reset.Unix()), entry["reset"])
	s.Require().Equal("manual", entry["source"])
	s.Require().NotContains(entry, "baseline_utilization")

	s.Require().NoError(s.repo.SetQuotaWindowRestart(s.ctx, account.ID, "seven_day", nil))
	s.Require().Nil(s.sevenDayRestart(account.ID))
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("x", got.Extra["keep"])
	s.Require().InDelta(0.4, got.Extra["passive_usage_7d_utilization"], 1e-12)

	// Removing when nothing (or a malformed value) is stored is a no-op.
	s.Require().NoError(s.repo.SetQuotaWindowRestart(s.ctx, account.ID, "seven_day", nil))
	odd := s.newRestartAccount("acc-restart-odd", map[string]any{service.QuotaWindowRestartsExtraKey: "bad"})
	s.Require().NoError(s.repo.SetQuotaWindowRestart(s.ctx, odd.ID, "seven_day", nil))
	s.Require().NoError(s.repo.SetQuotaWindowRestart(s.ctx, odd.ID, "seven_day",
		&service.QuotaWindowRestart{Start: start, Reset: reset, Source: service.QuotaWindowRestartSourceManual}))
	s.Require().Equal("manual", s.sevenDayRestart(odd.ID)["source"])
	nilExtra := s.newRestartAccount("acc-restart-nil", nil)
	s.Require().NoError(s.repo.SetQuotaWindowRestart(s.ctx, nilExtra.ID, "seven_day", nil))

	s.Require().True(errors.Is(s.repo.SetQuotaWindowRestart(s.ctx, 987654321, "seven_day", nil), service.ErrAccountNotFound))
}

func (s *AccountRepoSuite) TestClearQuotaSnapshot_KeepsQuotaWindowRestarts() {
	reset := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	account := s.newRestartAccount("acc-restart-clear", map[string]any{
		"passive_usage_7d_utilization":      0.4,
		"passive_usage_7d_reset":            reset.Unix(),
		service.QuotaWindowRestartsExtraKey: map[string]any{"seven_day": map[string]any{"start": time.Now().Add(-time.Hour).Unix(), "reset": reset.Unix(), "source": "manual"}},
	})
	s.Require().NoError(s.repo.ClearQuotaSnapshot(s.ctx, account.ID))
	got, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NotContains(got.Extra, "passive_usage_7d_utilization")
	s.Require().NotNil(s.sevenDayRestart(account.ID), "clearing the snapshot must not bring back pre-restart usage")
}
