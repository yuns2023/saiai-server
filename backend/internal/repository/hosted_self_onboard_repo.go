package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// hostedApplicationRepository persists hosted self-onboarding applications.
// Every state change is a conditional update on status = 'pending' (or runs
// under the row lock), so approve / cancel / reject / expire have exactly one
// winner. Secrets (credentials, proxy password) are read only inside the
// approval transaction and wiped by every terminal transition.
type hostedApplicationRepository struct {
	client   *dbent.Client
	db       *sql.DB
	accounts *accountRepository
}

// NewHostedApplicationRepository builds the repository. The scheduler cache
// is used only for the best-effort snapshot sync after commit.
func NewHostedApplicationRepository(client *dbent.Client, sqlDB *sql.DB, schedulerCache service.SchedulerCache) service.HostedApplicationRepository {
	return &hostedApplicationRepository{
		client:   client,
		db:       sqlDB,
		accounts: newAccountRepositoryWithSQL(client, sqlDB, schedulerCache),
	}
}

const hostedApplicationColumns = `
	a.id, a.user_id, COALESCE(u.email, ''), a.status, a.display_name, a.platform,
	a.upstream_account_uuid::text, COALESCE(a.upstream_org_uuid, ''), COALESCE(a.upstream_email, ''),
	COALESCE(a.proxy_protocol, ''), COALESCE(a.proxy_host, ''), COALESCE(a.proxy_port, 0),
	COALESCE(a.proxy_username, '') <> '',
	a.account_id, a.group_id, a.reviewed_by, a.reviewed_at, COALESCE(a.reject_reason, ''),
	a.created_at, a.updated_at`

const hostedApplicationFrom = `
	FROM hosted_account_applications a
	LEFT JOIN users u ON u.id = a.user_id`

func scanHostedApplication(row rowScanner) (*service.HostedApplication, error) {
	var (
		app                            service.HostedApplication
		accountID, groupID, reviewedBy sql.NullInt64
		reviewedAt                     sql.NullTime
	)
	if err := row.Scan(
		&app.ID, &app.UserID, &app.UserEmail, &app.Status, &app.DisplayName, &app.Platform,
		&app.UpstreamAccountUUID, &app.UpstreamOrgUUID, &app.UpstreamEmail,
		&app.ProxyProtocol, &app.ProxyHost, &app.ProxyPort, &app.HasProxyAuth,
		&accountID, &groupID, &reviewedBy, &reviewedAt, &app.RejectReason,
		&app.CreatedAt, &app.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if accountID.Valid {
		v := accountID.Int64
		app.AccountID = &v
	}
	if groupID.Valid {
		v := groupID.Int64
		app.GroupID = &v
	}
	if reviewedBy.Valid {
		v := reviewedBy.Int64
		app.ReviewedBy = &v
	}
	if reviewedAt.Valid {
		v := reviewedAt.Time.UTC()
		app.ReviewedAt = &v
	}
	app.CreatedAt = app.CreatedAt.UTC()
	app.UpdatedAt = app.UpdatedAt.UTC()
	return &app, nil
}

func (r *hostedApplicationRepository) queryApplications(ctx context.Context, q sqlQueryer, where string, args ...any) ([]service.HostedApplication, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+hostedApplicationColumns+hostedApplicationFrom+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.HostedApplication, 0)
	for rows.Next() {
		app, err := scanHostedApplication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *app)
	}
	return out, rows.Err()
}

func (r *hostedApplicationRepository) getWith(ctx context.Context, q sqlQueryer, id int64) (*service.HostedApplication, error) {
	apps, err := r.queryApplications(ctx, q, "WHERE a.id = $1", id)
	if err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, service.ErrSelfOnboardNotFound
	}
	return &apps[0], nil
}

func (r *hostedApplicationRepository) Get(ctx context.Context, id int64) (*service.HostedApplication, error) {
	if id <= 0 {
		return nil, service.ErrSelfOnboardNotFound
	}
	return r.getWith(ctx, r.db, id)
}

