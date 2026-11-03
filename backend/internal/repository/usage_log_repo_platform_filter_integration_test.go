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

type platformFilterRow struct {
	account     *service.Account
	groupID     int64
	model       string
	upstream    string
	session     string
	requestType service.RequestType
	billingType int8
	at          time.Time
}

func (s *UsageLogRepoSuite) createPlatformFilterRow(user *service.User, apiKey *service.APIKey, row platformFilterRow) int64 {
	inbound, upstreamEndpoint := "/v1/responses", "/v1/responses"
	log := &service.UsageLog{
		UserID:           user.ID,
		APIKeyID:         apiKey.ID,
		AccountID:        row.account.ID,
		GroupID:          &row.groupID,
		RequestID:        uuid.New().String(),
		SessionID:        &row.session,
		Model:            row.model,
		InboundEndpoint:  &inbound,
		UpstreamEndpoint: &upstreamEndpoint,
		RequestType:      row.requestType,
		Stream:           row.requestType == service.RequestTypeStream,
		BillingType:      row.billingType,
		InputTokens:      10,
		OutputTokens:     20,
		TotalCost:        1,
		ActualCost:       1,
		CreatedAt:        row.at,
	}
	if row.upstream != "" {
		log.UpstreamModel = &row.upstream
	}
	_, err := s.repo.Create(s.ctx, log)
	s.Require().NoError(err)
	return log.ID
}

