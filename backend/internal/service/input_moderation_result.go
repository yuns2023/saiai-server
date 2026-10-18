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
	Status        string             `json:"status"`
	Reason        string             `json:"reason,omitempty"`
	Scores        map[string]float64 `json:"scores,omitempty"`
	MinConfidence int                `json:"min_confidence"`
	ModelVersion  string             `json:"model_version,omitempty"`
	UpdatedAt     time.Time          `json:"updated_at"`
}
type InputModerationResultRepository interface {
	SaveInputModerationResult(context.Context, InputModerationTask, InputModerationResult) error
}

func (s *InputModerationService) recordResult(task InputModerationTask, result InputModerationResult) error {
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
