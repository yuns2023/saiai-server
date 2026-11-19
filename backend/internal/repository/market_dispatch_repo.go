package repository

import (
	"context"
	"database/sql"
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
	// The switch only gates new buyer admissions (re-read in the admission
	// snapshot); a failed read must not drop the listed set: treat as off.
	enabled, err := r.marketEnabled(ctx, r.db)
	if err == nil {
		state.Enabled = enabled
	}
	return state, nil
}

// AdmitMarketCandidate runs one candidate's final admission.
//
// Buyer path: the admission contract AdmitMarket (REPEATABLE READ, listing
// row share-locked, one snapshot incl. the market switch, check then insert,
// one rerun on 40001); evaluate runs inside its check (pure, may run twice).
//
// Owner / platform paths: the same reads in a read-only REPEATABLE READ
// transaction, plus the owner's hosted_owner_groups mapping and the group's
// hosted/exclusive flags; nothing is written.
func (r *marketListingRepository) AdmitMarketCandidate(ctx context.Context, req *service.MarketAdmissionRequest, evaluate func(*service.MarketAuthoritativeFacts) string) (*service.MarketAdmissionVerdict, error) {
	if req == nil || evaluate == nil {
		return nil, errors.New("invalid market admission request")
	}
	if req.Class != service.MarketClassBuyer {
		return r.checkMarketCandidate(ctx, req, evaluate)
	}
	var (
		facts  *service.MarketAuthoritativeFacts
		reason string
	)
	admission, err := r.AdmitMarket(ctx, &service.MarketAdmission{
		AttemptID:   req.AttemptID,
		RequestRef:  req.RequestRef,
		AccountID:   req.AccountID,
		GroupID:     req.GroupID,
		BuyerUserID: req.AuthUserID,
		APIKeyID:    req.APIKeyID,
	}, func(state *service.MarketAdmissionState, in *service.MarketAdmission) error {
		facts = marketFactsFromState(state)
		reason = evaluate(facts)
		if reason != "" {
			return errMarketCandidateRefused
		}
		in.OwnerUserID = *state.Account.OwnerUserID
		in.OwnerEventID = state.OwnerEventID
		in.ListingVersion = state.Listing.Version
		return nil
	})
	if errors.Is(err, errMarketCandidateRefused) {
		return &service.MarketAdmissionVerdict{Reason: reason, Facts: facts}, nil
	}
	if err != nil {
		return nil, err
	}
	return &service.MarketAdmissionVerdict{Admitted: true, Admission: admission, Facts: facts}, nil
}

var errMarketCandidateRefused = errors.New("market candidate refused")

func marketFactsFromState(state *service.MarketAdmissionState) *service.MarketAuthoritativeFacts {
	f := &service.MarketAuthoritativeFacts{
		AccountFound:       state.Account != nil,
		Account:            state.Account,
		GroupFound:         state.GroupFound,
		GroupMarketTier:    state.GroupMarketTier,
		GroupActive:        state.GroupStatus == service.StatusActive,
		GroupPlatform:      state.GroupPlatform,
		OwnerUserStatus:    state.OwnerUserStatus,
		LatestOwnerEventID: state.OwnerEventID,
		MarketEnabled:      state.MarketEnabled,
		Listing:            state.Listing,
	}
	if state.OwnerEventUserID != nil {
		v := *state.OwnerEventUserID
		f.LatestOwnerEventOwner = &v
	}
	if state.Account != nil {
		f.OwnerPaused = state.Account.OwnerPausedAt != nil
		f.OwnerWithdrawRequested = state.Account.OwnerWithdrawRequestedAt != nil
	}
	switch {
	case state.MarketMember:
		f.MembershipSource = service.AccountGroupSourceMarket
	case state.AdminMember:
		f.MembershipSource = service.AccountGroupSourceAdmin
	}
	return f
}

