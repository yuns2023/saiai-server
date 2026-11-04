//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The admin save path (repository Update) must accept a hosted Anthropic group;
// before migration 119 the database check rejected it with a 500.
func TestMigration119HostedUsageAllowsAnthropicGroups(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	groups := newGroupRepositoryWithSQL(tx.Client(), tx)

	g := &service.Group{Name: "hosted-anthropic-" + uuid.NewString(), Platform: service.PlatformAnthropic,
		IsExclusive: true, SubscriptionType: service.SubscriptionTypeStandard, Status: service.StatusActive}
	require.NoError(t, groups.Create(ctx, g))
	g.HostedUsageEnabled = true
	require.NoError(t, groups.Update(ctx, g))
	loaded, err := groups.GetByIDLite(ctx, g.ID)
	require.NoError(t, err)
	require.True(t, loaded.UsesHostedUsage())

	openai := &service.Group{Name: "hosted-openai-" + uuid.NewString(), Platform: service.PlatformOpenAI, IsExclusive: true,
		HostedUsageEnabled: true, SubscriptionType: service.SubscriptionTypeStandard, Status: service.StatusActive}
	require.NoError(t, groups.Create(ctx, openai), "OpenAI hosted groups keep working")

	// The rest of the rule is unchanged.
	for _, set := range []string{"platform='gemini'", "rate_multiplier=1", "is_exclusive=false", "subscription_type='subscription'"} {
		_, err = tx.ExecContext(ctx, "SAVEPOINT p")
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, "UPDATE groups SET "+set+" WHERE id=$1", g.ID)
		require.ErrorContains(t, err, "groups_hosted_usage_policy", set)
		_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT p")
		require.NoError(t, err)
	}
	// Without hosted usage the other combinations stay allowed.
	_, err = tx.ExecContext(ctx, "UPDATE groups SET hosted_usage_enabled=false, rate_multiplier=1, is_exclusive=false WHERE id=$1", g.ID)
	require.NoError(t, err)
}
