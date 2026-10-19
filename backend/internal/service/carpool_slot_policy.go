package service

import (
	"context"
	"errors"
	"time"
)

const CarpoolNextMaintenanceKey = "claude_oauth_carpool_next_maintenance_at"

// The legacy enable flag remains wire-compatible; expansion is no longer performed.
func (a *Account) GetCarpoolMaintenanceIntervalHours() int {
	n := a.getExtraInt("claude_oauth_carpool_maintenance_interval_hours")
	if n < 1 {
		return 24
	}
	return min(n, 720)
}
func (a *Account) GetCarpoolMaintenanceFreeSlots() int {
	n := a.getExtraInt("claude_oauth_carpool_maintenance_free_slots")
	if n < 1 {
		n = 1
	}
	return min(n, a.GetClaudeOAuthCarpoolDeviceLimit())
}
func (a *Account) GetCarpoolNextMaintenance() string {
	return a.getExtraString(CarpoolNextMaintenanceKey)
}

// NormalizeCarpoolMaintenance protects the server-owned schedule from submitted
// extras. Enabling or changing the interval starts a full new interval.
func NormalizeCarpoolMaintenance(a, previous *Account, now time.Time) {
	if a.Extra == nil {
		return
	}
	delete(a.Extra, CarpoolNextMaintenanceKey)
	if !a.IsClaudeOAuthCarpoolAutoExpandEnabled() {
		return
	}
	next := ""
	if previous != nil && previous.IsClaudeOAuthCarpoolAutoExpandEnabled() && previous.GetCarpoolMaintenanceIntervalHours() == a.GetCarpoolMaintenanceIntervalHours() {
		next = previous.GetCarpoolNextMaintenance()
	}
	if _, err := time.Parse(time.RFC3339, next); err != nil {
		next = now.UTC().Add(time.Duration(a.GetCarpoolMaintenanceIntervalHours()) * time.Hour).Format(time.RFC3339)
	}
	a.Extra[CarpoolNextMaintenanceKey] = next
}

type carpoolScheduleRepository interface {
	AdvanceCarpoolMaintenance(context.Context, int64, string, string) error
}
type carpoolSlotCache interface {
	FreeCarpoolSlots(context.Context, int64, int, int, string) (int, error)
}

func (s *CarpoolMaintenanceService) maintainCarpoolSlots(ctx context.Context, account *Account, now time.Time, stats *carpoolMaintenanceStats) error {
	current, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil {
		return err
	}
	if !current.IsClaudeOAuthCarpoolAutoExpandEnabled() {
		return nil
	}
	repo, ok := s.accountRepo.(carpoolScheduleRepository)
	if !ok {
		return errors.New("carpool schedule repository unavailable")
	}
	interval := time.Duration(current.GetCarpoolMaintenanceIntervalHours()) * time.Hour
	expected := current.GetCarpoolNextMaintenance()
	due, err := time.Parse(time.RFC3339, expected)
	if err != nil {
		// Lazy migration: preserve the actual limit and wait a full interval.
		return repo.AdvanceCarpoolMaintenance(ctx, current.ID, expected, now.UTC().Add(interval).Format(time.RFC3339))
	}
	if now.Before(due) {
		stats.AlreadyProcessed++
		return nil
	}
	cache, ok := s.cache.(carpoolSlotCache)
	if !ok {
		return errors.New("carpool slot cache unavailable")
	}
	removed, err := cache.FreeCarpoolSlots(ctx, current.ID, current.GetClaudeOAuthCarpoolDeviceLimit(), current.GetCarpoolMaintenanceFreeSlots(), expected)
	if err != nil {
		return err
	}
	// Skip missed periods while retaining the original account's schedule.
	next := due.Add((now.Sub(due)/interval + 1) * interval).UTC().Format(time.RFC3339)
	if err := repo.AdvanceCarpoolMaintenance(ctx, current.ID, expected, next); err != nil {
		return err
	}
	if removed > 0 {
		stats.Rotated++
	} else {
		stats.FreeCapacity++
	}
	return nil
}