func (r *marketListingRepository) marketEnabled(ctx context.Context, q sqlQueryer) (bool, error) {
	var value sql.NullString
	if err := scanSingleRow(ctx, q, "SELECT value FROM settings WHERE key = $1",
		[]any{service.SettingKeyMarketEnabled}, &value); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return value.Valid && value.String == "true", nil
}

// checkMarketCandidate is the read-only authoritative check (owner and
// platform paths), one REPEATABLE READ snapshot.
func (r *marketListingRepository) checkMarketCandidate(ctx context.Context, req *service.MarketAdmissionRequest, evaluate func(*service.MarketAuthoritativeFacts) string) (*service.MarketAdmissionVerdict, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	f := &service.MarketAuthoritativeFacts{}
	if f.Listing, err = queryMarketListing(ctx, tx, "SELECT "+marketListingColumns+
		" FROM account_market_listings WHERE account_id = $1", req.AccountID); err != nil {
		return nil, err
	}
	account, err := scanMarketAccount(ctx, tx,
		"SELECT "+marketAccountColumns+" FROM accounts WHERE id = $1 AND deleted_at IS NULL", req.AccountID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		f.AccountFound, f.Account = true, account
		f.OwnerPaused = account.OwnerPausedAt != nil
		f.OwnerWithdrawRequested = account.OwnerWithdrawRequestedAt != nil
	}
	var (
		membership, groupStatus, ownerStatus sql.NullString
		hostedGroup                          sql.NullInt64
	)
	err = scanSingleRow(ctx, tx, `
		SELECT g.market_tier, g.status, g.platform, g.hosted_usage_enabled, g.is_exclusive
		FROM groups g WHERE g.id = $1 AND g.deleted_at IS NULL`, []any{req.GroupID},
		&f.GroupMarketTier, &groupStatus, &f.GroupPlatform, &f.GroupHosted, &f.GroupExclusive)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		f.GroupFound = true
		f.GroupActive = groupStatus.String == service.StatusActive
	}
	if err := scanSingleRow(ctx, tx, `
		SELECT (SELECT source FROM account_groups WHERE account_id = $1 AND group_id = $2),
			(SELECT group_id FROM hosted_owner_groups WHERE user_id = $3)`,
		[]any{req.AccountID, req.GroupID, req.AuthUserID}, &membership, &hostedGroup); err != nil {
		return nil, err
	}
	f.MembershipSource = membership.String
	f.HostedOwnerGroupID = hostedGroup.Int64
	if f.Account != nil && f.Account.OwnerUserID != nil {
		if err := scanSingleRow(ctx, tx, "SELECT status FROM users WHERE id = $1 AND deleted_at IS NULL",
			[]any{*f.Account.OwnerUserID}, &ownerStatus); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		f.OwnerUserStatus = ownerStatus.String
	}
	if f.MarketEnabled, err = r.marketEnabled(ctx, tx); err != nil {
		return nil, err
	}
	verdict := &service.MarketAdmissionVerdict{Facts: f, Reason: evaluate(f)}
	verdict.Admitted = verdict.Reason == ""
	return verdict, nil
}

// CountMarketTierPlatformMembers counts the live admin members of a tier
// without an owner (count_tokens: "configured" vs "temporarily unavailable").
func (r *marketListingRepository) CountMarketTierPlatformMembers(ctx context.Context, groupID int64) (int, error) {
	var n int
	err := scanSingleRow(ctx, r.db, `
		SELECT COUNT(*) FROM account_groups ag JOIN accounts a ON a.id = ag.account_id
		WHERE ag.group_id = $1 AND ag.source = 'admin' AND a.deleted_at IS NULL AND a.owner_user_id IS NULL
			-- the accounts that can serve count_tokens in a tier
			-- (MarketCountTokensCapable): anthropic, not Bedrock, not pinned
			AND a.platform = 'anthropic' AND a.type <> 'bedrock'
			AND NOT (a.type IN ('oauth', 'setup-token') AND COALESCE(lower(trim(a.extra->>'claude_oauth_mode')), '') = 'pinned')`,
		[]any{groupID}, &n)
	return n, err
}
