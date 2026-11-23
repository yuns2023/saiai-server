package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// The owner list filter loads the (few, bounded) accounts of one owner and
// applies the other list filters in memory instead of extending the paginated
// repository query.

var errInvalidOwnerFilter = infraerrors.BadRequest("INVALID_OWNER_FILTER", "invalid owner filter")

// SetOwner handles PUT /api/v1/admin/accounts/:id/owner.
// Body: {"owner_user_id": <positive integer>} sets, {"owner_user_id": null}
// clears. The key is required so an empty body can never clear an owner.
func (h *AccountHandler) SetOwner(c *gin.Context) {
	if h.hostedOwnerService == nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("HOSTED_OWNER_UNAVAILABLE", "hosted owner service unavailable"))
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	ownerUserID, err := parseOwnerUserIDBody(c)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	var adminUserID int64
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		adminUserID = subject.UserID
	}
	change, err := h.hostedOwnerService.SetAccountOwner(c.Request.Context(), accountID, ownerUserID, adminUserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, change)
}

func parseOwnerUserIDBody(c *gin.Context) (*int64, error) {
	var body map[string]json.RawMessage
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(&body); err != nil || body == nil {
		return nil, service.ErrHostedOwnerInvalidOwnerID
	}
	// Exactly one JSON object: trailing data is rejected, never ignored.
	if decoder.More() {
		return nil, service.ErrHostedOwnerInvalidOwnerID
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, service.ErrHostedOwnerInvalidOwnerID
	}
	raw, ok := body["owner_user_id"]
	if !ok {
		return nil, service.ErrHostedOwnerInvalidOwnerID
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	// Only plain positive integers: no strings, floats, exponents or signs.
	text := string(trimmed)
	if text == "" || strings.ContainsAny(text, ".eE+-\"") {
		return nil, service.ErrHostedOwnerInvalidOwnerID
	}
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil || id <= 0 {
		return nil, service.ErrHostedOwnerInvalidOwnerID
	}
	return &id, nil
}

// parseOwnerFilter returns the owner_user_id list filter (0 = absent).
func parseOwnerFilter(c *gin.Context) (int64, error) {
	raw := strings.TrimSpace(c.Query("owner_user_id"))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errInvalidOwnerFilter
	}
	return id, nil
}

// listAccountsByOwner applies the regular list filters in memory to one
// owner's accounts and paginates the result.
func (h *AccountHandler) listAccountsByOwner(ctx context.Context, ownerUserID int64, page, pageSize int, platform, accountType, status, search string, groupID int64, listSort service.AccountListSort) ([]service.Account, int64, error) {
	all, err := h.hostedOwnerService.ListAccountsOwnedBy(ctx, ownerUserID)
	if err != nil {
		return nil, 0, err
	}
	accounts, total := filterOwnedAccounts(all, page, pageSize, platform, accountType, status, search, groupID, listSort)
	return accounts, total, nil
}

func filterOwnedAccounts(all []service.Account, page, pageSize int, platform, accountType, status, search string, groupID int64, listSort service.AccountListSort) ([]service.Account, int64) {
	needle := strings.ToLower(search)
	now := time.Now()
	filtered := make([]service.Account, 0, len(all))
	for _, account := range all {
		if platform != "" && account.Platform != platform {
			continue
		}
		if accountType != "" && account.Type != accountType {
			continue
		}
		if status != "" && !ownedAccountMatchesStatus(account, status, now) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(account.Name), needle) {
			continue
		}
		if groupID == service.AccountListGroupUngrouped && len(account.GroupIDs) > 0 {
			continue
		}
		if groupID > 0 && !containsInt64(account.GroupIDs, groupID) {
			continue
		}
		filtered = append(filtered, account)
	}
	// Same order as the regular admin list (repository accountListOrder).
	sortOwnedAccounts(filtered, listSort)
	total := int64(len(filtered))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	// Clamp before any arithmetic so huge page/pageSize values cannot overflow:
	// with pageSize <= len and page <= pages, (page-1)*pageSize <= len.
	if pageSize > len(filtered) {
		pageSize = len(filtered)
	}
	if pageSize < 1 {
		return []service.Account{}, total
	}
	pages := (len(filtered) + pageSize - 1) / pageSize
	if page > pages {
		return []service.Account{}, total
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[start:end], total
}