// TestPlatformFilter_CombinedWithEveryOtherFilter sets platform together with
// session, model, group/account, request type and billing type. Each
// distractor row differs from the target in exactly one dimension, so a
// swapped trailing sessionID/platform argument (both strings) or a dropped
// filter returns the wrong count.
func (s *UsageLogRepoSuite) TestPlatformFilter_CombinedWithEveryOtherFilter() {
	base := time.Date(2019, 6, 3, 10, 0, 0, 0, time.UTC)
	start, end := base.Add(-time.Hour), base.Add(3*time.Hour)

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "platform-combined@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-platform-combined", Name: "k"})
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-platform-combined"})
	otherGroup := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-platform-combined-other"})
	anthropicAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-pc-anthropic", Platform: service.PlatformAnthropic})
	openaiAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-pc-openai", Platform: service.PlatformOpenAI})

	const session = "sess-platform-combined-0001"
	target := platformFilterRow{
		account: openaiAcc, groupID: group.ID, model: "gpt-5", upstream: "gpt-5-codex", session: session,
		requestType: service.RequestTypeStream, billingType: service.BillingTypeSubscription, at: base,
	}
	s.createPlatformFilterRow(user, apiKey, target)
	distractors := []func(r *platformFilterRow){
		func(r *platformFilterRow) { r.account = anthropicAcc },
		func(r *platformFilterRow) { r.session = "sess-platform-combined-0002" },
		func(r *platformFilterRow) { r.model, r.upstream = "gpt-4.1", "" },
		func(r *platformFilterRow) { r.groupID = otherGroup.ID },
		func(r *platformFilterRow) { r.requestType = service.RequestTypeSync },
		func(r *platformFilterRow) { r.billingType = service.BillingTypeBalance },
	}
	for i, mutate := range distractors {
		row := target
		row.at = base.Add(time.Duration(i+1) * time.Minute)
		mutate(&row)
		s.createPlatformFilterRow(user, apiKey, row)
	}

	requestType := int16(service.RequestTypeStream)
	billingType := service.BillingTypeSubscription
	platform := service.PlatformOpenAI

	// Group + every other filter.
	trend, err := s.repo.GetUsageTrendWithFilters(s.ctx, start, end, "hour", 0, 0, 0, group.ID, "gpt-5", &requestType, nil, &billingType, session, platform)
	s.Require().NoError(err)
	var trendRequests int64
	for _, p := range trend {
		trendRequests += p.Requests
	}
	s.Require().Equal(int64(1), trendRequests, "trend")

	// Model stats take no model filter, so the gpt-4.1 distractor is its own row.
	byModel := func(stats []ModelStat) map[string]int64 {
		out := map[string]int64{}
		for _, m := range stats {
			out[m.Model] = m.Requests
		}
		return out
	}
	requested, err := s.repo.GetModelStatsWithFilters(s.ctx, start, end, 0, 0, 0, group.ID, &requestType, nil, &billingType, session, platform)
	s.Require().NoError(err)
	s.Require().Equal(map[string]int64{"gpt-5": 1, "gpt-4.1": 1}, byModel(requested), "requested model stats")
	mapping, err := s.repo.GetModelStatsWithFiltersBySource(s.ctx, start, end, 0, 0, 0, group.ID, &requestType, nil, &billingType, usagestats.ModelSourceMapping, session, platform)
	s.Require().NoError(err)
	s.Require().Equal(map[string]int64{"gpt-5 -> gpt-5-codex": 1, "gpt-4.1 -> gpt-4.1": 1}, byModel(mapping), "mapping model stats")

	groups, err := s.repo.GetGroupStatsWithFilters(s.ctx, start, end, 0, 0, 0, group.ID, &requestType, nil, &billingType, session, platform)
	s.Require().NoError(err)
	s.Require().Len(groups, 1)
	s.Require().Equal(group.ID, groups[0].GroupID)
	s.Require().Equal(int64(2), groups[0].Requests, "group stats (target + model distractor)")

	sumEndpoints := func(stats []EndpointStat, err error) int64 {
		s.Require().NoError(err)
		var n int64
		for _, e := range stats {
			n += e.Requests
		}
		return n
	}
	s.Require().Equal(int64(1), sumEndpoints(s.repo.GetEndpointStatsWithFilters(s.ctx, start, end, 0, 0, 0, group.ID, "gpt-5", &requestType, nil, &billingType, session, platform)), "inbound endpoints")
	s.Require().Equal(int64(1), sumEndpoints(s.repo.GetUpstreamEndpointStatsWithFilters(s.ctx, start, end, 0, 0, 0, group.ID, "gpt-5", &requestType, nil, &billingType, session, platform)), "upstream endpoints")
	s.Require().Equal(int64(1), sumEndpoints(s.repo.getEndpointPathStatsWithFilters(s.ctx, start, end, 0, 0, 0, group.ID, "gpt-5", &requestType, nil, &billingType, session, platform)), "endpoint paths")

	users, err := s.repo.GetUserBreakdownStats(s.ctx, start, end, usagestats.UserBreakdownDimension{
		GroupID: group.ID, Platform: platform, ModelFilter: "gpt-5", SessionID: session,
		RequestType: &requestType, BillingType: &billingType,
	}, 10)
	s.Require().NoError(err)
	s.Require().Len(users, 1)
	s.Require().Equal(int64(1), users[0].Requests, "user breakdown")

	filters := usagestats.UsageLogFilters{
		GroupID: group.ID, Platform: platform, Model: "gpt-5", SessionID: session,
		RequestType: &requestType, BillingType: &billingType, StartTime: &start, EndTime: &end,
	}
	logs, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, filters)
	s.Require().NoError(err)
	s.Require().Len(logs, 1, "list")
	s.Require().Equal(openaiAcc.ID, logs[0].AccountID)
	stats, err := s.repo.GetStatsWithFilters(s.ctx, filters)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), stats.TotalRequests, "stats")
	var statsEndpoints int64
	for _, e := range stats.EndpointPaths {
		statsEndpoints += e.Requests
	}
	s.Require().Equal(int64(1), statsEndpoints, "stats endpoint paths")

	// Account instead of group: platform still ANDs with the account. Without
	// the group filter the other-group distractor matches as well.
	for _, tc := range []struct {
		platform string
		want     int64
	}{{service.PlatformOpenAI, 2}, {service.PlatformAnthropic, 0}} {
		trend, err := s.repo.GetUsageTrendWithFilters(s.ctx, start, end, "day", 0, 0, openaiAcc.ID, 0, "gpt-5", &requestType, nil, &billingType, session, tc.platform)
		s.Require().NoError(err)
		var n int64
		for _, p := range trend {
			n += p.Requests
		}
		s.Require().Equal(tc.want, n, "account trend platform=%s", tc.platform)
		s.Require().Equal(tc.want, sumEndpoints(s.repo.GetEndpointStatsWithFilters(s.ctx, start, end, 0, 0, openaiAcc.ID, 0, "gpt-5", &requestType, nil, &billingType, session, tc.platform)), "account endpoints platform=%s", tc.platform)
	}
}

