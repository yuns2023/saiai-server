package usagestats

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageAnalyticsResultFinalizeAndJSON(t *testing.T) {
	r := UsageAnalyticsResult{
		Items: []UsageAnalyticsItem{
			{Model: "a", UsageAnalyticsMetrics: UsageAnalyticsMetrics{Requests: 3, InputTokens: 30, OutputTokens: 10, TotalTokens: 40, Cost: 3, ActualCost: 0, UsageCost: 3}},
			{Model: "b", UsageAnalyticsMetrics: UsageAnalyticsMetrics{Requests: 1}},
		},
		Total:   2,
		Summary: UsageAnalyticsSummary{UsageAnalyticsMetrics: UsageAnalyticsMetrics{Requests: 4, InputTokens: 30, OutputTokens: 10, TotalTokens: 40, Cost: 3, UsageCost: 3}},
	}
	r.Finalize()

	a := r.Items[0]
	require.InDelta(t, 0.75, *a.Share.Requests, 1e-12)
	require.InDelta(t, 1.0, *a.Share.Tokens, 1e-12)
	require.InDelta(t, 1.0, *a.Share.UsageCost, 1e-12)
	require.Nil(t, a.Share.ActualCost, "zero denominator is null")
	require.InDelta(t, 0.75, *a.TokenMix.Input, 1e-12)
	require.InDelta(t, 0.0, *a.TokenMix.CacheRead, 1e-12)
	b := r.Items[1]
	require.InDelta(t, 0.0, *b.Share.Tokens, 1e-12, "zero numerator with a non-zero denominator is 0")
	require.Nil(t, b.TokenMix.Input, "a row without tokens has no mix")

	raw, err := json.Marshal(r)
	require.NoError(t, err)
	var decoded struct {
		Items   []map[string]json.RawMessage `json:"items"`
		Summary struct {
			Requests int64                  `json:"requests"`
			TokenMix UsageAnalyticsTokenMix `json:"token_mix"`
		} `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	item := decoded.Items[1]
	require.NotContains(t, item, "UsageAnalyticsMetrics", "metrics are flattened")
	require.JSONEq(t, "1", string(item["requests"]))
	require.JSONEq(t, "0", string(item["total_tokens"]), "zero metrics are present")
	var share map[string]*float64
	require.NoError(t, json.Unmarshal(item["share"], &share))
	require.Contains(t, share, "actual_cost")
	require.Nil(t, share["actual_cost"])
	require.NotContains(t, item, "api_key_id")
	require.EqualValues(t, 4, decoded.Summary.Requests)
	require.InDelta(t, 0.25, *decoded.Summary.TokenMix.Output, 1e-12)

	empty, err := json.Marshal(UsageAnalyticsResult{Items: []UsageAnalyticsItem{}})
	require.NoError(t, err)
	require.Contains(t, string(empty), `"items":[]`)
}
