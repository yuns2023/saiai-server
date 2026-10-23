//go:build unit

package service

import (
	"context"
	"math"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAdminService_CreateGroup_RejectsInvalidRateMultiplier(t *testing.T) {
	for name, rate := range map[string]float64{"negative": -0.5, "nan": math.NaN(), "inf": math.Inf(1)} {
		t.Run(name, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{}
			svc := &adminServiceImpl{groupRepo: repo}
			_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
				Name: "g", Platform: PlatformOpenAI, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: rate,
			})
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "INVALID_RATE_MULTIPLIER", infraerrors.Reason(err))
			require.Nil(t, repo.created)
		})
	}
}

func TestAdminService_CreateGroup_AllowsZeroRateMultiplier(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}
	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name: "g", Platform: PlatformOpenAI, SubscriptionType: SubscriptionTypeStandard, IsExclusive: true,
		RateMultiplier: 0, HostedUsageEnabled: true,
	})
	require.NoError(t, err)
	require.NotNil(t, repo.created)
	require.Zero(t, repo.created.RateMultiplier)
	require.True(t, repo.created.UsesHostedUsage())
}

func TestAdminService_UpdateGroup_RejectsInvalidRateMultiplier(t *testing.T) {
	for name, rate := range map[string]float64{"negative": -1, "nan": math.NaN(), "inf": math.Inf(-1)} {
		t.Run(name, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{getByID: &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1}}
			svc := &adminServiceImpl{groupRepo: repo}
			_, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{RateMultiplier: &rate})
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "INVALID_RATE_MULTIPLIER", infraerrors.Reason(err))
			require.Nil(t, repo.updated)
		})
	}
}

func TestAdminService_UpdateGroup_AllowsZeroRateMultiplier(t *testing.T) {
	repo := &groupRepoStubForAdmin{getByID: &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1}}
	svc := &adminServiceImpl{groupRepo: repo}
	zero := 0.0
	_, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{RateMultiplier: &zero})
	require.NoError(t, err)
	require.NotNil(t, repo.updated)
	require.Zero(t, repo.updated.RateMultiplier)
}
