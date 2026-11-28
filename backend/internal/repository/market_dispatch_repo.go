package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
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
// is small; status 'listed' ⇔ the account has a market membership), the
// admin groups that are fully shared by one user, and the market switch.
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
	if len(state.ListedAccountIDs) > 0 {
		// Only the early 403 check uses it: a failed read leaves it empty
		// (that check is skipped), the listed set still refreshes.
		if groups, groupsErr := r.loadFullySharedGroups(ctx); groupsErr == nil {
			state.FullySharedGroups = groups
		}
	}
	// The switch only gates new buyer admissions (re-read in the admission
	// snapshot); a failed read must not drop the listed set: treat as off.
	enabled, err := r.marketEnabled(ctx, r.db)
	if err == nil {
		state.Enabled = enabled
	}
	return state, nil
}

// loadFullySharedGroups: the owner groups (marketOwnerGroupPredicate for
// that user) of listed accounts whose every live admin member is listed and
// owned by that one user (only groups with a listed member are scanned;
// nothing runs without listings).
func (r *marketListingRepository) loadFullySharedGroups(ctx context.Context) (map[int64]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		WITH candidate AS (
			SELECT DISTINCT ag.group_id
			FROM account_market_listings l
			JOIN account_groups ag ON ag.account_id = l.account_id AND ag.source = 'admin'
			WHERE l.status = 'listed'
		), fully AS (
			SELECT ag.group_id, min(a.owner_user_id) AS owner_user_id
			FROM candidate c
			JOIN account_groups ag ON ag.group_id = c.group_id AND ag.source = 'admin'
			JOIN accounts a ON a.id = ag.account_id AND a.deleted_at IS NULL
			LEFT JOIN account_market_listings l ON l.account_id = a.id
			GROUP BY ag.group_id
			HAVING bool_and(COALESCE(l.status = 'listed', FALSE) AND a.owner_user_id IS NOT NULL)
			   AND count(DISTINCT a.owner_user_id) = 1
		)
		SELECT f.group_id, f.owner_user_id
		FROM fully f JOIN groups g ON g.id = f.group_id
		WHERE `+strings.ReplaceAll(marketOwnerGroupPredicate, "$2", "f.owner_user_id"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]int64{}
	for rows.Next() {
		var groupID, owner int64
		if err := rows.Scan(&groupID, &owner); err != nil {
			return nil, err
		}
		out[groupID] = owner
	}
	return out, rows.Err()
}

// AdmitMarketCandidate runs one candidate's final admission.
//
// Buyer path: the admission contract AdmitMarket (REPEATABLE READ, listing
// row share-locked, one snapshot incl. the market switch, check then insert,
// one rerun on 40001); evaluate runs inside its check (pure, may run twice).
//
// Owner / platform paths: the same reads in a read-only REPEATABLE READ
// transaction, plus (owner class) the owner-group predicate
// (marketOwnerGroupQuery); nothing is written.
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
		GroupMarketPlan:    state.GroupMarketPlan,
		GroupActive:        state.GroupStatus == service.StatusActive,
		GroupPlatform:      state.GroupPlatform,
		OwnerUserStatus:    state.OwnerUserStatus,
		LatestOwnerEventID: state.OwnerEventID,
		MarketEnabled:      state.MarketEnabled,
		Listing:            state.Listing,
		GroupRate:          state.GroupRateMultiplier,
		GroupModelRates:    state.GroupModelRateMultipliers,
		BuyerRateOverride:  state.BuyerRateOverride,
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

// marketOwnerGroupQuery ($1 group, $2 authenticated user) is the owner-path
// group predicate, for P2c-mapped and admin-built groups alike:
//   - the group is live, active, Anthropic, standard (not a subscription),
//     hosted-usage, exclusive, not a market tier, with a finite non-negative
//     fee rate (Group.UsesHostedUsage; numeric NaN compares above every
//     number, so it is excluded explicitly; the column cannot hold infinity);
//   - the user has an allowed row and no other user has one;
//   - hosted_owner_groups does not map the group to another user;
//   - every live member account (any source) is owned by the user (a
//     platform account without an owner fails).
//
// The account's own admin membership, owner and owner status are checked
// by EvaluateMarketAdmission on the facts of the same snapshot.
const marketOwnerGroupQuery = `
	SELECT EXISTS (
		SELECT 1 FROM groups g
		WHERE g.id = $1 AND ` + marketOwnerGroupPredicate + `
	)`

// marketOwnerGroupPredicate is the owner-group condition on a groups row g
// for the user in $2 (shared by the owner admission, the all-shared read
// and, with $2 replaced, the registry's fully-shared index).
const marketOwnerGroupPredicate = `g.deleted_at IS NULL AND g.status = 'active'
			AND g.platform = 'anthropic' AND g.subscription_type = 'standard'
			AND g.hosted_usage_enabled AND g.is_exclusive AND NOT g.market_tier
			AND g.rate_multiplier >= 0 AND g.rate_multiplier <> 'NaN'::numeric
			AND EXISTS (SELECT 1 FROM user_allowed_groups u WHERE u.group_id = g.id AND u.user_id = $2)
			AND NOT EXISTS (SELECT 1 FROM user_allowed_groups u WHERE u.group_id = g.id AND u.user_id <> $2)
			AND NOT EXISTS (SELECT 1 FROM hosted_owner_groups h WHERE h.group_id = g.id AND h.user_id <> $2)
			AND NOT EXISTS (
				SELECT 1 FROM account_groups ag JOIN accounts a ON a.id = ag.account_id
				WHERE ag.group_id = g.id AND a.deleted_at IS NULL AND a.owner_user_id IS DISTINCT FROM $2
			)`

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
	var membership, groupStatus, ownerStatus sql.NullString
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
	if err := scanSingleRow(ctx, tx,
		"SELECT (SELECT source FROM account_groups WHERE account_id = $1 AND group_id = $2)",
		[]any{req.AccountID, req.GroupID}, &membership); err != nil {
		return nil, err
	}
	f.MembershipSource = membership.String
	if req.Class == service.MarketClassOwner && req.AuthUserID > 0 {
		if err := scanSingleRow(ctx, tx, marketOwnerGroupQuery,
			[]any{req.GroupID, req.AuthUserID}, &f.OwnerGroupQualified); err != nil {
			return nil, err
		}
	}
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

// HostedGroupSharedAccounts reads, in one statement, the live admin member
// accounts of groupID (total) and the ids of those whose listing is listed
// and whose current owner is userID (the HOSTED_ACCOUNT_SHARED decision:
// every account the owner's hosted Key could use is shared). A group that is
// not (any more) an owner group of userID (marketOwnerGroupQuery: live,
// active, hosted, exclusive, only this user, every member owned by them)
// yields 0 accounts, so no 403 is derived from a stale Key snapshot.
func (r *marketListingRepository) HostedGroupSharedAccounts(ctx context.Context, groupID, userID int64) (int, []int64, error) {
	var (
		total  int
		shared pq.Int64Array
	)
	err := scanSingleRow(ctx, r.db, `
		SELECT COUNT(*),
		       COALESCE(array_agg(a.id ORDER BY a.id) FILTER (WHERE l.status = 'listed' AND a.owner_user_id = $2), '{}')
		FROM account_groups ag
		JOIN accounts a ON a.id = ag.account_id AND a.deleted_at IS NULL
		LEFT JOIN account_market_listings l ON l.account_id = a.id
		WHERE ag.group_id = $1 AND ag.source = 'admin' AND (`+marketOwnerGroupQuery+`)`,
		[]any{groupID, userID}, &total, &shared)
	if err != nil {
		return 0, nil, err
	}
	return total, []int64(shared), nil
}
