//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Unimplemented embedded methods panic, so these tests also catch any query
// issued before authorization succeeds.
type hostedOwnerRepoStub struct {
	HostedOwnerRepository
	current  []int64
	ever     []HostedOwnerHistoricalAccount
	allowed  bool
	records  []HostedOwnerUsageRecord
	lastQ    HostedOwnerUsageQuery
	last24h  map[int64]HostedOwnerUsageTotals
	last7d   map[int64]HostedOwnerUsageTotals
	summedID []int64
}

func (r *hostedOwnerRepoStub) ListCurrentOwnedAccountIDs(context.Context, int64, int) ([]int64, error) {
	return r.current, nil
}
func (r *hostedOwnerRepoStub) ListEverOwnedAccounts(context.Context, int64, int) ([]HostedOwnerHistoricalAccount, error) {
	return r.ever, nil
}
func (r *hostedOwnerRepoStub) CanViewOwnedAccount(context.Context, int64, int64) (bool, error) {
	return r.allowed, nil
}
func (r *hostedOwnerRepoStub) ListOwnedUsage(_ context.Context, q HostedOwnerUsageQuery) ([]HostedOwnerUsageRecord, error) {
	r.lastQ = q
	if len(r.records) > q.Limit {
		return r.records[:q.Limit], nil
	}
	return r.records, nil
}
func (r *hostedOwnerRepoStub) SummarizeOwnedUsage(_ context.Context, _ int64, ids []int64, _, _, _ time.Time) (map[int64]HostedOwnerUsageTotals, map[int64]HostedOwnerUsageTotals, error) {
	r.summedID = ids
	return r.last24h, r.last7d, nil
}

type hostedOwnerAccountsStub struct {
	AccountRepository
	rows []*Account
}

func (r *hostedOwnerAccountsStub) GetByIDs(context.Context, []int64) ([]*Account, error) {
	return r.rows, nil
}

func validUsageParams() HostedOwnerUsageParams {
	return HostedOwnerUsageParams{Start: "2026-11-01T00:00:00Z", End: "2026-11-08T00:00:00Z"}
}

func TestParseHostedOwnerUsageQueryValidation(t *testing.T) {
	q, err := parseHostedOwnerUsageQuery(1, 2, validUsageParams())
	require.NoError(t, err)
	require.Equal(t, HostedOwnerUsageDefaultLimit, q.Limit)
	require.Nil(t, q.After)

	bad := []HostedOwnerUsageParams{
		{End: "2026-11-08T00:00:00Z"},
		{Start: "2026-11-01", End: "2026-11-08T00:00:00Z"},
		{Start: "2026-11-08T00:00:00Z", End: "2026-11-08T00:00:00Z"},
		{Start: "2026-11-09T00:00:00Z", End: "2026-11-08T00:00:00Z"},
		{Start: "2026-10-01T00:00:00Z", End: "2026-11-02T00:00:00Z"},
		{Start: "0001-01-01T00:00:00Z", End: "9999-12-31T23:59:59Z"},
	}
	for _, params := range bad {
		_, err := parseHostedOwnerUsageQuery(1, 2, params)
		require.ErrorIs(t, err, ErrHostedOwnerUsageRange, params)
	}
	exact := HostedOwnerUsageParams{Start: "2026-10-01T00:00:00Z", End: "2026-11-01T00:00:00Z"}
	_, err = parseHostedOwnerUsageQuery(1, 2, exact)
	require.NoError(t, err, "exactly 31 days is allowed")

	for _, limit := range []string{"0", "-1", "101", "abc", "1e2", "99999999999999999999"} {
		params := validUsageParams()
		params.Limit = limit
		_, err := parseHostedOwnerUsageQuery(1, 2, params)
		require.ErrorIs(t, err, ErrHostedOwnerUsageLimit, limit)
	}
	params := validUsageParams()
	params.Limit = "100"
	q, err = parseHostedOwnerUsageQuery(1, 2, params)
	require.NoError(t, err)
	require.Equal(t, 100, q.Limit)
}

