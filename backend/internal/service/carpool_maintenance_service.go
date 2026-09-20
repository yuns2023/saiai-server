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
	carpoolMaintenanceSchedule   = "0 0 * * *"
	carpoolMaintenanceDayLayout  = "2006-01-02"
	carpoolMaintenanceRunTimeout = 15 * time.Minute

	carpoolDeviceLimitKey     = "claude_oauth_carpool_device_limit"
	carpoolLastMaintenanceKey = "claude_oauth_carpool_last_maintenance_day"
)

var carpoolMaintenanceCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// CarpoolMaintenanceService expands bounded carpool capacity up to 16 and,
// once the configured limit is exactly 16, frees one LRU device whenever the
// registry is full. A limit above 16 is administrator-controlled and is never
// changed or rotated by this maintenance job.
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
		if !account.IsClaudeOAuthCarpoolAutoExpandEnabled() {
			continue
		}
		stats.Eligible++
		if account.GetClaudeOAuthCarpoolLastMaintenanceDay() == day {
			stats.AlreadyProcessed++
			continue
		}

		// Refresh immediately before mutation so an administrator's latest mode,
		// switch, unlimited flag, and limit win over the daily snapshot.
		current, getErr := s.accountRepo.GetByID(ctx, account.ID)
		if getErr != nil {
			stats.Failed++
			runErr = errors.Join(runErr, fmt.Errorf("account %d refresh: %w", account.ID, getErr))
			continue
		}
		if !current.IsClaudeOAuthCarpoolAutoExpandEnabled() {
			continue
		}
		if current.GetClaudeOAuthCarpoolLastMaintenanceDay() == day {
			stats.AlreadyProcessed++
			continue
		}

		limit := current.GetClaudeOAuthCarpoolDeviceLimit()
		if limit < ClaudeOAuthCarpoolAutoExpandLimit {
			nextLimit := min(limit+1, ClaudeOAuthCarpoolAutoExpandLimit)
			if updateErr := s.accountRepo.UpdateExtra(ctx, current.ID, map[string]any{
				carpoolDeviceLimitKey:     nextLimit,
				carpoolLastMaintenanceKey: day,
			}); updateErr != nil {
				stats.Failed++
				runErr = errors.Join(runErr, fmt.Errorf("account %d expand: %w", current.ID, updateErr))
				continue
			}
			stats.Expanded++
			continue
		}
		if limit > ClaudeOAuthCarpoolAutoExpandLimit {
			// Administrators who explicitly allow more than 16 devices own that
			// capacity and device lifecycle. Do not evict or change their slots.
			if updateErr := s.accountRepo.UpdateExtra(ctx, current.ID, map[string]any{carpoolLastMaintenanceKey: day}); updateErr != nil {
				stats.Failed++
				runErr = errors.Join(runErr, fmt.Errorf("account %d record maintenance day: %w", current.ID, updateErr))
				continue
			}
			stats.FreeCapacity++
			continue
		}

		rotation, rotateErr := s.cache.RotateCarpoolDeviceForDay(ctx, current.ID, limit, day)
		if rotateErr != nil {
			stats.Failed++
			runErr = errors.Join(runErr, fmt.Errorf("account %d rotate: %w", current.ID, rotateErr))
			continue
		}
		if updateErr := s.accountRepo.UpdateExtra(ctx, current.ID, map[string]any{carpoolLastMaintenanceKey: day}); updateErr != nil {
			stats.Failed++
			runErr = errors.Join(runErr, fmt.Errorf("account %d record maintenance day: %w", current.ID, updateErr))
			continue
		}
		if rotation == nil || !rotation.Applied {
			stats.AlreadyProcessed++
			continue
		}
		if rotation.Evicted == nil {
			stats.FreeCapacity++
			continue
		}
		stats.Rotated++
		logger.LegacyPrintf(
			"service.carpool_maintenance",
			"[CarpoolMaintenance] evicted LRU device account_id=%d device_key=%s last_seen_at=%d limit=%d",
			current.ID,
			rotation.Evicted.DeviceKey,
			rotation.Evicted.LastSeenAt,
			limit,
		)
	}

	return stats, runErr
}

func (s *CarpoolMaintenanceService) location() *time.Location {
	if s != nil && s.cfg != nil && strings.TrimSpace(s.cfg.Timezone) != "" {
		if loc, err := time.LoadLocation(strings.TrimSpace(s.cfg.Timezone)); err == nil && loc != nil {
			return loc
		}
	}
	return time.Local
}
