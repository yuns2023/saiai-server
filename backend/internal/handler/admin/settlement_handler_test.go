package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type settlementReconcileRepoStub struct {
	calls int
	lastQ service.SettlementReconcileQuery
	err   error
}

func (s *settlementReconcileRepoStub) Reconcile(_ context.Context, q service.SettlementReconcileQuery) (*service.SettlementReconcileData, error) {
	s.calls++
	s.lastQ = q
	if s.err != nil {
		return nil, s.err
	}
	group := int64(3)
	cat := service.SettlementCategoryHostedSelf
	return &service.SettlementReconcileData{
		Summary: []service.SettlementReconcileSummaryRow{{Category: service.SettlementCategoryHostedSelf, GroupID: &group, ChargeSource: service.SettlementChargeSourceBalance, EventCount: 2, StandardCost: 1, ChargeInput: 0.2}},
		Diffs:   []service.SettlementReconcileDiff{{RequestID: "r1", APIKeyID: 9, Kind: service.SettlementDiffLogOnly, Status: service.SettlementDiffStatusCoverageUnknown, Category: &cat}},
		Counts:  []service.SettlementReconcileDiffCount{{Kind: service.SettlementDiffLogOnly, Status: service.SettlementDiffStatusCoverageUnknown, Count: 1}},
		Total:   1,
		Market: &service.SettlementMarketReconcile{
			Summary:     service.SettlementMarketSummary{SaleCount: 1, ChargedAmount: "1.5", OwnerEarning: "1.2", PlatformAmount: "0.3", AccruedAmount: "1.2", ReversedAmount: "0", ConvertedAmount: "0"},
			IssueCounts: []service.SettlementReconcileDiffCount{{Kind: service.SettlementMarketIssueAdmissionWithoutEvent, Status: service.SettlementMarketStatusUndetermined, Count: 1}},
			Issues:      []service.SettlementMarketIssue{{Kind: service.SettlementMarketIssueAdmissionWithoutEvent, Status: service.SettlementMarketStatusUndetermined}},
		},
	}, nil
}

func newSettlementRouter(repo service.SettlementReconcileRepository, cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewSettlementHandler(service.NewSettlementReconcileService(repo, cfg))
	r.GET("/reconcile", h.Reconcile)
	return r
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestSettlementReconcileJSONFieldNames(t *testing.T) {
	repo := &settlementReconcileRepoStub{}
	r := newSettlementRouter(repo, &config.Config{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reconcile?from=2026-11-10T00:00:00Z&to=2026-11-12T00:00:00Z&coverage_start=2026-11-11T00:00:00Z&page=2&page_size=50", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, []string{"coverage", "coverage_start", "diff_counts", "diffs", "from", "market", "page", "page_size", "process_metrics", "status", "summary", "to", "total"}, sortedKeys(body.Data))
	require.Equal(t, "ok", body.Data["status"])
	market, ok := body.Data["market"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"issue_counts", "issues", "scope", "summary"}, sortedKeys(market))
	require.Equal(t, "owner_backed_sales", market["scope"])
	marketSummary, ok := market["summary"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"accrued_amount", "charged_amount", "converted_amount", "earning_sale_count", "owner_earning", "platform_amount", "reversed_amount", "sale_count"}, sortedKeys(marketSummary))
	issues, ok := market["issues"].([]any)
	require.True(t, ok)
	issue, ok := issues[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"account_id", "admission_id", "at", "entry_id", "event_id", "kind", "owner_user_id", "request_id", "status"}, sortedKeys(issue))
	require.Equal(t, "partial", body.Data["coverage"])
	summary, ok := body.Data["summary"].([]any)
	require.True(t, ok)
	row, ok := summary[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"anomaly_count", "category", "charge_input", "charge_source", "event_count", "group_id", "invalid_amount_count", "standard_cost"}, sortedKeys(row))
	diffs, ok := body.Data["diffs"].([]any)
	require.True(t, ok)
	diff, ok := diffs[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"account_id", "api_key_id", "category", "event_charge_input", "event_created_at", "event_group_id", "event_owner_user_id", "event_standard_cost", "invalid_fields", "kind", "log_actual_cost", "log_created_at", "log_group_id", "log_owner_user_id", "log_total_cost", "owner_differs", "request_id", "status"}, sortedKeys(diff))
	counts, ok := body.Data["diff_counts"].([]any)
	require.True(t, ok)
	count, ok := counts[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"count", "kind", "status"}, sortedKeys(count))
	metrics, ok := body.Data["process_metrics"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"market_apply_rolled_back", "market_commit_unknown", "market_integrity_errors", "market_not_enqueued", "skipped_low_budget", "write_failures"}, sortedKeys(metrics))
	require.Equal(t, 2, repo.lastQ.Page)
	require.Equal(t, 50, repo.lastQ.PageSize)
	require.False(t, repo.lastQ.Now.IsZero())
}

