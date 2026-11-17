package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Admin reconciliation of shadow settlement events against usage_logs.
// Events are best effort and usage_logs are best effort (and have a retention
// period), so differences are findings to inspect, not proof of lost money.
// Coverage: sales identified at settlement time as hosted or owned. Sales
// recorded before the new version was fully live are not covered; the
// administrator passes that moment as coverage_start.

const (
	SettlementReconcileMaxWindow       = 7 * 24 * time.Hour
	SettlementReconcileDefaultPageSize = 100
	SettlementReconcileMaxPageSize     = 500
	SettlementReconcileMaxPage         = 1000
	// SettlementReconcileLogGrace is how long a missing usage log counts as
	// pending (usage logs are written after billing commits).
	SettlementReconcileLogGrace = 10 * time.Minute

	SettlementReconcileStatusOK            = "ok"
	SettlementReconcileStatusNotApplicable = "not_applicable"

	SettlementCoverageCovered = "covered"
	SettlementCoveragePartial = "partial"
	SettlementCoverageUnknown = "coverage_unknown"

	SettlementDiffEventOnly      = "event_only"
	SettlementDiffLogOnly        = "log_only"
	SettlementDiffAmountMismatch = "amount_mismatch"
	SettlementDiffOwnerMismatch  = "owner_mismatch"

	SettlementDiffStatusPending         = "pending"
	SettlementDiffStatusMissingLog      = "missing_log"
	SettlementDiffStatusMissingEvent    = "missing_event"
	SettlementDiffStatusCoverageUnknown = "coverage_unknown"
	SettlementDiffStatusMismatch        = "mismatch"
	// SettlementDiffStatusOwnerTiming is a hint, not an error: the event reads
	// the owner inside the billing transaction, the usage log when it is
	// inserted afterwards.
	SettlementDiffStatusOwnerTiming = "possible_owner_timing_difference"
)

var (
	ErrSettlementReconcileInvalidWindow = infraerrors.BadRequest("INVALID_RECONCILE_WINDOW", "from and to are required RFC3339 times with from < to and a window of at most 7 days")
	ErrSettlementReconcileCoverage      = infraerrors.BadRequest("INVALID_RECONCILE_COVERAGE_START", "coverage_start is a required RFC3339 time")
	ErrSettlementReconcileInvalidPage   = infraerrors.BadRequest("INVALID_RECONCILE_PAGE", "page must be 1-1000 and page_size 1-500")
	// ErrSettlementReconcileTimeout is a cancelled reconcile query (normally
	// the database-side statement timeout): narrow the window and retry.
	ErrSettlementReconcileTimeout = infraerrors.ServiceUnavailable("SETTLEMENT_RECONCILE_TIMEOUT", "settlement reconcile query timed out; narrow the time window and retry")
)

type SettlementReconcileQuery struct {
	From          time.Time
	To            time.Time
	CoverageStart time.Time
	Page          int
	PageSize      int
	// Now is set by the service (grace evaluation).
	Now time.Time
}

type SettlementReconcileSummaryRow struct {
	Category           string  `json:"category"`
	GroupID            *int64  `json:"group_id"`
	ChargeSource       string  `json:"charge_source"`
	EventCount         int64   `json:"event_count"`
	AnomalyCount       int64   `json:"anomaly_count"`
	InvalidAmountCount int64   `json:"invalid_amount_count"`
	StandardCost       float64 `json:"standard_cost"`
	ChargeInput        float64 `json:"charge_input"`
}

type SettlementReconcileDiffCount struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

type SettlementReconcileDiff struct {
	RequestID         string     `json:"request_id"`
	APIKeyID          int64      `json:"api_key_id"`
	Kind              string     `json:"kind"`
	Status            string     `json:"status"`
	OwnerDiffers      bool       `json:"owner_differs"`
	Category          *string    `json:"category"`
	EventGroupID      *int64     `json:"event_group_id"`
	LogGroupID        *int64     `json:"log_group_id"`
	AccountID         *int64     `json:"account_id"`
	EventOwnerUserID  *int64     `json:"event_owner_user_id"`
	LogOwnerUserID    *int64     `json:"log_owner_user_id"`
	EventStandardCost *float64   `json:"event_standard_cost"`
	LogTotalCost      *float64   `json:"log_total_cost"`
	EventChargeInput  *float64   `json:"event_charge_input"`
	LogActualCost     *float64   `json:"log_actual_cost"`
	InvalidFields     []string   `json:"invalid_fields"`
	EventCreatedAt    *time.Time `json:"event_created_at"`
	LogCreatedAt      *time.Time `json:"log_created_at"`
}

