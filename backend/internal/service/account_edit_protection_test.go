//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The edit page submits {...props.account.credentials}: tokens from page
// load. A refresh that rotated them meanwhile must survive the save.
func TestApplyUpdateAccountInput_EditorKeepsCurrentOAuthTokens(t *testing.T) {
	current := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{
			"access_token": "new-access", "refresh_token": "rotated-refresh", "expires_at": "2026-11-13T00:00:00Z",
			"_token_version": int64(2), "model_mapping": map[string]any{"a": "b"},
		}}
	submitted := map[string]any{ // page-load snapshot + one config change
		"access_token": "old-access", "refresh_token": "old-refresh", "expires_at": "2026-11-12T00:00:00Z",
		"_token_version": int64(1), "id_token": "stale-id-token", // absent now: must stay absent
		"model_mapping":             map[string]any{"a": "c"},
		"intercept_warmup_requests": true,
	}

	require.NoError(t, applyUpdateAccountInput(current, &UpdateAccountInput{Credentials: submitted, EditorSave: true}, nil, time.Now()))

	require.Equal(t, "new-access", current.Credentials["access_token"])
	require.Equal(t, "rotated-refresh", current.Credentials["refresh_token"])
	require.Equal(t, "2026-11-13T00:00:00Z", current.Credentials["expires_at"])
	require.Equal(t, int64(2), current.Credentials["_token_version"])
	_, hasIDToken := current.Credentials["id_token"]
	require.False(t, hasIDToken, "a token key absent now is not revived from the page")
	require.Equal(t, map[string]any{"a": "c"}, current.Credentials["model_mapping"], "config keys follow the page")
	require.Equal(t, true, current.Credentials["intercept_warmup_requests"])
	require.Equal(t, "old-refresh", submitted["refresh_token"], "the input map is not modified")
}

// Refresh / re-auth callers intend to write tokens.
func TestApplyUpdateAccountInput_NonEditorWritesTokens(t *testing.T) {
	current := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "a1", "refresh_token": "r1"}}
	require.NoError(t, applyUpdateAccountInput(current, &UpdateAccountInput{
		Credentials: map[string]any{"access_token": "a2", "refresh_token": "r2"}}, nil, time.Now()))
	require.Equal(t, "r2", current.Credentials["refresh_token"])
}

// API-key style accounts edit their secrets on the page; those are not
// protected.
func TestApplyUpdateAccountInput_EditorWritesAPIKey(t *testing.T) {
	current := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{"api_key": "sk-old", "base_url": "https://a"}}
	require.NoError(t, applyUpdateAccountInput(current, &UpdateAccountInput{
		Credentials: map[string]any{"api_key": "sk-new", "base_url": "https://a"}, EditorSave: true}, nil, time.Now()))
	require.Equal(t, "sk-new", current.Credentials["api_key"])
}

func TestApplyUpdateAccountInput_EditorKeepsCurrentRuntimeExtra(t *testing.T) {
	rateLimits := map[string]any{"claude-x": map[string]any{"reset_at": "later"}}
	current := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive,
		Credentials: map[string]any{"access_token": "t"},
		Extra: map[string]any{
			"passive_usage_7d_utilization": 80.0, "passive_usage_sampled_at": "now",
			"session_window_utilization": 0.5, "codex_5h_used_percent": 40.0,
			"quota_used": 12.5, modelRateLimitsKey: rateLimits,
			"window_cost_limit": 100.0,
		}}
	submitted := map[string]any{ // page snapshot from hours ago + config edits
		"passive_usage_7d_utilization": 10.0, "passive_usage_7d_reset": "stale", // absent now
		"session_window_utilization": 0.1, "codex_5h_used_percent": 5.0,
		"quota_used": 1.0, modelRateLimitsKey: nil, // null must not clear it
		"window_cost_limit": 200.0, "codex_cli_only": true, // config (codex_ prefix but not runtime)
	}

	require.NoError(t, applyUpdateAccountInput(current, &UpdateAccountInput{Extra: submitted, EditorSave: true}, nil, time.Now()))

	require.Equal(t, 80.0, current.Extra["passive_usage_7d_utilization"])
	require.Equal(t, "now", current.Extra["passive_usage_sampled_at"])
	_, revived := current.Extra["passive_usage_7d_reset"]
	require.False(t, revived)
	require.Equal(t, 0.5, current.Extra["session_window_utilization"])
	require.Equal(t, 40.0, current.Extra["codex_5h_used_percent"])
	require.Equal(t, 12.5, current.Extra["quota_used"])
	require.Equal(t, rateLimits, current.Extra[modelRateLimitsKey])
	require.Equal(t, 200.0, current.Extra["window_cost_limit"])
	require.Equal(t, true, current.Extra["codex_cli_only"])
}

