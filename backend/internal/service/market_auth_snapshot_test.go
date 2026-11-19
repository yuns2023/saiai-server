//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// authCachedKey sends a key through the auth cache encoding (snapshot, JSON,
// decode, apply) as a cache hit would return it.
func authCachedKey(t *testing.T, key *APIKey) (*APIKey, *APIKeyAuthCacheEntry) {
	t.Helper()
	svc := &APIKeyService{}
	snapshot := svc.snapshotFromAPIKey(key)
	require.NotNil(t, snapshot)
	require.Equal(t, apiKeyAuthSnapshotSchemaVersion, snapshot.SchemaVersion)
	encoded, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	var decoded APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	restored, ok, err := svc.applyAuthCacheEntry(key.Key, &decoded)
	require.NoError(t, err)
	require.True(t, ok)
	return restored, &decoded
}

func TestAuthSnapshotRoundTripsMarketTier(t *testing.T) {
	for _, tier := range []bool{true, false} {
		groupID := int64(21)
		key := &APIKey{ID: 1, UserID: 2, Key: "k", User: &User{ID: 2, Status: StatusActive}, GroupID: &groupID,
			Group: &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1.2, MarketTier: tier, Hydrated: true}}
		restored, entry := authCachedKey(t, key)
		require.Equal(t, tier, restored.Group.MarketTier)
		require.True(t, restored.Group.Hydrated)

		// v7 snapshots were written while GetByKeyForAuth did not load
		// market_tier (always false): never trusted, rebuilt from the DB.
		entry.Snapshot.SchemaVersion = 7
		stale, ok, err := (&APIKeyService{}).applyAuthCacheEntry("k", entry)
		require.NoError(t, err)
		require.False(t, ok)
		require.Nil(t, stale)
	}
}

// The buyer path classifies with the context group the auth middleware sets
// from the (cached) key: a tier group restored from the auth snapshot must
// classify the listed account as buyer, the same group without market_tier
// (the shape the old projection produced) excludes it.
func TestMarketClassificationWithAuthSnapshotGroup(t *testing.T) {
	env := newMarketEnv(t, nil)
	listed := mktListedAccount(1)
	env.usage.obs[1] = freshObservations()

	classify := func(group *Group) string {
		gid := group.ID
		dbKey := &APIKey{ID: mktKey, UserID: mktBuyer, Key: "buyer-key", GroupID: &gid, Group: group, User: &User{ID: mktBuyer, Status: StatusActive}}
		authKey, _ := authCachedKey(t, dbKey)
		ctx := WithAccountUserID(context.Background(), mktBuyer)
		ctx = WithAccountAPIKeyID(ctx, mktKey)
		ctx = WithMarketDispatch(ctx, authKey)
		ctx = context.WithValue(ctx, ctxkey.Group, authKey.Group)
		ctx = withMarketRequestedModel(withMarketSelectionGroup(ctx, authKey.GroupID), "claude-fable-5")
		a := listed
		c, _ := env.svc.classifyMarketCandidate(ctx, &a)
		return c
	}

	tierGroup := *mktGroups()[mktTier]
	require.Equal(t, MarketClassBuyer, classify(&tierGroup))

	missing := tierGroup
	missing.MarketTier = false
	require.Equal(t, MarketClassExcluded, classify(&missing))
}
