package repository

import (
	"context"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *inputModerationRepository) SaveInputModerationResult(ctx context.Context, t service.InputModerationTask, v service.InputModerationResult) error {
	return saveInputModerationResult(ctx, r.db, t, v)
}

// saveInputModerationResult never downgrades a final (passed/flagged) result.
func saveInputModerationResult(ctx context.Context, db sqlExecutor, t service.InputModerationTask, v service.InputModerationResult) error {
	scores, err := json.Marshal(v.Scores)
	if err != nil {
		return err
	}
	diagnostics, err := json.Marshal(v.Diagnostics)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO input_moderation_results
 (request_id,source,turn_number,user_id,api_key_id,group_id,status,reason,scores,min_confidence,model_version,updated_at,decision_snapshot,diagnostics)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$12,$13::jsonb,$14::jsonb)
 ON CONFLICT (request_id,source,turn_number,user_id,api_key_id,group_id) DO UPDATE SET
 status=EXCLUDED.status,reason=EXCLUDED.reason,scores=EXCLUDED.scores,
 min_confidence=EXCLUDED.min_confidence,model_version=EXCLUDED.model_version,updated_at=EXCLUDED.updated_at,decision_snapshot=EXCLUDED.decision_snapshot,diagnostics=EXCLUDED.diagnostics
 WHERE input_moderation_results.status NOT IN ('passed','flagged')`,
		t.RequestID, t.Source, t.TurnNumber, t.UserID, t.APIKeyID, t.GroupID, v.Status, v.Reason, string(scores), v.MinConfidence, v.ModelVersion, v.UpdatedAt, moderationDecisionJSON(v.DecisionSnapshot), string(diagnostics))
	return err
}

func moderationDecisionJSON(v *service.ModerationDecisionSnapshot) any {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return string(b)
}
