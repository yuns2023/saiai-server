//go:build integration

package repository

import (
	"time"

	"github.com/google/uuid"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TestPlatformFilter_AppliesToEveryAdminUsageQuery seeds one Anthropic row and
// two OpenAI rows (one served by an account that is soft-deleted afterwards)
// and checks that every query the admin usage page uses honors the platform
// of the serving account.
func (s *UsageLogRepoSuite) TestPlatformFilter_AppliesToEveryAdminUsageQuery() {
	now := time.Now().UTC().Truncate(time.Second)
	dayStart := truncateToDayUTC(now).Add(-24 * time.Hour) // yesterday: fully aggregated
	dayEnd := dayStart.Add(24 * time.Hour)

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "platform-filter@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-platform-filter", Name: "k"})
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-platform-filter"})
	anthropicAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-pf-anthropic", Platform: service.PlatformAnthropic})
	openaiAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-pf-openai", Platform: service.PlatformOpenAI})
	retiredOpenAIAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-pf-openai-deleted", Platform: service.PlatformOpenAI})

	create := func(account *service.Account, model, endpoint string, input, output int, cost float64, at time.Time) {
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID:          user.ID,
			APIKeyID:        apiKey.ID,
			AccountID:       account.ID,
			GroupID:         &group.ID,
			RequestID:       uuid.New().String(),
			Model:           model,
			InboundEndpoint: &endpoint,
			InputTokens:     input,
			OutputTokens:    output,
			TotalCost:       cost,
			ActualCost:      cost,
			CreatedAt:       at,
		})
		s.Require().NoError(err)
	}
	create(anthropicAcc, "claude-opus", "/v1/messages", 10, 20, 1.0, dayStart.Add(2*time.Hour))
	create(openaiAcc, "gpt-5", "/v1/responses", 100, 200, 2.0, dayStart.Add(3*time.Hour))
	create(retiredOpenAIAcc, "gpt-5", "/v1/responses", 1000, 2000, 4.0, dayStart.Add(4*time.Hour))

	// Soft-delete one OpenAI account: its history must stay filterable.
	s.Require().NoError(s.client.Account.DeleteOneID(retiredOpenAIAcc.ID).Exec(s.ctx))
	var deleted bool
	s.Require().NoError(scanSingleRow(s.ctx, s.tx, `SELECT deleted_at IS NOT NULL FROM accounts WHERE id = $1`, []any{retiredOpenAIAcc.ID}, &deleted))
	s.Require().True(deleted, "account must be soft-deleted, not removed")

	// Populate the pre-aggregated buckets (no account dimension) so the
	// unfiltered trend reads them and the platform-filtered one must not.
	aggRepo := newDashboardAggregationRepositoryWithSQL(s.tx)
	s.Require().NoError(aggRepo.AggregateRange(s.ctx, dayStart, dayEnd))
	s.Require().NoError(aggRepo.UpdateAggregationWatermark(s.ctx, now))

	start, end := dayStart, dayEnd
	filters := func(platform string) usagestats.UsageLogFilters {
		return usagestats.UsageLogFilters{Platform: platform, StartTime: &start, EndTime: &end, ExactTotal: true}
	}
	sumTrend := func(points []TrendDataPoint) (requests int64) {
		for _, p := range points {
			requests += p.Requests
		}
		return requests
	}
	sumModels := func(stats []ModelStat) (requests int64) {
		for _, m := range stats {
			requests += m.Requests
		}
		return requests
	}

	for _, tc := range []struct {
		platform string
		requests int64
		cost     float64
	}{
		{"", 3, 7.0},
		{service.PlatformOpenAI, 2, 6.0},
		{service.PlatformAnthropic, 1, 1.0},
		{service.PlatformGemini, 0, 0},
	} {
		name := "platform=" + tc.platform

		logs, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, filters(tc.platform))
		s.Require().NoError(err, name)
		s.Require().Len(logs, int(tc.requests), name)
		s.Require().Equal(tc.requests, page.Total, name)

		stats, err := s.repo.GetStatsWithFilters(s.ctx, filters(tc.platform))
		s.Require().NoError(err, name)
		s.Require().Equal(tc.requests, stats.TotalRequests, name)
		s.Require().InDelta(tc.cost, stats.TotalCost, 1e-9, name)
		var endpointRequests int64
		for _, e := range stats.Endpoints {
			endpointRequests += e.Requests
		}
		s.Require().Equal(tc.requests, endpointRequests, name)

		for _, g := range []string{"day", "hour"} {
			trend, err := s.repo.GetUsageTrendWithFilters(s.ctx, start, end, g, 0, 0, 0, 0, "", nil, nil, nil, "", tc.platform)
			s.Require().NoError(err, name)
			s.Require().Equal(tc.requests, sumTrend(trend), name+" "+g)
		}

		models, err := s.repo.GetModelStatsWithFilters(s.ctx, start, end, 0, 0, 0, 0, nil, nil, nil, "", tc.platform)
		s.Require().NoError(err, name)
		s.Require().Equal(tc.requests, sumModels(models), name)
		upstreamModels, err := s.repo.GetModelStatsWithFiltersBySource(s.ctx, start, end, 0, 0, 0, 0, nil, nil, nil, usagestats.ModelSourceUpstream, "", tc.platform)
		s.Require().NoError(err, name)
		s.Require().Equal(tc.requests, sumModels(upstreamModels), name)

		groups, err := s.repo.GetGroupStatsWithFilters(s.ctx, start, end, 0, 0, 0, 0, nil, nil, nil, "", tc.platform)
		s.Require().NoError(err, name)
		var groupRequests int64
		for _, g := range groups {
			groupRequests += g.Requests
		}
		s.Require().Equal(tc.requests, groupRequests, name)

		users, err := s.repo.GetUserBreakdownStats(s.ctx, start, end, usagestats.UserBreakdownDimension{GroupID: group.ID, Platform: tc.platform}, 10)
		s.Require().NoError(err, name)
		var userRequests int64
		for _, u := range users {
			userRequests += u.Requests
		}
		s.Require().Equal(tc.requests, userRequests, name)
	}

	// The default (fast, inexact total) list path filters too.
	fast := filters(service.PlatformOpenAI)
	fast.ExactTotal = false
	logs, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, fast)
	s.Require().NoError(err)
	s.Require().Len(logs, 2)
	for _, log := range logs {
		s.Require().NotEqual(anthropicAcc.ID, log.AccountID)
	}

	// The platform filter composes with other filters (AND, not OR).
	combined := filters(service.PlatformOpenAI)
	combined.AccountID = anthropicAcc.ID
	stats, err := s.repo.GetStatsWithFilters(s.ctx, combined)
	s.Require().NoError(err)
	s.Require().Equal(int64(0), stats.TotalRequests)
}
