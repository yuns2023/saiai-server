//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageService_InvalidateUsageCaches(t *testing.T) {
	invalidator := &authCacheInvalidatorStub{}
	svc := &UsageService{authCacheInvalidator: invalidator}

	svc.invalidateUsageCaches(context.Background(), 7, false)
	require.Empty(t, invalidator.userIDs)

	svc.invalidateUsageCaches(context.Background(), 7, true)
	require.Equal(t, []int64{7}, invalidator.userIDs)
}

func TestRedeemService_InvalidateRedeemCaches_AuthCache(t *testing.T) {
	invalidator := &authCacheInvalidatorStub{}
	svc := &RedeemService{authCacheInvalidator: invalidator}

	svc.invalidateRedeemCaches(context.Background(), 11, &RedeemCode{Type: RedeemTypeBalance})
	svc.invalidateRedeemCaches(context.Background(), 11, &RedeemCode{Type: RedeemTypeConcurrency})
	groupID := int64(3)
	svc.invalidateRedeemCaches(context.Background(), 11, &RedeemCode{Type: RedeemTypeSubscription, GroupID: &groupID})

	require.Equal(t, []int64{11, 11, 11}, invalidator.userIDs)
}

// ctxRecordingInvalidator records the context state each call received.
type ctxRecordingInvalidator struct {
	errs         []error
	hasDeadlines []bool
}

func (r *ctxRecordingInvalidator) InvalidateAuthCacheByKey(context.Context, string) {}
func (r *ctxRecordingInvalidator) InvalidateAuthCacheByUserID(ctx context.Context, _ int64) {
	_, ok := ctx.Deadline()
	r.errs = append(r.errs, ctx.Err())
	r.hasDeadlines = append(r.hasDeadlines, ok)
}
func (r *ctxRecordingInvalidator) InvalidateAuthCacheByGroupID(context.Context, int64) {}

// The request may already be cancelled (client hung up after the commit); the
// auth cache must still be invalidated, under its own deadline.
func TestRedeemService_InvalidateRedeemCaches_SurvivesCancelledRequest(t *testing.T) {
	inv := &ctxRecordingInvalidator{}
	svc := &RedeemService{authCacheInvalidator: inv}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	groupID := int64(3)
	for _, code := range []*RedeemCode{
		{Type: RedeemTypeBalance},
		{Type: RedeemTypeConcurrency},
		{Type: RedeemTypeSubscription, GroupID: &groupID},
		{Type: RedeemTypeClaudeDevice},
	} {
		svc.invalidateRedeemCaches(ctx, 11, code)
	}
	require.Len(t, inv.errs, 4)
	for i := range inv.errs {
		require.NoError(t, inv.errs[i])
		require.True(t, inv.hasDeadlines[i])
	}
}