func TestSettlementReconcileCoverage(t *testing.T) {
	cases := map[string]string{
		"2026-11-10T00:00:00Z": "covered",
		"2026-11-09T00:00:00Z": "covered",
		"2026-11-11T00:00:00Z": "partial",
		"2026-11-12T00:00:00Z": "coverage_unknown",
		"2026-11-20T00:00:00Z": "coverage_unknown",
	}
	for coverageStart, want := range cases {
		r := newSettlementRouter(&settlementReconcileRepoStub{}, &config.Config{})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reconcile?from=2026-11-10T00:00:00Z&to=2026-11-12T00:00:00Z&coverage_start="+coverageStart, nil))
		require.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Data struct {
				Coverage string `json:"coverage"`
				Page     int    `json:"page"`
				PageSize int    `json:"page_size"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, want, body.Data.Coverage, coverageStart)
		require.Equal(t, 1, body.Data.Page)
		require.Equal(t, service.SettlementReconcileDefaultPageSize, body.Data.PageSize)
	}
}

func TestSettlementReconcileRejectsBadWindowsAndPages(t *testing.T) {
	const ok = "from=2026-11-10T00:00:00Z&to=2026-11-12T00:00:00Z&coverage_start=2026-11-10T00:00:00Z"
	bad := []string{
		"to=2026-11-12T00:00:00Z&coverage_start=2026-11-10T00:00:00Z",
		"from=2026-11-10T00:00:00Z&coverage_start=2026-11-10T00:00:00Z",
		"from=2026-11-10T00:00:00Z&to=2026-11-12T00:00:00Z",
		"from=2026-11-12T00:00:00Z&to=2026-11-10T00:00:00Z&coverage_start=2026-11-10T00:00:00Z",
		"from=2026-11-10T00:00:00Z&to=2026-11-10T00:00:00Z&coverage_start=2026-11-10T00:00:00Z",
		"from=2026-11-01T00:00:00Z&to=2026-11-08T00:00:01Z&coverage_start=2026-11-10T00:00:00Z",
		"from=yesterday&to=2026-11-12T00:00:00Z&coverage_start=2026-11-10T00:00:00Z",
		ok + "&page=0",
		ok + "&page=-1",
		ok + "&page=1001",
		ok + "&page_size=501",
		ok + "&page_size=abc",
		ok + "&page=99999999999999999999",
	}
	for _, q := range bad {
		repo := &settlementReconcileRepoStub{}
		r := newSettlementRouter(repo, &config.Config{})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reconcile?"+q, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, q)
		require.Zero(t, repo.calls, q)
	}
	// Exactly 7 days, max page and max page size are accepted.
	r := newSettlementRouter(&settlementReconcileRepoStub{}, &config.Config{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reconcile?from=2026-11-01T00:00:00Z&to=2026-11-08T00:00:00Z&coverage_start=2026-11-01T00:00:00Z&page=1000&page_size=500", nil))
	require.Equal(t, http.StatusOK, w.Code)
}

func TestSettlementReconcileSimpleModeNotApplicable(t *testing.T) {
	repo := &settlementReconcileRepoStub{}
	r := newSettlementRouter(repo, &config.Config{RunMode: config.RunModeSimple})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reconcile", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "not_applicable", body.Data["status"])
	require.Zero(t, repo.calls)
}

func TestSettlementReconcileTimeoutIs503(t *testing.T) {
	r := newSettlementRouter(&settlementReconcileRepoStub{err: service.ErrSettlementReconcileTimeout}, &config.Config{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reconcile?from=2026-11-10T00:00:00Z&to=2026-11-12T00:00:00Z&coverage_start=2026-11-10T00:00:00Z", nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "SETTLEMENT_RECONCILE_TIMEOUT")
}
