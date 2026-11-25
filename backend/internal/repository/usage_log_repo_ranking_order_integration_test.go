//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// Ascending orders and the cache read rate sort run against real Postgres:
// lowest cost first (Keys used very little but still in use), numeric rate
// division, an undefined rate last in both directions, rows without a known
// request size last when ascending, and the order applied before LIMIT.
func (s *UsageLogRepoSuite) TestAPIKeySpendingRankingOrders() {
	dayStart := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-ranking-orders"})
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "ranking-orders@test.com"})
	newKey := func(name string) *service.APIKey {
		return mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-ranking-orders-" + name, Name: name})
	}
	add := func(key *service.APIKey, input, output, write, read int, cost float64, size *int64) {
		log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(),
			Model: "claude-3", InputTokens: input, OutputTokens: output, CacheCreationTokens: write, CacheReadTokens: read,
			TotalCost: cost, ActualCost: cost, RequestBodyBytes: size, CreatedAt: dayStart.Add(time.Hour)}
		_, err := s.repo.Create(s.ctx, log)
		s.Require().NoError(err)
	}
	size := func(v int64) *int64 { return &v }
	poor, mid, good, outputOnly := newKey("poor"), newKey("mid"), newKey("good"), newKey("output-only")
	add(poor, 7, 0, 1, 2, 3, size(300))  // 20%
	add(mid, 2, 0, 3, 5, 0.5, size(100)) // 50%
	add(good, 1, 0, 4, 95, 9, size(200)) // 95%
	add(outputOnly, 0, 10, 0, 0, 1, nil) // rate undefined, size unknown

	ids := func(sort usagestats.SpendingRankingSort, order usagestats.SpendingRankingOrder, limit int) []int64 {
		r, err := s.repo.GetAPIKeySpendingRanking(s.ctx, dayStart, dayEnd, user.ID, limit, sort, order)
		s.Require().NoError(err)
		s.Require().Equal(13.5, r.TotalCost, "totals cover every Key")
		out := make([]int64, 0, len(r.Ranking))
		for _, item := range r.Ranking {
			out = append(out, item.APIKeyID)
		}
		return out
	}
	cost, body, rate := usagestats.SpendingRankingSortCost, usagestats.SpendingRankingSortRequestBodyBytes, usagestats.SpendingRankingSortCacheReadRate
	asc, desc := usagestats.SpendingRankingOrderAsc, usagestats.SpendingRankingOrderDesc
	s.Require().Equal([]int64{good.ID, poor.ID, outputOnly.ID, mid.ID}, ids(cost, desc, 20))
	s.Require().Equal([]int64{mid.ID, outputOnly.ID}, ids(cost, asc, 2), "lowest cost first, before LIMIT")
	s.Require().Equal([]int64{poor.ID, good.ID, mid.ID, outputOnly.ID}, ids(body, desc, 20))
	s.Require().Equal([]int64{mid.ID, good.ID, poor.ID, outputOnly.ID}, ids(body, asc, 20), "unknown size last")
	s.Require().Equal([]int64{good.ID, mid.ID, poor.ID, outputOnly.ID}, ids(rate, "", 20), "undefined rate last")
	s.Require().Equal([]int64{poor.ID, mid.ID, good.ID, outputOnly.ID}, ids(rate, asc, 20), "undefined rate last")
}

// Sessions counts distinct client session IDs per Key; NULL and empty IDs are
// not a session and land in RequestsWithoutSession instead.
func (s *UsageLogRepoSuite) TestAPIKeySpendingRankingSessions() {
	dayStart := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-ranking-sessions"})
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "ranking-sessions@test.com"})
	newKey := func(name string) *service.APIKey {
		return mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-ranking-sessions-" + name, Name: name})
	}
	add := func(key *service.APIKey, sessionID *string) {
		log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(),
			Model: "claude-3", InputTokens: 1, TotalCost: 1, ActualCost: 1, SessionID: sessionID, CreatedAt: dayStart.Add(time.Hour)}
		_, err := s.repo.Create(s.ctx, log)
		s.Require().NoError(err)
	}
	sid := func(v string) *string { return &v }
	mixed, none := newKey("mixed"), newKey("none")
	add(mixed, sid("a"))
	add(mixed, sid("a"))
	add(mixed, sid("b"))
	add(mixed, nil)
	add(none, nil)
	add(none, nil)
	_, err := s.repo.sql.ExecContext(s.ctx, "UPDATE usage_logs SET session_id = '' WHERE api_key_id = $1 AND id = (SELECT MIN(id) FROM usage_logs WHERE api_key_id = $1)", none.ID)
	s.Require().NoError(err)

	r, err := s.repo.GetAPIKeySpendingRanking(s.ctx, dayStart, dayEnd, user.ID, 20, usagestats.SpendingRankingSortCost, "")
	s.Require().NoError(err)
	got := map[int64][2]int64{}
	for _, item := range r.Ranking {
		got[item.APIKeyID] = [2]int64{item.Sessions, item.RequestsWithoutSession}
	}
	s.Require().Equal(map[int64][2]int64{mixed.ID: {2, 1}, none.ID: {0, 2}}, got)
}
