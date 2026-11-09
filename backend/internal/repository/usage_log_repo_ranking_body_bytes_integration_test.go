//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// Rankings by inbound request size run against real Postgres: SUM/AVG of a
// bigint are numeric and must scan into int64, NULL sizes are excluded, and
// the size order is applied before LIMIT.
func (s *UsageLogRepoSuite) TestSpendingRankingsRequestBodyBytes() {
	dayStart := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-body-bytes-ranking"})
	type party struct {
		user *service.User
		key  *service.APIKey
	}
	newParty := func(name string) party {
		user := mustCreateUser(s.T(), s.client, &service.User{Email: name + "-body-bytes@test.com"})
		key := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-body-bytes-" + name, Name: name})
		return party{user, key}
	}
	add := func(p party, cost float64, size *int64, at time.Duration) {
		log := &service.UsageLog{UserID: p.user.ID, APIKeyID: p.key.ID, AccountID: account.ID, RequestID: uuid.NewString(),
			Model: "claude-3", InputTokens: 10, TotalCost: cost, ActualCost: cost, RequestBodyBytes: size, CreatedAt: dayStart.Add(at)}
		_, err := s.repo.Create(s.ctx, log)
		s.Require().NoError(err)
	}
	size := func(v int64) *int64 { return &v }
	expensive, huge, unknown := newParty("expensive"), newParty("huge"), newParty("unknown")
	add(expensive, 9, size(1000), time.Hour)
	add(expensive, 9, size(3000), 2*time.Hour)
	add(expensive, 9, nil, 3*time.Hour) // unknown size: not in avg/max/count
	add(huge, 1, size(40<<20), time.Hour)
	add(unknown, 5, nil, time.Hour) // only old rows without a size

	byCost, err := s.repo.GetUserSpendingRanking(s.ctx, dayStart, dayEnd, 12, usagestats.SpendingRankingSortCost)
	s.Require().NoError(err)
	s.Require().Len(byCost.Ranking, 3)
	s.Require().Equal(expensive.user.ID, byCost.Ranking[0].UserID, "default order is unchanged")
	s.Require().Equal(int64(4000), byCost.Ranking[0].RequestBodyBytesTotal)
	s.Require().Equal(int64(2000), *byCost.Ranking[0].RequestBodyBytesAvg)
	s.Require().Equal(int64(3000), *byCost.Ranking[0].RequestBodyBytesMax)
	s.Require().Equal(int64(2), byCost.Ranking[0].RequestBodyBytesKnownCount)
	s.Require().Equal(int64(3), byCost.Ranking[0].Requests)

	bySize, err := s.repo.GetUserSpendingRanking(s.ctx, dayStart, dayEnd, 12, usagestats.SpendingRankingSortRequestBodyBytes)
	s.Require().NoError(err)
	s.Require().Equal([]int64{huge.user.ID, expensive.user.ID, unknown.user.ID},
		[]int64{bySize.Ranking[0].UserID, bySize.Ranking[1].UserID, bySize.Ranking[2].UserID})
	s.Require().Equal(int64(40<<20), bySize.Ranking[0].RequestBodyBytesTotal)
	last := bySize.Ranking[2]
	s.Require().Zero(last.RequestBodyBytesTotal)
	s.Require().Nil(last.RequestBodyBytesAvg)
	s.Require().Nil(last.RequestBodyBytesMax)
	s.Require().Zero(last.RequestBodyBytesKnownCount)
	s.Require().InDelta(33.0, bySize.TotalCost, 1e-9, "totals still cover every user")

	top, err := s.repo.GetUserSpendingRanking(s.ctx, dayStart, dayEnd, 1, usagestats.SpendingRankingSortRequestBodyBytes)
	s.Require().NoError(err)
	s.Require().Len(top.Ranking, 1)
	s.Require().Equal(huge.user.ID, top.Ranking[0].UserID, "size order applies before LIMIT")

	keys, err := s.repo.GetAPIKeySpendingRanking(s.ctx, dayStart, dayEnd, 0, 1, usagestats.SpendingRankingSortRequestBodyBytes)
	s.Require().NoError(err)
	s.Require().Len(keys.Ranking, 1)
	s.Require().Equal(huge.key.ID, keys.Ranking[0].APIKeyID)
	s.Require().Equal(int64(40<<20), *keys.Ranking[0].RequestBodyBytesMax)
	s.Require().Equal(int64(1), keys.Ranking[0].RequestBodyBytesKnownCount)

	keysByCost, err := s.repo.GetAPIKeySpendingRanking(s.ctx, dayStart, dayEnd, unknown.user.ID, 12, usagestats.SpendingRankingSortCost)
	s.Require().NoError(err)
	s.Require().Len(keysByCost.Ranking, 1)
	s.Require().Nil(keysByCost.Ranking[0].RequestBodyBytesAvg)
	s.Require().Zero(keysByCost.Ranking[0].RequestBodyBytesTotal)
}