func TestApplyUpdateAccountInput_EditorQuotaResetAtPerPart(t *testing.T) {
	dailyReset := "2030-01-01T00:00:00Z" // stored value a recompute could not produce
	weeklyReset := "2026-11-16T00:00:00Z"
	cfg := func(weeklyHour int) map[string]any {
		return map[string]any{
			"quota_daily_limit": 10.0, "quota_daily_reset_mode": "fixed", "quota_daily_reset_hour": 0.0,
			"quota_weekly_limit": 50.0, "quota_weekly_reset_mode": "fixed", "quota_weekly_reset_day": 1.0,
			"quota_weekly_reset_hour": float64(weeklyHour), "quota_reset_timezone": "UTC",
		}
	}
	currentExtra := cfg(0)
	currentExtra["quota_daily_reset_at"] = dailyReset
	currentExtra["quota_weekly_reset_at"] = weeklyReset
	current := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{"api_key": "k"}, Extra: currentExtra}

	submitted := cfg(6) // only the weekly hour changes
	submitted["quota_daily_reset_at"] = "2026-11-12T00:00:00Z"
	submitted["quota_weekly_reset_at"] = "2026-11-09T00:00:00Z"
	require.NoError(t, applyUpdateAccountInput(current, &UpdateAccountInput{Extra: submitted, EditorSave: true}, nil, time.Now()))

	require.Equal(t, dailyReset, current.Extra["quota_daily_reset_at"], "unchanged daily config keeps the stored reset")
	weekly, _ := current.Extra["quota_weekly_reset_at"].(string)
	parsed, err := time.Parse(time.RFC3339, weekly)
	require.NoError(t, err)
	require.Equal(t, 6, parsed.Hour(), "changed weekly config is recomputed")
}

func TestApplyUpdateAccountInput_EditorQuotaDisableKeepsCounters(t *testing.T) {
	current := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{"api_key": "k"},
		Extra: map[string]any{"quota_daily_limit": 10.0, "quota_daily_reset_mode": "fixed",
			"quota_daily_reset_at": "2026-11-13T00:00:00Z", "quota_daily_used": 3.0}}
	require.NoError(t, applyUpdateAccountInput(current, &UpdateAccountInput{Extra: map[string]any{}, EditorSave: true}, nil, time.Now()))
	_, hasLimit := current.Extra["quota_daily_limit"]
	_, hasReset := current.Extra["quota_daily_reset_at"]
	require.False(t, hasLimit)
	require.False(t, hasReset, "turning the fixed reset off removes its reset time")
	require.Equal(t, 3.0, current.Extra["quota_daily_used"], "usage counters are kept")
}

// The carpool worker advances next_maintenance_at; an unchanged carpool
// config must keep the advanced value, not the page's.
func TestApplyUpdateAccountInput_EditorKeepsCarpoolSchedule(t *testing.T) {
	carpool := func() map[string]any {
		return map[string]any{
			"claude_oauth_mode": ClaudeOAuthModeCarpool, "claude_oauth_carpool_auto_expand_enabled": true,
			"claude_oauth_carpool_maintenance_interval_hours": 24.0,
		}
	}
	currentExtra := carpool()
	currentExtra[CarpoolNextMaintenanceKey] = "2026-11-14T00:00:00Z"
	currentExtra[CarpoolInitialMaintenanceKey] = "2026-11-01T00:00:00Z"
	currentExtra[CarpoolMaintenanceGenerationKey] = "gen-2"
	current := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive,
		Credentials: map[string]any{"access_token": "t"}, Extra: currentExtra}

	submitted := carpool()
	submitted[CarpoolNextMaintenanceKey] = "2026-11-12T00:00:00Z"
	submitted[CarpoolMaintenanceGenerationKey] = "gen-1"
	require.NoError(t, applyUpdateAccountInput(current, &UpdateAccountInput{Extra: submitted, EditorSave: true}, nil, time.Now()))
	require.Equal(t, "2026-11-14T00:00:00Z", current.Extra[CarpoolNextMaintenanceKey])
	require.Equal(t, "gen-2", current.Extra[CarpoolMaintenanceGenerationKey])
}

