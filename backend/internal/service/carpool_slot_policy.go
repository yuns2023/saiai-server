package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const CarpoolNextMaintenanceKey = "claude_oauth_carpool_next_maintenance_at"
const CarpoolMaintenanceGenerationKey = "claude_oauth_carpool_maintenance_generation"

type CarpoolAdmissionPolicy struct {
	Generation string
	Admissions int
}

// CarpoolAdmissionCache keeps admission accounting atomic with device registration.
type CarpoolAdmissionCache interface {
	GetOrCreateCarpoolDeviceWithPolicy(context.Context, int64, string, ClientHints, int, int64, CarpoolAdmissionPolicy) (*CarpoolDeviceRecord, error)
	CarpoolAdmissionsRemaining(context.Context, int64, CarpoolAdmissionPolicy) (int, error)
}

func (a *Account) CarpoolAdmissionPolicy() CarpoolAdmissionPolicy {
	generation := a.getExtraString(CarpoolMaintenanceGenerationKey)
	if generation == "" {
		generation = "00000000000000000000"
	}
	return CarpoolAdmissionPolicy{Generation: generation, Admissions: a.GetCarpoolAdmissionsPerCycle()}
}

// The legacy enable flag remains wire-compatible; expansion is no longer performed.
func (a *Account) GetCarpoolMaintenanceIntervalHours() int {
	n := a.getExtraInt("claude_oauth_carpool_maintenance_interval_hours")
	if n < 1 {
		return 24
	}
	return min(n, 720)
}
func (a *Account) GetCarpoolAdmissionsPerCycle() int {
	n := a.getExtraInt("claude_oauth_carpool_maintenance_admissions_per_cycle")
	if _, configured := a.Extra["claude_oauth_carpool_maintenance_admissions_per_cycle"]; !configured {
		n = a.getExtraInt("claude_oauth_carpool_maintenance_free_slots")
	}
	n = max(0, n)
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
	delete(a.Extra, CarpoolMaintenanceGenerationKey)
	if !a.IsClaudeOAuthCarpoolAutoExpandEnabled() {
		return
	}
	next := ""
	generation := fmt.Sprintf("%020d", now.UnixNano())
	if previous != nil && previous.IsClaudeOAuthCarpoolAutoExpandEnabled() && previous.GetCarpoolMaintenanceIntervalHours() == a.GetCarpoolMaintenanceIntervalHours() {
		next = previous.GetCarpoolNextMaintenance()
		generation = previous.CarpoolAdmissionPolicy().Generation
	}
	if _, err := time.Parse(time.RFC3339, next); err != nil {
		next = now.UTC().Add(time.Duration(a.GetCarpoolMaintenanceIntervalHours()) * time.Hour).Format(time.RFC3339)
	}
	a.Extra[CarpoolNextMaintenanceKey] = next
	a.Extra[CarpoolMaintenanceGenerationKey] = generation
}

type carpoolScheduleRepository interface {
	AdvanceCarpoolMaintenance(context.Context, int64, string, string) error
}
type carpoolSlotCache interface {
	ResetCarpoolAdmissionCycle(context.Context, int64, int, CarpoolAdmissionPolicy, string) (int, error)
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
	removed, err := cache.ResetCarpoolAdmissionCycle(ctx, current.ID, current.GetClaudeOAuthCarpoolDeviceLimit(), current.CarpoolAdmissionPolicy(), expected)
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