func TestHostedOwnerCursorRoundTripAndTampering(t *testing.T) {
	at := time.Date(2026, 11, 16, 9, 59, 12, 345678000, time.UTC)
	encoded := encodeHostedOwnerCursor(HostedOwnerUsageCursor{CreatedAt: at, ID: 42})
	params := validUsageParams()
	params.Cursor = encoded
	q, err := parseHostedOwnerUsageQuery(1, 2, params)
	require.NoError(t, err)
	require.NotNil(t, q.After)
	require.True(t, at.Equal(q.After.CreatedAt), "microsecond precision survives")
	require.Equal(t, int64(42), q.After.ID)

	enc := func(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
	for _, cursor := range []string{
		"!!!not-base64",
		enc("v2:1:1"),
		enc("v1:1"),
		enc("v1:-5:1"),
		enc("v1:5:0"),
		enc("v1:5:-1"),
		enc("v1:x:1"),
		enc("v1:1:2:3"),
		strings.Repeat("A", hostedOwnerCursorMaxLen+1),
	} {
		params := validUsageParams()
		params.Cursor = cursor
		_, err := parseHostedOwnerUsageQuery(1, 2, params)
		require.ErrorIs(t, err, ErrHostedOwnerUsageCursor, cursor)
	}
}

func TestListOwnedAccountUsageAuthorizationAndPaging(t *testing.T) {
	ctx := context.Background()
	repo := &hostedOwnerRepoStub{allowed: false}
	svc := NewHostedOwnerService(repo, nil)
	_, err := svc.ListOwnedAccountUsage(ctx, 1, 2, validUsageParams())
	require.ErrorIs(t, err, ErrHostedOwnedAccountNotFound)
	_, err = svc.ListOwnedAccountUsage(ctx, 1, 0, validUsageParams())
	require.ErrorIs(t, err, ErrHostedOwnedAccountNotFound)

	key, charged := "mine", 1.5
	t0 := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	repo = &hostedOwnerRepoStub{allowed: true, records: []HostedOwnerUsageRecord{
		{ID: 9, Row: HostedOwnerUsageRow{CreatedAt: t0, IsSelf: true, APIKeyName: &key, Charged: &charged, StandardCost: 1}},
		// A repository bug leaking identity for another user's row must be stripped.
		{ID: 8, Row: HostedOwnerUsageRow{CreatedAt: t0, IsSelf: false, APIKeyName: &key, Charged: &charged, StandardCost: 2}},
		{ID: 7, Row: HostedOwnerUsageRow{CreatedAt: t0, IsSelf: false, StandardCost: 3}},
	}}
	svc = NewHostedOwnerService(repo, nil)
	params := validUsageParams()
	params.Limit = "2"
	page, err := svc.ListOwnedAccountUsage(ctx, 1, 2, params)
	require.NoError(t, err)
	require.Equal(t, 3, repo.lastQ.Limit, "fetches one extra row to detect the next page")
	require.Equal(t, int64(1), repo.lastQ.OwnerUserID)
	require.Equal(t, int64(2), repo.lastQ.AccountID)
	require.Len(t, page.Items, 2)
	require.Nil(t, page.Items[1].APIKeyName)
	require.Nil(t, page.Items[1].Charged)
	require.NotNil(t, page.NextCursor)
	next, err := decodeHostedOwnerCursor(*page.NextCursor)
	require.NoError(t, err)
	require.Equal(t, int64(8), next.ID, "cursor points at the last returned row")

	params.Limit = "5"
	page, err = svc.ListOwnedAccountUsage(ctx, 1, 2, params)
	require.NoError(t, err)
	require.Len(t, page.Items, 3)
	require.Nil(t, page.NextCursor)
}

func TestListOwnedAccountsMergesCurrentAndHistory(t *testing.T) {
	platform := PlatformAnthropic
	repo := &hostedOwnerRepoStub{
		current: []int64{5},
		ever:    []HostedOwnerHistoricalAccount{{AccountID: 3, Platform: &platform}, {AccountID: 5, Platform: &platform}, {AccountID: 9}},
		last24h: map[int64]HostedOwnerUsageTotals{5: {SelfRequests: 1}},
		last7d:  map[int64]HostedOwnerUsageTotals{3: {OthersRequests: 4}, 5: {SelfRequests: 2}},
	}
	accounts := &hostedOwnerAccountsStub{rows: []*Account{{ID: 5, Name: "seller@example.invalid 5x", Notes: ptrStr("internal"),
		Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true}}}
	svc := NewHostedOwnerService(repo, accounts)
	list, err := svc.ListOwnedAccounts(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, list.Items, 3)
	require.Equal(t, int64(5), list.Items[0].AccountID)
	require.True(t, list.Items[0].Current)
	require.Equal(t, "托管号 #5", list.Items[0].DisplayName)
	require.NotNil(t, list.Items[0].Status)
	require.Equal(t, int64(2), list.Items[0].Last7d.SelfRequests)
	require.Equal(t, int64(1), list.Items[0].Last24h.SelfRequests)
	require.False(t, list.Items[1].Current)
	require.Equal(t, int64(3), list.Items[1].AccountID)
	require.Nil(t, list.Items[1].Status)
	require.NotNil(t, list.Items[1].Windows)
	require.Equal(t, int64(4), list.Items[1].Last7d.OthersRequests)
	require.Nil(t, list.Items[2].Platform)
	sorted := append([]int64(nil), repo.summedID...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	require.Equal(t, []int64{3, 5, 9}, sorted)

	raw, err := json.Marshal(list)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "seller@example.invalid")
	require.NotContains(t, string(raw), "internal")
}

func TestHostedOwnerJSONAllowlists(t *testing.T) {
	keysOf := func(v any) []string {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	require.Equal(t, []string{"account_id", "current", "display_name", "last_24h", "last_7d", "platform", "stale", "status", "type", "updated_at", "windows"},
		keysOf(OwnedHostedAccount{}))
	require.Equal(t, []string{"api_key_name", "cache_creation_tokens", "cache_read_tokens", "charged", "created_at", "input_tokens", "is_self", "model", "output_tokens", "standard_cost"},
		keysOf(HostedOwnerUsageRow{}))
}

func TestHostedAccountSnapshotNeverReturnsAdminName(t *testing.T) {
	snapshot := hostedAccountSnapshot(&Account{ID: 22, Name: "seller@example.invalid 1017 pro", Platform: PlatformAnthropic,
		Status: StatusActive, Schedulable: true}, time.Now())
	require.Equal(t, "托管号 #22", snapshot.Name)
}

func ptrStr(v string) *string { return &v }
