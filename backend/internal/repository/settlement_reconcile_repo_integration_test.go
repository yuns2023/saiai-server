//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestSettlementReconcileRepository(t *testing.T) {
	ctx := context.Background()
	f := newSettlementFixture(t, service.PlatformAnthropic)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM usage_logs WHERE api_key_id=$1", f.key.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM usage_settlement_events WHERE api_key_id=$1", f.key.ID)
	})
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return v
	}
	owner := f.user.ID
	event := func(id string, created string, standard, charge float64, eventOwner *int64, invalid []string) {
		var inv any
		if invalid != nil {
			inv = pq.Array(invalid)
		}
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_settlement_events (request_id, api_key_id, category, rule_version, buyer_user_id, account_id, owner_user_id, owner_source, self_purchase, group_id, standard_cost, charge_input, charge_source, invalid_fields, created_at)
			VALUES ($1, $2, 'hosted_self', 1, $3, $4, $5, 'settlement', false, 4242, $6, $7, 'balance', $8, $9)`,
			id, f.key.ID, f.user.ID, f.account.ID, eventOwner, standard, charge, inv, at(created))
		require.NoError(t, err)
	}
	usage := func(id string, created string, total, actual float64, metered *float64, logOwner *int64) {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs (user_id, api_key_id, account_id, request_id, model, total_cost, actual_cost, metered_cost, account_owner_user_id, group_id, created_at)
			VALUES ($1, $2, $3, $4, 'TEST_ONLY_model', $5, $6, $7, $8, NULL, $9)`,
			f.user.ID, f.key.ID, f.account.ID, id, total, actual, metered, logOwner, at(created))
		require.NoError(t, err)
	}
	one := 1.0
	prefix := "reconcile-" + time.Now().Format("150405.000000") + "-"
	// A matched; B amount mismatch; C owner differs; D/E event only (old /
	// within grace); F/G log only (after / before coverage start); H platform
	// log (not a candidate); I invalid amounts (not compared); J log outside
	// the window joined by identity.
	event(prefix+"A", "2020-01-02T01:00:00Z", 1, 0.5, &owner, nil)
	usage(prefix+"A", "2020-01-02T01:00:01Z", 1, 0.5, &one, &owner)
	event(prefix+"B", "2020-01-02T02:00:00Z", 1, 0.5, &owner, nil)
	usage(prefix+"B", "2020-01-02T02:00:01Z", 1, 0.7, &one, &owner)
	event(prefix+"C", "2020-01-02T03:00:00Z", 1, 0.5, &owner, nil)
	usage(prefix+"C", "2020-01-02T03:00:01Z", 1, 0.5, &one, nil)
	event(prefix+"D", "2020-01-02T04:00:00Z", 1, 0.5, &owner, nil)
	event(prefix+"E", "2020-01-02T23:55:00Z", 1, 0.5, &owner, nil)
	usage(prefix+"F", "2020-01-02T05:00:00Z", 1, 0.5, &one, nil)
	usage(prefix+"G", "2020-01-01T06:00:00Z", 1, 0.5, nil, &owner)
	usage(prefix+"H", "2020-01-02T06:00:00Z", 1, 0.5, nil, nil)
	event(prefix+"I", "2020-01-02T07:00:00Z", 0, 0, &owner, []string{"standard_cost", "charge_input"})
	usage(prefix+"I", "2020-01-02T07:00:01Z", 9, 9, &one, &owner)
	// K: legacy log without request_id cannot be matched and is skipped.
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs (user_id, api_key_id, account_id, request_id, model, total_cost, actual_cost, metered_cost, created_at)
		VALUES ($1, $2, $3, NULL, 'TEST_ONLY_model', 1, 1, 1, $4)`, f.user.ID, f.key.ID, f.account.ID, at("2020-01-02T08:00:00Z"))
	require.NoError(t, err)
	event(prefix+"J", "2020-01-02T23:59:59Z", 1, 0.5, &owner, nil)
	usage(prefix+"J", "2020-01-03T00:00:05Z", 1, 0.5, &one, &owner)

	repo := NewSettlementReconcileRepository(integrationDB)
	from, to := at("2020-01-01T00:00:00Z"), at("2020-01-03T00:00:00Z")
	q := service.SettlementReconcileQuery{From: from, To: to, CoverageStart: at("2020-01-01T12:00:00Z"), Page: 1, PageSize: 100, Now: to}
	data, err := repo.Reconcile(ctx, q)
	require.NoError(t, err)
	summary := data.Summary
	require.Len(t, summary, 1)
	require.Equal(t, int64(7), summary[0].EventCount)
	require.Equal(t, int64(1), summary[0].InvalidAmountCount)
	require.InDelta(t, 6, summary[0].StandardCost, 1e-9, "invalid rows excluded")
	require.InDelta(t, 3, summary[0].ChargeInput, 1e-9)
	require.Equal(t, int64(4242), *summary[0].GroupID)

	diffs, counts, total := data.Diffs, data.Counts, data.Total
	got := map[string]service.SettlementReconcileDiff{}
	for _, d := range diffs {
		got[d.RequestID[len(prefix):]] = d
	}
	require.Equal(t, int64(6), total)
	require.Len(t, got, 6)
	expect := map[string][2]string{
		"B": {"amount_mismatch", "mismatch"},
		"C": {"owner_mismatch", "possible_owner_timing_difference"},
		"D": {"event_only", "missing_log"},
		"E": {"event_only", "pending"},
		"F": {"log_only", "missing_event"},
		"G": {"log_only", "coverage_unknown"},
	}
	for id, want := range expect {
		d, ok := got[id]
		require.True(t, ok, id)
		require.Equal(t, want[0], d.Kind, id)
		require.Equal(t, want[1], d.Status, id)
	}
	require.True(t, got["C"].OwnerDiffers)
	require.False(t, got["B"].OwnerDiffers)
	require.InDelta(t, 0.7, *got["B"].LogActualCost, 1e-12)
	require.InDelta(t, 0.5, *got["B"].EventChargeInput, 1e-12)
	require.Nil(t, got["F"].EventChargeInput)
	require.Equal(t, f.account.ID, *got["F"].AccountID)
	var countTotal int64
	for _, c := range counts {
		countTotal += c.Count
	}
	require.Equal(t, total, countTotal)

	// Paging keeps the order and the total.
	q.PageSize, q.Page = 4, 2
	page2, err := repo.Reconcile(ctx, q)
	require.NoError(t, err)
	require.Equal(t, total, page2.Total)
	require.Len(t, page2.Diffs, 2)
	require.Equal(t, diffs[4].RequestID, page2.Diffs[0].RequestID)
}

// A reconcile query that exceeds the database-side statement timeout returns
// the dedicated 503 error instead of hanging the admin request.
func TestSettlementReconcileRepository_StatementTimeout(t *testing.T) {
	settlementReconcileStatementTimeout = "200ms"
	t.Cleanup(func() { settlementReconcileStatementTimeout = "15s" })
	release := holdSettlementTableLock(t)
	defer release()
	repo := NewSettlementReconcileRepository(integrationDB)
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	// The client-side deadline only keeps a broken implementation from
	// hanging the test; the database timeout must fire first.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	_, err := repo.Reconcile(ctx, service.SettlementReconcileQuery{From: from, To: from.Add(time.Hour), CoverageStart: from, Page: 1, PageSize: 10, Now: from})
	require.ErrorIs(t, err, service.ErrSettlementReconcileTimeout)
	require.Less(t, time.Since(started), 3*time.Second)
}