// ownedAccountMatchesStatus mirrors the repository list filter: the
// rate_limited and temp_unschedulable filters are computed from reset times,
// every other value compares the stored status.
func ownedAccountMatchesStatus(account service.Account, status string, now time.Time) bool {
	switch status {
	case "rate_limited":
		return account.RateLimitResetAt != nil && account.RateLimitResetAt.After(now)
	case "temp_unschedulable":
		return account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(now)
	default:
		return account.Status == status
	}
}

// exportAccountsByOwner returns the accounts of one owner (up to the admin
// owner bound, see ListAccountsOwnedBy) that match the export filters across
// all pages, so an owner-filtered export never includes
// other owners' accounts.
func (h *AccountHandler) exportAccountsByOwner(ctx context.Context, ownerUserID int64, platform, accountType, status, search string) ([]service.Account, error) {
	all, err := h.hostedOwnerService.ListAccountsOwnedBy(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	accounts, _ := filterOwnedAccounts(all, 1, len(all)+1, platform, accountType, status, search, 0, service.AccountListSort{})
	return accounts, nil
}

// sortOwnedAccounts mirrors the repository admin list order in memory: the
// primary key, then id in the same direction; NULL timestamps always last.
// Name compares lower-cased bytes, which can differ from the database
// collation for non-ASCII names (owner lists are small and admin-only).
func sortOwnedAccounts(accounts []service.Account, listSort service.AccountListSort) {
	if listSort.IsDefault() {
		sort.SliceStable(accounts, func(i, j int) bool { return accounts[i].ID > accounts[j].ID })
		return
	}
	desc := listSort.Desc
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := &accounts[i], &accounts[j]
		var c int
		switch listSort.By {
		case service.AccountSortByName:
			c = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		case service.AccountSortByStatus:
			c = strings.Compare(a.Status, b.Status)
		case service.AccountSortBySchedulable:
			c = compareBool(a.Schedulable, b.Schedulable)
		case service.AccountSortByPriority:
			c = compareOrdered(a.Priority, b.Priority)
		case service.AccountSortByRateMultiplier:
			c = compareOrdered(ownedRateMultiplier(a), ownedRateMultiplier(b))
		case service.AccountSortByLastUsedAt, service.AccountSortByExpiresAt:
			ta, tb := a.LastUsedAt, b.LastUsedAt
			if listSort.By == service.AccountSortByExpiresAt {
				ta, tb = a.ExpiresAt, b.ExpiresAt
			}
			switch {
			case ta == nil && tb == nil:
				c = 0
			case ta == nil:
				return false // NULLs last in both directions
			case tb == nil:
				return true
			default:
				c = ta.Compare(*tb)
			}
		}
		if c == 0 {
			c = compareOrdered(a.ID, b.ID)
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
}

// ownedRateMultiplier is the stored rate_multiplier (NOT NULL, default 1.0).
func ownedRateMultiplier(a *service.Account) float64 {
	if a.RateMultiplier == nil {
		return 1.0
	}
	return *a.RateMultiplier
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}

func compareOrdered[T int | int64 | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func containsInt64(values []int64, target int64) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// fillAccountOwnerEmail sets OwnerEmail on one admin account DTO (best effort).
func fillAccountOwnerEmail(ctx context.Context, svc *service.HostedOwnerService, account *service.Account, out *dto.Account) {
	if svc == nil || account == nil || out == nil || out.OwnerUserID == nil {
		return
	}
	emails, err := svc.OwnerEmails(ctx, []service.Account{*account})
	if err != nil {
		return
	}
	if email, ok := emails[*out.OwnerUserID]; ok {
		value := email
		out.OwnerEmail = &value
	}
}

// fillOwnerEmails sets OwnerEmail on admin account DTOs (one batch query).
func (h *AccountHandler) fillOwnerEmails(ctx context.Context, accounts []service.Account, items []AccountWithConcurrency) {
	if h.hostedOwnerService == nil || len(items) == 0 {
		return
	}
	emails, err := h.hostedOwnerService.OwnerEmails(ctx, accounts)
	if err != nil || len(emails) == 0 {
		return
	}
	for i := range items {
		if items[i].Account == nil || items[i].OwnerUserID == nil {
			continue
		}
		if email, ok := emails[*items[i].OwnerUserID]; ok {
			value := email
			items[i].OwnerEmail = &value
		}
	}
}
