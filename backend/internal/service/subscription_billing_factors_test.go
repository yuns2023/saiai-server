//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionBillingUsesActualCost(t *testing.T) {
	for _, actualCost := range []float64{20, 5, 0} {
		for _, atomicBilling := range []bool{true, false} {
			t.Run(fmt.Sprintf("cost=%g/atomic=%t", actualCost, atomicBilling), func(t *testing.T) {
				subRepo := &subscriptionCostRepoStub{}
				userRepo := &openAIRecordUsageUserRepoStub{}
				cache := &BillingCacheService{cache: &billingCacheWorkerStub{}, cacheWriteChan: make(chan cacheWriteTask, 4)}
				params := &postUsageBillingParams{
					Cost:    &CostBreakdown{TotalCost: 10, ActualCost: actualCost},
					User:    &User{ID: 1},
					APIKey:  &APIKey{ID: 2, GroupID: i64p(3), Quota: 100},
					Account: &Account{ID: 4}, Subscription: &UserSubscription{ID: 5},
					IsSubscriptionBill: true, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
				}
				deps := &billingDeps{userRepo: userRepo, userSubRepo: subRepo, billingCacheService: cache, deferredService: &DeferredService{}}
				var repo UsageBillingRepository
				billingRepo := &openAIRecordUsageBillingRepoStub{}
				if atomicBilling {
					repo = billingRepo
				}
				applied, err := applyUsageBilling(context.Background(), "subscription-factors", nil, params, deps, repo)
				require.NoError(t, err)
				require.True(t, applied)
				require.Zero(t, userRepo.deductCalls, "a free subscription request must never fall back to the wallet")
				if atomicBilling {
					require.Equal(t, actualCost, billingRepo.lastCmd.SubscriptionCost)
					require.Zero(t, billingRepo.lastCmd.BalanceCost)
					require.Equal(t, int64(5), *billingRepo.lastCmd.SubscriptionID)
					require.Equal(t, actualCost, billingRepo.lastCmd.APIKeyQuotaCost)
				} else {
					require.Equal(t, actualCost, subRepo.amount)
				}
				if actualCost == 0 {
					require.Empty(t, cache.cacheWriteChan)
					require.Zero(t, subRepo.calls)
				} else {
					require.Len(t, cache.cacheWriteChan, 1, "subscription cache must be updated exactly once")
					task := <-cache.cacheWriteChan
					require.Equal(t, cacheWriteUpdateSubscriptionUsage, task.kind)
					require.Equal(t, actualCost, task.amount)
				}
			})
		}
	}
}

type subscriptionCostRepoStub struct {
	UserSubscriptionRepository
	amount float64
	calls  int
}

func (s *subscriptionCostRepoStub) IncrementUsage(_ context.Context, _ int64, amount float64) error {
	s.amount += amount
	s.calls++
	return nil
}
