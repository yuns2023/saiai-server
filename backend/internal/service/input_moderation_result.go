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
	result = s.finalizeResult(task, result)
	s.reportResult(task, result)
	repo, ok := s.eventRepo.(InputModerationResultRepository)
	if !ok || strings.TrimSpace(task.RequestID) == "" {
		return nil
	}
	task = normalizeModerationResultTask(task)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := repo.SaveInputModerationResult(ctx, task, result); err != nil {
		slog.Warn("input_moderation_result_write_failed")
		return fmt.Errorf("moderation result storage unavailable")
	}
	return nil
}

// finalizeResult snapshots the job diagnostics into the result.
func (s *InputModerationService) finalizeResult(task InputModerationTask, result InputModerationResult) InputModerationResult {
	if task.Diagnostics != nil {
		diagnostics := *task.Diagnostics
		diagnostics.DurationMS = time.Since(diagnostics.started).Milliseconds()
		if diagnostics.DurationMS < 0 {
			diagnostics.DurationMS = 0
		}
		diagnostics.EvidenceOmittedCategories = append([]string(nil), task.Diagnostics.EvidenceOmittedCategories...)
		task.Diagnostics.DurationMS = diagnostics.DurationMS
		task.Diagnostics.status = result.Status
		task.Diagnostics.reason = result.Reason
		result.Diagnostics = &diagnostics
	}
	result.UpdatedAt = time.Now()
	return result
}

// reportResult emits metrics and the metadata-only result log line.
func (s *InputModerationService) reportResult(task InputModerationTask, result InputModerationResult) {
	if task.Diagnostics == nil || !task.Diagnostics.processing {
		inputModerationMetrics.observe(result)
	}
	if result.Status != "pending" {
		slog.Info("input_moderation_result", "request_id", task.RequestID, "source", normalizeInputModerationSource(task.Source),
			"turn", task.TurnNumber, "status", result.Status, "reason", result.Reason, "diagnostics", result.Diagnostics)
	}
}

func normalizeModerationResultTask(task InputModerationTask) InputModerationTask {
	task.RequestID = truncateModerationText(strings.TrimSpace(task.RequestID), 128)
	task.Source = normalizeInputModerationSource(task.Source)
	return task
}