func (r *hostedApplicationRepository) CountPending(ctx context.Context, userID int64) (int, error) {
	var n int
	err := scanSingleRow(ctx, r.db,
		"SELECT COUNT(*) FROM hosted_account_applications WHERE user_id = $1 AND status = 'pending'",
		[]any{userID}, &n)
	return n, err
}

func (r *hostedApplicationRepository) CreatePending(ctx context.Context, in *service.HostedApplicationCreate) (*service.HostedApplication, error) {
	if in == nil || in.UserID <= 0 || len(in.Credentials) == 0 {
		return nil, service.ErrSelfOnboardInvalidInput
	}
	credentials, err := json.Marshal(in.Credentials)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// The user row lock serializes this user's submissions, so the pending
	// count below cannot be raced past the limit. FOR NO KEY UPDATE (not FOR
	// UPDATE): it does not conflict with the KEY SHARE locks that foreign-key
	// checks of concurrent inserts (accounts, api keys, ...) take on users.
	var status string
	if err := scanSingleRow(ctx, tx,
		"SELECT status FROM users WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE",
		[]any{in.UserID}, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrSelfOnboardInvalidState
		}
		return nil, err
	}
	if status != service.StatusActive {
		return nil, service.ErrSelfOnboardInvalidState
	}
	var enabled string
	// Share lock, like the approval: a concurrent "switch off" save waits
	// for this insert instead of racing it.
	if err := scanSingleRow(ctx, tx, "SELECT value FROM settings WHERE key = $1 FOR SHARE",
		[]any{service.SettingKeyHostedSelfOnboardEnabled}, &enabled); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if enabled != "true" {
		return nil, service.ErrSelfOnboardDisabled
	}
	var pending int
	if err := scanSingleRow(ctx, tx,
		"SELECT COUNT(*) FROM hosted_account_applications WHERE user_id = $1 AND status = 'pending'",
		[]any{in.UserID}, &pending); err != nil {
		return nil, err
	}
	if pending >= in.MaxPending {
		return nil, service.ErrSelfOnboardPendingLimit
	}
	var hosted bool
	if err := scanSingleRow(ctx, tx, `
		SELECT EXISTS (SELECT 1 FROM accounts WHERE platform = $1 AND upstream_account_uuid = $2::uuid AND deleted_at IS NULL)
		    OR EXISTS (SELECT 1 FROM hosted_account_applications WHERE platform = $1 AND upstream_account_uuid = $2::uuid AND status = 'pending')`,
		[]any{in.Platform, in.UpstreamAccountUUID}, &hosted); err != nil {
		return nil, err
	}
	if hosted {
		return nil, service.ErrSelfOnboardAlreadyHosted
	}

	var proxyProtocol, proxyHost, proxyUsername, proxyPassword sql.NullString
	var proxyPort sql.NullInt64
	if in.Proxy != nil {
		proxyProtocol = sql.NullString{String: in.Proxy.Protocol, Valid: true}
		proxyHost = sql.NullString{String: in.Proxy.Host, Valid: true}
		proxyPort = sql.NullInt64{Int64: int64(in.Proxy.Port), Valid: true}
		if in.Proxy.Username != "" {
			proxyUsername = sql.NullString{String: in.Proxy.Username, Valid: true}
			proxyPassword = sql.NullString{String: in.Proxy.Password, Valid: true}
		}
	}
	var id int64
	if err := scanSingleRow(ctx, tx, `
		INSERT INTO hosted_account_applications (
			user_id, status, display_name, platform, upstream_account_uuid, upstream_org_uuid, upstream_email,
			credentials, proxy_protocol, proxy_host, proxy_port, proxy_username, proxy_password)
		VALUES ($1, 'pending', $2, $3, $4::uuid, NULLIF($5, ''), NULLIF($6, ''), $7::jsonb, $8, $9, $10, $11, $12)
		RETURNING id`,
		[]any{in.UserID, in.DisplayName, in.Platform, in.UpstreamAccountUUID, in.UpstreamOrgUUID, in.UpstreamEmail,
			string(credentials), proxyProtocol, proxyHost, proxyPort, proxyUsername, proxyPassword},
		&id); err != nil {
		if isUniqueViolation(err, "hosted_account_applications_pending_upstream_key") {
			return nil, service.ErrSelfOnboardAlreadyHosted
		}
		return nil, err
	}
	// The response is read inside the transaction: once committed, the
	// caller always gets the stored application.
	app, err := r.getWith(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return app, nil
}

