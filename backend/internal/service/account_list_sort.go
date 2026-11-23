package service

import "strings"

// Admin account list sort keys accepted from the API (sort_by). Anything else
// falls back to the default order (id DESC).
const (
	AccountSortByID             = "id"
	AccountSortByName           = "name"
	AccountSortByStatus         = "status"
	AccountSortBySchedulable    = "schedulable"
	AccountSortByPriority       = "priority"
	AccountSortByRateMultiplier = "rate_multiplier"
	AccountSortByLastUsedAt     = "last_used_at"
	AccountSortByExpiresAt      = "expires_at"
)

// AccountListSort is a normalized admin account list order. The zero value
// means the default order (id DESC). By is always a whitelisted key or "".
type AccountListSort struct {
	By   string
	Desc bool
}

// IsDefault reports whether the sort is the default id DESC order.
func (s AccountListSort) IsDefault() bool {
	return s.By == "" || (s.By == AccountSortByID && s.Desc)
}

// NormalizeAccountListSort maps raw sort_by/sort_order query values onto the
// whitelist. Unknown or empty sort_by yields the default order; sort_order is
// "asc" (case-insensitive) for ascending and descending otherwise.
func NormalizeAccountListSort(sortBy, sortOrder string) AccountListSort {
	by := strings.ToLower(strings.TrimSpace(sortBy))
	switch by {
	case AccountSortByID, AccountSortByName, AccountSortByStatus, AccountSortBySchedulable,
		AccountSortByPriority, AccountSortByRateMultiplier, AccountSortByLastUsedAt, AccountSortByExpiresAt:
	default:
		return AccountListSort{}
	}
	desc := !strings.EqualFold(strings.TrimSpace(sortOrder), "asc")
	return AccountListSort{By: by, Desc: desc}
}
