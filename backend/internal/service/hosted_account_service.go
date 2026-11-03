package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const hostedQuotaFreshness = 15 * time.Minute

// HostedAccountService only reads repositories. In particular it has no usage
// fetcher, token refresh service, or upstream/model client.
type HostedAccountService struct {
	users    UserRepository
	groups   GroupRepository
	accounts AccountRepository
	costs    AccountWindowCostReader
}

// HostedGroupAudienceReader lists users who can use a group (allowlist or
// subscription), up to limit rows.
type HostedGroupAudienceReader interface {
	ListGroupAudienceUserIDs(ctx context.Context, groupID int64, limit int) ([]int64, error)
}

func NewHostedAccountService(users UserRepository, groups GroupRepository, accounts AccountRepository, usageLogs UsageLogRepository) *HostedAccountService {
	s := &HostedAccountService{users: users, groups: groups, accounts: accounts}
	if reader, ok := usageLogs.(AccountWindowCostReader); ok {
		s.costs = reader
	}
	return s
}

type HostedGroup struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

type HostedQuotaWindow struct {
	Key         string     `json:"key"`
	Utilization float64    `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at"`
	Expired     bool       `json:"expired"`
	// Platform usage of the account (all users, standard price) in the current
	// window, and the full-window capacity estimated from it. Null when unknown
	// or not disclosed to this viewer.
	UsedCostUSD          *float64 `json:"used_cost_usd"`
	EstimatedCapacityUSD *float64 `json:"estimated_capacity_usd"`
}

// This allowlist deliberately excludes credentials, Extra, email, proxy,
// account notes, raw error messages and internal scheduling configuration.
type HostedAccount struct {
	ID        int64               `json:"id"`
	Name      string              `json:"name"`
	Platform  string              `json:"platform"`
	Status    string              `json:"status"`
	UpdatedAt *time.Time          `json:"updated_at"`
	Stale     bool                `json:"stale"`
	Windows   []HostedQuotaWindow `json:"windows"`
}

func canViewHostedAccounts(user *User, group *Group) bool {
	return user != nil && user.IsActive() && group != nil && group.Status == StatusActive &&
		group.IsExclusive && group.ShowAccountQuota &&
		(group.RequiredLevelID == nil || group.RequiredLevel != nil) && user.CanAccessGroup(group)
}

func (s *HostedAccountService) ListGroups(ctx context.Context, userID int64) ([]HostedGroup, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	groups, err := s.groups.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]HostedGroup, 0)
	for i := range groups {
		g := &groups[i]
		if canViewHostedAccounts(user, g) {
			result = append(result, HostedGroup{ID: g.ID, Name: g.Name, Platform: g.Platform})
		}
	}
	return result, nil
}

func (s *HostedAccountService) ListAccounts(ctx context.Context, userID, groupID int64, params pagination.PaginationParams) ([]HostedAccount, *pagination.PaginationResult, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	group, err := s.groups.GetByIDLite(ctx, groupID)
	if err != nil {
		return nil, nil, err
	}
	// Return the same result for hidden, disabled and unauthorized groups.
	if groupID <= 0 || !canViewHostedAccounts(user, group) {
		return nil, nil, ErrGroupNotFound
	}
	accounts, page, err := s.accounts.ListWithFilters(ctx, params, "", "", "", "", groupID)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	result := make([]HostedAccount, 0, len(accounts))
	for i := range accounts {
		result = append(result, hostedAccountSnapshot(&accounts[i], now))
	}
	if len(accounts) > 0 && s.groupHasOnlyViewer(ctx, groupID, userID) {
		disclosed := make([]int, 0, len(accounts))
		for i := range accounts {
			if hostedAccountUsedOnlyBy(&accounts[i], groupID, userID) {
				disclosed = append(disclosed, i)
			}
		}
		s.attachWindowCosts(ctx, accounts, result, disclosed, now)
	}
	return result, page, nil
}

// groupHasOnlyViewer is the disclosure guard for window costs: the account
// total includes every user's requests, so it is shown only when the viewer is
// the group's sole allowed or subscribed user. Errors hide the costs.
func (s *HostedAccountService) groupHasOnlyViewer(ctx context.Context, groupID, userID int64) bool {
	reader, ok := s.users.(HostedGroupAudienceReader)
	if !ok || s.costs == nil {
		return false
	}
	ids, err := reader.ListGroupAudienceUserIDs(ctx, groupID, 2)
	if err != nil {
		slog.Warn("hosted_group_audience_failed", "group_id", groupID, "error", err)
		return false
	}
	return len(ids) == 1 && ids[0] == userID
}

// hostedAccountUsedOnlyBy reports whether no other group or carpool user can
// route requests to the account.
func hostedAccountUsedOnlyBy(account *Account, groupID, userID int64) bool {
	if len(account.GroupIDs) == 0 {
		return false
	}
	for _, id := range account.GroupIDs {
		if id != groupID {
			return false
		}
	}
	for _, id := range account.CarpoolUserIDs {
		if id != userID {
			return false
		}
	}
	return true
}

