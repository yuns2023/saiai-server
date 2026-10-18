package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type riskCounterStub struct {
	UserRepository
	fail       bool
	ids        []int64
	start, end time.Time
}

func (r *riskCounterStub) CountInputRiskAlerts(_ context.Context, ids []int64, start, end time.Time) (map[int64]int64, error) {
	r.ids = ids
	r.start = start
	r.end = end
	if r.fail {
		return nil, errors.New("unavailable")
	}
	return map[int64]int64{1: 2}, nil
}
func TestInputRiskSummaryBatchAndUnavailable(t *testing.T) {
	repo := &riskCounterStub{}
	s := &adminServiceImpl{userRepo: repo}
	users := []User{{ID: 1}, {ID: 2}}
	s.enrichInputRiskAlerts(context.Background(), users)
	require.Equal(t, []int64{1, 2}, repo.ids)
	require.Equal(t, 30*24*time.Hour, repo.end.Sub(repo.start))
	require.Equal(t, int64(2), users[0].InputRiskAlerts.Count)
	require.Zero(t, users[1].InputRiskAlerts.Count)
	require.Equal(t, users[0].InputRiskAlerts.StartTime, users[1].InputRiskAlerts.StartTime)
	repo.fail = true
	users = []User{{ID: 1}}
	s.enrichInputRiskAlerts(context.Background(), users)
	require.Nil(t, users[0].InputRiskAlerts)
}
