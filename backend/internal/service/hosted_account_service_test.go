//go:build unit

package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

// Unimplemented embedded methods panic: these tests also catch accidental writes
// and any attempt to invoke an account query before authorization succeeds.
type hostedUsersStub struct {
	UserRepository
	user *User
}

func (r *hostedUsersStub) GetByID(context.Context, int64) (*User, error) { return r.user, nil }

type hostedGroupsStub struct {
	GroupRepository
	group  *Group
	groups []Group
}

func (r *hostedGroupsStub) GetByIDLite(context.Context, int64) (*Group, error) { return r.group, nil }
func (r *hostedGroupsStub) ListActive(context.Context) ([]Group, error)        { return r.groups, nil }

type hostedAccountsStub struct {
	AccountRepository
	rows    []Account
	groupID int64
	params  pagination.PaginationParams
}

func (r *hostedAccountsStub) ListWithFilters(_ context.Context, params pagination.PaginationParams, platform, kind, status, search string, groupID int64) ([]Account, *pagination.PaginationResult, error) {
	r.groupID = groupID
	r.params = params
	return r.rows, &pagination.PaginationResult{Total: int64(len(r.rows)), Page: params.Page, PageSize: params.PageSize, Pages: 1}, nil
}

func TestHostedAccountPermissionsAndRevocation(t *testing.T) {
	user := &User{ID: 1, Status: StatusActive, AllowedGroups: []int64{10}}
	group := &Group{ID: 10, Status: StatusActive, IsExclusive: true, ShowAccountQuota: true}
	u := &hostedUsersStub{user: user}
	g := &hostedGroupsStub{group: group}
	a := &hostedAccountsStub{rows: []Account{{ID: 2, Name: "Hosted", Status: StatusActive, Schedulable: true}}}
	svc := NewHostedAccountService(u, g, a)
	params := pagination.PaginationParams{Page: 2, PageSize: 20}
	rows, page, err := svc.ListAccounts(context.Background(), 1, 10, params)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(10), a.groupID)
	require.Equal(t, params, a.params)
	require.Equal(t, 2, page.Page)
	for _, change := range []struct {
		name  string
		apply func()
	}{
		{"grant removed", func() { user.AllowedGroups = nil }},
		{"different user", func() { user.AllowedGroups = []int64{11} }},
		{"visibility disabled", func() { group.ShowAccountQuota = false }},
		{"public group", func() { group.IsExclusive = false }},
		{"inactive group", func() { group.Status = "inactive" }},
		{"inactive user", func() { user.Status = "inactive" }},
		{"required level not hydrated", func() { id := int64(1); group.RequiredLevelID = &id }},
		{"insufficient level", func() { group.RequiredLevel = &AccessLevel{Rank: 5} }},
	} {
		t.Run(change.name, func(t *testing.T) {
			*user = User{ID: 1, Status: StatusActive, AllowedGroups: []int64{10}}
			*group = Group{ID: 10, Status: StatusActive, IsExclusive: true, ShowAccountQuota: true}
			change.apply()
			a.groupID = 0
			_, _, err := svc.ListAccounts(context.Background(), 1, 10, params)
			require.ErrorIs(t, err, ErrGroupNotFound)
			require.Zero(t, a.groupID)
		})
	}
}

func TestHostedAccountGroupListFiltersVisibility(t *testing.T) {
	user := &User{Status: StatusActive, AllowedGroups: []int64{1, 2, 3, 4}}
	groups := &hostedGroupsStub{groups: []Group{
		{ID: 1, Name: "Visible", Platform: PlatformAnthropic, Status: StatusActive, IsExclusive: true, ShowAccountQuota: true},
		{ID: 2, Status: StatusActive, IsExclusive: true},
		{ID: 3, Status: StatusActive, ShowAccountQuota: true},
		{ID: 4, Status: "inactive", IsExclusive: true, ShowAccountQuota: true},
		{ID: 5, Status: StatusActive, IsExclusive: true, ShowAccountQuota: true},
	}}
	svc := NewHostedAccountService(&hostedUsersStub{user: user}, groups, nil)
	result, err := svc.ListGroups(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, []HostedGroup{{ID: 1, Name: "Visible", Platform: PlatformAnthropic}}, result)
	user.AllowedGroups = nil
	result, err = svc.ListGroups(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Empty(t, result)
}

func TestHostedAccountClaudeSnapshotPreservesZeroAndExpiredData(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	a := &Account{ID: 1, Name: "Hosted Claude", Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, SessionWindowEnd: &reset,
		Extra:       map[string]any{"session_window_utilization": 0.0, "passive_usage_7d_utilization": json.Number("0.42"), "passive_usage_7d_reset": now.Add(24 * time.Hour).Unix(), "passive_usage_sampled_at": now.Add(-time.Minute).Format(time.RFC3339)},
		Credentials: map[string]any{"access_token": "TEST_ONLY_CREDENTIAL"}, ErrorMessage: "TEST_ONLY_PRIVATE_ERROR"}
	row := hostedAccountSnapshot(a, now)
	require.False(t, row.Stale)
	require.Len(t, row.Windows, 2)
	require.Zero(t, row.Windows[0].Utilization)
	require.Equal(t, 42.0, row.Windows[1].Utilization)
	encoded, err := json.Marshal(row)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "TEST_ONLY_CREDENTIAL")
	require.NotContains(t, string(encoded), "TEST_ONLY_PRIVATE_ERROR")
	require.NotContains(t, string(encoded), "credentials")
	require.NotContains(t, string(encoded), "extra")
	a.Extra["session_window_utilization"] = 0.75
	row = hostedAccountSnapshot(a, now.Add(2*time.Hour))
	require.True(t, row.Stale)
	require.True(t, row.Windows[0].Expired)
	require.Equal(t, 75.0, row.Windows[0].Utilization)
	// The snapshot's timestamp, rather than response time, remains authoritative.
	require.Equal(t, now.Add(-time.Minute), *row.UpdatedAt)
}

