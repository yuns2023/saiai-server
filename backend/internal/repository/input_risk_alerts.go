package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Match the complete moderation identity; EXISTS cannot multiply usage rows.
const flaggedUsagePredicate = `EXISTS (SELECT 1 FROM input_moderation_results r
 WHERE r.status='flagged' AND r.request_id=usage_logs.moderation_ref->>'RequestID'
 AND r.source=usage_logs.moderation_ref->>'Source'
 AND r.turn_number::text=usage_logs.moderation_ref->>'TurnNumber'
 AND r.user_id=usage_logs.user_id AND r.api_key_id=usage_logs.api_key_id AND r.group_id=usage_logs.group_id)`

func (r *userRepository) CountInputRiskAlerts(ctx context.Context, ids []int64, start, end time.Time) (map[int64]int64, error) {
	counts := map[int64]int64{}
	if len(ids) == 0 {
		return counts, nil
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT user_id,COUNT(*) FROM input_moderation_results
 WHERE status='flagged' AND user_id=ANY($1) AND updated_at >= $2 AND updated_at < $3 GROUP BY user_id`, pq.Array(ids), start, end)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		counts[id] = n
	}
	return counts, rows.Err()
}
func (r *usageLogRepository) ListInputRiskAlerts(ctx context.Context, userID int64, start, end time.Time, page, size int) ([]service.InputRiskAlert, int64, error) {
	const where = ` FROM input_moderation_results r WHERE status='flagged' AND ($1::bigint=0 OR user_id=$1) AND updated_at >= $2 AND updated_at < $3`
	var total int64
	if err := scanSingleRow(ctx, r.sql, `SELECT COUNT(*)`+where, []any{userID, start, end}, &total); err != nil {
		return nil, 0, err
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT request_id,source,turn_number,user_id,api_key_id,group_id,status,reason,scores,min_confidence,model_version,updated_at,decision_snapshot,
 (SELECT COALESCE(jsonb_agg(jsonb_build_object('categories',e.categories,'scores',e.scores,
 'min_confidence',e.min_confidence,'action',e.action,'counted_as_strike',e.counted_as_strike,
 'model_version',COALESCE(e.model_version,'')) ORDER BY e.id),'[]'::jsonb)
 FROM input_moderation_events e WHERE e.request_id=r.request_id AND e.source=r.source
 AND COALESCE(e.turn_number,0)=r.turn_number AND e.user_id=r.user_id AND e.api_key_id=r.api_key_id AND e.group_id=r.group_id)`+where+` ORDER BY updated_at DESC,request_id,source,turn_number,user_id,api_key_id,group_id LIMIT $4 OFFSET $5`, userID, start, end, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.InputRiskAlert, 0)
	for rows.Next() {
		var a service.InputRiskAlert
		v := &service.InputModerationResult{}
		var scores, snapshot, events []byte
		if err := rows.Scan(&a.RequestID, &a.Source, &a.TurnNumber, &a.UserID, &a.APIKeyID, &a.GroupID, &v.Status, &v.Reason, &scores, &v.MinConfidence, &v.ModelVersion, &v.UpdatedAt, &snapshot, &events); err != nil {
			return nil, 0, err
		}
		if len(scores) > 0 {
			if err := json.Unmarshal(scores, &v.Scores); err != nil {
				return nil, 0, err
			}
		}
		if len(snapshot) > 0 {
			if err := json.Unmarshal(snapshot, &v.DecisionSnapshot); err != nil {
				return nil, 0, err
			}
		}
		if err := json.Unmarshal(events, &a.Events); err != nil {
			return nil, 0, err
		}
		a.Result = v
		out = append(out, a)
	}
	return out, total, rows.Err()
}
