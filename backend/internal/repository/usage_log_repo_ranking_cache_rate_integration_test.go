//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// The cache read rate ranking runs against real Postgres: the rate is a
// numeric division (not integer), Keys under the token threshold or with no
// input are excluded, and the scope totals still cover every Key.
func (s *UsageLogRepoSuite) TestAPIKeySpendingRankingCacheReadRate() {
	dayStart := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-cache-rate-ranking"})
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "cache-rate-ranking@test.com"})
	newKey := func(name string) *service.APIKey {
		return mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-cache-rate-" + name, Name: name})
	}
	add := func(key *service.APIKey, input, output, write, read int, cost float64) {
		log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(),
			Model: "claude-3", InputTokens: input, OutputTokens: output, CacheCreationTokens: write, CacheReadTokens: read,
			TotalCost: cost, ActualCost: cost, CreatedAt: dayStart.Add(time.Hour)}
		_, err := s.repo.Create(s.ctx, log)
		s.Require().NoError(err)
	}
	poor, mid, good, tiny, outputOnly := newKey("poor"), newKey("mid"), newKey("good"), newKey("tiny"), newKey("output-only")
	add(poor, 700_000, 0, 100_000, 200_000, 1) // 20%
	add(mid, 200_000, 0, 300_000, 500_000, 2)  // 50%
	add(good, 10_000, 0, 40_000, 950_000, 3)   // 95%
	add(good, 0, 0, 0, 0, 0)                   // a second, empty row does not change the rate
	add(tiny, 100, 0, 0, 0, 50)                // 0% but under 1M tokens: excluded
	add(outputOnly, 0, 2_000_000, 0, 0, 4)     // enough tokens, no input: rate undefined, excluded

	asc, err := s.repo.GetAPIKeySpendingRanking(s.ctx, dayStart, dayEnd, user.ID, 20, usagestats.SpendingRankingSortCacheReadRate, usagestats.SpendingRankingOrderAsc)
	s.Require().NoError(err)
	s.Require().Equal([]int64{poor.ID, mid.ID, good.ID}, rankingKeyIDs(asc))
	s.Require().Equal(60.0, asc.TotalCost, "totals include Keys filtered out by the threshold")
	s.Require().Equal(int64(6), asc.TotalRequests)

	desc, err := s.repo.GetAPIKeySpendingRanking(s.ctx, dayStart, dayEnd, user.ID, 2, usagestats.SpendingRankingSortCacheReadRate, usagestats.SpendingRankingOrderDesc)
	s.Require().NoError(err)
	s.Require().Equal([]int64{good.ID, mid.ID}, rankingKeyIDs(desc), "order is applied before LIMIT")
	s.Require().Equal(60.0, desc.TotalCost)
}

func rankingKeyIDs(r *usagestats.APIKeySpendingRankingResponse) []int64 {
	ids := make([]int64, 0, len(r.Ranking))
	for _, item := range r.Ranking {
		ids = append(ids, item.APIKeyID)
	}
	return ids
}
