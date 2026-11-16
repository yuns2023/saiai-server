package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Hosted owner (P1): an upstream account can belong to the platform user who
// brought it. Admins assign the owner; owners get a read-only view of their
// accounts and of the usage served by them.
//
// Attribution never uses cached Account objects: usage rows snapshot the owner
// from the database inside the usage INSERT (usage_logs.account_owner_user_id),
// and every owner query filters on that snapshot, so usage from before an
// assignment (or from another owner's period) is never shown.

var (
	ErrAccountOwnerTransferForbidden = infraerrors.Conflict(
		"ACCOUNT_OWNER_TRANSFER_FORBIDDEN",
		"this account already belonged to another user; it can only be cleared or set back to that user (delete and re-import the account to give it to someone else)",
	)
	ErrHostedOwnedAccountNotFound = infraerrors.NotFound("HOSTED_ACCOUNT_NOT_FOUND", "hosted account not found")
	ErrHostedOwnerUsageRange      = infraerrors.BadRequest("INVALID_USAGE_RANGE", "start and end must be RFC3339 times with start < end and a span of at most 31 days")
	ErrHostedOwnerUsageLimit      = infraerrors.BadRequest("INVALID_USAGE_LIMIT", "limit must be between 1 and 100")
	ErrHostedOwnerUsageCursor     = infraerrors.BadRequest("INVALID_CURSOR", "invalid cursor")
	ErrHostedOwnerInvalidOwnerID  = infraerrors.BadRequest("INVALID_OWNER_USER_ID", "owner_user_id must be a positive integer or null")
)

const (
	HostedOwnerUsageMaxSpan      = 31 * 24 * time.Hour
	HostedOwnerUsageMaxLimit     = 100
	HostedOwnerUsageDefaultLimit = 50
	// hostedOwnerMaxAccounts bounds every per-owner account list (current and
	// historical). Real owners have a handful of accounts.
	hostedOwnerMaxAccounts = 200
	// hostedOwnerCursorMaxLen bounds the opaque cursor before decoding.
	hostedOwnerCursorMaxLen = 128
	hostedOwnerCursorPrefix = "v1:"
)

// HostedOwnerUsageTotals splits usage served by one owned account into the
// owner's own requests and everyone else's, at standard (official) price.
type HostedOwnerUsageTotals struct {
	SelfRequests       int64   `json:"self_requests"`
	SelfStandardCost   float64 `json:"self_standard_cost"`
	OthersRequests     int64   `json:"others_requests"`
	OthersStandardCost float64 `json:"others_standard_cost"`
}

// OwnedHostedAccount is the owner-facing allowlist view of one account. It
// deliberately excludes the admin name, notes, credentials, Extra, proxy,
// groups and scheduling configuration (admin names can embed seller emails).
type OwnedHostedAccount struct {
	AccountID   int64                  `json:"account_id"`
	Current     bool                   `json:"current"`
	DisplayName string                 `json:"display_name"`
	Platform    *string                `json:"platform"`
	Type        *string                `json:"type"`
	Status      *string                `json:"status"`
	UpdatedAt   *time.Time             `json:"updated_at"`
	Stale       bool                   `json:"stale"`
	Windows     []HostedQuotaWindow    `json:"windows"`
	Last24h     HostedOwnerUsageTotals `json:"last_24h"`
	Last7d      HostedOwnerUsageTotals `json:"last_7d"`
}

// OwnedHostedAccountList is the response of GET /hosted/owned-accounts.
type OwnedHostedAccountList struct {
	Items []OwnedHostedAccount `json:"items"`
}