func TestHostedAccountMissingAndInvalidQuotaIsUnknown(t *testing.T) {
	now := time.Now()
	for _, value := range []any{nil, "invalid", "NaN", math.Inf(1), -0.1, 1.1, true} {
		a := &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Extra: map[string]any{"session_window_utilization": value, "passive_usage_sampled_at": now.Format(time.RFC3339)}}
		row := hostedAccountSnapshot(a, now)
		require.Empty(t, row.Windows)
		require.True(t, row.Stale)
	}
	a := &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken, SessionWindowStatus: "allowed_warning"}
	require.Empty(t, hostedAccountSnapshot(a, now).Windows, "do not estimate usage from scheduling status")
	a.Platform = PlatformGemini
	a.Type = AccountTypeOAuth
	require.Empty(t, hostedAccountSnapshot(a, now).Windows)
}

func TestHostedAccountCodexSnapshotUsesActualSampleTime(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sampled := now.Add(-time.Minute)
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
		"codex_usage_updated_at": sampled.Format(time.RFC3339), "codex_5h_used_percent": "35", "codex_5h_reset_after_seconds": 600,
		"codex_7d_used_percent": 0.0, "codex_7d_reset_at": now.Add(time.Hour).Format(time.RFC3339),
	}}
	row := hostedAccountSnapshot(a, now)
	require.Len(t, row.Windows, 2)
	require.False(t, row.Stale)
	require.Equal(t, sampled.Add(10*time.Minute), *row.Windows[0].ResetsAt)
	require.Zero(t, row.Windows[1].Utilization)
	delete(a.Extra, "codex_usage_updated_at")
	row = hostedAccountSnapshot(a, now)
	require.Nil(t, row.UpdatedAt)
	require.Nil(t, row.Windows[0].ResetsAt)
	require.True(t, row.Stale)
	a.Extra["codex_usage_updated_at"] = now.Add(time.Hour).Format(time.RFC3339)
	require.Nil(t, hostedAccountSnapshot(a, now).UpdatedAt)
	a.Extra["codex_usage_updated_at"] = now.Add(-hostedQuotaFreshness).Format(time.RFC3339)
	require.True(t, hostedAccountSnapshot(a, now).Stale)
}

func TestHostedAccountStatusDoesNotExposeRawErrors(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	for _, tc := range []struct {
		account Account
		want    string
	}{
		{Account{Status: StatusError, ErrorMessage: "private"}, "needs_attention"},
		{Account{Status: "inactive"}, "paused"},
		{Account{Status: StatusActive, Schedulable: true, RateLimitResetAt: &future}, "rate_limited"},
		{Account{Status: StatusActive, Schedulable: true, TempUnschedulableUntil: &future}, "temporarily_unavailable"},
		{Account{Status: StatusActive, Schedulable: true}, "available"},
	} {
		require.Equal(t, tc.want, hostedAccountSnapshot(&tc.account, now).Status)
	}
}

func TestHostedAccountExhaustedQuotaRequiresFreshGlobalWindow(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(time.Hour)
	a := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, SessionWindowEnd: &reset, Extra: map[string]any{
		"session_window_utilization": 1.0, "passive_usage_sampled_at": now.Format(time.RFC3339),
	}}
	require.Equal(t, "quota_exhausted", hostedAccountSnapshot(a, now).Status)
	require.Equal(t, "available", hostedAccountSnapshot(a, now.Add(hostedQuotaFreshness)).Status)
	delete(a.Extra, "session_window_utilization")
	a.Extra["passive_usage_7d_sonnet_utilization"] = 1.0
	require.Equal(t, "available", hostedAccountSnapshot(a, now).Status, "a model-scoped window does not exhaust all models")
}