// TestPlatformFilter_AccountSwitchAcrossPlatforms: a session served by an
// Anthropic and an OpenAI account still counts as switched when the list is
// narrowed to one platform, but only that platform's row is returned.
func (s *UsageLogRepoSuite) TestPlatformFilter_AccountSwitchAcrossPlatforms() {
	base := time.Date(2019, 6, 4, 10, 0, 0, 0, time.UTC)
	start, end := base.Add(-time.Hour), base.Add(time.Hour)

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "platform-switch@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-platform-switch", Name: "k"})
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-platform-switch"})
	anthropicAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-ps-anthropic", Platform: service.PlatformAnthropic})
	openaiAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-ps-openai", Platform: service.PlatformOpenAI})

	row := platformFilterRow{groupID: group.ID, model: "gpt-5", requestType: service.RequestTypeStream}
	switched := row
	switched.account, switched.session, switched.at = anthropicAcc, "sess-platform-switch-0001", base
	s.createPlatformFilterRow(user, apiKey, switched)
	switched.account, switched.at = openaiAcc, base.Add(time.Minute)
	openaiSwitchedID := s.createPlatformFilterRow(user, apiKey, switched)
	solo := row
	solo.account, solo.session, solo.at = openaiAcc, "sess-platform-solo-0001", base.Add(2*time.Minute)
	s.createPlatformFilterRow(user, apiKey, solo)

	filters := func(platform string, accountSwitch bool) usagestats.UsageLogFilters {
		return usagestats.UsageLogFilters{GroupID: group.ID, Platform: platform, AccountSwitch: accountSwitch, StartTime: &start, EndTime: &end, ExactTotal: true}
	}

	logs, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, filters(service.PlatformOpenAI, true))
	s.Require().NoError(err)
	s.Require().Len(logs, 1)
	s.Require().Equal(openaiSwitchedID, logs[0].ID)
	s.Require().Equal(2, logs[0].SessionAccountCount, "the session still used two accounts")
	stats, err := s.repo.GetStatsWithFilters(s.ctx, filters(service.PlatformOpenAI, true))
	s.Require().NoError(err)
	s.Require().Equal(int64(1), stats.TotalRequests)

	logs, _, err = s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, filters(service.PlatformAnthropic, true))
	s.Require().NoError(err)
	s.Require().Len(logs, 1)
	s.Require().Equal(anthropicAcc.ID, logs[0].AccountID)

	logs, _, err = s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, filters(service.PlatformOpenAI, false))
	s.Require().NoError(err)
	s.Require().Len(logs, 2, "without account_switch both OpenAI rows match")
}