// staleThenLockedRepo returns an old row to UpdateAccount's first read and
// the current row inside UpdateLocked, as when a refresh and usage samples
// land between the two.
type staleThenLockedRepo struct {
	mockAccountRepoForGemini
	stale, current, saved *Account
	reads                 int
}

func (r *staleThenLockedRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	r.reads++
	if r.saved != nil {
		return r.saved, nil
	}
	clone := *r.stale
	clone.Credentials = cloneExtraMap(r.stale.Credentials)
	clone.Extra = cloneExtraMap(r.stale.Extra)
	return &clone, nil
}

func (r *staleThenLockedRepo) UpdateLocked(_ context.Context, _ int64, apply func(*Account) error) (*Account, error) {
	locked := *r.current
	locked.Credentials = cloneExtraMap(r.current.Credentials)
	locked.Extra = cloneExtraMap(r.current.Extra)
	if err := apply(&locked); err != nil {
		return nil, err
	}
	r.saved = &locked
	return &locked, nil
}

// UpdateAccount must apply the edit to the row read under the lock, not to
// its own earlier read.
func TestAdminUpdateAccount_AppliesEditToLockedRow(t *testing.T) {
	stale := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Name: "old",
		Credentials: map[string]any{"access_token": "a1", "refresh_token": "r1"},
		Extra:       map[string]any{"codex_5h_used_percent": 5.0}}
	current := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Name: "old",
		Credentials: map[string]any{"access_token": "a2", "refresh_token": "r2"},
		Extra:       map[string]any{"codex_5h_used_percent": 60.0}}
	repo := &staleThenLockedRepo{stale: stale, current: current}
	svc := &adminServiceImpl{accountRepo: repo}

	// The page loaded the stale row and submits it back with a new name.
	_, err := svc.UpdateAccount(context.Background(), 9, &UpdateAccountInput{
		Name:        "renamed",
		Credentials: map[string]any{"access_token": "a1", "refresh_token": "r1", "intercept_warmup_requests": true},
		Extra:       map[string]any{"codex_5h_used_percent": 5.0, "codex_cli_only": true},
		EditorSave:  true,
	})
	require.NoError(t, err)
	require.NotNil(t, repo.saved)
	require.Equal(t, "renamed", repo.saved.Name)
	require.Equal(t, "r2", repo.saved.Credentials["refresh_token"], "rotated token survives the save")
	require.Equal(t, "a2", repo.saved.Credentials["access_token"])
	require.Equal(t, true, repo.saved.Credentials["intercept_warmup_requests"])
	require.Equal(t, 60.0, repo.saved.Extra["codex_5h_used_percent"], "usage sample from the current row")
	require.Equal(t, true, repo.saved.Extra["codex_cli_only"])
}

// Protection follows the stored type: changing Type in the same save does
// not let the page's stale tokens through.
func TestApplyUpdateAccountInput_EditorProtectionUsesStoredType(t *testing.T) {
	current := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "current", "refresh_token": "current-r"}}
	require.NoError(t, applyUpdateAccountInput(current, &UpdateAccountInput{
		Type:        AccountTypeSetupToken,
		Credentials: map[string]any{"access_token": "stale", "refresh_token": "stale-r"},
		EditorSave:  true,
	}, nil, time.Now()))
	require.Equal(t, AccountTypeSetupToken, current.Type)
	require.Equal(t, "current-r", current.Credentials["refresh_token"])
}

func TestApplyUpdateAccountInput_EditorKeepsQuotaWindowRestarts(t *testing.T) {
	restarts := map[string]any{"seven_day": map[string]any{"start": 1794500000.0, "reset": 1794574800.0, "source": "manual"}}
	current := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive,
		Credentials: map[string]any{"access_token": "t"},
		Extra:       map[string]any{QuotaWindowRestartsExtraKey: restarts}}
	// The page was loaded before the restart was recorded: its snapshot lacks the key.
	require.NoError(t, applyUpdateAccountInput(current, &UpdateAccountInput{Extra: map[string]any{"window_cost_limit": 1.0}, EditorSave: true}, nil, time.Now()))
	require.Equal(t, restarts, current.Extra[QuotaWindowRestartsExtraKey])

	// A stale snapshot carrying an old restart must not bring it back after removal.
	current.Extra = map[string]any{}
	require.NoError(t, applyUpdateAccountInput(current, &UpdateAccountInput{Extra: map[string]any{QuotaWindowRestartsExtraKey: restarts}, EditorSave: true}, nil, time.Now()))
	require.NotContains(t, current.Extra, QuotaWindowRestartsExtraKey)
}
