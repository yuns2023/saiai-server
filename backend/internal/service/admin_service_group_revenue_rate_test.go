//go:build unit

package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminService_UpdateGroup_SubscriptionRevenueRateIsTriState(t *testing.T) {
	rate := func(v float64) *float64 { return &v }
	cases := []struct {
		name  string
		input UpdateGroupInput
		want  *float64
	}{
		{"absent keeps the stored rate", UpdateGroupInput{}, rate(0.64)},
		{"explicit null clears it", UpdateGroupInput{SubscriptionRevenueRateSet: true}, nil},
		{"zero is a configured free rate", UpdateGroupInput{SubscriptionRevenueRateSet: true, SubscriptionRevenueRate: rate(0)}, rate(0)},
		{"a value replaces it", UpdateGroupInput{SubscriptionRevenueRateSet: true, SubscriptionRevenueRate: rate(0.7)}, rate(0.7)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := &Group{ID: 1, Platform: PlatformAnthropic, Status: StatusActive, SubscriptionRevenueRate: rate(0.64)}
			repo := &groupRepoStubForAdmin{getByID: current}
			svc := &adminServiceImpl{groupRepo: repo}
			input := tc.input
			_, err := svc.UpdateGroup(context.Background(), 1, &input)
			require.NoError(t, err)
			require.NotNil(t, repo.updated)
			require.Equal(t, tc.want, repo.updated.SubscriptionRevenueRate)
		})
	}
}

func TestValidateSubscriptionRevenueRate(t *testing.T) {
	a, b := 0.1, 0.2
	ok := []float64{0, 0.64, 0.0001, 1000, 999.9999, a + b}
	for _, v := range ok {
		require.NoError(t, validateSubscriptionRevenueRate(&v), v)
	}
	sum := a + b
	require.Equal(t, 0.3, *normalizeRevenueRate(&sum))
	require.Nil(t, normalizeRevenueRate(nil))
	require.NoError(t, validateSubscriptionRevenueRate(nil))
	bad := []float64{-0.01, 1000.0001, 0.00004, 0.64001, math.NaN(), math.Inf(1)}
	for _, v := range bad {
		require.Error(t, validateSubscriptionRevenueRate(&v), v)
	}
}

func TestAdminService_CreateGroup_RejectsInvalidSubscriptionRevenueRate(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}
	bad := 0.00004
	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name: "g1", Platform: PlatformAnthropic, SubscriptionType: SubscriptionTypeSubscription, SubscriptionRevenueRate: &bad,
	})
	require.Error(t, err)
	require.Nil(t, repo.created)
}
