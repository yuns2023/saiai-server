package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// The window uses detection completion time, including results without billed usage.
type InputRiskAlertSummary struct {
	Count     int64     `json:"count"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
}
type InputRiskAlert struct {
	Events     []UsageModerationEvent `json:"moderation_events"`
	RequestID  string                 `json:"request_id"`
	Source     string                 `json:"source"`
	TurnNumber int                    `json:"turn_number"`
	UserID     int64                  `json:"user_id"`
	APIKeyID   int64                  `json:"api_key_id"`
	GroupID    int64                  `json:"group_id"`
	Result     *InputModerationResult `json:"moderation_result"`
}
type inputRiskCounter interface {
	CountInputRiskAlerts(context.Context, []int64, time.Time, time.Time) (map[int64]int64, error)
}

func (s *adminServiceImpl) enrichInputRiskAlerts(ctx context.Context, users []User) {
	repo, ok := s.userRepo.(inputRiskCounter)
	if !ok || len(users) == 0 {
		return
	}
	end := time.Now().UTC()
	start := end.Add(-30 * 24 * time.Hour)
	ids := make([]int64, len(users))
	for i := range users {
		ids[i] = users[i].ID
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	counts, err := repo.CountInputRiskAlerts(lookupCtx, ids, start, end)
	if err != nil {
		slog.Warn("admin_user_input_risk_summary_unavailable")
		// Unknown stays null, never a misleading zero.
		return
	}
	for i := range users {
		users[i].InputRiskAlerts = &InputRiskAlertSummary{Count: counts[users[i].ID], StartTime: start, EndTime: end}
	}
}

type inputRiskReader interface {
	ListInputRiskAlerts(context.Context, int64, time.Time, time.Time, int, int) ([]InputRiskAlert, int64, error)
}

func (s *UsageService) ListInputRiskAlerts(ctx context.Context, userID int64, start, end time.Time, page, size int) ([]InputRiskAlert, int64, error) {
	repo, ok := s.usageRepo.(inputRiskReader)
	if !ok {
		return nil, 0, fmt.Errorf("input risk alerts unavailable")
	}
	return repo.ListInputRiskAlerts(ctx, userID, start, end, page, size)
}
