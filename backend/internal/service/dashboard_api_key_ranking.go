package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// APIKeySpendingRankingRepository is an optional dashboard reporting capability.
type APIKeySpendingRankingRepository interface {
	GetAPIKeySpendingRanking(ctx context.Context, startTime, endTime time.Time, userID int64, limit int) (*usagestats.APIKeySpendingRankingResponse, error)
}

func (s *DashboardService) GetAPIKeySpendingRanking(ctx context.Context, startTime, endTime time.Time, userID int64, limit int) (*usagestats.APIKeySpendingRankingResponse, error) {
	repo, ok := s.usageRepo.(APIKeySpendingRankingRepository)
	if !ok {
		return nil, fmt.Errorf("API Key spending ranking is unavailable")
	}
	ranking, err := repo.GetAPIKeySpendingRanking(ctx, startTime, endTime, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("get API Key spending ranking: %w", err)
	}
	return ranking, nil
}
