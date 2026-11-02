//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration117ClaudeEntrypointPolicyAppliesTwice(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE SCHEMA claude_entrypoint_migration_test; SET LOCAL search_path=claude_entrypoint_migration_test;
 CREATE TABLE groups(id BIGINT PRIMARY KEY); INSERT INTO groups VALUES(1);`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("117_group_claude_entrypoint_policy.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	var policy, allowlist string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT claude_entrypoint_policy, claude_entrypoint_allowlist::text FROM groups WHERE id=1`).Scan(&policy, &allowlist))
	require.Equal(t, "off", policy, "existing groups stay off")
	var items []string
	require.NoError(t, json.Unmarshal([]byte(allowlist), &items))
	require.Equal(t, []string{"cli", "claude-vscode", "sdk-cli"}, items)
	_, err = tx.ExecContext(ctx, `UPDATE groups SET claude_entrypoint_policy='enforce' WHERE id=1`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SAVEPOINT invalid_policy`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE groups SET claude_entrypoint_policy='block' WHERE id=1`)
	require.Error(t, err, "CHECK constraint rejects unknown policies")
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT invalid_policy`)
	require.NoError(t, err)
}

func TestClaudeEntrypointObservationRepo_UpsertAndAggregate(t *testing.T) {
	tx := testTx(t)
	repo := &claudeEntrypointObservationRepository{sql: tx}
	ctx := context.Background()
	groupID := int64(987654321)
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	row := func(day, ua, result string, requests int64, first, last time.Time, sample string) service.ClaudeEntrypointObservation {
		return service.ClaudeEntrypointObservation{
			ClaudeEntrypointObservationKey: service.ClaudeEntrypointObservationKey{
				GroupID: groupID, Day: day, UAEntrypoint: ua, BodyEntrypoint: ua, BodyStatus: "valid", CLIVersion: "2.1.316",
				Policy: "observe", Result: result, Reason: map[string]string{"allowed": "ok"}[result] + map[string]string{"violation_observed": "ua_not_allowed"}[result],
			},
			Requests: requests, FirstSeenAt: first, LastSeenAt: last, SampleUserAgent: sample,
		}
	}

	require.NoError(t, repo.UpsertClaudeEntrypointObservations(ctx, []service.ClaudeEntrypointObservation{
		row("2026-10-31", "t3-code", "violation_observed", 2, t0.Add(-48*time.Hour), t0.Add(-47*time.Hour), "ua-old"),
		row("2026-11-01", "t3-code", "violation_observed", 3, t0, t0.Add(time.Minute), "ua-1"),
		row("2026-11-01", "cli", "allowed", 5, t0, t0.Add(2*time.Minute), "cli-ua"),
		row("2026-10-01", "t3-code", "violation_observed", 7, t0.Add(-800*time.Hour), t0.Add(-800*time.Hour), "ua-ancient"),
	}))
	// Second flush for an existing key adds requests and extends the range.
	require.NoError(t, repo.UpsertClaudeEntrypointObservations(ctx, []service.ClaudeEntrypointObservation{
		row("2026-11-01", "t3-code", "violation_observed", 4, t0.Add(-time.Minute), t0.Add(5*time.Minute), "ua-2"),
	}))

	var requests int64
	var sample string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT requests, sample_user_agent FROM group_claude_entrypoint_observations
		WHERE group_id=$1 AND day='2026-11-01' AND ua_entrypoint='t3-code'`, groupID).Scan(&requests, &sample))
	require.Equal(t, int64(7), requests)
	require.Equal(t, "ua-2", sample)

	items, err := repo.ListClaudeEntrypointObservations(ctx, groupID, "2026-10-27", 500)
	require.NoError(t, err)
	require.Len(t, items, 2, "rows before sinceDay excluded, days aggregated")
	require.Equal(t, "t3-code", items[0].UAEntrypoint, "ordered by last_seen desc")
	require.Equal(t, int64(9), items[0].Requests, "2 (10-31) + 3 + 4 (11-01)")
	require.True(t, items[0].FirstSeenAt.Equal(t0.Add(-48*time.Hour)))
	require.True(t, items[0].LastSeenAt.Equal(t0.Add(5*time.Minute)))
	require.Equal(t, "ua-2", items[0].SampleUserAgent, "sample of the most recent row")
	require.Equal(t, "cli", items[1].UAEntrypoint)
	require.Equal(t, int64(5), items[1].Requests)

	items, err = repo.ListClaudeEntrypointObservations(ctx, groupID, "2026-10-27", 1)
	require.NoError(t, err)
	require.Len(t, items, 1)

	deleted, err := repo.DeleteClaudeEntrypointObservationsBefore(ctx, "2026-10-31")
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	items, err = repo.ListClaudeEntrypointObservations(ctx, groupID, "2000-01-01", 500)
	require.NoError(t, err)
	require.Len(t, items, 2)

	// Chunking: more rows than one statement carries.
	many := make([]service.ClaudeEntrypointObservation, 0, claudeEntrypointObservationUpsertChunk+3)
	for i := 0; i < claudeEntrypointObservationUpsertChunk+3; i++ {
		r := row("2026-11-02", "cli", "allowed", 1, t0, t0, "x")
		r.CLIVersion = time.Duration(i).String()
		many = append(many, r)
	}
	require.NoError(t, repo.UpsertClaudeEntrypointObservations(ctx, many))
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_claude_entrypoint_observations WHERE group_id=$1 AND day='2026-11-02'`, groupID).Scan(&count))
	require.Equal(t, claudeEntrypointObservationUpsertChunk+3, count)
}

func TestGroupRepoRoundTripsClaudeEntrypointPolicy(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newGroupRepositoryWithSQL(tx.Client(), tx)
	g := &service.Group{Name: uniqueTestValue(t, "entrypoint-group"), Platform: service.PlatformAnthropic, Status: service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard, RateMultiplier: 1}
	require.NoError(t, repo.Create(ctx, g))
	got, err := repo.GetByID(ctx, g.ID)
	require.NoError(t, err)
	require.Equal(t, service.ClaudeEntrypointPolicyOff, got.ClaudeEntrypointPolicy)
	require.Equal(t, service.DefaultClaudeEntrypointAllowlist(), got.ClaudeEntrypointAllowlist)

	got.ClaudeEntrypointPolicy = service.ClaudeEntrypointPolicyEnforce
	got.ClaudeEntrypointAllowlist = []string{}
	require.NoError(t, repo.Update(ctx, got))
	got, err = repo.GetByID(ctx, g.ID)
	require.NoError(t, err)
	require.Equal(t, service.ClaudeEntrypointPolicyEnforce, got.ClaudeEntrypointPolicy)
	require.NotNil(t, got.ClaudeEntrypointAllowlist)
	require.Empty(t, got.ClaudeEntrypointAllowlist)

	// The auth path loads groups with an explicit column list.
	user := mustCreateUser(t, tx.Client(), &service.User{Email: uniqueTestValue(t, "entrypoint") + "@test.com"})
	keys := newAPIKeyRepositoryWithSQL(tx.Client(), tx)
	key := &service.APIKey{UserID: user.ID, Key: uniqueTestValue(t, "entrypoint-key"), Name: "k", Status: service.StatusActive, GroupID: &g.ID}
	require.NoError(t, keys.Create(ctx, key))
	auth, err := keys.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.NotNil(t, auth.Group)
	require.Equal(t, service.ClaudeEntrypointPolicyEnforce, auth.Group.ClaudeEntrypointPolicy)
	require.NotNil(t, auth.Group.ClaudeEntrypointAllowlist)
	require.Empty(t, auth.Group.ClaudeEntrypointAllowlist)
}
