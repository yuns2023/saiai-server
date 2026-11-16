package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

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
func (h *AccountHandler) listAccountsByOwner(ctx context.Context, ownerUserID int64, page, pageSize int, platform, accountType, status, search string, groupID int64) ([]service.Account, int64, error) {
	all, err := h.hostedOwnerService.ListAccountsOwnedBy(ctx, ownerUserID)
	if err != nil {
		return nil, 0, err
	}
	accounts, total := filterOwnedAccounts(all, page, pageSize, platform, accountType, status, search, groupID)
	return accounts, total, nil
}

func filterOwnedAccounts(all []service.Account, page, pageSize int, platform, accountType, status, search string, groupID int64) ([]service.Account, int64) {
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
	total := int64(len(filtered))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	start := (page - 1) * pageSize
	if start >= len(filtered) {
		return []service.Account{}, total
	}
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

func containsInt64(values []int64, target int64) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
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
