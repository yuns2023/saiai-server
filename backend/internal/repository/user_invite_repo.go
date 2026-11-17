package repository

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type userInviteRepository struct {
	client *dbent.Client
}

func NewUserInviteRepository(client *dbent.Client) service.UserInviteRepository {
	return &userInviteRepository{client: client}
}

// Redeem types that show the user has put money (or a paid code) into the account.
// admin_balance / admin_concurrency are admin adjustments; invitation is how they signed up.
var userInviteQualifyingTypes = []string{
	service.RedeemTypeBalance,
	service.RedeemTypeSubscription,
	service.RedeemTypeConcurrency,
}

func (r *userInviteRepository) HasQualifyingTopUp(ctx context.Context, userID int64) (bool, error) {
	paid, err := r.client.PaymentOrder.Query().
		Where(paymentorder.UserIDEQ(userID), paymentorder.StatusEQ(payment.OrderStatusCompleted)).
		Exist(ctx)
	if err != nil || paid {
		return paid, err
	}
	// Stablecoin top-ups reach the balance with neither a payment order nor a redeem code.
	crypto, err := creditedCryptoTopUps(r.client, userID).Exist(ctx)
	if err != nil || crypto {
		return crypto, err
	}
	return r.client.RedeemCode.Query().
		Where(
			redeemcode.UsedByEQ(userID),
			redeemcode.TypeIn(userInviteQualifyingTypes...),
			// A negative balance code is a deduction, not a top-up.
			redeemcode.Or(redeemcode.TypeNEQ(service.RedeemTypeBalance), redeemcode.ValueGT(0)),
		).
		Exist(ctx)
}

func ownerUnusedQuery(client *dbent.Client, ownerID int64) *dbent.RedeemCodeQuery {
	return client.RedeemCode.Query().Where(
		redeemcode.OwnerUserIDEQ(ownerID),
		redeemcode.TypeEQ(service.RedeemTypeInvitation),
		redeemcode.StatusEQ(service.StatusUnused),
	)
}

// invitedCount counts every account created with the owner's codes, soft-deleted ones
// included: deleting an invitee must not hand the owner a fresh invitation.
func invitedCount(ctx context.Context, client *dbent.Client, ownerID int64) (int, error) {
	return client.User.Query().
		Where(dbuser.InvitedByUserIDEQ(ownerID)).
		Count(mixins.SkipSoftDelete(ctx))
}

func (r *userInviteRepository) Counts(ctx context.Context, ownerID int64) (service.UserInviteCounts, error) {
	var out service.UserInviteCounts
	var err error
	if out.Unused, err = ownerUnusedQuery(r.client, ownerID).Count(ctx); err != nil {
		return out, err
	}
	if out.Invited, err = invitedCount(ctx, r.client, ownerID); err != nil {
		return out, err
	}
	if out.CreatedToday, err = createdTodayCount(ctx, r.client, ownerID); err != nil {
		return out, err
	}
	if out.CreatedThisWeek, err = createdThisWeekCount(ctx, r.client, ownerID); err != nil {
		return out, err
	}
	return out, nil
}

// createdTodayCount counts codes generated in the rolling last 24h, revoked ones included.
func createdTodayCount(ctx context.Context, client *dbent.Client, ownerID int64) (int, error) {
	return client.RedeemCode.Query().
		Where(
			redeemcode.OwnerUserIDEQ(ownerID),
			redeemcode.TypeEQ(service.RedeemTypeInvitation),
			redeemcode.CreatedAtGTE(time.Now().Add(-24*time.Hour)),
		).
		Count(ctx)
}

// createdThisWeekCount counts codes generated in the rolling last 7 days that were not revoked.
// RevokeByOwner (and the admin "expire" action) keep the row and set status=expired, so expired
// rows are the revoked ones; unused and used codes both count.
func createdThisWeekCount(ctx context.Context, client *dbent.Client, ownerID int64) (int, error) {
	return client.RedeemCode.Query().
		Where(
			redeemcode.OwnerUserIDEQ(ownerID),
			redeemcode.TypeEQ(service.RedeemTypeInvitation),
			redeemcode.StatusNEQ(service.StatusExpired),
			redeemcode.CreatedAtGTE(time.Now().Add(-service.UserInviteWeekWindow)),
		).
		Count(ctx)
}

