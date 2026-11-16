package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// CryptoPayAdminDeposit is the full admin view of a deposit (raw status).
type CryptoPayAdminDeposit struct {
	ID            int64      `json:"id"`
	UserID        int64      `json:"user_id"`
	UserEmail     string     `json:"user_email"`
	Network       string     `json:"network"`
	Asset         string     `json:"asset"`
	AssetContract string     `json:"asset_contract"`
	Address       string     `json:"address"`
	TokenAccount  string     `json:"token_account"`
	FromAddress   string     `json:"from_address"`
	Units         string     `json:"units"`
	Amount        string     `json:"amount"`
	TxHash        string     `json:"tx_hash"`
	EventKey      string     `json:"event_key"`
	BlockNumber   int64      `json:"block_number"`
	BlockHash     string     `json:"block_hash"`
	BlockTime     time.Time  `json:"block_time"`
	Status        string     `json:"status"`
	HoldReason    string     `json:"hold_reason"`
	ObservedAt    time.Time  `json:"observed_at"`
	FinalizedAt   *time.Time `json:"finalized_at"`
	CreditedAt    *time.Time `json:"credited_at"`
	OrphanedAt    *time.Time `json:"orphaned_at"`
}

// CryptoPayAdminDepositFilter filters the admin deposit list.
type CryptoPayAdminDepositFilter struct {
	Status  string
	Network string
	UserID  int64
}

var cryptoPayDepositStatuses = map[string]bool{
	cryptoDepositObserved: true, cryptoDepositFinalized: true, cryptoDepositCredited: true,
	cryptoDepositHeld: true, cryptoDepositOrphaned: true,
}

func nullTimePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// AdminListDeposits lists deposits newest first, optionally filtered.
func (s *CryptoPayService) AdminListDeposits(ctx context.Context, f CryptoPayAdminDepositFilter, page, pageSize int) ([]CryptoPayAdminDeposit, int64, error) {
	var conds []string
	var args []any
	if f.Status = strings.TrimSpace(f.Status); f.Status != "" {
		if !cryptoPayDepositStatuses[f.Status] {
			return nil, 0, infraerrors.BadRequest("CRYPTO_PAY_STATUS_INVALID", "invalid status filter")
		}
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("d.status = $%d", len(args)))
	}
	if f.Network = strings.TrimSpace(f.Network); f.Network != "" {
		args = append(args, f.Network)
		conds = append(conds, fmt.Sprintf("d.network = $%d", len(args)))
	}
	if f.UserID > 0 {
		args = append(args, f.UserID)
		conds = append(conds, fmt.Sprintf("d.user_id = $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM crypto_pay_deposits d`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count deposits: %w", err)
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.user_id, COALESCE(u.email, ''), d.network, d.asset_symbol, d.asset_contract, d.address, d.token_account,
		       d.from_address, d.units::text, d.amount, d.tx_hash, d.event_key, d.block_number, d.block_hash, d.block_time,
		       d.status, d.hold_reason, d.observed_at, d.finalized_at, d.credited_at, d.orphaned_at
		FROM crypto_pay_deposits d LEFT JOIN users u ON u.id = d.user_id`+where+
		fmt.Sprintf(` ORDER BY d.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list deposits: %w", err)
	}
	out := []CryptoPayAdminDeposit{}
	for rows.Next() {
		var d CryptoPayAdminDeposit
		var amount decimal.Decimal
		var fin, cred, orph sql.NullTime
		if err := rows.Scan(&d.ID, &d.UserID, &d.UserEmail, &d.Network, &d.Asset, &d.AssetContract, &d.Address, &d.TokenAccount,
			&d.FromAddress, &d.Units, &amount, &d.TxHash, &d.EventKey, &d.BlockNumber, &d.BlockHash, &d.BlockTime,
			&d.Status, &d.HoldReason, &d.ObservedAt, &fin, &cred, &orph); err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		d.Amount = formatCryptoAmount(amount)
		d.FinalizedAt, d.CreditedAt, d.OrphanedAt = nullTimePtr(fin), nullTimePtr(cred), nullTimePtr(orph)
		out = append(out, d)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// CryptoPayReconciliationRow sums deposits per network/asset/status.
type CryptoPayReconciliationRow struct {
	Network string `json:"network"`
	Asset   string `json:"asset"`
	Status  string `json:"status"`
	Count   int64  `json:"count"`
	Units   string `json:"units"`
	Amount  string `json:"amount"`
}

// CryptoPayCursorStatus is one scan watermark.
type CryptoPayCursorStatus struct {
	ScopeKey      string     `json:"scope_key"`
	Network       string     `json:"network"`
	Position      string     `json:"position"`
	LastScannedAt *time.Time `json:"last_scanned_at"`
	LastError     string     `json:"last_error"`
}

// CryptoPayReconciliation compares the ledger with credited deposits.
type CryptoPayReconciliation struct {
	Deposits              []CryptoPayReconciliationRow `json:"deposits"`
	CreditsTotal          string                       `json:"credits_total"`
	CreditsCount          int64                        `json:"credits_count"`
	CreditedDepositsTotal string                       `json:"credited_deposits_total"`
	CreditedDepositsCount int64                        `json:"credited_deposits_count"`
	// Credits whose deposit is not "credited", or credited deposits without a
	// credit row, or amount mismatches. All must be 0.
	MismatchCount int64                   `json:"mismatch_count"`
	Consistent    bool                    `json:"consistent"`
	Keysets       []CryptoPayKeysetInfo   `json:"keysets"`
	CursorErrors  []CryptoPayCursorStatus `json:"cursor_errors"`
	StaleCursors  []CryptoPayCursorStatus `json:"stale_cursors"`
}

// Reconciliation returns totals, ledger consistency, pool remaining and
// cursors that failed or have not advanced for more than a day.
func (s *CryptoPayService) Reconciliation(ctx context.Context) (*CryptoPayReconciliation, error) {
	out := &CryptoPayReconciliation{Deposits: []CryptoPayReconciliationRow{}, CursorErrors: []CryptoPayCursorStatus{}, StaleCursors: []CryptoPayCursorStatus{}}
	rows, err := s.db.QueryContext(ctx, `
		SELECT network, asset_symbol, status, COUNT(*), SUM(units)::text, SUM(amount)
		FROM crypto_pay_deposits GROUP BY network, asset_symbol, status ORDER BY network, asset_symbol, status`)
	if err != nil {
		return nil, fmt.Errorf("reconcile deposits: %w", err)
	}
	for rows.Next() {
		var r CryptoPayReconciliationRow
		var amount decimal.Decimal
		if err := rows.Scan(&r.Network, &r.Asset, &r.Status, &r.Count, &r.Units, &amount); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.Amount = formatCryptoAmount(amount)
		out.Deposits = append(out.Deposits, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var creditsTotal, depositsTotal decimal.Decimal
	if err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM crypto_pay_credits), (SELECT COALESCE(SUM(amount), 0) FROM crypto_pay_credits),
		       (SELECT COUNT(*) FROM crypto_pay_deposits WHERE status = 'credited'),
		       (SELECT COALESCE(SUM(amount), 0) FROM crypto_pay_deposits WHERE status = 'credited'),
		       (SELECT COUNT(*) FROM crypto_pay_credits c LEFT JOIN crypto_pay_deposits d ON d.id = c.deposit_id
		         WHERE d.id IS NULL OR d.status <> 'credited' OR d.amount <> c.amount OR d.user_id <> c.user_id)
		     + (SELECT COUNT(*) FROM crypto_pay_deposits d WHERE d.status = 'credited'
		         AND NOT EXISTS (SELECT 1 FROM crypto_pay_credits c WHERE c.deposit_id = d.id))`).
		Scan(&out.CreditsCount, &creditsTotal, &out.CreditedDepositsCount, &depositsTotal, &out.MismatchCount); err != nil {
		return nil, fmt.Errorf("reconcile credits: %w", err)
	}
	out.CreditsTotal, out.CreditedDepositsTotal = formatCryptoAmount(creditsTotal), formatCryptoAmount(depositsTotal)
	out.Consistent = out.MismatchCount == 0 && creditsTotal.Equal(depositsTotal)
	if out.Keysets, err = s.ListKeysets(ctx); err != nil {
		return nil, err
	}
	if out.CursorErrors, err = s.queryCursors(ctx, `WHERE last_error <> '' ORDER BY updated_at DESC LIMIT 50`); err != nil {
		return nil, err
	}
	if out.StaleCursors, err = s.queryCursors(ctx, `WHERE COALESCE(last_scanned_at, updated_at) < NOW() - INTERVAL '25 hours' ORDER BY COALESCE(last_scanned_at, updated_at) LIMIT 50`); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *CryptoPayService) queryCursors(ctx context.Context, where string) ([]CryptoPayCursorStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT scope_key, network, position, last_scanned_at, last_error FROM crypto_pay_scan_cursors `+where)
	if err != nil {
		return nil, fmt.Errorf("list cursors: %w", err)
	}
	out := []CryptoPayCursorStatus{}
	for rows.Next() {
		var c CryptoPayCursorStatus
		var last sql.NullTime
		if err := rows.Scan(&c.ScopeKey, &c.Network, &c.Position, &last, &c.LastError); err != nil {
			_ = rows.Close()
			return nil, err
		}
		c.LastScannedAt = nullTimePtr(last)
		out = append(out, c)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}
