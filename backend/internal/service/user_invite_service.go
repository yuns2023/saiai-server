package service

import (
	"context"
	"errors"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// User-generated invitation codes.
//
// Rules (admin settings, all default off / conservative):
//   - user_invite_enabled: users may generate codes. Turning it off stops new codes; codes
//     already handed out keep working (a code is a bearer token like admin codes).
//   - Eligibility: the user has topped up (a completed payment order) or redeemed a
//     balance / subscription / concurrency code. Admin adjustments and the invitation the
//     user signed up with do not count.
//   - user_invite_max_unused: unused codes a user may hold at once (default 5).
//   - user_invite_max_total: optional lifetime cap on people invited (0 = unlimited).
//
// Codes live in redeem_codes (type=invitation, owner_user_id=user) so the existing sign-up
// paths consume them unchanged; the owner is copied to users.invited_by_user_id.
const (
	DefaultUserInviteMaxUnused = 5
	MaxUserInviteMaxUnused     = 100
	MaxUserInviteMaxTotal      = 100000
	// MaxUserInviteCreatesPerDay bounds generate→revoke→generate churn per user (rolling 24h).
	MaxUserInviteCreatesPerDay = 20
)

var (
	ErrUserInviteDisabled    = infraerrors.Forbidden("USER_INVITE_DISABLED", "inviting is not enabled on this site")
	ErrUserInviteNotEligible = infraerrors.Forbidden("USER_INVITE_NOT_ELIGIBLE", "top up or redeem a code before inviting others")
	ErrUserInviteUnusedLimit = infraerrors.Conflict("USER_INVITE_UNUSED_LIMIT", "you already hold the maximum number of unused invitation codes")
	ErrUserInviteTotalLimit  = infraerrors.Conflict("USER_INVITE_TOTAL_LIMIT", "you have reached the maximum number of invitations")
	ErrUserInviteDailyLimit  = infraerrors.TooManyRequests("USER_INVITE_DAILY_LIMIT", "too many invitation codes generated today, try again later")
	ErrUserInviteNotFound    = infraerrors.NotFound("USER_INVITE_NOT_FOUND", "unused invitation code not found")
)

// UserInviteLimits is what CreateForOwner enforces under the owner's row lock.
type UserInviteLimits struct {
	MaxUnused int
	MaxTotal  int // 0 = unlimited
}

// UserInviteCode is one code as its owner sees it. The repository returns the raw
// InviteeEmail; Overview masks it before it leaves the service.
type UserInviteCode struct {
	Code         string     `json:"code"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	UsedAt       *time.Time `json:"used_at,omitempty"`
	InviteeEmail string     `json:"invitee_email,omitempty"`
}

// InviterRef identifies an inviter for admin views (soft-deleted users included).
type InviterRef struct {
	ID      int64
	Email   string
	Deleted bool
}

type UserInviteRepository interface {
	// HasQualifyingTopUp reports a completed payment or a redeemed balance/subscription/concurrency code.
	HasQualifyingTopUp(ctx context.Context, userID int64) (bool, error)
	// Counts returns the owner's unused codes and the users who signed up with the owner's codes.
	Counts(ctx context.Context, ownerID int64) (unused int, invited int, err error)
	ListByOwner(ctx context.Context, ownerID int64, params pagination.PaginationParams) ([]UserInviteCode, *pagination.PaginationResult, error)
	// CreateForOwner locks the owner row, re-checks that the owner is active and the limits,
	// and inserts the code, all in one transaction. Returns ErrUserNotActive / ErrUserNotFound /
	// ErrUserInviteUnusedLimit / ErrUserInviteTotalLimit / ErrUserInviteDailyLimit.
	CreateForOwner(ctx context.Context, ownerID int64, code string, limits UserInviteLimits) (*UserInviteCode, error)
	// RevokeByOwner expires one unused code owned by ownerID in a single conditional update.
	RevokeByOwner(ctx context.Context, ownerID int64, code string) error
	// InviterRefs resolves user ids for admin views, including soft-deleted users.
	InviterRefs(ctx context.Context, ids []int64) (map[int64]InviterRef, error)
}

// UserInviteOverview is the user console's invitation page.
type UserInviteOverview struct {
	Enabled   bool `json:"enabled"`
	Eligible  bool `json:"eligible"`
	MaxUnused int  `json:"max_unused"`
	MaxTotal  int  `json:"max_total"`
	Unused    int  `json:"unused"`
	Invited   int  `json:"invited"`
	// CanCreate is the server's answer; the UI only mirrors it.
	CanCreate  bool                         `json:"can_create"`
	Codes      []UserInviteCode             `json:"codes"`
	Pagination *pagination.PaginationResult `json:"pagination"`
}

type UserInviteService struct {
	repo     UserInviteRepository
	settings *SettingService
}

func NewUserInviteService(repo UserInviteRepository, settings *SettingService) *UserInviteService {
	return &UserInviteService{repo: repo, settings: settings}
}

func (s *UserInviteService) Overview(ctx context.Context, userID int64, params pagination.PaginationParams) (*UserInviteOverview, error) {
	cfg := s.settings.GetUserInviteSettings(ctx)
	out := &UserInviteOverview{
		Enabled:   cfg.Enabled,
		MaxUnused: cfg.MaxUnused,
		MaxTotal:  cfg.MaxTotal,
		Codes:     []UserInviteCode{},
	}
	eligible, err := s.repo.HasQualifyingTopUp(ctx, userID)
	if err != nil {
		return nil, err
	}
	out.Eligible = eligible
	unused, invited, err := s.repo.Counts(ctx, userID)
	if err != nil {
		return nil, err
	}
	out.Unused, out.Invited = unused, invited
	out.CanCreate = cfg.Enabled && eligible && unused < cfg.MaxUnused && (cfg.MaxTotal == 0 || invited < cfg.MaxTotal)
	// Listing stays available after the switch is turned off: handed-out codes still work
	// and the owner can still see and revoke them.
	codes, page, err := s.repo.ListByOwner(ctx, userID, params)
	if err != nil {
		return nil, err
	}
	for i := range codes {
		if codes[i].InviteeEmail != "" {
			codes[i].InviteeEmail = maskInviteeEmail(codes[i].InviteeEmail)
		}
	}
	if codes != nil {
		out.Codes = codes
	}
	out.Pagination = page
	return out, nil
}

func (s *UserInviteService) Create(ctx context.Context, userID int64) (*UserInviteCode, error) {
	cfg := s.settings.GetUserInviteSettings(ctx)
	if !cfg.Enabled {
		return nil, ErrUserInviteDisabled
	}
	eligible, err := s.repo.HasQualifyingTopUp(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, ErrUserInviteNotEligible
	}
	limits := UserInviteLimits{MaxUnused: cfg.MaxUnused, MaxTotal: cfg.MaxTotal}
	// A random 32-hex collision is practically impossible; retry once anyway.
	for attempt := 0; ; attempt++ {
		code, err := GenerateRedeemCode()
		if err != nil {
			return nil, err
		}
		created, err := s.repo.CreateForOwner(ctx, userID, code, limits)
		if errors.Is(err, ErrUserInviteCodeTaken) && attempt == 0 {
			continue
		}
		if err != nil {
			if !isUserInviteExpectedError(err) {
				logger.LegacyPrintf("service.user_invite", "[UserInvite] create failed: user_id=%d err=%v", userID, err)
			}
			return nil, err
		}
		return created, nil
	}
}

func (s *UserInviteService) Revoke(ctx context.Context, userID int64, code string) error {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 32 {
		return ErrUserInviteNotFound
	}
	return s.repo.RevokeByOwner(ctx, userID, code)
}

// InviterRefs is used by admin views to label "invited by" and code owners.
func (s *UserInviteService) InviterRefs(ctx context.Context, ids []int64) (map[int64]InviterRef, error) {
	if len(ids) == 0 {
		return map[int64]InviterRef{}, nil
	}
	return s.repo.InviterRefs(ctx, ids)
}

// ErrUserInviteCodeTaken is returned by the repository on a unique-code collision.
var ErrUserInviteCodeTaken = errors.New("user invite: code already exists")

func isUserInviteExpectedError(err error) bool {
	return errors.Is(err, ErrUserInviteUnusedLimit) || errors.Is(err, ErrUserInviteTotalLimit) ||
		errors.Is(err, ErrUserInviteDailyLimit) || errors.Is(err, ErrUserNotActive) || errors.Is(err, ErrUserNotFound)
}

// maskInviteeEmail keeps the first character of the local part and the domain: "a***@qq.com".
// Synthetic OAuth emails (…@google-connect.invalid) are shown by provider only.
func maskInviteeEmail(email string) string {
	email = strings.TrimSpace(email)
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "***"
	}
	local, domain := email[:at], email[at+1:]
	if strings.HasSuffix(domain, ".invalid") {
		return "***@" + domain
	}
	first := []rune(local)[0]
	return string(first) + "***@" + domain
}