func (r *userInviteRepository) ListByOwner(ctx context.Context, ownerID int64, params pagination.PaginationParams) ([]service.UserInviteCode, *pagination.PaginationResult, error) {
	q := r.client.RedeemCode.Query().Where(
		redeemcode.OwnerUserIDEQ(ownerID),
		redeemcode.TypeEQ(service.RedeemTypeInvitation),
	)
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := q.
		Order(dbent.Desc(redeemcode.FieldID)).
		Offset(params.Offset()).
		Limit(params.Limit()).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Invitee emails, soft-deleted accounts included (the owner did invite them).
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.UsedBy != nil {
			ids = append(ids, *row.UsedBy)
		}
	}
	emails := map[int64]string{}
	if len(ids) > 0 {
		users, err := r.client.User.Query().
			Where(dbuser.IDIn(ids...)).
			Select(dbuser.FieldID, dbuser.FieldEmail).
			All(mixins.SkipSoftDelete(ctx))
		if err != nil {
			return nil, nil, err
		}
		for _, u := range users {
			emails[u.ID] = u.Email
		}
	}

	out := make([]service.UserInviteCode, 0, len(rows))
	for _, row := range rows {
		item := service.UserInviteCode{
			Code:      row.Code,
			Status:    row.Status,
			CreatedAt: row.CreatedAt,
			UsedAt:    row.UsedAt,
		}
		if row.UsedBy != nil {
			item.InviteeEmail = emails[*row.UsedBy]
		}
		out = append(out, item)
	}
	return out, paginationResultFromTotal(int64(total), params), nil
}

func (r *userInviteRepository) CreateForOwner(ctx context.Context, ownerID int64, code string, limits service.UserInviteLimits) (*service.UserInviteCode, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	// Serialize this owner's generate requests: every check below runs under the row lock,
	// on the same connection as the insert. Soft-deleted users are filtered out → not found.
	owner, err := client.User.Query().Where(dbuser.IDEQ(ownerID)).ForUpdate().Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	if owner.Status != service.StatusActive {
		return nil, service.ErrUserNotActive
	}
	unused, err := ownerUnusedQuery(client, ownerID).Count(ctx)
	if err != nil {
		return nil, err
	}
	if unused >= limits.MaxUnused {
		return nil, service.ErrUserInviteUnusedLimit
	}
	createdToday, err := createdTodayCount(ctx, client, ownerID)
	if err != nil {
		return nil, err
	}
	if createdToday >= service.MaxUserInviteCreatesPerDay {
		return nil, service.ErrUserInviteDailyLimit
	}
	if limits.MaxPerWeek > 0 {
		createdThisWeek, err := createdThisWeekCount(ctx, client, ownerID)
		if err != nil {
			return nil, err
		}
		if createdThisWeek >= limits.MaxPerWeek {
			return nil, service.ErrUserInviteWeeklyLimit
		}
	}
	if limits.MaxTotal > 0 {
		invited, err := invitedCount(ctx, client, ownerID)
		if err != nil {
			return nil, err
		}
		// Unused codes count against the lifetime budget too, so holding codes cannot
		// overshoot it once they are all used.
		if invited+unused >= limits.MaxTotal {
			return nil, service.ErrUserInviteTotalLimit
		}
	}

	notes := "user invite"
	created, err := client.RedeemCode.Create().
		SetCode(code).
		SetType(service.RedeemTypeInvitation).
		SetValue(0).
		SetStatus(service.StatusUnused).
		SetNotes(notes).
		SetOwnerUserID(ownerID).
		Save(ctx)
	if err != nil {
		if isUniqueConstraintViolation(err) {
			return nil, service.ErrUserInviteCodeTaken
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &service.UserInviteCode{Code: created.Code, Status: created.Status, CreatedAt: created.CreatedAt}, nil
}

func (r *userInviteRepository) RevokeByOwner(ctx context.Context, ownerID int64, code string) error {
	// One conditional statement: a concurrent sign-up that consumes the code wins or loses
	// atomically, and nobody can expire another user's (or an admin's) code.
	affected, err := r.client.RedeemCode.Update().
		Where(
			redeemcode.CodeEQ(code),
			redeemcode.OwnerUserIDEQ(ownerID),
			redeemcode.TypeEQ(service.RedeemTypeInvitation),
			redeemcode.StatusEQ(service.StatusUnused),
			redeemcode.UsedByIsNil(),
		).
		SetStatus(service.StatusExpired).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrUserInviteNotFound
	}
	return nil
}

func (r *userInviteRepository) InviterRefs(ctx context.Context, ids []int64) (map[int64]service.InviterRef, error) {
	out := make(map[int64]service.InviterRef, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	users, err := r.client.User.Query().
		Where(dbuser.IDIn(ids...)).
		Select(dbuser.FieldID, dbuser.FieldEmail, dbuser.FieldDeletedAt).
		All(mixins.SkipSoftDelete(ctx))
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		out[u.ID] = service.InviterRef{ID: u.ID, Email: u.Email, Deleted: u.DeletedAt != nil}
	}
	return out, nil
}