// attachWindowCosts fills the cost fields of result[i] for every index in
// indexes with a single query. Failures leave the fields null (unknown).
func (s *HostedAccountService) attachWindowCosts(ctx context.Context, accounts []Account, result []HostedAccount, indexes []int, now time.Time) {
	if s.costs == nil || len(indexes) == 0 {
		return
	}
	type target struct {
		account, window int
		cost            quotaCostWindow
	}
	targets := make([]target, 0, len(indexes)*2)
	queries := make([]AccountWindowCostQuery, 0, len(indexes)*2)
	for _, i := range indexes {
		for _, w := range quotaCostWindows(result[i], now) {
			for j := range result[i].Windows {
				if result[i].Windows[j].Key == w.Key {
					targets = append(targets, target{account: i, window: j, cost: w})
					queries = append(queries, w.query(accounts[i].ID, now))
					break
				}
			}
		}
	}
	if len(queries) == 0 {
		return
	}
	costs, err := s.costs.GetAccountWindowCosts(ctx, queries)
	if err != nil || len(costs) != len(queries) {
		slog.Warn("hosted_window_costs_failed", "queries", len(queries), "error", err)
		return
	}
	for k, t := range targets {
		used := costs[k].UsedCost
		window := &result[t.account].Windows[t.window]
		window.UsedCostUSD = &used
		window.EstimatedCapacityUSD = t.cost.estimate(costs[k].SampledCost)
	}
}

func hostedAccountSnapshot(account *Account, now time.Time) HostedAccount {
	result := HostedAccount{ID: account.ID, Name: account.Name, Platform: account.Platform,
		Status: hostedAccountStatus(account, now), Stale: true, Windows: make([]HostedQuotaWindow, 0)}
	extra := account.Extra
	if account.IsAnthropicOAuthOrSetupToken() {
		result.UpdatedAt = hostedSnapshotTime(extra["passive_usage_sampled_at"], now)
		if used, ok := hostedQuotaNumber(extra["session_window_utilization"]); ok && used >= 0 && used <= 1 && !account.IsAnthropicFiveHourWindowAbsent() {
			result.Windows = append(result.Windows, newHostedQuotaWindow("five_hour", used*100, account.SessionWindowEnd, now))
		}
		for _, window := range []struct{ key, prefix string }{
			{"seven_day", "passive_usage_7d"},
			{"seven_day_sonnet", "passive_usage_7d_sonnet"}, {"seven_day_fable", "passive_usage_7d_oi"},
		} {
			used, ok := hostedQuotaNumber(extra[window.prefix+"_utilization"])
			if !ok || used < 0 || used > 1 {
				continue
			}
			var reset *time.Time
			if seconds, valid := hostedQuotaNumber(extra[window.prefix+"_reset"]); valid && seconds > 0 && seconds < 253402300800 {
				t := time.Unix(int64(seconds), 0)
				reset = &t
			}
			result.Windows = append(result.Windows, newHostedQuotaWindow(window.key, used*100, reset, now))
		}
	} else if account.Platform == PlatformOpenAI && account.IsOAuth() {
		result.UpdatedAt = hostedSnapshotTime(extra["codex_usage_updated_at"], now)
		for _, window := range []struct{ key, prefix string }{{"five_hour", "codex_5h"}, {"seven_day", "codex_7d"}} {
			used, ok := hostedQuotaNumber(extra[window.prefix+"_used_percent"])
			if !ok || used < 0 || used > 100 {
				continue
			}
			reset := hostedResetTime(extra[window.prefix+"_reset_at"])
			if reset == nil && result.UpdatedAt != nil {
				if seconds, valid := hostedQuotaNumber(extra[window.prefix+"_reset_after_seconds"]); valid && seconds > 0 && seconds <= 366*24*60*60 {
					t := result.UpdatedAt.Add(time.Duration(seconds) * time.Second)
					reset = &t
				}
			}
			result.Windows = append(result.Windows, newHostedQuotaWindow(window.key, used, reset, now))
		}
	}
	if result.UpdatedAt != nil && len(result.Windows) > 0 {
		result.Stale = now.Sub(*result.UpdatedAt) >= hostedQuotaFreshness
	}
	for _, w := range result.Windows {
		if w.Expired {
			result.Stale = true
		}
	}
	if result.Status == "available" && !result.Stale {
		for _, w := range result.Windows {
			if (w.Key == "five_hour" || w.Key == "seven_day") && w.Utilization >= 100 {
				result.Status = "quota_exhausted"
				break
			}
		}
	}
	return result
}

func newHostedQuotaWindow(key string, used float64, reset *time.Time, now time.Time) HostedQuotaWindow {
	return HostedQuotaWindow{Key: key, Utilization: used, ResetsAt: reset, Expired: reset != nil && !now.Before(*reset)}
}

func hostedResetTime(value any) *time.Time {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	t, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil
	}
	return &t
}

func hostedSnapshotTime(value any, now time.Time) *time.Time {
	t := hostedResetTime(value)
	if t == nil || t.After(now) {
		return nil
	}
	return t
}

func hostedQuotaNumber(value any) (float64, bool) {
	var n float64
	var err error
	switch v := value.(type) {
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case json.Number:
		n, err = v.Float64()
	case string:
		n, err = strconv.ParseFloat(v, 64)
	default:
		return 0, false
	}
	return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
}

func hostedAccountStatus(a *Account, now time.Time) string {
	switch {
	case a.Status == StatusError:
		return "needs_attention"
	case a.Status != StatusActive || !a.Schedulable:
		return "paused"
	case a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt):
		return "expired"
	case a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt):
		return "rate_limited"
	case a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil), a.OverloadUntil != nil && now.Before(*a.OverloadUntil):
		return "temporarily_unavailable"
	default:
		return "available"
	}
}
