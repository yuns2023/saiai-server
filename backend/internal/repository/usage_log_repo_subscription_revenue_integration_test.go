//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// Subscription revenue is a statistics-only estimate: subscription rows
// (billing_type = 1) are priced with each group's current revenue rate; rows
// whose group has no rate, or no group, are reported as unpriced; a 0 rate is
// a configured free group; balance and hosted rows never contribute.
func (s *UsageLogRepoSuite) TestSubscriptionRevenueAcrossStatsRankingsAndAccounts() {
	dayStart := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)
	rate := func(v float64) *float64 { return &v }
	priced := mustCreateGroup(s.T(), s.client, &service.Group{Name: "rev-priced", SubscriptionType: service.SubscriptionTypeSubscription, SubscriptionRevenueRate: rate(0.64)})
	free := mustCreateGroup(s.T(), s.client, &service.Group{Name: "rev-free", SubscriptionType: service.SubscriptionTypeSubscription, SubscriptionRevenueRate: rate(0)})
	unpriced := mustCreateGroup(s.T(), s.client, &service.Group{Name: "rev-unpriced", SubscriptionType: service.SubscriptionTypeSubscription})
	// A group that is no longer a subscription group keeps pricing its historical subscription rows.
	converted := mustCreateGroup(s.T(), s.client, &service.Group{Name: "rev-converted", SubscriptionRevenueRate: rate(0.5)})

	accountA := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-rev-a"})
	accountB := mustCreateAccount(s.T(), s.client, &service.Account{Name: "acc-rev-b"})
	user := mustCreateUser(s.T(), s.client, &service.User{Email: "rev-user@test.com"})
	other := mustCreateUser(s.T(), s.client, &service.User{Email: "rev-other@test.com"})
	key := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: user.ID, Key: "sk-rev-user", Name: "rev-user"})
	otherKey := mustCreateApiKey(s.T(), s.client, &service.APIKey{UserID: other.ID, Key: "sk-rev-other", Name: "rev-other"})

	add := func(apiKey *service.APIKey, account *service.Account, group *service.Group, billing int8, actual float64, metered *float64) {
		log := &service.UsageLog{UserID: apiKey.UserID, APIKeyID: apiKey.ID, AccountID: account.ID, RequestID: uuid.NewString(),
			Model: "claude-3", InputTokens: 10, TotalCost: actual, ActualCost: actual, BillingType: billing, MeteredCost: metered,
			CreatedAt: dayStart.Add(time.Hour)}
		if group != nil {
			log.GroupID = &group.ID
		}
		_, err := s.repo.Create(s.ctx, log)
		s.Require().NoError(err)
	}
	sub, bal := int8(service.BillingTypeSubscription), int8(service.BillingTypeBalance)
	add(key, accountA, priced, sub, 100, nil)        // 64 yuan
	add(key, accountA, free, sub, 50, nil)           // configured free: 0 yuan, not unpriced
	add(key, accountA, unpriced, sub, 30, nil)       // unpriced $30
	add(key, accountA, priced, bal, 40, nil)         // balance: ignored
	add(key, accountB, converted, sub, 10, nil)      // 5 yuan
	add(otherKey, accountB, nil, sub, 7, nil)        // no group: unpriced $7
	add(otherKey, accountB, priced, sub, 0, rate(9)) // hosted: ignored
	add(otherKey, accountB, priced, bal, 3, rate(1)) // hosted with a stray charge: not balance revenue
	add(otherKey, accountB, priced, 2, 2, nil)       // unknown billing type: unclassified $2

	stats, err := s.repo.GetStatsWithFilters(s.ctx, usagestats.UsageLogFilters{StartTime: &dayStart, EndTime: &dayEnd})
	s.Require().NoError(err)
	s.Require().NotNil(stats.TotalSubscriptionRevenue)
	s.Require().InDelta(69.0, *stats.TotalSubscriptionRevenue, 1e-9)
	s.Require().InDelta(37.0, *stats.SubscriptionRevenueUnpricedCost, 1e-9)
	s.Require().InDelta(197.0, stats.TotalSubscriptionCost, 1e-9, "existing subscription total is unchanged")

	filtered, err := s.repo.GetStatsWithFilters(s.ctx, usagestats.UsageLogFilters{StartTime: &dayStart, EndTime: &dayEnd, UserID: other.ID})
	s.Require().NoError(err)
	s.Require().InDelta(0.0, *filtered.TotalSubscriptionRevenue, 1e-9, "revenue honours the same filters")
	s.Require().InDelta(7.0, *filtered.SubscriptionRevenueUnpricedCost, 1e-9)

	users, err := s.repo.GetUserSpendingRanking(s.ctx, dayStart, dayEnd, 1, usagestats.SpendingRankingSortCost)
	s.Require().NoError(err)
	s.Require().Len(users.Ranking, 1)
	s.Require().Equal(user.ID, users.Ranking[0].UserID)
	s.Require().InDelta(69.0, users.Ranking[0].SubscriptionRevenue, 1e-9)
	s.Require().InDelta(30.0, users.Ranking[0].SubscriptionRevenueUnpricedCost, 1e-9)
	s.Require().InDelta(69.0, users.TotalSubscriptionRevenue, 1e-9, "totals are taken before LIMIT")
	s.Require().InDelta(37.0, users.TotalSubscriptionRevenueUnpricedCost, 1e-9)
	s.Require().InDelta(40.0, users.Ranking[0].BalanceCost, 1e-9)
	s.Require().InDelta(190.0, users.Ranking[0].SubscriptionCost, 1e-9)
	s.Require().InDelta(0.0, users.Ranking[0].UnclassifiedCost, 1e-9)
	s.Require().InDelta(40.0, users.TotalBalanceCost, 1e-9, "hosted rows are not balance revenue")
	s.Require().InDelta(197.0, users.TotalSubscriptionCost, 1e-9)
	s.Require().InDelta(2.0, users.TotalUnclassifiedCost, 1e-9)

	keys, err := s.repo.GetAPIKeySpendingRanking(s.ctx, dayStart, dayEnd, other.ID, 12, usagestats.SpendingRankingSortCost)
	s.Require().NoError(err)
	s.Require().Len(keys.Ranking, 1)
	s.Require().InDelta(0.0, keys.Ranking[0].SubscriptionRevenue, 1e-9)
	s.Require().InDelta(7.0, keys.Ranking[0].SubscriptionRevenueUnpricedCost, 1e-9)
	s.Require().InDelta(0.0, keys.Ranking[0].BalanceCost, 1e-9)
	s.Require().InDelta(7.0, keys.Ranking[0].SubscriptionCost, 1e-9)
	s.Require().InDelta(2.0, keys.Ranking[0].UnclassifiedCost, 1e-9)
	s.Require().InDelta(0.0, keys.TotalBalanceCost, 1e-9)
	s.Require().InDelta(7.0, keys.TotalSubscriptionCost, 1e-9)
	s.Require().InDelta(2.0, keys.TotalUnclassifiedCost, 1e-9)

	byAccount, err := s.repo.GetAccountWindowStatsBatch(s.ctx, []int64{accountA.ID, accountB.ID}, dayStart)
	s.Require().NoError(err)
	s.Require().InDelta(64.0, byAccount[accountA.ID].SubscriptionRevenue, 1e-9)
	s.Require().InDelta(30.0, byAccount[accountA.ID].SubscriptionRevenueUnpricedCost, 1e-9)
	s.Require().Equal(int64(4), byAccount[accountA.ID].Requests, "per-group pre-aggregation keeps request counts")
	s.Require().InDelta(220.0, byAccount[accountA.ID].UserCost, 1e-9)
	s.Require().InDelta(5.0, byAccount[accountB.ID].SubscriptionRevenue, 1e-9)
	s.Require().InDelta(7.0, byAccount[accountB.ID].SubscriptionRevenueUnpricedCost, 1e-9)
	s.Require().Equal(int64(2), byAccount[accountB.ID].HostedRequests)
	s.Require().InDelta(10.0, byAccount[accountB.ID].HostedCost, 1e-9)
	s.Require().InDelta(40.0, byAccount[accountA.ID].BalanceCost, 1e-9)
	s.Require().InDelta(180.0, byAccount[accountA.ID].SubscriptionCost, 1e-9)
	s.Require().InDelta(0.0, byAccount[accountB.ID].BalanceCost, 1e-9, "hosted rows are not balance revenue")
	s.Require().InDelta(17.0, byAccount[accountB.ID].SubscriptionCost, 1e-9)
	s.Require().InDelta(2.0, byAccount[accountB.ID].UnclassifiedCost, 1e-9)

	single, err := s.repo.GetAccountWindowStats(s.ctx, accountB.ID, dayStart)
	s.Require().NoError(err)
	s.Require().Equal(*byAccount[accountB.ID], *single, "single and batch account queries agree")

	empty, err := s.repo.GetAccountWindowStats(s.ctx, accountA.ID, dayEnd)
	s.Require().NoError(err)
	s.Require().Equal(usagestats.AccountStats{}, *empty, "an empty window still returns one zero row")
}