type SettlementReconcileResult struct {
	Status        string                          `json:"status"`
	Coverage      string                          `json:"coverage,omitempty"`
	From          time.Time                       `json:"from"`
	To            time.Time                       `json:"to"`
	CoverageStart time.Time                       `json:"coverage_start"`
	Summary       []SettlementReconcileSummaryRow `json:"summary"`
	DiffCounts    []SettlementReconcileDiffCount  `json:"diff_counts"`
	Diffs         []SettlementReconcileDiff       `json:"diffs"`
	Total         int64                           `json:"total"`
	Page          int                             `json:"page"`
	PageSize      int                             `json:"page_size"`
	// ProcessMetrics are counters of this server process only.
	ProcessMetrics UsageSettlementMetricsSnapshot `json:"process_metrics"`
}

// SettlementReconcileData is one consistent read of the reconcile view.
type SettlementReconcileData struct {
	Summary []SettlementReconcileSummaryRow
	Diffs   []SettlementReconcileDiff
	Counts  []SettlementReconcileDiffCount
	Total   int64
}

type SettlementReconcileRepository interface {
	// Reconcile reads everything in one read-only snapshot with a
	// database-side statement timeout (ErrSettlementReconcileTimeout).
	Reconcile(ctx context.Context, q SettlementReconcileQuery) (*SettlementReconcileData, error)
}

type SettlementReconcileService struct {
	repo SettlementReconcileRepository
	cfg  *config.Config
	now  func() time.Time
}

func NewSettlementReconcileService(repo SettlementReconcileRepository, cfg *config.Config) *SettlementReconcileService {
	return &SettlementReconcileService{repo: repo, cfg: cfg, now: time.Now}
}

func (s *SettlementReconcileService) Reconcile(ctx context.Context, q SettlementReconcileQuery) (*SettlementReconcileResult, error) {
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		// Simple mode never bills, so there is nothing to reconcile.
		return &SettlementReconcileResult{Status: SettlementReconcileStatusNotApplicable, Summary: []SettlementReconcileSummaryRow{}, DiffCounts: []SettlementReconcileDiffCount{}, Diffs: []SettlementReconcileDiff{}}, nil
	}
	if q.From.IsZero() || q.To.IsZero() || !q.From.Before(q.To) || q.To.Sub(q.From) > SettlementReconcileMaxWindow {
		return nil, ErrSettlementReconcileInvalidWindow
	}
	if q.CoverageStart.IsZero() {
		return nil, ErrSettlementReconcileCoverage
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = SettlementReconcileDefaultPageSize
	}
	if q.Page < 1 || q.Page > SettlementReconcileMaxPage || q.PageSize < 1 || q.PageSize > SettlementReconcileMaxPageSize {
		return nil, ErrSettlementReconcileInvalidPage
	}
	if s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SETTLEMENT_RECONCILE_UNAVAILABLE", "settlement reconcile unavailable")
	}
	q.Now = s.now()
	data, err := s.repo.Reconcile(ctx, q)
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = &SettlementReconcileData{}
	}
	summary, diffs, counts, total := data.Summary, data.Diffs, data.Counts, data.Total
	if summary == nil {
		summary = []SettlementReconcileSummaryRow{}
	}
	if counts == nil {
		counts = []SettlementReconcileDiffCount{}
	}
	if diffs == nil {
		diffs = []SettlementReconcileDiff{}
	}
	coverage := SettlementCoveragePartial
	switch {
	case !q.CoverageStart.After(q.From):
		coverage = SettlementCoverageCovered
	case !q.CoverageStart.Before(q.To):
		coverage = SettlementCoverageUnknown
	}
	return &SettlementReconcileResult{
		Status:         SettlementReconcileStatusOK,
		Coverage:       coverage,
		From:           q.From,
		To:             q.To,
		CoverageStart:  q.CoverageStart,
		Summary:        summary,
		DiffCounts:     counts,
		Diffs:          diffs,
		Total:          total,
		Page:           q.Page,
		PageSize:       q.PageSize,
		ProcessMetrics: GetUsageSettlementMetricsSnapshot(),
	}, nil
}
