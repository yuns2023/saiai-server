package usagestats

import "time"

// Usage analytics dimensions accepted by the user analytics endpoint. The
// repository maps each to a fixed SQL expression; no request value reaches SQL.
const (
	UsageAnalyticsByModel           = "model"
	UsageAnalyticsByReasoningEffort = "reasoning_effort"
	UsageAnalyticsByAPIKey          = "api_key"
	UsageAnalyticsByAPIKeyModel     = "api_key_model"
)

// Usage analytics sort orders.
const (
	UsageAnalyticsSortActualCost = "actual_cost_desc"
	UsageAnalyticsSortCost       = "cost_desc"
	UsageAnalyticsSortUsageCost  = "usage_cost_desc"
	UsageAnalyticsSortTokens     = "tokens_desc"
	UsageAnalyticsSortRequests   = "requests_desc"
)

// UsageAnalyticsQuery is a user-scoped aggregate over usage_logs in the
// half-open range [StartTime, EndTime). UserID must be positive; every other
// filter only narrows that user's records.
type UsageAnalyticsQuery struct {
	UserID    int64
	StartTime time.Time
	EndTime   time.Time
	GroupBy   string
	Sort      string
	Page      int
	PageSize  int

	APIKeyID    int64
	GroupID     int64
	Model       string
	RequestType *int16
	BillingType *int8
	// ReasoningEffort matches the recorded (trimmed) value. ReasoningEffortMissing
	// selects records without one; the two are mutually exclusive.
	ReasoningEffort        string
	ReasoningEffortMissing bool
}

// UsageAnalyticsMetrics are summed from recorded usage, never re-priced.
// Cost is the standard (pre-multiplier) cost, ActualCost the user-billed
// amount (balance or subscription quota) and UsageCost the reference usage
// COALESCE(metered_cost, actual_cost): hosted records contribute their metered
// reference, all others their actual cost.
type UsageAnalyticsMetrics struct {
	Requests            int64   `json:"requests"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	Cost                float64 `json:"cost"`
	ActualCost          float64 `json:"actual_cost"`
	UsageCost           float64 `json:"usage_cost"`
}

// UsageAnalyticsTokenMix is each token class's fraction of the row's total
// tokens. Fields are nil when the row has no tokens.
type UsageAnalyticsTokenMix struct {
	Input         *float64 `json:"input"`
	Output        *float64 `json:"output"`
	CacheCreation *float64 `json:"cache_creation"`
	CacheRead     *float64 `json:"cache_read"`
}

// UsageAnalyticsShare is a row's fraction of the whole filtered result (all
// pages). Fields are nil when the result total is zero.
type UsageAnalyticsShare struct {
	Requests   *float64 `json:"requests"`
	Tokens     *float64 `json:"tokens"`
	Cost       *float64 `json:"cost"`
	ActualCost *float64 `json:"actual_cost"`
	UsageCost  *float64 `json:"usage_cost"`
}

// UsageAnalyticsItem is one group. Only the fields of the requested dimension
// are set. A record without a reasoning effort is reported with
// ReasoningEffortMissing=true. APIKeyName is the Key's current name; the raw
// Key value is never selected.
type UsageAnalyticsItem struct {
	Model                  string `json:"model,omitempty"`
	ReasoningEffort        string `json:"reasoning_effort,omitempty"`
	ReasoningEffortMissing bool   `json:"reasoning_effort_missing,omitempty"`
	APIKeyID               int64  `json:"api_key_id,omitempty"`
	APIKeyName             string `json:"api_key_name,omitempty"`
	APIKeyDeleted          bool   `json:"api_key_deleted,omitempty"`
	UsageAnalyticsMetrics
	Share    UsageAnalyticsShare    `json:"share"`
	TokenMix UsageAnalyticsTokenMix `json:"token_mix"`
}

// UsageAnalyticsSummary covers every matching record, not only the page.
type UsageAnalyticsSummary struct {
	UsageAnalyticsMetrics
	TokenMix UsageAnalyticsTokenMix `json:"token_mix"`
}

// UsageAnalyticsResult is one consistent aggregate: Total (number of groups),
// Summary and the page of Items come from the same query.
type UsageAnalyticsResult struct {
	Items   []UsageAnalyticsItem  `json:"items"`
	Total   int64                 `json:"total"`
	Summary UsageAnalyticsSummary `json:"summary"`
}

// Finalize fills shares and token mixes from the integer and summed totals.
func (r *UsageAnalyticsResult) Finalize() {
	r.Summary.TokenMix = usageAnalyticsTokenMix(r.Summary.UsageAnalyticsMetrics)
	s := r.Summary.UsageAnalyticsMetrics
	for i := range r.Items {
		m := r.Items[i].UsageAnalyticsMetrics
		r.Items[i].TokenMix = usageAnalyticsTokenMix(m)
		r.Items[i].Share = UsageAnalyticsShare{
			Requests:   usageAnalyticsRatio(float64(m.Requests), float64(s.Requests)),
			Tokens:     usageAnalyticsRatio(float64(m.TotalTokens), float64(s.TotalTokens)),
			Cost:       usageAnalyticsRatio(m.Cost, s.Cost),
			ActualCost: usageAnalyticsRatio(m.ActualCost, s.ActualCost),
			UsageCost:  usageAnalyticsRatio(m.UsageCost, s.UsageCost),
		}
	}
}

func usageAnalyticsTokenMix(m UsageAnalyticsMetrics) UsageAnalyticsTokenMix {
	total := float64(m.TotalTokens)
	return UsageAnalyticsTokenMix{
		Input:         usageAnalyticsRatio(float64(m.InputTokens), total),
		Output:        usageAnalyticsRatio(float64(m.OutputTokens), total),
		CacheCreation: usageAnalyticsRatio(float64(m.CacheCreationTokens), total),
		CacheRead:     usageAnalyticsRatio(float64(m.CacheReadTokens), total),
	}
}

func usageAnalyticsRatio(part, total float64) *float64 {
	if total <= 0 {
		return nil
	}
	v := part / total
	return &v
}

// UsageRecordPosition is a keyset position in (created_at DESC, id DESC)
// order: the last record already returned.
type UsageRecordPosition struct {
	CreatedAt time.Time
	ID        int64
}
