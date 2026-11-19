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
	tier, plain, target := f.group(t, true), f.group(t, false), f.group(t, false)
	// Non-zero values, so a column dropped from the projection shows up in
	// the full-row comparison below.
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET claude_code_only = TRUE,
		model_rate_multipliers = '{"claude-fable-5": 0.5}'::jsonb WHERE id = $1`, tier)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET fallback_group_id = $2,
		fallback_group_id_on_invalid_request = $2, hosted_usage_enabled = TRUE, is_exclusive = TRUE WHERE id = $1`, plain, target)
	require.NoError(t, err)
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
		require.Equal(t, tc.tier, got.Group.ClaudeCodeOnly)
		require.Equal(t, !tc.tier, got.Group.HostedUsageEnabled)
		require.Equal(t, !tc.tier, got.Group.IsExclusive)
		require.Equal(t, !tc.tier, got.Group.FallbackGroupID != nil)

		// Every other field the auth path loads matches a full row load.
		full, err := f.groups.GetByIDLite(ctx, tc.groupID)
		require.NoError(t, err)
		require.Equal(t, authOnlyGroupFields(full), authOnlyGroupFields(got.Group))
	}
}

// countingAuthRepo counts the database auth loads.
type countingAuthRepo struct {
	*apiKeyRepository
	loads int
}

func (r *countingAuthRepo) GetByKeyForAuth(ctx context.Context, key string) (*service.APIKey, error) {
	r.loads++
	return r.apiKeyRepository.GetByKeyForAuth(ctx, key)
}

// The same projection through the real auth service and the real Redis auth
// cache: a v7 entry (written while market_tier was not loaded, so false) is a
// miss, rebuilt from the database as v8 with market_tier, and a fresh service
// (no L1) is then served by that Redis entry without a database load.
func TestAuthCacheRebuildsV7MarketTierThroughRedis(t *testing.T) {
	ctx := context.Background()
	f := newMarketFixture(t)
	buyer := f.user(t)
	tier := f.group(t, true)
	repo := &countingAuthRepo{apiKeyRepository: newAPIKeyRepositoryWithSQL(integrationEntClient, integrationDB)}
	key := marketAuthKey(t, repo.apiKeyRepository, buyer, tier)

	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60, NegativeTTLSeconds: 30}}
	cache := NewAPIKeyCache(testRedis(t))
	sum := sha256.Sum256([]byte(key.Key)) // APIKeyService.authCacheKey
	cacheKey := hex.EncodeToString(sum[:])

	// The entry the buggy binary wrote.
	gid := tier
	require.NoError(t, cache.SetAuthCache(ctx, cacheKey, &service.APIKeyAuthCacheEntry{Snapshot: &service.APIKeyAuthSnapshot{
		SchemaVersion: 7, APIKeyID: key.ID, UserID: buyer, GroupID: &gid, Status: service.StatusActive,
		User:  service.APIKeyAuthUserSnapshot{ID: buyer, Status: service.StatusActive, Role: service.RoleUser},
		Group: &service.APIKeyAuthGroupSnapshot{ID: tier, Platform: service.PlatformAnthropic, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard, RateMultiplier: 1.2},
	}}, time.Minute))

	first, err := service.NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg).GetByKey(ctx, key.Key)
	require.NoError(t, err)
	require.True(t, first.Group.MarketTier, "the v7 entry is not trusted")
	require.Equal(t, 1, repo.loads)

	stored, err := cache.GetAuthCache(ctx, cacheKey)
	require.NoError(t, err)
	require.NotNil(t, stored.Snapshot)
	require.Equal(t, 8, stored.Snapshot.SchemaVersion)
	require.NotNil(t, stored.Snapshot.Group)
	require.True(t, stored.Snapshot.Group.MarketTier, "rebuilt in the auth cache")

	second, err := service.NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg).GetByKey(ctx, key.Key)
	require.NoError(t, err)
	require.True(t, second.Group.MarketTier, "served from the auth cache")
	require.True(t, second.Group.Hydrated)
	require.Equal(t, 1, repo.loads, "the v8 entry is a real L2 hit")
}