func (r *hostedApplicationRepository) ListByUser(ctx context.Context, userID int64, limit int) ([]service.HostedApplication, error) {
	if userID <= 0 || limit <= 0 {
		return []service.HostedApplication{}, nil
	}
	return r.queryApplications(ctx, r.db, "WHERE a.user_id = $1 ORDER BY a.id DESC LIMIT $2", userID, limit)
}

func (r *hostedApplicationRepository) ListAdmin(ctx context.Context, status string, page, pageSize int) ([]service.HostedApplication, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	pageSize = min(pageSize, service.HostedApplicationListMaxPageSize)
	page = min(page, service.HostedApplicationListMaxPage) // keeps the offset far from overflow
	where := ""
	args := []any{}
	if status != "" {
		where = "WHERE a.status = $1"
		args = append(args, status)
	}
	var total int64
	if err := scanSingleRow(ctx, r.db, "SELECT COUNT(*) FROM hosted_account_applications a "+where, args, &total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	items, err := r.queryApplications(ctx, r.db,
		fmt.Sprintf("%s ORDER BY a.id DESC LIMIT $%d OFFSET $%d", where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// terminalUpdate applies a pending → terminal transition and wipes secrets.
// It returns NotFound when the row is not visible to the caller, and
// InvalidState when it is visible but no longer pending.
func (r *hostedApplicationRepository) terminalUpdate(ctx context.Context, id int64, userID int64, set string, args ...any) (*service.HostedApplication, error) {
	base := []any{id}
	ownerFilter := ""
	if userID > 0 {
		base = append(base, userID)
		ownerFilter = " AND user_id = $2"
	}
	all := append(base, args...)
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE hosted_account_applications
		SET %s, credentials = NULL, proxy_password = NULL, updated_at = NOW()
		WHERE id = $1%s AND status = 'pending'`, set, ownerFilter), all...)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	app, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if userID > 0 && app.UserID != userID {
		return nil, service.ErrSelfOnboardNotFound
	}
	if affected == 0 {
		return nil, service.ErrSelfOnboardInvalidState
	}
	return app, nil
}

func (r *hostedApplicationRepository) Cancel(ctx context.Context, userID, id int64) (*service.HostedApplication, error) {
	if userID <= 0 || id <= 0 {
		return nil, service.ErrSelfOnboardNotFound
	}
	return r.terminalUpdate(ctx, id, userID, "status = 'cancelled'")
}

func (r *hostedApplicationRepository) Reject(ctx context.Context, adminUserID, id int64, reason string) (*service.HostedApplication, error) {
	if id <= 0 {
		return nil, service.ErrSelfOnboardNotFound
	}
	var admin any
	if adminUserID > 0 {
		admin = adminUserID
	}
	return r.terminalUpdate(ctx, id, 0, "status = 'rejected', reject_reason = $2, reviewed_by = $3, reviewed_at = NOW()", reason, admin)
}

func (r *hostedApplicationRepository) ExpirePending(ctx context.Context, createdBefore time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	// SKIP LOCKED: a row being approved right now is left alone; its approval
	// either commits (approved) or rolls back (expired on the next pass).
	res, err := r.db.ExecContext(ctx, `
		UPDATE hosted_account_applications
		SET status = 'expired', credentials = NULL, proxy_password = NULL, updated_at = NOW()
		WHERE status = 'pending' AND id IN (
			SELECT id FROM hosted_account_applications
			WHERE status = 'pending' AND created_at < $1
			ORDER BY id
			LIMIT $2
			FOR UPDATE SKIP LOCKED)`, createdBefore, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *hostedApplicationRepository) SetOwnerAccountState(ctx context.Context, ownerUserID, accountID int64, action string) (*service.HostedOwnedAccountState, error) {
	var set string
	switch action {
	case service.HostedOwnerActionPause:
		set = "owner_paused_at = COALESCE(owner_paused_at, NOW())"
	case service.HostedOwnerActionResume:
		set = "owner_paused_at = NULL"
	case service.HostedOwnerActionWithdraw:
		set = "owner_withdraw_requested_at = COALESCE(owner_withdraw_requested_at, NOW())"
	default:
		return nil, service.ErrSelfOnboardInvalidInput
	}
	if ownerUserID <= 0 || accountID <= 0 {
		return nil, service.ErrSelfOnboardNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Only the CURRENT owner of a live account (historic owners can still
	// view usage, but never change the account).
	state := &service.HostedOwnedAccountState{AccountID: accountID}
	if err := scanSingleRow(ctx, tx, fmt.Sprintf(`
		UPDATE accounts SET %s, updated_at = NOW()
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
		RETURNING owner_paused_at IS NOT NULL, owner_withdraw_requested_at IS NOT NULL`, set),
		[]any{accountID, ownerUserID}, &state.OwnerPaused, &state.WithdrawRequested); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrSelfOnboardNotFound
		}
		return nil, err
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.accounts.syncSchedulerAccountSnapshot(ctx, accountID)
	return state, nil
}

// Approve is the approval unit of work, one transaction:
// lock application → (approved: replay) → lock applicant → identity check →
// build spec → owner group (get/verify or create) → proxy (existing or
// created from the application) → account → group binding → allowed group →
// owner + owner event → scheduler outbox → application approved + wiped.
// Any error rolls everything back. Cache sync happens after commit.
func (r *hostedApplicationRepository) Approve(ctx context.Context, p service.HostedApproveParams) (*service.HostedApproveResult, error) {
	if p.ApplicationID <= 0 || p.Build == nil {
		return nil, service.ErrSelfOnboardNotFound
	}
	if dbent.TxFromContext(ctx) != nil {
		return nil, errors.New("hosted approval must not run inside a caller transaction")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txc := tx.Client()

	app, err := lockHostedApplication(ctx, txc, p.ApplicationID)
	if err != nil {
		return nil, err
	}
	switch app.Status {
	case service.HostedApplicationStatusApproved:
		stored, err := r.getWith(ctx, txc, app.ID)
		if err != nil {
			return nil, err
		}
		result := &service.HostedApproveResult{Application: stored, Replayed: true}
		if app.AccountID != nil {
			result.AccountID = *app.AccountID
		}
		if app.GroupID != nil {
			result.GroupID = *app.GroupID
		}
		return result, nil
	case service.HostedApplicationStatusPending:
	default:
		return nil, service.ErrSelfOnboardInvalidState
	}

	// Re-read the switch under a share lock: a concurrent settings save
	// (row update) waits for this approval, and an approval that started
	// before "off" was saved cannot create an account after it.
	var enabled string
	if err := scanSingleRow(ctx, txc, "SELECT value FROM settings WHERE key = $1 FOR SHARE",
		[]any{service.SettingKeyHostedSelfOnboardEnabled}, &enabled); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if enabled != "true" {
		return nil, service.ErrSelfOnboardDisabled
	}

	var userStatus string
	if err := scanSingleRow(ctx, txc,
		"SELECT status FROM users WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE",
		[]any{app.UserID}, &userStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrSelfOnboardInvalidState.WithMetadata(map[string]string{"detail": "applicant_unavailable"})
		}
		return nil, err
	}
	if userStatus != service.StatusActive {
		return nil, service.ErrSelfOnboardInvalidState.WithMetadata(map[string]string{"detail": "applicant_unavailable"})
	}

	var hosted bool
	if err := scanSingleRow(ctx, txc,
		"SELECT EXISTS (SELECT 1 FROM accounts WHERE platform = $1 AND upstream_account_uuid = $2::uuid AND deleted_at IS NULL)",
		[]any{app.Platform, app.UpstreamAccountUUID}, &hosted); err != nil {
		return nil, err
	}
	if hosted {
		return nil, service.ErrSelfOnboardAlreadyHosted
	}

	spec, err := p.Build(app)
	if err != nil {
		return nil, err
	}
	if spec == nil {
		return nil, service.ErrSelfOnboardInvalidState
	}

	groupID, err := ensureHostedOwnerGroup(ctx, txc, app.UserID, spec)
	if err != nil {
		return nil, err
	}

	var proxyID, createdProxy int64
	if p.UseSubmitted {
		if !app.HasProxy() {
			return nil, service.ErrSelfOnboardProxyRequired
		}
		create := txc.Proxy.Create().
			SetName(truncateForColumn(fmt.Sprintf("自助托管 #%d", app.ID), 100)).
			SetProtocol(app.ProxyProtocol).
			SetHost(app.ProxyHost).
			SetPort(app.ProxyPort).
			SetStatus(service.StatusActive)
		if app.ProxyUsername != "" && app.ProxyPassword != "" {
			create.SetUsername(app.ProxyUsername).SetPassword(app.ProxyPassword)
		}
		created, err := create.Save(ctx)
		if err != nil {
			return nil, err
		}
		proxyID, createdProxy = created.ID, created.ID
	} else {
		if p.ExistingProxyID <= 0 {
			return nil, service.ErrSelfOnboardProxyRequired
		}
		var status, host string
		if err := scanSingleRow(ctx, txc,
			"SELECT status, host FROM proxies WHERE id = $1 AND deleted_at IS NULL FOR SHARE",
			[]any{p.ExistingProxyID}, &status, &host); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, service.ErrSelfOnboardProxyRequired
			}
			return nil, err
		}
		if status != service.StatusActive || strings.TrimSpace(host) == "" {
			return nil, service.ErrSelfOnboardProxyRequired
		}
		proxyID = p.ExistingProxyID
	}

	account, err := txc.Account.Create().
		SetName(spec.Name).
		SetPlatform(service.PlatformAnthropic).
		SetType(service.AccountTypeSetupToken).
		SetCredentials(normalizeJSONMap(spec.Credentials)).
		SetExtra(normalizeJSONMap(spec.Extra)).
		SetProxyID(proxyID).
		SetConcurrency(spec.Concurrency).
		SetPriority(spec.Priority).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetAutoPauseOnExpired(true).
		SetUpstreamAccountUUID(app.UpstreamAccountUUID).
		SetSelfOnboarded(true).
		Save(ctx)
	if err != nil {
		if isUpstreamIdentityConflict(err) {
			return nil, service.ErrSelfOnboardAlreadyHosted
		}
		return nil, err
	}
	accountID := account.ID

	if _, err := txc.ExecContext(ctx,
		"INSERT INTO account_groups (account_id, group_id, priority, created_at) VALUES ($1, $2, 1, NOW())",
		accountID, groupID); err != nil {
		return nil, err
	}
	if _, err := txc.ExecContext(ctx,
		"INSERT INTO user_allowed_groups (user_id, group_id, created_at) VALUES ($1, $2, NOW()) ON CONFLICT (user_id, group_id) DO NOTHING",
		app.UserID, groupID); err != nil {
		return nil, err
	}
	ownerID := app.UserID
	if _, err := setAccountOwnerTx(ctx, txc, accountID, &ownerID, p.AdminUserID); err != nil {
		return nil, err
	}
	groupPayload := buildSchedulerGroupPayload([]int64{groupID})
	if err := enqueueSchedulerOutbox(ctx, txc, service.SchedulerOutboxEventAccountChanged, &accountID, nil, groupPayload); err != nil {
		return nil, err
	}
	if err := enqueueSchedulerOutbox(ctx, txc, service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, groupPayload); err != nil {
		return nil, err
	}

	var admin any
	if p.AdminUserID > 0 {
		admin = p.AdminUserID
	}
	res, err := txc.ExecContext(ctx, `
		UPDATE hosted_account_applications
		SET status = 'approved', account_id = $2, group_id = $3, reviewed_by = $4, reviewed_at = NOW(),
		    credentials = NULL, proxy_password = NULL, updated_at = NOW()
		WHERE id = $1 AND status = 'pending'`, app.ID, accountID, groupID, admin)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n != 1 {
		return nil, service.ErrSelfOnboardInvalidState
	}
	approved, err := r.getWith(ctx, txc, app.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	r.accounts.syncSchedulerAccountSnapshot(ctx, accountID)
	return &service.HostedApproveResult{
		Application:  approved,
		AccountID:    accountID,
		GroupID:      groupID,
		CreatedProxy: createdProxy,
	}, nil
}

// lockHostedApplication reads the application with its secrets under a row
// lock (FOR UPDATE: approval, cancel, reject and expiry all serialize here).
func lockHostedApplication(ctx context.Context, q sqlQueryer, id int64) (*service.HostedApplicationSecrets, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT a.status, a.user_id, a.display_name, a.platform, a.upstream_account_uuid::text,
		       COALESCE(a.upstream_org_uuid, ''), COALESCE(a.upstream_email, ''), a.credentials,
		       COALESCE(a.proxy_protocol, ''), COALESCE(a.proxy_host, ''), COALESCE(a.proxy_port, 0),
		       COALESCE(a.proxy_username, ''), COALESCE(a.proxy_password, ''),
		       a.account_id, a.group_id, a.created_at, a.updated_at
		FROM hosted_account_applications a
		WHERE a.id = $1
		FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrSelfOnboardNotFound
	}
	app := &service.HostedApplicationSecrets{}
	app.ID = id
	var (
		credentials        []byte
		accountID, groupID sql.NullInt64
	)
	if err := rows.Scan(&app.Status, &app.UserID, &app.DisplayName, &app.Platform, &app.UpstreamAccountUUID,
		&app.UpstreamOrgUUID, &app.UpstreamEmail, &credentials,
		&app.ProxyProtocol, &app.ProxyHost, &app.ProxyPort, &app.ProxyUsername, &app.ProxyPassword,
		&accountID, &groupID, &app.CreatedAt, &app.UpdatedAt); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	app.HasProxyAuth = app.ProxyUsername != ""
	if accountID.Valid {
		v := accountID.Int64
		app.AccountID = &v
	}
	if groupID.Valid {
		v := groupID.Int64
		app.GroupID = &v
	}
	if len(credentials) > 0 {
		if err := json.Unmarshal(credentials, &app.Credentials); err != nil {
			return nil, err
		}
	}
	return app, nil
}

// ensureHostedOwnerGroup returns the owner's exclusive hosted group, creating
// it on first approval. The applicant's user row is already locked, so two
// approvals for one owner cannot both create a group. A mapped group is
// reused only while it is still a live, active Anthropic hosted group that
// no other user is allowed to use and that holds no other owner's account;
// a soft-deleted mapped group is replaced.
func ensureHostedOwnerGroup(ctx context.Context, txc *dbent.Client, userID int64, spec *service.HostedApprovedAccountSpec) (int64, error) {
	var mapped sql.NullInt64
	if err := scanSingleRow(ctx, txc,
		"SELECT group_id FROM hosted_owner_groups WHERE user_id = $1 FOR UPDATE",
		[]any{userID}, &mapped); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if mapped.Valid {
		// FOR UPDATE (not NO KEY): it conflicts with the KEY SHARE lock that the
		// foreign-key check of a concurrent account_groups / user_allowed_groups
		// insert takes on this group, so an uncommitted grant to another user
		// is waited for and then seen by the "shared" check below.
		var (
			platform, status, subscriptionType string
			hostedUsage, exclusive, deleted    bool
		)
		err := scanSingleRow(ctx, txc, `
			SELECT platform, status, subscription_type, hosted_usage_enabled, is_exclusive, deleted_at IS NOT NULL
			FROM groups WHERE id = $1 FOR UPDATE`,
			[]any{mapped.Int64}, &platform, &status, &subscriptionType, &hostedUsage, &exclusive, &deleted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if err == nil && !deleted {
			if platform != service.PlatformAnthropic || status != service.StatusActive ||
				subscriptionType != service.SubscriptionTypeStandard || !hostedUsage || !exclusive {
				return 0, service.ErrSelfOnboardInvalidState.WithMetadata(map[string]string{"detail": "owner_group_changed"})
			}
			var foreign bool
			if err := scanSingleRow(ctx, txc, `
				SELECT EXISTS (
					SELECT 1 FROM account_groups ag JOIN accounts a ON a.id = ag.account_id
					WHERE ag.group_id = $1 AND a.deleted_at IS NULL AND a.owner_user_id IS DISTINCT FROM $2
				) OR EXISTS (
					SELECT 1 FROM user_allowed_groups WHERE group_id = $1 AND user_id <> $2
				)`, []any{mapped.Int64, userID}, &foreign); err != nil {
				return 0, err
			}
			if foreign {
				return 0, service.ErrSelfOnboardInvalidState.WithMetadata(map[string]string{"detail": "owner_group_shared"})
			}
			return mapped.Int64, nil
		}
	}

	// Create the group. The name is unique among live groups; fall back to
	// a suffixed name when an admin already uses the plain one.
	name := truncateForColumn(spec.GroupName, 100)
	var taken bool
	if err := scanSingleRow(ctx, txc,
		"SELECT EXISTS (SELECT 1 FROM groups WHERE name = $1 AND deleted_at IS NULL)",
		[]any{name}, &taken); err != nil {
		return 0, err
	}
	if taken {
		name = truncateForColumn(fmt.Sprintf("%s-%d", spec.GroupName, time.Now().UnixNano()), 100)
	}
	group := &service.Group{
		Name:               name,
		Description:        spec.GroupDescription,
		Platform:           service.PlatformAnthropic,
		RateMultiplier:     spec.FeeRate,
		IsExclusive:        true,
		ShowAccountQuota:   true,
		HostedUsageEnabled: true,
		Status:             service.StatusActive,
		SubscriptionType:   service.SubscriptionTypeStandard,
	}
	groups := newGroupRepositoryWithSQL(txc, txc)
	if err := groups.Create(ctx, group); err != nil {
		return 0, err
	}
	if _, err := txc.ExecContext(ctx, `
		INSERT INTO hosted_owner_groups (user_id, group_id) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET group_id = EXCLUDED.group_id, updated_at = NOW()`,
		userID, group.ID); err != nil {
		return 0, err
	}
	if err := enqueueSchedulerOutbox(ctx, txc, service.SchedulerOutboxEventGroupChanged, nil, &group.ID, nil); err != nil {
		return 0, err
	}
	return group.ID, nil
}

func truncateForColumn(v string, maxRunes int) string {
	r := []rune(v)
	if len(r) <= maxRunes {
		return v
	}
	return string(r[:maxRunes])
}

func isUniqueViolation(err error, constraint string) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505" && (constraint == "" || pqErr.Constraint == constraint)
	}
	return false
}
