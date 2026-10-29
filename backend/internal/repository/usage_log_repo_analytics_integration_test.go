//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

type analyticsRow struct {
	key        *service.APIKey
	model      string
	effort     *string
	in, out    int
	cw, cr     int
	cost       float64
	actual     float64
	metered    *float64
	billing    int8
	createdAt  time.Time
	requestTyp service.RequestType
}

func (s *UsageLogRepoSuite) createAnalyticsLog(user *service.User, account *service.Account, r analyticsRow) {
	log := &service.UsageLog{
		UserID:              user.ID,
		APIKeyID:            r.key.ID,
		AccountID:           account.ID,
		RequestID:           uuid.NewString(),
		Model:               r.model,
		ReasoningEffort:     r.effort,
		InputTokens:         r.in,
		OutputTokens:        r.out,
		CacheCreationTokens: r.cw,
		CacheReadTokens:     r.cr,
		TotalCost:           r.cost,
		ActualCost:          r.actual,
		MeteredCost:         r.metered,
		BillingType:         r.billing,
		RequestType:         r.requestTyp,
		CreatedAt:           r.createdAt,
	}
	_, err := s.repo.Create(s.ctx, log)
	s.Require().NoError(err)
}

func (s *UsageLogRepoSuite) TestGetUserUsageAnalytics() {
	strp := func(v string) *string { return &v }
	f64p := func(v float64) *float64 { return &v }

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "analytics-" + uuid.NewString() + "@test.com"})
	other := mustCreateUser(s.T(), s.client, &service.User{Email: "analytics-other-" + uuid.NewString() + "@test.com"})
	keyA := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-an-a-" + uuid.NewString(), Name: "alpha"})
	keyB := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-an-b-" + uuid.NewString(), Name: "beta"})
	keyOther := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: other.ID, Key: "sk-an-o-" + uuid.NewString(), Name: "other"})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-analytics-" + uuid.NewString()})

	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	at := start.Add(time.Hour)

	rows := []analyticsRow{
		// keyA: gpt high x2, gpt with " High " (normalizes to high), gpt without effort
		{key: keyA, model: "gpt", effort: strp("high"), in: 100, out: 50, cw: 0, cr: 50, cost: 2, actual: 1, billing: service.BillingTypeBalance, createdAt: at, requestTyp: service.RequestTypeStream},
		{key: keyA, model: "gpt", effort: strp(" High "), in: 100, out: 50, cw: 0, cr: 50, cost: 2, actual: 1, billing: service.BillingTypeBalance, createdAt: at, requestTyp: service.RequestTypeStream},
		{key: keyA, model: "gpt", effort: nil, in: 10, out: 10, cw: 0, cr: 0, cost: 0.2, actual: 0.1, billing: service.BillingTypeBalance, createdAt: at, requestTyp: service.RequestTypeSync},
		{key: keyA, model: "gpt", effort: strp(""), in: 10, out: 10, cw: 0, cr: 0, cost: 0.2, actual: 0.1, billing: service.BillingTypeBalance, createdAt: at, requestTyp: service.RequestTypeSync},
		// keyB: claude "none" effort (a real value), hosted (metered) usage, subscription
		{key: keyB, model: "claude", effort: strp("none"), in: 200, out: 100, cw: 100, cr: 0, cost: 4, actual: 0, metered: f64p(4), billing: service.BillingTypeSubscription, createdAt: at, requestTyp: service.RequestTypeStream},
		// out of range (end is exclusive) and another user's usage
		{key: keyA, model: "gpt", effort: strp("high"), in: 999, out: 999, cost: 99, actual: 99, createdAt: end, requestTyp: service.RequestTypeStream},
		{key: keyOther, model: "gpt", effort: strp("high"), in: 999, out: 999, cost: 99, actual: 99, createdAt: at, requestTyp: service.RequestTypeStream},
	}
	for _, r := range rows {
		target := user
		if r.key.ID == keyOther.ID {
			target = other
		}
		s.createAnalyticsLog(target, account, r)
	}

	base := usagestats.UsageAnalyticsQuery{UserID: user.ID, StartTime: start, EndTime: end, PageSize: 50, Page: 1}

	// By model: summary covers only the user's in-range rows; shares and mixes.
	q := base
	q.GroupBy = usagestats.UsageAnalyticsByModel
	res, err := s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(2, res.Total)
	s.Require().EqualValues(5, res.Summary.Requests)
	s.Require().EqualValues(420, res.Summary.InputTokens)
	s.Require().EqualValues(840, res.Summary.TotalTokens)
	s.Require().InDelta(8.4, res.Summary.Cost, 1e-9)
	s.Require().InDelta(2.2, res.Summary.ActualCost, 1e-9)
	s.Require().InDelta(6.2, res.Summary.UsageCost, 1e-9) // hosted reference counted
	s.Require().Len(res.Items, 2)
	s.Require().Equal("gpt", res.Items[0].Model) // actual_cost_desc
	s.Require().EqualValues(4, res.Items[0].Requests)
	s.Require().InDelta(1.0, *res.Items[0].Share.ActualCost, 1e-9)
	s.Require().InDelta(2.2/6.2, *res.Items[0].Share.UsageCost, 1e-9)
	s.Require().InDelta(100.0/440.0, *res.Items[0].TokenMix.CacheRead, 1e-9)
	s.Require().Equal("claude", res.Items[1].Model)
	s.Require().InDelta(0.0, *res.Items[1].Share.ActualCost, 1e-9)
	s.Require().Zero(res.Items[1].APIKeyID)

	// By reasoning effort: trimmed/lowercased, NULL and '' are one missing bucket, "none" is a value.
	q = base
	q.GroupBy = usagestats.UsageAnalyticsByReasoningEffort
	q.Sort = usagestats.UsageAnalyticsSortRequests
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(3, res.Total)
	got := map[string]int64{}
	for _, it := range res.Items {
		name := it.ReasoningEffort
		if it.ReasoningEffortMissing {
			s.Require().Empty(name)
			name = "<missing>"
		}
		got[name] = it.Requests
	}
	s.Require().Equal(map[string]int64{"high": 2, "<missing>": 2, "none": 1}, got)

	// Effort filters.
	q.ReasoningEffort = "HIGH"
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(2, res.Summary.Requests)
	q.ReasoningEffort = ""
	q.ReasoningEffortMissing = true
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(2, res.Summary.Requests)

	// By Key x model, including a deleted Key; names come from the user's own Keys.
	_, err = s.tx.ExecContext(s.ctx, `UPDATE api_keys SET deleted_at = NOW() WHERE id = $1`, keyB.ID)
	s.Require().NoError(err)
	q = base
	q.GroupBy = usagestats.UsageAnalyticsByAPIKeyModel
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(2, res.Total)
	s.Require().Equal(keyA.ID, res.Items[0].APIKeyID)
	s.Require().Equal("alpha", res.Items[0].APIKeyName)
	s.Require().Equal("gpt", res.Items[0].Model)
	s.Require().False(res.Items[0].APIKeyDeleted)
	s.Require().Equal(keyB.ID, res.Items[1].APIKeyID)
	s.Require().Equal("beta", res.Items[1].APIKeyName)
	s.Require().True(res.Items[1].APIKeyDeleted)

	// Filters: Key, billing type, request type, model.
	q = base
	q.GroupBy = usagestats.UsageAnalyticsByAPIKey
	q.APIKeyID = keyB.ID
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(1, res.Summary.Requests)
	sub := service.BillingTypeSubscription
	q = base
	q.GroupBy = usagestats.UsageAnalyticsByModel
	q.BillingType = &sub
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(1, res.Summary.Requests)
	syncType := int16(service.RequestTypeSync)
	q = base
	q.GroupBy = usagestats.UsageAnalyticsByModel
	q.RequestType = &syncType
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(2, res.Summary.Requests)
	q = base
	q.GroupBy = usagestats.UsageAnalyticsByModel
	q.Model = "claude"
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(1, res.Summary.Requests)

	// Another user's Key matches nothing: no cross-user leak.
	q = base
	q.GroupBy = usagestats.UsageAnalyticsByModel
	q.APIKeyID = keyOther.ID
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(0, res.Total)
	s.Require().EqualValues(0, res.Summary.Requests)
	s.Require().Empty(res.Items)
	s.Require().Nil(res.Summary.TokenMix.Input)

	// Pagination: stable order, page past the end keeps the summary.
	q = base
	q.GroupBy = usagestats.UsageAnalyticsByReasoningEffort
	q.PageSize = 1
	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		q.Page = page
		res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
		s.Require().NoError(err)
		s.Require().Len(res.Items, 1)
		s.Require().EqualValues(3, res.Total)
		key := res.Items[0].ReasoningEffort
		if res.Items[0].ReasoningEffortMissing {
			key = "<missing>"
		}
		s.Require().False(seen[key], "duplicate group across pages: %s", key)
		seen[key] = true
	}
	q.Page = 4
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().Empty(res.Items)
	s.Require().EqualValues(3, res.Total)
	s.Require().EqualValues(5, res.Summary.Requests)

	// Guard rails.
	_, err = s.repo.GetUserUsageAnalytics(s.ctx, usagestats.UsageAnalyticsQuery{StartTime: start, EndTime: end, GroupBy: usagestats.UsageAnalyticsByModel})
	s.Require().Error(err)
	q = base
	q.GroupBy = "model; DROP TABLE usage_logs"
	_, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().Error(err)
}

