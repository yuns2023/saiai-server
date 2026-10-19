package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/robfig/cron/v3"
)

const (
	carpoolMaintenanceSchedule   = "* * * * *"
	carpoolMaintenanceDayLayout  = "2006-01-02"
	carpoolMaintenanceRunTimeout = 15 * time.Minute
)

var carpoolMaintenanceCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// CarpoolMaintenanceService checks per-account carpool schedules and retains daily single-device maintenance.
type CarpoolMaintenanceService struct {
	accountRepo AccountRepository
	cache       IdentityCache
	cfg         *config.Config

	cron      *cron.Cron
	startOnce sync.Once
	stopOnce  sync.Once
}

type carpoolMaintenanceStats struct {
	Eligible         int
	Expanded         int
	Rotated          int
	AlreadyProcessed int
	FreeCapacity     int
	Failed           int
}

func (s carpoolMaintenanceStats) String() string {
	return fmt.Sprintf(
		"eligible=%d expanded=%d rotated=%d already_processed=%d free_capacity=%d failed=%d",
		s.Eligible,
		s.Expanded,
		s.Rotated,
		s.AlreadyProcessed,
		s.FreeCapacity,
		s.Failed,
	)
}

func NewCarpoolMaintenanceService(accountRepo AccountRepository, cache IdentityCache, cfg *config.Config) *CarpoolMaintenanceService {
	return &CarpoolMaintenanceService{accountRepo: accountRepo, cache: cache, cfg: cfg}
}

func (s *CarpoolMaintenanceService) Start() {
	if s == nil || s.accountRepo == nil || s.cache == nil {
		return
	}
	s.startOnce.Do(func() {
		loc := s.location()
		c := cron.New(cron.WithParser(carpoolMaintenanceCronParser), cron.WithLocation(loc))
		if _, err := c.AddFunc(carpoolMaintenanceSchedule, s.runScheduled); err != nil {
			logger.LegacyPrintf("service.carpool_maintenance", "[CarpoolMaintenance] not started: %v", err)
			return
		}
		s.cron = c
		s.cron.Start()
		logger.LegacyPrintf("service.carpool_maintenance", "[CarpoolMaintenance] started (schedule=%q tz=%s)", carpoolMaintenanceSchedule, loc.String())
	})
}

func (s *CarpoolMaintenanceService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cron == nil {
			return
		}
		ctx := s.cron.Stop()
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
			logger.LegacyPrintf("service.carpool_maintenance", "[CarpoolMaintenance] cron stop timed out")
		}
	})
}

func (s *CarpoolMaintenanceService) runScheduled() {
	ctx, cancel := context.WithTimeout(context.Background(), carpoolMaintenanceRunTimeout)
	defer cancel()

	stats, err := s.runOnce(ctx, time.Now())
	if err != nil {
		logger.LegacyPrintf("service.carpool_maintenance", "[CarpoolMaintenance] completed with errors: %s err=%v", stats, err)
		return
	}
	logger.LegacyPrintf("service.carpool_maintenance", "[CarpoolMaintenance] complete: %s", stats)
}

func (s *CarpoolMaintenanceService) runOnce(ctx context.Context, now time.Time) (carpoolMaintenanceStats, error) {
	stats := carpoolMaintenanceStats{}
	if s == nil || s.accountRepo == nil || s.cache == nil {
		return stats, nil
	}

	day := now.In(s.location()).Format(carpoolMaintenanceDayLayout)
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformAnthropic)
	if err != nil {
		return stats, err
	}

	var runErr error
	for i := range accounts {
		account := &accounts[i]
		if account.IsClaudeOAuthSingleDeviceAdmissionAutoExpandEnabled() {
			if local := now.In(s.location()); local.Hour() != 0 || local.Minute() != 0 {
				continue
			}
			stats.Eligible++
			if err := s.maintainSingleDeviceAdmission(ctx, account, day, &stats); err != nil {
				stats.Failed++
				runErr = errors.Join(runErr, fmt.Errorf("account %d single_device admission: %w", account.ID, err))
			}
			continue
		}
		if !account.IsClaudeOAuthCarpoolAutoExpandEnabled() {
			continue
		}
		stats.Eligible++
		if err := s.maintainCarpoolSlots(ctx, account, now, &stats); err != nil {
			stats.Failed++
			runErr = errors.Join(runErr, fmt.Errorf("account %d carpool slots: %w", account.ID, err))
		}
	}

	return stats, runErr
}

func (s *CarpoolMaintenanceService) maintainSingleDeviceAdmission(ctx context.Context, account *Account, day string, stats *carpoolMaintenanceStats) error {
	if account.GetClaudeOAuthSingleDeviceAdmissionLastMaintenanceDay() == day {
		stats.AlreadyProcessed++
		return nil
	}
	current, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil {
		return err
	}
	if !current.IsClaudeOAuthSingleDeviceAdmissionAutoExpandEnabled() {
		return nil
	}
	if current.GetClaudeOAuthSingleDeviceAdmissionLastMaintenanceDay() == day {
		stats.AlreadyProcessed++
		return nil
	}
	const limitKey = "claude_oauth_single_device_admission_limit"
	const dayKey = "claude_oauth_single_device_admission_last_maintenance_day"
	limit := current.GetClaudeOAuthSingleDeviceAdmissionLimit()
	target := current.GetClaudeOAuthSingleDeviceAdmissionTarget()
	if limit < target {
		if err := s.accountRepo.UpdateExtra(ctx, current.ID, map[string]any{limitKey: min(limit+1, target), dayKey: day}); err != nil {
			return err
		}
		stats.Expanded++
		return nil
	}
	if limit > target {
		if err := s.accountRepo.UpdateExtra(ctx, current.ID, map[string]any{dayKey: day}); err != nil {
			return err
		}
		stats.FreeCapacity++
		return nil
	}
	rotation, err := s.cache.RotateSingleDeviceAdmissionForDay(ctx, current.ID, limit, day)
	if err != nil {
		return err
	}
	if err := s.accountRepo.UpdateExtra(ctx, current.ID, map[string]any{dayKey: day}); err != nil {
		return err
	}
	if rotation == nil || !rotation.Applied {
		stats.AlreadyProcessed++
	} else if rotation.Evicted == nil {
		stats.FreeCapacity++
	} else {
		stats.Rotated++
		logger.LegacyPrintf("service.carpool_maintenance", "[CarpoolMaintenance] rotated single_device incoming device account_id=%d device_key=%s", current.ID, rotation.Evicted.DeviceKey)
	}
	return nil
}

func (s *CarpoolMaintenanceService) location() *time.Location {
	if s != nil && s.cfg != nil && strings.TrimSpace(s.cfg.Timezone) != "" {
		if loc, err := time.LoadLocation(strings.TrimSpace(s.cfg.Timezone)); err == nil && loc != nil {
			return loc
		}
	}
	return time.Local
}