// HostedOwnerUsageRow is one usage row as shown to the account owner. Other
// users' rows never carry identity or billing fields.
type HostedOwnerUsageRow struct {
	CreatedAt           time.Time `json:"created_at"`
	Model               string    `json:"model"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	StandardCost        float64   `json:"standard_cost"`
	IsSelf              bool      `json:"is_self"`
	APIKeyName          *string   `json:"api_key_name"`
	Charged             *float64  `json:"charged"`
}

// HostedOwnerUsagePage is the response of GET /hosted/owned-accounts/:id/usage.
type HostedOwnerUsagePage struct {
	Items      []HostedOwnerUsageRow `json:"items"`
	NextCursor *string               `json:"next_cursor"`
}

// HostedOwnerUsageRecord is a repository row: the public row plus the keyset
// position. Repositories must only fill APIKeyName/Charged for the owner's
// own rows.
type HostedOwnerUsageRecord struct {
	ID  int64
	Row HostedOwnerUsageRow
}

// HostedOwnerUsageQuery is a validated usage page request. Start/End are a
// half-open UTC range; After, when set, is the exclusive keyset position
// (created_at DESC, id DESC).
type HostedOwnerUsageQuery struct {
	OwnerUserID int64
	AccountID   int64
	Start       time.Time
	End         time.Time
	Limit       int
	After       *HostedOwnerUsageCursor
}

type HostedOwnerUsageCursor struct {
	CreatedAt time.Time
	ID        int64
}

// HostedOwnerHistoricalAccount is an account the user owned at some point.
type HostedOwnerHistoricalAccount struct {
	AccountID int64
	Platform  *string
}

// AccountOwnerChange is the result of an admin owner assignment.
type AccountOwnerChange struct {
	AccountID   int64   `json:"account_id"`
	OwnerUserID *int64  `json:"owner_user_id"`
	OwnerEmail  *string `json:"owner_email"`
}

// HostedOwnerRepository persists ownership and serves owner-scoped queries.
// Every usage query must filter on usage_logs.account_owner_user_id.
type HostedOwnerRepository interface {
	// SetAccountOwner applies the ownership state machine in one transaction:
	// lock the account row, no-op on the same value, reject (409) a transfer
	// to a user other than any previous owner, write the column + history.
	SetAccountOwner(ctx context.Context, accountID int64, ownerUserID *int64, adminUserID int64) (changed bool, err error)
	// ListCurrentOwnedAccountIDs returns non-deleted accounts owned now.
	ListCurrentOwnedAccountIDs(ctx context.Context, ownerUserID int64, limit int) ([]int64, error)
	// ListEverOwnedAccounts returns accounts the user owned at any time
	// (ownership history), including deleted ones; Platform may be nil.
	ListEverOwnedAccounts(ctx context.Context, ownerUserID int64, limit int) ([]HostedOwnerHistoricalAccount, error)
	// CanViewOwnedAccount reports whether the user owns the account now or
	// owned it in the past.
	CanViewOwnedAccount(ctx context.Context, ownerUserID, accountID int64) (bool, error)
	// SummarizeOwnedUsage aggregates owner-snapshot usage per account over
	// [since7d, until) with a 24h sub-window starting at since24h.
	SummarizeOwnedUsage(ctx context.Context, ownerUserID int64, accountIDs []int64, since7d, since24h, until time.Time) (last24h, last7d map[int64]HostedOwnerUsageTotals, err error)
	ListOwnedUsage(ctx context.Context, q HostedOwnerUsageQuery) ([]HostedOwnerUsageRecord, error)
	// GetUserEmails returns emails of the given users (admin display only).
	GetUserEmails(ctx context.Context, userIDs []int64) (map[int64]string, error)
	// UserExists reports whether a non-deleted user exists.
	UserExists(ctx context.Context, userID int64) (bool, error)
}

type HostedOwnerService struct {
	repo     HostedOwnerRepository
	accounts AccountRepository
	now      func() time.Time
}

func NewHostedOwnerService(repo HostedOwnerRepository, accounts AccountRepository) *HostedOwnerService {
	return &HostedOwnerService{repo: repo, accounts: accounts, now: time.Now}
}

// HostedAccountDisplayName is the only account label shown to non-admin
// users: admin account names can embed seller emails and internal notes.
func HostedAccountDisplayName(accountID int64) string {
	return fmt.Sprintf("托管号 #%d", accountID)
}

// ListOwnedAccounts returns the user's current accounts (with status and
// quota windows) followed by accounts they owned in the past (usage only).
func (s *HostedOwnerService) ListOwnedAccounts(ctx context.Context, userID int64) (*OwnedHostedAccountList, error) {
	if userID <= 0 {
		return &OwnedHostedAccountList{Items: []OwnedHostedAccount{}}, nil
	}
	currentIDs, err := s.repo.ListCurrentOwnedAccountIDs(ctx, userID, hostedOwnerMaxAccounts)
	if err != nil {
		return nil, err
	}
	ever, err := s.repo.ListEverOwnedAccounts(ctx, userID, hostedOwnerMaxAccounts)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()

	items := make([]OwnedHostedAccount, 0, len(currentIDs)+len(ever))
	current := make(map[int64]bool, len(currentIDs))
	if len(currentIDs) > 0 {
		accounts, err := s.accounts.GetByIDs(ctx, currentIDs)
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			if account == nil {
				continue
			}
			snapshot := hostedAccountSnapshot(account, now)
			platform, accountType, status := account.Platform, account.Type, snapshot.Status
			windows := snapshot.Windows
			if windows == nil {
				windows = []HostedQuotaWindow{}
			}
			current[account.ID] = true
			items = append(items, OwnedHostedAccount{
				AccountID:   account.ID,
				Current:     true,
				DisplayName: HostedAccountDisplayName(account.ID),
				Platform:    &platform,
				Type:        &accountType,
				Status:      &status,
				UpdatedAt:   snapshot.UpdatedAt,
				Stale:       snapshot.Stale,
				Windows:     windows,
			})
		}
	}
	for _, past := range ever {
		if current[past.AccountID] {
			continue
		}
		items = append(items, OwnedHostedAccount{
			AccountID:   past.AccountID,
			Current:     false,
			DisplayName: HostedAccountDisplayName(past.AccountID),
			Platform:    past.Platform,
			Windows:     []HostedQuotaWindow{},
		})
	}
	if len(items) == 0 {
		return &OwnedHostedAccountList{Items: items}, nil
	}

	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.AccountID)
	}
	last24h, last7d, err := s.repo.SummarizeOwnedUsage(ctx, userID, ids, now.Add(-7*24*time.Hour), now.Add(-24*time.Hour), now)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Last24h = last24h[items[i].AccountID]
		items[i].Last7d = last7d[items[i].AccountID]
	}
	return &OwnedHostedAccountList{Items: items}, nil
}

// HostedOwnerUsageParams are the raw query parameters of the usage endpoint.
type HostedOwnerUsageParams struct {
	Start  string
	End    string
	Limit  string
	Cursor string
}

// ListOwnedAccountUsage returns one page of owner-snapshot usage for an
// account the user owns now or owned before. Other accounts are 404.
func (s *HostedOwnerService) ListOwnedAccountUsage(ctx context.Context, userID, accountID int64, params HostedOwnerUsageParams) (*HostedOwnerUsagePage, error) {
	if userID <= 0 || accountID <= 0 {
		return nil, ErrHostedOwnedAccountNotFound
	}
	query, err := parseHostedOwnerUsageQuery(userID, accountID, params)
	if err != nil {
		return nil, err
	}
	allowed, err := s.repo.CanViewOwnedAccount(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrHostedOwnedAccountNotFound
	}
	fetch := query
	fetch.Limit = query.Limit + 1
	records, err := s.repo.ListOwnedUsage(ctx, fetch)
	if err != nil {
		return nil, err
	}
	page := &HostedOwnerUsagePage{Items: make([]HostedOwnerUsageRow, 0, minInt(len(records), query.Limit))}
	for i, record := range records {
		if i == query.Limit {
			last := records[i-1]
			cursor := encodeHostedOwnerCursor(HostedOwnerUsageCursor{CreatedAt: last.Row.CreatedAt, ID: last.ID})
			page.NextCursor = &cursor
			break
		}
		row := record.Row
		if !row.IsSelf {
			// Defense in depth: never leak identity or billing of others.
			row.APIKeyName = nil
			row.Charged = nil
		}
		page.Items = append(page.Items, row)
	}
	return page, nil
}

// SetAccountOwner assigns or clears the owner of an account (admin action).
func (s *HostedOwnerService) SetAccountOwner(ctx context.Context, accountID int64, ownerUserID *int64, adminUserID int64) (*AccountOwnerChange, error) {
	if accountID <= 0 {
		return nil, ErrAccountNotFound
	}
	if ownerUserID != nil {
		if *ownerUserID <= 0 {
			return nil, ErrHostedOwnerInvalidOwnerID
		}
		exists, err := s.repo.UserExists(ctx, *ownerUserID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrUserNotFound
		}
	}
	if _, err := s.repo.SetAccountOwner(ctx, accountID, ownerUserID, adminUserID); err != nil {
		return nil, err
	}
	change := &AccountOwnerChange{AccountID: accountID}
	if ownerUserID != nil {
		id := *ownerUserID
		change.OwnerUserID = &id
		emails, err := s.repo.GetUserEmails(ctx, []int64{id})
		if err != nil {
			return nil, err
		}
		if email, ok := emails[id]; ok {
			change.OwnerEmail = &email
		}
	}
	return change, nil
}

// OwnerEmails returns owner emails for admin account views.
func (s *HostedOwnerService) OwnerEmails(ctx context.Context, accounts []Account) (map[int64]string, error) {
	seen := make(map[int64]bool)
	ids := make([]int64, 0)
	for i := range accounts {
		if id := accounts[i].OwnerUserID; id != nil && *id > 0 && !seen[*id] {
			seen[*id] = true
			ids = append(ids, *id)
		}
	}
	if len(ids) == 0 {
		return map[int64]string{}, nil
	}
	return s.repo.GetUserEmails(ctx, ids)
}

// ListAccountsOwnedBy returns the non-deleted accounts currently owned by a
// user (admin filter). Bounded by hostedOwnerMaxAccounts.
func (s *HostedOwnerService) ListAccountsOwnedBy(ctx context.Context, ownerUserID int64) ([]Account, error) {
	if ownerUserID <= 0 {
		return []Account{}, nil
	}
	ids, err := s.repo.ListCurrentOwnedAccountIDs(ctx, ownerUserID, hostedOwnerMaxAccounts)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []Account{}, nil
	}
	accounts, err := s.accounts.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if account != nil {
			out = append(out, *account)
		}
	}
	return out, nil
}

func parseHostedOwnerUsageQuery(userID, accountID int64, params HostedOwnerUsageParams) (HostedOwnerUsageQuery, error) {
	query := HostedOwnerUsageQuery{OwnerUserID: userID, AccountID: accountID, Limit: HostedOwnerUsageDefaultLimit}
	start, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(params.Start))
	if err != nil {
		return query, ErrHostedOwnerUsageRange
	}
	end, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(params.End))
	if err != nil {
		return query, ErrHostedOwnerUsageRange
	}
	start, end = start.UTC(), end.UTC()
	if !start.Before(end) || end.Sub(start) > HostedOwnerUsageMaxSpan {
		return query, ErrHostedOwnerUsageRange
	}
	query.Start, query.End = start, end
	if raw := strings.TrimSpace(params.Limit); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > HostedOwnerUsageMaxLimit {
			return query, ErrHostedOwnerUsageLimit
		}
		query.Limit = limit
	}
	if raw := strings.TrimSpace(params.Cursor); raw != "" {
		cursor, err := decodeHostedOwnerCursor(raw)
		if err != nil {
			return query, ErrHostedOwnerUsageCursor
		}
		query.After = &cursor
	}
	return query, nil
}

// The cursor only positions the page; authorization always comes from the
// owner and account predicates in SQL, so a forged cursor can only move
// within rows the caller may already read.
func encodeHostedOwnerCursor(c HostedOwnerUsageCursor) string {
	raw := fmt.Sprintf("%s%d:%d", hostedOwnerCursorPrefix, c.CreatedAt.UTC().UnixMicro(), c.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeHostedOwnerCursor(value string) (HostedOwnerUsageCursor, error) {
	var out HostedOwnerUsageCursor
	if len(value) > hostedOwnerCursorMaxLen {
		return out, fmt.Errorf("cursor too long")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return out, err
	}
	raw := string(decoded)
	if !strings.HasPrefix(raw, hostedOwnerCursorPrefix) {
		return out, fmt.Errorf("cursor version")
	}
	parts := strings.Split(strings.TrimPrefix(raw, hostedOwnerCursorPrefix), ":")
	if len(parts) != 2 {
		return out, fmt.Errorf("cursor shape")
	}
	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || micros <= 0 {
		return out, fmt.Errorf("cursor time")
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return out, fmt.Errorf("cursor id")
	}
	out.CreatedAt = time.UnixMicro(micros).UTC()
	out.ID = id
	return out, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
