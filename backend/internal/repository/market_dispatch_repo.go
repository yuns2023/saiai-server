package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Market dispatch store (P3a part 2): the polled "listed accounts + switch"
// state, the authoritative final-admission read (+ the buyer admission row
// in the same transaction), and the count_tokens tier configuration probe.

var _ service.MarketDispatchStore = (*marketListingRepository)(nil)

// NewMarketDispatchStore serves the market selection and final admission.
func NewMarketDispatchStore(sqlDB *sql.DB) service.MarketDispatchStore {
	return &marketListingRepository{db: sqlDB}
}

// LoadMarketDispatchState reads the listed account ids (account_market_listings
// is small; status 'listed' ⇔ the account has a market membership) and the
// market switch.
func (r *marketListingRepository) LoadMarketDispatchState(ctx context.Context) (*service.MarketDispatchState, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT account_id FROM account_market_listings WHERE status = 'listed'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	state := &service.MarketDispatchState{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		state.ListedAccountIDs = append(state.ListedAccountIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var value sql.NullString
	if err := scanSingleRow(ctx, r.db, "SELECT value FROM settings WHERE key = $1",
		[]any{service.SettingKeyMarketEnabled}, &value); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	state.Enabled = value.Valid && value.String == "true"
	return state, nil
}

// AdmitMarketCandidate runs one candidate's final admission in a short
// transaction: share-lock the listing row (serialises with every listing
// lifecycle change, including owner clear / pause / withdraw / delete, which
// all rewrite it under the account lock), read the authoritative facts in one
// statement, let evaluate decide, and for the buyer path insert the
// immutable admission (admitted_at = clock_timestamp() after the lock).
func (r *marketListingRepository) AdmitMarketCandidate(ctx context.Context, req *service.MarketAdmissionRequest, evaluate func(*service.MarketAuthoritativeFacts) string) (*service.MarketAdmissionVerdict, error) {
	if req == nil || evaluate == nil {
		return nil, errors.New("invalid market admission request")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	facts := &service.MarketAuthoritativeFacts{}
	listing, err := queryMarketListing(ctx, tx, "SELECT "+marketListingColumns+
		" FROM account_market_listings WHERE account_id = $1 FOR SHARE", req.AccountID)
	if err != nil {
		return nil, err
	}
	facts.Listing = listing

	var (
		found                                   bool
		deleted, schedulable, paused, withdraw  sql.NullBool
		status, platform, accountType           sql.NullString
		owner, latestEventID, latestEventOwner  sql.NullInt64
		hostedGroupID                           sql.NullInt64
		concurrency                             sql.NullInt64
		extra                                   []byte
		membership                              sql.NullString
		groupFound                              bool
		tier, groupDeleted, hosted, exclusive   sql.NullBool
		groupStatus, groupPlatform, ownerStatus sql.NullString
		ownerDeleted                            sql.NullBool
		marketEnabled                           sql.NullString
	)
	err = scanSingleRow(ctx, tx, `
		SELECT a.id IS NOT NULL, a.deleted_at IS NOT NULL, a.status, a.schedulable, a.owner_user_id, a.concurrency,
			a.platform, a.type, a.extra, a.owner_paused_at IS NOT NULL, a.owner_withdraw_requested_at IS NOT NULL,
			(SELECT ag.source FROM account_groups ag WHERE ag.account_id = $1 AND ag.group_id = $2),
			g.id IS NOT NULL, g.market_tier, g.deleted_at IS NOT NULL, g.status, g.platform, g.hosted_usage_enabled, g.is_exclusive,
			ou.status, ou.deleted_at IS NOT NULL,
			ev.id, ev.new_owner_user_id,
			(SELECT h.group_id FROM hosted_owner_groups h WHERE h.user_id = $3),
			(SELECT s.value FROM settings s WHERE s.key = $4)
		FROM (SELECT 1) one
		LEFT JOIN accounts a ON a.id = $1
		LEFT JOIN groups g ON g.id = $2
		LEFT JOIN users ou ON ou.id = a.owner_user_id
		LEFT JOIN LATERAL (
			SELECT e.id, e.new_owner_user_id FROM account_owner_events e
			WHERE e.account_id = $1 ORDER BY e.id DESC LIMIT 1
		) ev ON TRUE`,
		[]any{req.AccountID, req.GroupID, req.AuthUserID, service.SettingKeyMarketEnabled},
		&found, &deleted, &status, &schedulable, &owner, &concurrency,
		&platform, &accountType, &extra, &paused, &withdraw,
		&membership,
		&groupFound, &tier, &groupDeleted, &groupStatus, &groupPlatform, &hosted, &exclusive,
		&ownerStatus, &ownerDeleted,
		&latestEventID, &latestEventOwner,
		&hostedGroupID,
		&marketEnabled)
	if err != nil {
		return nil, err
	}
	facts.AccountFound = found && !deleted.Bool
	if facts.AccountFound {
		acc := &service.Account{
			ID:          req.AccountID,
			Platform:    platform.String,
			Type:        accountType.String,
			Status:      status.String,
			Schedulable: schedulable.Bool,
			Concurrency: int(concurrency.Int64),
		}
		if len(extra) > 0 {
			if err := json.Unmarshal(extra, &acc.Extra); err != nil {
				return nil, err
			}
		}
		if owner.Valid {
			v := owner.Int64
			acc.OwnerUserID = &v
		}
		facts.Account = acc
		facts.OwnerPaused = paused.Bool
		facts.OwnerWithdrawRequested = withdraw.Bool
	}
	facts.MembershipSource = membership.String
	facts.GroupFound = groupFound && !groupDeleted.Bool
	facts.GroupMarketTier = tier.Bool
	facts.GroupActive = groupStatus.String == service.StatusActive
	facts.GroupPlatform = groupPlatform.String
	facts.GroupHosted = hosted.Bool
	facts.GroupExclusive = exclusive.Bool
	if ownerStatus.Valid && !ownerDeleted.Bool {
		facts.OwnerUserStatus = ownerStatus.String
	}
	if latestEventID.Valid {
		facts.LatestOwnerEventID = latestEventID.Int64
		if latestEventOwner.Valid {
			v := latestEventOwner.Int64
			facts.LatestOwnerEventOwner = &v
		}
	}
	facts.HostedOwnerGroupID = hostedGroupID.Int64
	facts.MarketEnabled = marketEnabled.Valid && marketEnabled.String == "true"

	verdict := &service.MarketAdmissionVerdict{Facts: facts}
	if reason := evaluate(facts); reason != "" {
		verdict.Reason = reason
		return verdict, nil
	}
	if req.Class == service.MarketClassBuyer {
		admission, err := insertMarketAdmissionTx(ctx, tx, &service.MarketAdmission{
			AttemptID:      req.AttemptID,
			RequestRef:     req.RequestRef,
			AccountID:      req.AccountID,
			GroupID:        req.GroupID,
			OwnerUserID:    *facts.Account.OwnerUserID,
			OwnerEventID:   facts.LatestOwnerEventID,
			ListingVersion: facts.Listing.Version,
			BuyerUserID:    req.AuthUserID,
			APIKeyID:       req.APIKeyID,
		})
		if err != nil {
			return nil, err
		}
		verdict.Admission = admission
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	verdict.Admitted = true
	return verdict, nil
}

// CountMarketTierPlatformMembers counts the live admin members of a tier
// without an owner (count_tokens: "configured" vs "temporarily unavailable").
func (r *marketListingRepository) CountMarketTierPlatformMembers(ctx context.Context, groupID int64) (int, error) {
	var n int
	err := scanSingleRow(ctx, r.db, `
		SELECT COUNT(*) FROM account_groups ag JOIN accounts a ON a.id = ag.account_id
		WHERE ag.group_id = $1 AND ag.source = 'admin' AND a.deleted_at IS NULL AND a.owner_user_id IS NULL`,
		[]any{groupID}, &n)
	return n, err
}
