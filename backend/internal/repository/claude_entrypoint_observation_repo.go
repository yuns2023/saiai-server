package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// claudeEntrypointObservationUpsertChunk keeps one statement well below the
// 65535 bind-parameter limit (13 parameters per row).
const claudeEntrypointObservationUpsertChunk = 500

type claudeEntrypointObservationRepository struct {
	sql sqlExecutor
}

func NewClaudeEntrypointObservationRepository(_ *dbent.Client, sqlDB *sql.DB) service.ClaudeEntrypointObservationRepository {
	return &claudeEntrypointObservationRepository{sql: sqlDB}
}

func (r *claudeEntrypointObservationRepository) UpsertClaudeEntrypointObservations(ctx context.Context, rows []service.ClaudeEntrypointObservation) error {
	for start := 0; start < len(rows); start += claudeEntrypointObservationUpsertChunk {
		end := start + claudeEntrypointObservationUpsertChunk
		if end > len(rows) {
			end = len(rows)
		}
		if err := r.upsertChunk(ctx, rows[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (r *claudeEntrypointObservationRepository) upsertChunk(ctx context.Context, rows []service.ClaudeEntrypointObservation) error {
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`INSERT INTO group_claude_entrypoint_observations (
		group_id, day, ua_entrypoint, body_entrypoint, body_status, cli_version, policy, result, reason,
		requests, first_seen_at, last_seen_at, sample_user_agent
	) VALUES `)
	args := make([]any, 0, len(rows)*13)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i * 13
		// day is passed as text and cast, so the session time zone cannot shift it.
		fmt.Fprintf(&b, "($%d, $%d::date, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12, base+13)
		args = append(args, row.GroupID, row.Day, row.UAEntrypoint, row.BodyEntrypoint, row.BodyStatus, row.CLIVersion,
			row.Policy, row.Result, row.Reason, row.Requests, row.FirstSeenAt, row.LastSeenAt, row.SampleUserAgent)
	}
	b.WriteString(` ON CONFLICT (group_id, day, ua_entrypoint, body_entrypoint, body_status, cli_version, policy, result, reason)
	DO UPDATE SET
		requests = group_claude_entrypoint_observations.requests + EXCLUDED.requests,
		first_seen_at = LEAST(group_claude_entrypoint_observations.first_seen_at, EXCLUDED.first_seen_at),
		last_seen_at = GREATEST(group_claude_entrypoint_observations.last_seen_at, EXCLUDED.last_seen_at),
		sample_user_agent = CASE
			WHEN EXCLUDED.last_seen_at >= group_claude_entrypoint_observations.last_seen_at THEN EXCLUDED.sample_user_agent
			ELSE group_claude_entrypoint_observations.sample_user_agent
		END`)
	_, err := r.sql.ExecContext(ctx, b.String(), args...)
	return err
}

func (r *claudeEntrypointObservationRepository) DeleteClaudeEntrypointObservationsBefore(ctx context.Context, beforeDay string) (int64, error) {
	res, err := r.sql.ExecContext(ctx, `DELETE FROM group_claude_entrypoint_observations WHERE day < $1::date`, beforeDay)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *claudeEntrypointObservationRepository) ListClaudeEntrypointObservations(ctx context.Context, groupID int64, sinceDay string, limit int) ([]service.ClaudeEntrypointObservationSummary, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT ua_entrypoint, body_entrypoint, body_status, cli_version, result, reason,
			SUM(requests)::bigint, MIN(first_seen_at), MAX(last_seen_at),
			(ARRAY_AGG(sample_user_agent ORDER BY last_seen_at DESC))[1]
		FROM group_claude_entrypoint_observations
		WHERE group_id = $1 AND day >= $2::date
		GROUP BY ua_entrypoint, body_entrypoint, body_status, cli_version, result, reason
		ORDER BY MAX(last_seen_at) DESC, ua_entrypoint, body_entrypoint, cli_version, result, reason
		LIMIT $3`, groupID, sinceDay, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.ClaudeEntrypointObservationSummary{}
	for rows.Next() {
		var item service.ClaudeEntrypointObservationSummary
		if err := rows.Scan(&item.UAEntrypoint, &item.BodyEntrypoint, &item.BodyStatus, &item.CLIVersion, &item.Result, &item.Reason,
			&item.Requests, &item.FirstSeenAt, &item.LastSeenAt, &item.SampleUserAgent); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
