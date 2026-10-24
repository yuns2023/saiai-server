package service

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrInputModerationCallBudget = errors.New("moderation detector call budget exhausted")

type InputModerationDiagnostics struct {
	InputChars        int `json:"input_chars"`
	InstructionChars  int `json:"instruction_chars"`
	InstructionBlocks int `json:"instruction_blocks"`
	// Bundles the instruction blocks were packed into; each is one stable check.
	InstructionBundles int `json:"instruction_bundles"`
	// The new text was checked without the instructions because together they
	// exceeded the coverage budget.
	JointSkipped     bool   `json:"joint_skipped"`
	PlannedChecks    int    `json:"planned_checks"`
	CompletedChecks  int    `json:"completed_checks"`
	CacheHits        int    `json:"cache_hits"`
	DetectorCalls    int    `json:"detector_calls"`
	ChunkSize        int    `json:"chunk_size"`
	MaxInputChars    int    `json:"max_input_chars"`
	MaxDetectorCalls int    `json:"max_detector_calls"`
	MaxPlannedChecks int    `json:"max_planned_checks"`
	DurationMS       int64  `json:"duration_ms"`
	QueueWaitMS      int64  `json:"queue_wait_ms"`
	Limit            string `json:"limit,omitempty"`
	started          time.Time
	processing       bool
	status           string
	reason           string
}

type moderationDiagnosticsKey struct{}

func WithInputModerationDiagnostics(ctx context.Context, diagnostics *InputModerationDiagnostics) context.Context {
	return context.WithValue(ctx, moderationDiagnosticsKey{}, diagnostics)
}

type InputModerationBudgetClient interface {
	UsesInputModerationCallBudget()
}

func newModerationDiagnostics(task InputModerationTask, size int) *InputModerationDiagnostics {
	if size <= 0 {
		size = 32768
	}
	diagnostics := &InputModerationDiagnostics{
		InputChars: utf8.RuneCountInString(task.Text), InstructionBlocks: len(task.Instructions),
		ChunkSize: size, MaxInputChars: size + (moderationMaxChunks-1)*(size-min(512, size/8)),
		MaxDetectorCalls: moderationMaxChunks, MaxPlannedChecks: moderationMaxPlannedChecks, started: time.Now(),
	}
	for _, instruction := range task.Instructions {
		diagnostics.InstructionChars += utf8.RuneCountInString(instruction)
	}
	if !task.EnqueuedAt.IsZero() {
		diagnostics.QueueWaitMS = time.Since(task.EnqueuedAt).Milliseconds()
		if diagnostics.QueueWaitMS < 0 {
			diagnostics.QueueWaitMS = 0
		}
	}
	return diagnostics
}

func ReserveInputModerationCall(ctx context.Context) error {
	diagnostics, _ := ctx.Value(moderationDiagnosticsKey{}).(*InputModerationDiagnostics)
	if diagnostics == nil {
		return nil
	}
	if diagnostics.DetectorCalls >= diagnostics.MaxDetectorCalls {
		diagnostics.Limit = "detector_calls"
		return ErrInputModerationCallBudget
	}
	diagnostics.DetectorCalls++
	return nil
}

func ObserveInputModerationCacheHit(ctx context.Context) {
	if diagnostics, ok := ctx.Value(moderationDiagnosticsKey{}).(*InputModerationDiagnostics); ok {
		diagnostics.CacheHits++
	}
}

type ModerationRuntimeMetrics struct {
	Since           time.Time         `json:"since"`
	Results         map[string]uint64 `json:"results"`
	CompletedChecks uint64            `json:"completed_checks"`
	CacheHits       uint64            `json:"cache_hits"`
	DetectorCalls   uint64            `json:"detector_calls"`
	DurationMS      uint64            `json:"duration_ms"`
	QueueWaitMS     uint64            `json:"queue_wait_ms"`
}

type moderationMetrics struct {
	mu sync.Mutex
	ModerationRuntimeMetrics
}

var inputModerationMetrics = moderationMetrics{ModerationRuntimeMetrics: ModerationRuntimeMetrics{Since: time.Now(), Results: map[string]uint64{}}}

func observeModerationAttempt(task InputModerationTask) {
	if task.Diagnostics == nil || task.Diagnostics.status == "" {
		return
	}
	inputModerationMetrics.observe(InputModerationResult{Status: task.Diagnostics.status, Reason: task.Diagnostics.reason, Diagnostics: task.Diagnostics})
}

func (metrics *moderationMetrics) observe(result InputModerationResult) {
	if result.Status == "pending" {
		return
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.Results[result.Status+"/"+result.Reason]++
	if diagnostics := result.Diagnostics; diagnostics != nil {
		metrics.CompletedChecks += uint64(diagnostics.CompletedChecks)
		metrics.CacheHits += uint64(diagnostics.CacheHits)
		metrics.DetectorCalls += uint64(diagnostics.DetectorCalls)
		metrics.DurationMS += uint64(diagnostics.DurationMS)
		metrics.QueueWaitMS += uint64(diagnostics.QueueWaitMS)
	}
}

func InputModerationMetricsSnapshot() ModerationRuntimeMetrics {
	inputModerationMetrics.mu.Lock()
	defer inputModerationMetrics.mu.Unlock()
	snapshot := inputModerationMetrics.ModerationRuntimeMetrics
	snapshot.Results = make(map[string]uint64, len(inputModerationMetrics.Results))
	for reason, count := range inputModerationMetrics.Results {
		snapshot.Results[reason] = count
	}
	return snapshot
}