func (s *UsageLogRepoSuite) TestGetUserUsageAnalyticsTieOrderAndCombinedFilters() {
	strp := func(v string) *string { return &v }
	f64p := func(v float64) *float64 { return &v }

	user := mustCreateUser(s.T(), s.client, &service.User{Email: "analytics-tie-" + uuid.NewString() + "@test.com"})
	keyA := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-tie-a-" + uuid.NewString(), Name: "a"})
	keyB := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-tie-b-" + uuid.NewString(), Name: "b"})
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-tie-" + uuid.NewString()})
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "g-tie-" + uuid.NewString()})

	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	at := start.Add(time.Hour)
	same := func(key *service.APIKey, model string, effort *string) analyticsRow {
		return analyticsRow{key: key, model: model, effort: effort, in: 10, out: 10, cost: 1, actual: 1, createdAt: at, requestTyp: service.RequestTypeStream}
	}
	// Identical metrics everywhere: order falls back to Key, model, effort (missing last).
	for _, r := range []analyticsRow{
		same(keyB, "m1", strp("low")),
		same(keyA, "m2", nil),
		same(keyA, "m1", strp("high")),
		same(keyB, "m2", strp("medium")),
	} {
		s.createAnalyticsLog(user, account, r)
	}
	base := usagestats.UsageAnalyticsQuery{UserID: user.ID, StartTime: start, EndTime: start.Add(24 * time.Hour), Page: 1, PageSize: 50}

	q := base
	q.GroupBy = usagestats.UsageAnalyticsByAPIKeyModel
	full, err := s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	type km struct {
		key   int64
		model string
	}
	var order []km
	for _, it := range full.Items {
		order = append(order, km{it.APIKeyID, it.Model})
	}
	s.Require().Equal([]km{{keyA.ID, "m1"}, {keyA.ID, "m2"}, {keyB.ID, "m1"}, {keyB.ID, "m2"}}, order)
	for _, sortValue := range []string{
		usagestats.UsageAnalyticsSortActualCost, usagestats.UsageAnalyticsSortCost, usagestats.UsageAnalyticsSortUsageCost,
		usagestats.UsageAnalyticsSortTokens, usagestats.UsageAnalyticsSortRequests,
	} {
		q.Sort = sortValue
		q.PageSize = 1
		var paged []km
		for page := 1; page <= 4; page++ {
			q.Page = page
			res, err := s.repo.GetUserUsageAnalytics(s.ctx, q)
			s.Require().NoError(err)
			s.Require().Len(res.Items, 1)
			paged = append(paged, km{res.Items[0].APIKeyID, res.Items[0].Model})
		}
		s.Require().Equal(order, paged, sortValue)
	}

	q = base
	q.GroupBy = usagestats.UsageAnalyticsByReasoningEffort
	res, err := s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	var efforts []string
	for _, it := range res.Items {
		if it.ReasoningEffortMissing {
			efforts = append(efforts, "<missing>")
		} else {
			efforts = append(efforts, it.ReasoningEffort)
		}
	}
	s.Require().Equal([]string{"high", "low", "medium", "<missing>"}, efforts)

	// Combined filters pick exactly one record; hosted metered_cost=0 is not replaced by actual_cost.
	target := analyticsRow{key: keyB, model: "m3", effort: strp("xhigh"), in: 5, out: 5, cost: 7, actual: 2, metered: f64p(0),
		billing: service.BillingTypeSubscription, createdAt: at, requestTyp: service.RequestTypeSync}
	log := &service.UsageLog{
		UserID: user.ID, APIKeyID: keyB.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: target.model,
		ReasoningEffort: target.effort, InputTokens: target.in, OutputTokens: target.out, TotalCost: target.cost,
		ActualCost: target.actual, MeteredCost: target.metered, BillingType: target.billing, RequestType: target.requestTyp,
		GroupID: &group.ID, CreatedAt: at,
	}
	_, err = s.repo.Create(s.ctx, log)
	s.Require().NoError(err)
	decoy := *log
	decoy.RequestID = uuid.NewString()
	decoy.GroupID = nil
	_, err = s.repo.Create(s.ctx, &decoy)
	s.Require().NoError(err)

	sub := service.BillingTypeSubscription
	syncType := int16(service.RequestTypeSync)
	q = base
	q.GroupBy = usagestats.UsageAnalyticsByModel
	q.APIKeyID = keyB.ID
	q.GroupID = group.ID
	q.Model = "m3"
	q.BillingType = &sub
	q.ReasoningEffort = "xhigh"
	q.RequestType = &syncType
	res, err = s.repo.GetUserUsageAnalytics(s.ctx, q)
	s.Require().NoError(err)
	s.Require().EqualValues(1, res.Total)
	s.Require().EqualValues(1, res.Summary.Requests)
	s.Require().EqualValues(1, res.Items[0].Requests)
	s.Require().InDelta(7.0, res.Summary.Cost, 1e-9)
	// metered_cost=0 is a recorded reference, not missing: it must not fall back to actual_cost.
	s.Require().InDelta(2.0, res.Summary.ActualCost, 1e-9)
	s.Require().InDelta(0.0, res.Summary.UsageCost, 1e-9)
	s.Require().Nil(res.Items[0].Share.UsageCost)
}
