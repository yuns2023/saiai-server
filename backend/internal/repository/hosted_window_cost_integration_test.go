//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestGetAccountWindowCostsBoundaries(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)

	user1 := mustCreateUser(t, client, &service.User{Email: "window-cost-1@test.com"})
	user2 := mustCreateUser(t, client, &service.User{Email: "window-cost-2@test.com"})
	key1 := mustCreateApiKey(t, client, &service.APIKey{UserID: user1.ID, Key: "sk-window-cost-1", Name: "k1"})
	key2 := mustCreateApiKey(t, client, &service.APIKey{UserID: user2.ID, Key: "sk-window-cost-2", Name: "k2"})
	account := mustCreateAccount(t, client, &service.Account{Name: "window-cost-acc"})
	other := mustCreateAccount(t, client, &service.Account{Name: "window-cost-other"})

	start := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	sample := start.Add(time.Hour)
	end := start.Add(2 * time.Hour)
	insert := func(accountID, userID, keyID int64, at time.Time, cost float64) {
		t.Helper()
		_, err := repo.Create(ctx, &service.UsageLog{
			UserID: userID, APIKeyID: keyID, AccountID: accountID, RequestID: uuid.NewString(),
			Model: "claude-test", InputTokens: 1, OutputTokens: 1, TotalCost: cost, ActualCost: 0, CreatedAt: at,
		})
		require.NoError(t, err)
	}
	insert(account.ID, user1.ID, key1.ID, start.Add(-time.Second), 1000) // before start: excluded
	insert(account.ID, user1.ID, key1.ID, start, 1)                      // exactly at start: included
	insert(account.ID, user2.ID, key2.ID, sample.Add(-time.Second), 2)   // other user, before sample
	insert(account.ID, user1.ID, key1.ID, sample, 4)                     // at sample end: used only
	insert(account.ID, user1.ID, key1.ID, end.Add(-time.Second), 8)      // last second: included
	insert(account.ID, user1.ID, key1.ID, end, 100)                      // exactly at end: excluded
	insert(other.ID, user1.ID, key1.ID, start.Add(time.Minute), 500)     // other account: excluded

	costs, err := repo.GetAccountWindowCosts(ctx, []service.AccountWindowCostQuery{
		{AccountID: account.ID, Start: start, SampleEnd: sample, End: end},
		{AccountID: account.ID, Start: start, SampleEnd: start, End: end}, // duplicate pair stays separate
		{AccountID: other.ID, Start: end, SampleEnd: end, End: end.Add(time.Hour)},
		{AccountID: -1, Start: start, SampleEnd: sample, End: end},
	})
	require.NoError(t, err)
	require.Len(t, costs, 4)
	require.InDelta(t, 15.0, costs[0].UsedCost, 1e-9)
	require.InDelta(t, 3.0, costs[0].SampledCost, 1e-9)
	require.InDelta(t, 15.0, costs[1].UsedCost, 1e-9)
	require.Zero(t, costs[1].SampledCost)
	require.Zero(t, costs[2].UsedCost, "known zero for an empty window")
	require.Zero(t, costs[3].UsedCost)

	empty, err := repo.GetAccountWindowCosts(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestListGroupAudienceUserIDs(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUserRepositoryWithSQL(client, tx)

	group := mustCreateGroup(t, client, &service.Group{Name: "audience-group", IsExclusive: true})
	otherGroup := mustCreateGroup(t, client, &service.Group{Name: "audience-other"})
	owner := mustCreateUser(t, client, &service.User{Email: "audience-owner@test.com", AllowedGroups: []int64{group.ID}})
	mustCreateUser(t, client, &service.User{Email: "audience-unrelated@test.com", AllowedGroups: []int64{otherGroup.ID}})

	ids, err := repo.ListGroupAudienceUserIDs(ctx, group.ID, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{owner.ID}, ids)

	subscriber := mustCreateUser(t, client, &service.User{Email: "audience-sub@test.com"})
	mustCreateSubscription(t, client, &service.UserSubscription{UserID: subscriber.ID, GroupID: group.ID})
	ids, err = repo.ListGroupAudienceUserIDs(ctx, group.ID, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{owner.ID, subscriber.ID}, ids)

	// Soft-deleted users no longer count.
	_, err = tx.ExecContext(ctx, "UPDATE users SET deleted_at = NOW() WHERE id = $1", subscriber.ID)
	require.NoError(t, err)
	ids, err = repo.ListGroupAudienceUserIDs(ctx, group.ID, 2)
	require.NoError(t, err)
	require.Equal(t, []int64{owner.ID}, ids)
}
