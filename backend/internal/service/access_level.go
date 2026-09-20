package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const AccessLevelBalanceMaintenanceSettingKey = "access_level_balance_maintenance_enabled"

var (
	ErrAccessLevelNotFound = infraerrors.NotFound("ACCESS_LEVEL_NOT_FOUND", "access level not found")
	ErrAccessLevelInUse    = infraerrors.Conflict("ACCESS_LEVEL_IN_USE", "access level is still in use")
)

// AccessLevel is a configurable balance tier. Name is intentionally editable
// so operators can use product-specific or playful display names.
type AccessLevel struct {
	ID                     int64     `json:"id"`
	Name                   string    `json:"name"`
	Rank                   int       `json:"rank"`
	BalanceThreshold       float64   `json:"balance_threshold"`
	PaygDiscountMultiplier float64   `json:"payg_discount_multiplier"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

func (l *AccessLevel) Validate() error {
	if l == nil || strings.TrimSpace(l.Name) == "" || len([]rune(strings.TrimSpace(l.Name))) > 100 {
		return infraerrors.BadRequest("INVALID_ACCESS_LEVEL_NAME", "access level name is required and must not exceed 100 characters")
	}
	if l.Rank < 0 {
		return infraerrors.BadRequest("INVALID_ACCESS_LEVEL_RANK", "access level rank must be non-negative")
	}
	if math.IsNaN(l.BalanceThreshold) || math.IsInf(l.BalanceThreshold, 0) || l.BalanceThreshold < 0 || math.Abs(l.BalanceThreshold*1e8-math.Round(l.BalanceThreshold*1e8)) > 1e-6 {
		return infraerrors.BadRequest("INVALID_ACCESS_LEVEL_THRESHOLD", "balance threshold must be non-negative with at most eight decimals")
	}
	if math.IsNaN(l.PaygDiscountMultiplier) || math.IsInf(l.PaygDiscountMultiplier, 0) || l.PaygDiscountMultiplier < 0 || l.PaygDiscountMultiplier > 1 || math.Abs(l.PaygDiscountMultiplier*1e4-math.Round(l.PaygDiscountMultiplier*1e4)) > 1e-7 {
		return infraerrors.BadRequest("INVALID_ACCESS_LEVEL_DISCOUNT", "PAYG discount multiplier must be between 0 and 1 with at most four decimals")
	}
	l.Name = strings.TrimSpace(l.Name)
	return nil
}

type AccessLevelRepository interface {
	List(ctx context.Context) ([]AccessLevel, error)
	GetByID(ctx context.Context, id int64) (*AccessLevel, error)
	Create(ctx context.Context, level *AccessLevel) error
	Update(ctx context.Context, level *AccessLevel) error
	Delete(ctx context.Context, id int64) error
	UsageCount(ctx context.Context, id int64) (int, error)
	ReferenceIDs(ctx context.Context, id int64) ([]int64, []int64, error)
	RecomputeUsers(ctx context.Context, maintenanceEnabled bool) ([]int64, error)
	SetManualLevel(ctx context.Context, userID, levelID int64) error
	ClearManualLevel(ctx context.Context, userID int64) error
}

type AccessLevelService struct {
	repo                 AccessLevelRepository
	settingRepo          SettingRepository
	authCacheInvalidator APIKeyAuthCacheInvalidator
}

func NewAccessLevelService(repo AccessLevelRepository, settingRepo SettingRepository, authCacheInvalidator APIKeyAuthCacheInvalidator) *AccessLevelService {
	return &AccessLevelService{repo: repo, settingRepo: settingRepo, authCacheInvalidator: authCacheInvalidator}
}

func (s *AccessLevelService) List(ctx context.Context) ([]AccessLevel, error) {
	return s.repo.List(ctx)
}

func (s *AccessLevelService) Create(ctx context.Context, level *AccessLevel) (*AccessLevel, error) {
	if err := level.Validate(); err != nil {
		return nil, err
	}
	levels, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateAccessLevelOrdering(append(levels, *level)); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, level); err != nil {
		return nil, fmt.Errorf("create access level: %w", err)
	}
	if err := s.recomputeAndInvalidate(ctx); err != nil {
		return nil, err
	}
	return level, nil
}

func (s *AccessLevelService) Update(ctx context.Context, level *AccessLevel) (*AccessLevel, error) {
	if err := level.Validate(); err != nil {
		return nil, err
	}
	levels, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for i := range levels {
		if levels[i].ID == level.ID {
			levels[i] = *level
			found = true
			break
		}
	}
	if !found {
		return nil, ErrAccessLevelNotFound
	}
	if err := validateAccessLevelOrdering(levels); err != nil {
		return nil, err
	}
	if err := s.repo.Update(ctx, level); err != nil {
		return nil, fmt.Errorf("update access level: %w", err)
	}
	userIDs, groupIDs, err := s.repo.ReferenceIDs(ctx, level.ID)
	if err != nil {
		return nil, err
	}
	s.invalidateReferences(ctx, userIDs, groupIDs)
	// Threshold, rank and discount edits all affect cached authorization/billing.
	if err := s.recomputeAndInvalidate(ctx); err != nil {
		return nil, err
	}
	return level, nil
}

func (s *AccessLevelService) Delete(ctx context.Context, id int64) error {
	levels, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	if len(levels) <= 1 {
		return infraerrors.Conflict("LAST_ACCESS_LEVEL", "the last access level cannot be deleted")
	}
	used, err := s.repo.UsageCount(ctx, id)
	if err != nil {
		return err
	}
	if used > 0 {
		return ErrAccessLevelInUse
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	return s.recomputeAndInvalidate(ctx)
}

func (s *AccessLevelService) BalanceMaintenanceEnabled(ctx context.Context) (bool, error) {
	value, err := s.settingRepo.GetValue(ctx, AccessLevelBalanceMaintenanceSettingKey)
	if err != nil {
		if err == ErrSettingNotFound {
			return false, nil
		}
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(value), "true"), nil
}

func (s *AccessLevelService) SetBalanceMaintenanceEnabled(ctx context.Context, enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	if err := s.settingRepo.Set(ctx, AccessLevelBalanceMaintenanceSettingKey, value); err != nil {
		return err
	}
	return s.recomputeAndInvalidateWithMode(ctx, enabled)
}

func (s *AccessLevelService) SetManualLevel(ctx context.Context, userID, levelID int64) error {
	if _, err := s.repo.GetByID(ctx, levelID); err != nil {
		return err
	}
	if err := s.repo.SetManualLevel(ctx, userID, levelID); err != nil {
		return err
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	return nil
}

func (s *AccessLevelService) ClearManualLevel(ctx context.Context, userID int64) error {
	if err := s.repo.ClearManualLevel(ctx, userID); err != nil {
		return err
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	return nil
}

func (s *AccessLevelService) recomputeAndInvalidate(ctx context.Context) error {
	enabled, err := s.BalanceMaintenanceEnabled(ctx)
	if err != nil {
		return err
	}
	return s.recomputeAndInvalidateWithMode(ctx, enabled)
}

func (s *AccessLevelService) recomputeAndInvalidateWithMode(ctx context.Context, enabled bool) error {
	userIDs, err := s.repo.RecomputeUsers(ctx, enabled)
	if err != nil {
		return err
	}
	s.invalidateReferences(ctx, userIDs, nil)
	return nil
}

func (s *AccessLevelService) invalidateReferences(ctx context.Context, userIDs, groupIDs []int64) {
	if s.authCacheInvalidator == nil {
		return
	}
	for _, userID := range userIDs {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	for _, groupID := range groupIDs {
		s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, groupID)
	}
}

func validateAccessLevelOrdering(levels []AccessLevel) error {
	if len(levels) == 0 {
		return infraerrors.BadRequest("EMPTY_ACCESS_LEVELS", "at least one access level is required")
	}
	sort.Slice(levels, func(i, j int) bool { return levels[i].Rank < levels[j].Rank })
	for i := range levels {
		if i > 0 && levels[i].Rank == levels[i-1].Rank {
			return infraerrors.Conflict("ACCESS_LEVEL_RANK_EXISTS", "access level rank already exists")
		}
		if i > 0 && levels[i].BalanceThreshold <= levels[i-1].BalanceThreshold {
			return infraerrors.BadRequest("INVALID_ACCESS_LEVEL_ORDER", "balance thresholds must increase with level rank")
		}
	}
	return nil
}
