//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// marketAuthKey creates a buyer key bound to groupID; deleted before the
// fixture's users (cleanups run last-registered first).
func marketAuthKey(t *testing.T, repo *apiKeyRepository, userID, groupID int64) *service.APIKey {
	t.Helper()
	gid := groupID
	key := &service.APIKey{UserID: userID, Key: "market-auth-" + uuid.NewString(), Name: "market auth", GroupID: &gid, Status: service.StatusActive}
	require.NoError(t, repo.Create(context.Background(), key))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM api_keys WHERE id = ANY($1)", pq.Array([]int64{key.ID}))
	})
	return key
}

// authOnlyGroupFields clears the group fields GetByKeyForAuth intentionally
// does not load, so the rest can be compared with a full row load.
func authOnlyGroupFields(g *service.Group) service.Group {
	out := *g
	out.Description = ""
	out.ShowAccountQuota = false
	out.SubscriptionRevenueRate = nil
	out.SoraStorageQuotaBytes = 0
	out.DefaultValidityDays = 0
	out.SortOrder = 0
	out.CreatedAt = time.Time{}
	out.UpdatedAt = time.Time{}
	return out
}

// The auth projection (the group the gateway puts in the request context and
// the auth snapshot caches) must carry market_tier: the market classification
// reads it from there, and a tier loaded as a plain group excludes every
// listed account (buyers got 503 "No available accounts").
func TestGetByKeyForAuthLoadsMarketTier(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	buyer := f.user(t)
	tier, plain := f.group(t, true), f.group(t, false)
	repo := newAPIKeyRepositoryWithSQL(integrationEntClient, integrationDB)
	tierKey := marketAuthKey(t, repo, buyer, tier)
	plainKey := marketAuthKey(t, repo, buyer, plain)

	for _, tc := range []struct {
		key     *service.APIKey
		groupID int64
		tier    bool
	}{{tierKey, tier, true}, {plainKey, plain, false}} {
		got, err := repo.GetByKeyForAuth(ctx, tc.key.Key)
		require.NoError(t, err)
		require.NotNil(t, got.Group)
		require.True(t, got.Group.Hydrated)
		require.Equal(t, tc.groupID, got.Group.ID)
		require.Equal(t, tc.tier, got.Group.MarketTier, "group %d market_tier from the auth projection", tc.groupID)

		// Every other field the auth path loads matches a full row load.
		full, err := f.groups.GetByIDLite(ctx, tc.groupID)
		require.NoError(t, err)
		require.Equal(t, authOnlyGroupFields(full), authOnlyGroupFields(got.Group))
	}
}

// The same projection through the real auth service and the real Redis
// auth cache: the DB load, then the L2 hit, both keep market_tier.
func TestAuthCacheKeepsMarketTierThroughRedis(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	buyer := f.user(t)
	tier := f.group(t, true)
	repo := newAPIKeyRepositoryWithSQL(integrationEntClient, integrationDB)
	key := marketAuthKey(t, repo, buyer, tier)

	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60, NegativeTTLSeconds: 30}}
	cache := NewAPIKeyCache(testRedis(t))
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg)

	first, err := svc.GetByKey(ctx, key.Key)
	require.NoError(t, err)
	require.True(t, first.Group.MarketTier, "loaded from the database")

	// A fresh service has no L1 and must be served by the Redis entry.
	sum := sha256.Sum256([]byte(key.Key)) // APIKeyService.authCacheKey
	stored, err := cache.GetAuthCache(ctx, hex.EncodeToString(sum[:]))
	require.NoError(t, err)
	require.NotNil(t, stored.Snapshot)
	require.NotNil(t, stored.Snapshot.Group)
	require.True(t, stored.Snapshot.Group.MarketTier, "stored in the auth cache")

	second, err := service.NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg).GetByKey(ctx, key.Key)
	require.NoError(t, err)
	require.True(t, second.Group.MarketTier, "served from the auth cache")
	require.True(t, second.Group.Hydrated)
}