// TestPlatformFilter_FastPaginationAcrossPages: on the inexact-total list path
// every page holds only the selected platform and hasMore is reported until
// the last matching row.
func (s *UsageLogRepoSuite) TestPlatformFilter_FastPaginationAcrossPages() {
	base := time.Date(2019, 6, 5, 10, 0, 0, 0, time.UTC)
	start, end := base.Add(-time.Hour), base.Add(time.Hour)

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "platform-pages@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-platform-pages", Name: "k"})
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-platform-pages"})
	anthropicAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-pp-anthropic", Platform: service.PlatformAnthropic})
	openaiAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-pp-openai", Platform: service.PlatformOpenAI})

	openaiIDs := map[int64]bool{}
	for i := 0; i < 10; i++ {
		row := platformFilterRow{account: anthropicAcc, groupID: group.ID, model: "m", session: "sess-platform-pages-0001", at: base.Add(time.Duration(i) * time.Second)}
		if i%2 == 0 {
			row.account = openaiAcc
		}
		id := s.createPlatformFilterRow(user, apiKey, row)
		if row.account == openaiAcc {
			openaiIDs[id] = true
		}
	}

	// No user/key/account/session filter: the fast pagination path.
	filters := usagestats.UsageLogFilters{GroupID: group.ID, Platform: service.PlatformOpenAI, StartTime: &start, EndTime: &end}
	s.Require().True(shouldUseFastUsageLogTotal(filters))

	seen := map[int64]bool{}
	for page, want := range []struct {
		rows  int
		pages int
	}{{2, 2}, {2, 3}, {1, 3}} {
		logs, result, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: page + 1, PageSize: 2}, filters)
		s.Require().NoError(err)
		s.Require().Len(logs, want.rows, "page %d", page+1)
		s.Require().Equal(want.pages, result.Pages, "page %d: Pages > Page signals hasMore", page+1)
		for _, log := range logs {
			s.Require().True(openaiIDs[log.ID], "page %d returned a non-OpenAI row", page+1)
			s.Require().False(seen[log.ID], "row repeated across pages")
			seen[log.ID] = true
		}
	}
	s.Require().Len(seen, len(openaiIDs))
}

// TestPlatformFilter_StatsAccountAndMeteredCost: with only Platform set, the
// always-on account cost and the hosted metered cost are summed over the
// platform-filtered rows only.
func (s *UsageLogRepoSuite) TestPlatformFilter_StatsAccountAndMeteredCost() {
	base := time.Date(2019, 6, 6, 10, 0, 0, 0, time.UTC)
	start, end := base.Add(-time.Hour), base.Add(time.Hour)

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "platform-metered@test.com"})
	apiKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-platform-metered", Name: "k"})
	anthropicAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-pm-anthropic", Platform: service.PlatformAnthropic})
	openaiAcc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-pm-openai", Platform: service.PlatformOpenAI})

	create := func(account *service.Account, totalCost, actualCost float64, accountMultiplier float64, metered *float64, at time.Time) {
		_, err := s.repo.Create(s.ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID,
			RequestID: uuid.New().String(), Model: "m",
			InputTokens: 10, OutputTokens: 20,
			TotalCost: totalCost, ActualCost: actualCost, MeteredCost: metered,
			AccountRateMultiplier: &accountMultiplier,
			CreatedAt:             at,
		})
		s.Require().NoError(err)
	}
	metered := 3.0
	create(openaiAcc, 1.5, 0, 1, &metered, base)                    // hosted row
	create(openaiAcc, 0.5, 0.5, 1, nil, base.Add(time.Minute))      // plain openai row
	create(anthropicAcc, 1.0, 1.0, 2, nil, base.Add(2*time.Minute)) // plain anthropic row

	statsFor := func(platform string) *UsageStats {
		stats, err := s.repo.GetStatsWithFilters(s.ctx, usagestats.UsageLogFilters{Platform: platform, StartTime: &start, EndTime: &end})
		s.Require().NoError(err)
		s.Require().NotNil(stats.TotalAccountCost, "platform=%s", platform)
		return stats
	}

	openai := statsFor(service.PlatformOpenAI)
	s.Require().Equal(int64(2), openai.TotalRequests)
	s.Require().InDelta(2.0, *openai.TotalAccountCost, 1e-9)
	s.Require().NotNil(openai.TotalMeteredCost)
	s.Require().InDelta(3.0, *openai.TotalMeteredCost, 1e-9)

	anthropic := statsFor(service.PlatformAnthropic)
	s.Require().Equal(int64(1), anthropic.TotalRequests)
	s.Require().InDelta(2.0, *anthropic.TotalAccountCost, 1e-9) // 1.0 × account multiplier 2
	s.Require().Nil(anthropic.TotalMeteredCost, "no hosted rows on this platform")

	all := statsFor("")
	s.Require().Equal(int64(3), all.TotalRequests)
	s.Require().InDelta(4.0, *all.TotalAccountCost, 1e-9)
	s.Require().NotNil(all.TotalMeteredCost)
	s.Require().InDelta(3.0, *all.TotalMeteredCost, 1e-9)
}
