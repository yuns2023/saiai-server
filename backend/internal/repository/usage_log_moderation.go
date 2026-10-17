package repository

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// The link can arrive before either the usage row or the classifier event.
// Joining on authenticated identity AND turn prevents request-ID cross-talk.
func (r *usageLogRepository) LinkInputModerationUsage(ctx context.Context, log *service.UsageLog) error {
	ref := log.ModerationRef
	if ref == nil || log.GroupID == nil {
		return nil
	}
	_, err := r.sql.ExecContext(ctx, `INSERT INTO input_moderation_usage_links
 (request_id, source, turn_number, user_id, api_key_id, group_id, usage_request_id)
 VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, ref.RequestID, ref.Source, ref.TurnNumber, log.UserID, log.APIKeyID, *log.GroupID, log.RequestID)
	return err
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
 FROM usage_logs u JOIN input_moderation_usage_links l
 ON l.usage_request_id=u.request_id AND l.user_id=u.user_id AND l.api_key_id=u.api_key_id AND l.group_id=u.group_id
 JOIN input_moderation_events e ON e.request_id=l.request_id AND e.source=l.source
 AND COALESCE(e.turn_number,0)=l.turn_number AND e.user_id=l.user_id AND e.api_key_id=l.api_key_id AND e.group_id=l.group_id
 WHERE u.id=ANY($1) ORDER BY u.id,e.id`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
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
