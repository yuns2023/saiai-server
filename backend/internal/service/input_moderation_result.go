package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Moderation results are separate from risk events and never count as strikes.
type InputModerationResult struct {
	Diagnostics      *InputModerationDiagnostics `json:"diagnostics,omitempty"`
	DecisionSnapshot *ModerationDecisionSnapshot `json:"decision_snapshot,omitempty"`
	Status           string                      `json:"status"`
	Reason           string                      `json:"reason,omitempty"`
	Scores           map[string]float64          `json:"scores,omitempty"`
	MinConfidence    int                         `json:"min_confidence"`
	ModelVersion     string                      `json:"model_version,omitempty"`
	UpdatedAt        time.Time                   `json:"updated_at"`
}
type InputModerationResultRepository interface {
	SaveInputModerationResult(context.Context, InputModerationTask, InputModerationResult) error
}

func (s *InputModerationService) recordResult(task InputModerationTask, result InputModerationResult) error {
	if task.Diagnostics != nil {
		diagnostics := *task.Diagnostics
		diagnostics.DurationMS = time.Since(diagnostics.started).Milliseconds()
		if diagnostics.DurationMS < 0 {
			diagnostics.DurationMS = 0
		}
		task.Diagnostics.DurationMS = diagnostics.DurationMS
		task.Diagnostics.status = result.Status
		task.Diagnostics.reason = result.Reason
		result.Diagnostics = &diagnostics
	}
	if task.Diagnostics == nil || !task.Diagnostics.processing {
		inputModerationMetrics.observe(result)
	}
	if result.Status != "pending" {
		slog.Info("input_moderation_result", "request_id", task.RequestID, "source", normalizeInputModerationSource(task.Source),
			"turn", task.TurnNumber, "status", result.Status, "reason", result.Reason, "diagnostics", result.Diagnostics)
	}
	repo, ok := s.eventRepo.(InputModerationResultRepository)
	if !ok || strings.TrimSpace(task.RequestID) == "" {
		return nil
	}
	task.RequestID = truncateModerationText(strings.TrimSpace(task.RequestID), 128)
	task.Source = normalizeInputModerationSource(task.Source)
	result.UpdatedAt = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := repo.SaveInputModerationResult(ctx, task, result); err != nil {
		slog.Warn("input_moderation_result_write_failed")
		return fmt.Errorf("moderation result storage unavailable")
	}
	return nil
}
