package repository

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"log/slog"
	"time"
)

// Correlation is part of the usage INSERT, including batch/retry paths.
// It stores no request text and expires with the usage row.
func moderationRefJSON(ref *service.InputModerationUsageRef) any {
	if ref == nil {
		return nil
	}
	data, _ := json.Marshal(ref)
	return string(data)
}

// Optional audit metadata must never make billing history unavailable.
func (r *usageLogRepository) enrichUsageLogModeration(ctx context.Context, logs []service.UsageLog) {
	lookupCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := r.hydrateUsageLogModeration(lookupCtx, logs); err != nil {
		slog.Warn("usage_moderation_enrichment_failed")
		for i := range logs {
			logs[i].ModerationEvents = nil
			logs[i].ModerationUnavailable = true
		}
	}
}

func (r *usageLogRepository) hydrateUsageLogModeration(ctx context.Context, logs []service.UsageLog) error {
	if len(logs) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(logs))
	positions := map[int64]int{}
	for i := range logs {
		ids = append(ids, logs[i].ID)
		positions[logs[i].ID] = i
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT DISTINCT u.id, e.id, e.categories, e.scores, e.min_confidence,
 e.action, e.counted_as_strike, COALESCE(e.model_version,'')
 FROM usage_logs u JOIN input_moderation_events e
 ON e.request_id=u.moderation_ref->>'RequestID' AND e.source=u.moderation_ref->>'Source'
 AND COALESCE(e.turn_number,0)::text=u.moderation_ref->>'TurnNumber'
 AND e.user_id=u.user_id AND e.api_key_id=u.api_key_id AND e.group_id=u.group_id
 WHERE u.id=ANY($1) ORDER BY u.id,e.id`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, eventID int64
		var categories, scores []byte
		var event service.UsageModerationEvent
		if err := rows.Scan(&id, &eventID, &categories, &scores, &event.MinConfidence, &event.Action, &event.CountedAsStrike, &event.ModelVersion); err != nil {
			return err
		}
		if err := json.Unmarshal(categories, &event.Categories); err != nil {
			return err
		}
		if len(scores) > 0 {
			if err := json.Unmarshal(scores, &event.Scores); err != nil {
				return err
			}
		}
		if i, ok := positions[id]; ok {
			logs[i].ModerationEvents = append(logs[i].ModerationEvents, event)
		}
	}
	return rows.Err()
}
