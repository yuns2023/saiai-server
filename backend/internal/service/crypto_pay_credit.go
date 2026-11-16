package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

// Hold reasons stored in crypto_pay_deposits.hold_reason.
const (
	CryptoPayHoldAmountOverThreshold = "amount_over_threshold"
	CryptoPayHoldUserInactive        = "user_inactive"
	CryptoPayHoldAssetNotWhitelisted = "asset_not_whitelisted"
)

// Credit modes.
const (
	cryptoCreditAuto    = "auto"    // worker / ingestion; hold rules apply
	cryptoCreditRescan  = "rescan"  // admin re-scan of a tx; hold rules apply
	cryptoCreditRelease = "release" // admin release of a held/finalized deposit; hold rules skipped
)

const (
	cryptoPayCreditBacklogBatch = 200
	cryptoPayBacklogRowTimeout  = 15 * time.Second
)

var cryptoPayEVMEventKeyRe = regexp.MustCompile(`^log:[0-9]{1,10}$`)

// CryptoPayIngestResult summarizes one ingestion batch.
type CryptoPayIngestResult struct {
	Inserted   int
	Finalized  int
	Revived    int
	Unchanged  int
	Ignored    int
	Conflicts  int
	DepositIDs []int64 // deposits touched by this batch (any status)
	// CreditableIDs are deposits in status finalized after this batch.
	CreditableIDs []int64
}

// cryptoPayNormalizedObs is a validated observation with whitelisted asset values.
type cryptoPayNormalizedObs struct {
	network      chain.Network
	asset        chain.Asset
	txHash       string
	eventKey     string
	toAddress    string
	tokenAccount string
	fromAddress  string
	units        decimal.Decimal
	amount       decimal.Decimal
	blockNumber  int64
	blockHash    string
	blockTime    time.Time
	finalized    bool
}

// cryptoPayUnitsToAmount converts raw units to an exact decimal amount.
func cryptoPayUnitsToAmount(units decimal.Decimal, decimals int32) decimal.Decimal {
	return units.Shift(-decimals)
}

// errCryptoPayObsIgnorable marks observations that are not ours to credit
// (unconfigured network, non-whitelisted asset). Any other normalization
// error on a whitelisted asset is a hard error: the batch fails and the scan
// cursor does not advance, so a real transfer is never skipped silently.
var errCryptoPayObsIgnorable = errors.New("observation not creditable here")

func (s *CryptoPayService) normalizeObservation(o chain.Observation) (*cryptoPayNormalizedObs, error) {
	n, ok := s.byNetwork[o.Network]
	if !ok {
		return nil, fmt.Errorf("%w: network %q not configured", errCryptoPayObsIgnorable, o.Network)
	}
	asset, ok := n.assetByContract(o.Asset.Contract, o.Asset.TokenProgram)
	if !ok {
		return nil, fmt.Errorf("%w: asset %q not whitelisted", errCryptoPayObsIgnorable, o.Asset.Contract)
	}
	if o.Asset.Network != o.Network {
		return nil, fmt.Errorf("asset network mismatch")
	}
	if o.Asset.Decimals != asset.Decimals {
		return nil, fmt.Errorf("asset decimals mismatch")
	}
	if o.Units == nil || o.Units.Sign() <= 0 {
		return nil, fmt.Errorf("non-positive units")
	}
	if o.BlockNumber > uint64(1<<62) {
		return nil, fmt.Errorf("block number out of range")
	}
	units := decimal.NewFromBigInt(o.Units, 0)
	amount := cryptoPayUnitsToAmount(units, asset.Decimals)
	if amount.GreaterThanOrEqual(cryptoPayAmountCeiling) {
		return nil, fmt.Errorf("amount out of range")
	}
	out := &cryptoPayNormalizedObs{
		network: o.Network, asset: asset, units: units, amount: amount, blockNumber: int64(o.BlockNumber),
		blockHash: strings.TrimSpace(o.BlockHash), blockTime: o.BlockTime.UTC(), finalized: o.Finalized,
		fromAddress: strings.TrimSpace(o.FromAddress),
	}
	switch n.Family {
	case chain.FamilyEVM:
		out.txHash = strings.ToLower(strings.TrimSpace(o.TxHash))
		out.eventKey = strings.TrimSpace(o.EventKey)
		out.toAddress = strings.ToLower(strings.TrimSpace(o.ToAddress))
		out.fromAddress = strings.ToLower(out.fromAddress)
		if !cryptoPayEVMTxHashRe.MatchString(out.txHash) || !cryptoPayEVMEventKeyRe.MatchString(out.eventKey) ||
			!cryptoPayEVMAddressRe.MatchString(out.toAddress) {
			return nil, fmt.Errorf("malformed EVM observation")
		}
	case chain.FamilySolana:
		out.txHash = strings.TrimSpace(o.TxHash)
		out.tokenAccount = strings.TrimSpace(o.TokenAccount)
		out.eventKey = strings.TrimSpace(o.EventKey)
		out.toAddress = strings.TrimSpace(o.ToAddress)
		if !cryptoPaySolSigRe.MatchString(out.txHash) || !cryptoPayBase58Re.MatchString(out.toAddress) ||
			!cryptoPayBase58Re.MatchString(out.tokenAccount) || out.eventKey != "acct:"+out.tokenAccount {
			return nil, fmt.Errorf("malformed Solana observation")
		}
	default:
		return nil, fmt.Errorf("unsupported family")
	}
	if len(out.blockHash) > 128 || len(out.fromAddress) > 64 {
		return nil, fmt.Errorf("oversized field")
	}
	if out.blockTime.IsZero() || out.blockTime.Year() < 2015 {
		out.blockTime = s.now().UTC()
	}
	return out, nil
}

// IngestObservations upserts observations idempotently and, when auto credit
// is on, credits the finalized ones. A returned error means the batch was not
// fully persisted (the caller must not advance its cursor); crediting
// failures are logged only, because the backlog sweep retries them.
func (s *CryptoPayService) IngestObservations(ctx context.Context, observations []chain.Observation) (*CryptoPayIngestResult, error) {
	if !s.Enabled() {
		return nil, infraerrors.ServiceUnavailable(CryptoPayReasonDisabled, "crypto top-up is disabled on this deployment")
	}
	res, err := s.ingest(ctx, observations)
	if err != nil {
		return res, err
	}
	st, err := s.GetSettings(ctx)
	if err != nil {
		cryptoPayLogf("settings unavailable after ingest, credit deferred: %v", err)
		return res, nil
	}
	if st.AutoCreditEnabled {
		for _, id := range res.CreditableIDs {
			if _, credErr := s.creditDeposit(ctx, id, cryptoPayOperatorSystem, cryptoCreditAuto, st); credErr != nil {
				cryptoPayLogf("auto credit deposit %d failed (will retry): %v", id, credErr)
			}
		}
	}
	return res, nil
}

func (s *CryptoPayService) ingest(ctx context.Context, observations []chain.Observation) (*CryptoPayIngestResult, error) {
	res := &CryptoPayIngestResult{}
	for _, o := range observations {
		norm, err := s.normalizeObservation(o)
		if err != nil {
			if !errors.Is(err, errCryptoPayObsIgnorable) {
				cryptoPayLogf("ALERT invalid observation of a whitelisted asset network=%s tx=%s event=%s: %v", o.Network, o.TxHash, o.EventKey, err)
				return res, fmt.Errorf("invalid observation tx=%s event=%s: %w", o.TxHash, o.EventKey, err)
			}
			res.Ignored++
			cryptoPayLogf("ignore observation network=%s tx=%s event=%s: %v", o.Network, o.TxHash, o.EventKey, err)
			continue
		}
		var addressID, userID int64
		err = s.db.QueryRowContext(ctx, `SELECT id, user_id FROM crypto_pay_addresses WHERE network = $1 AND address = $2`,
			string(norm.network), norm.toAddress).Scan(&addressID, &userID)
		if errors.Is(err, sql.ErrNoRows) {
			res.Ignored++
			cryptoPayLogf("ignore observation to unknown address network=%s tx=%s to=%s", norm.network, norm.txHash, norm.toAddress)
			continue
		}
		if err != nil {
			return res, fmt.Errorf("lookup deposit address: %w", err)
		}
		id, outcome, status, err := s.upsertDeposit(ctx, norm, addressID, userID)
		if err != nil {
			return res, err
		}
		switch outcome {
		case "inserted":
			res.Inserted++
		case "finalized":
			res.Finalized++
		case "revived":
			res.Revived++
		case "conflict":
			res.Conflicts++
		default:
			res.Unchanged++
		}
		if id > 0 {
			res.DepositIDs = append(res.DepositIDs, id)
			if status == cryptoDepositFinalized {
				res.CreditableIDs = append(res.CreditableIDs, id)
			}
		}
	}
	return res, nil
}

// upsertDeposit applies one observation under a row lock. Transitions:
// new → observed|finalized; observed|orphaned + finalized obs → finalized;
// orphaned + unfinalized obs → observed; finalized/credited/held never move
// back and never change content.
func (s *CryptoPayService) upsertDeposit(ctx context.Context, o *cryptoPayNormalizedObs, addressID, userID int64) (id int64, outcome, status string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", "", err
	}
	defer func() { _ = tx.Rollback() }()
	status = cryptoDepositObserved
	if o.finalized {
		status = cryptoDepositFinalized
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO crypto_pay_deposits (network, tx_hash, event_key, address_id, user_id, address, asset_symbol, asset_contract,
			token_account, from_address, units, decimals, amount, block_number, block_hash, block_time, status, finalized_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::numeric, $12, $13::numeric, $14, $15, $16, $17,
			CASE WHEN $18 THEN NOW() END)
		ON CONFLICT (network, tx_hash, event_key) DO NOTHING
		RETURNING id`,
		string(o.network), o.txHash, o.eventKey, addressID, userID, o.toAddress, o.asset.Symbol, o.asset.Contract,
		o.tokenAccount, o.fromAddress, o.units.String(), o.asset.Decimals, o.amount.String(), o.blockNumber, o.blockHash,
		o.blockTime, status, o.finalized).Scan(&id)
	if err == nil {
		if err := writeCryptoPayAudit(ctx, tx, "DEPOSIT_"+strings.ToUpper(status), &id, &userID, nil, cryptoPayOperatorSystem,
			map[string]any{"network": o.network, "tx_hash": o.txHash, "event_key": o.eventKey, "units": o.units.String(), "asset": o.asset.Symbol}); err != nil {
			return 0, "", "", err
		}
		if err := tx.Commit(); err != nil {
			return 0, "", "", err
		}
		return id, "inserted", status, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", "", fmt.Errorf("insert deposit: %w", err)
	}
	var cur struct {
		status, contract, blockHash string
		addressID                   int64
		units                       decimal.Decimal
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT id, status, asset_contract, block_hash, address_id, units
		FROM crypto_pay_deposits WHERE network = $1 AND tx_hash = $2 AND event_key = $3 FOR UPDATE`,
		string(o.network), o.txHash, o.eventKey).Scan(&id, &cur.status, &cur.contract, &cur.blockHash, &cur.addressID, &cur.units); err != nil {
		return 0, "", "", fmt.Errorf("lock deposit: %w", err)
	}
	// o.asset.Contract comes from the whitelist (EVM lowercase), and stored
	// rows were written from it, so an exact compare is right for both families.
	sameContent := cur.units.Equal(o.units) && cur.addressID == addressID && cur.contract == o.asset.Contract
	settled := cur.status == cryptoDepositFinalized || cur.status == cryptoDepositCredited || cur.status == cryptoDepositHeld
	if settled {
		if !sameContent || (o.finalized && o.blockHash != "" && cur.blockHash != "" && o.blockHash != cur.blockHash) {
			cryptoPayLogf("ALERT deposit %d (%s) observation disagrees with settled row; ignored", id, cur.status)
			if err := writeCryptoPayAudit(ctx, tx, "DEPOSIT_CONFLICT", &id, &userID, nil, cryptoPayOperatorSystem,
				map[string]any{"status": cur.status, "obs_units": o.units.String(), "obs_block_hash": o.blockHash, "obs_finalized": o.finalized}); err != nil {
				return 0, "", "", err
			}
			return id, "conflict", cur.status, tx.Commit()
		}
		return id, "unchanged", cur.status, nil
	}
	// observed or orphaned: the newer observation wins (a reorg may move the
	// event to another block); a finalized observation finalizes it.
	next := cur.status
	outcome = "unchanged"
	switch {
	case o.finalized:
		next, outcome = cryptoDepositFinalized, "finalized"
	case cur.status == cryptoDepositOrphaned:
		next, outcome = cryptoDepositObserved, "revived"
	}
	if !sameContent {
		cryptoPayLogf("deposit %d content changed before finality (old units=%s new units=%s)", id, cur.units, o.units)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE crypto_pay_deposits SET status = $2, address_id = $3, user_id = $4, address = $5, asset_symbol = $6,
			asset_contract = $7, token_account = $8, from_address = $9, units = $10::numeric, decimals = $11, amount = $12::numeric,
			block_number = $13, block_hash = $14, block_time = $15,
			finalized_at = CASE WHEN $16 THEN NOW() ELSE finalized_at END,
			orphaned_at = NULL,
			updated_at = NOW()
		WHERE id = $1`,
		id, next, addressID, userID, o.toAddress, o.asset.Symbol, o.asset.Contract, o.tokenAccount, o.fromAddress,
		o.units.String(), o.asset.Decimals, o.amount.String(), o.blockNumber, o.blockHash, o.blockTime,
		next == cryptoDepositFinalized && cur.status != cryptoDepositFinalized); err != nil {
		return 0, "", "", fmt.Errorf("update deposit: %w", err)
	}
	if outcome != "unchanged" {
		if err := writeCryptoPayAudit(ctx, tx, "DEPOSIT_"+strings.ToUpper(next), &id, &userID, nil, cryptoPayOperatorSystem,
			map[string]any{"from": cur.status, "block_number": o.blockNumber, "block_hash": o.blockHash}); err != nil {
			return 0, "", "", err
		}
	}
	return id, outcome, next, tx.Commit()
}

// OrphanStaleObservations marks observed (never finalized) deposits as
// orphaned once they are older than window and, for EVM, the address's
// finalized cursor has moved past their block without finalizing them. An
// orphaned row returns to observed/finalized if it is seen again, so a
// premature orphan only affects display, never crediting.
func (s *CryptoPayService) OrphanStaleObservations(ctx context.Context, network chain.Network, window time.Duration) (int64, error) {
	n, ok := s.byNetwork[network]
	if !ok {
		return 0, nil
	}
	cutoff := s.now().Add(-window)
	query := `
		UPDATE crypto_pay_deposits d SET status = 'orphaned', orphaned_at = NOW(), updated_at = NOW()
		WHERE d.network = $1 AND d.status = 'observed' AND d.updated_at < $2`
	if n.Family == chain.FamilyEVM {
		query += ` AND EXISTS (
			SELECT 1 FROM crypto_pay_scan_cursors c
			WHERE c.scope_key = 'evm:' || d.network || ':fin:' || d.address
			  AND c.position ~ '^[0-9]+$' AND c.position::numeric >= d.block_number)`
	}
	// The status change and its audit rows are one statement (one transaction).
	query = `WITH orphaned AS (` + query + ` RETURNING d.id, d.user_id, d.tx_hash, d.event_key, d.block_number)
		INSERT INTO crypto_pay_audit_logs (action, deposit_id, user_id, operator, detail)
		SELECT 'DEPOSIT_ORPHANED', id, user_id, 'system',
		       json_build_object('tx_hash', tx_hash, 'event_key', event_key, 'block_number', block_number, 'window', $3::text)::text
		FROM orphaned`
	res, err := s.db.ExecContext(ctx, query, string(network), cutoff, window.String())
	if err != nil {
		return 0, fmt.Errorf("orphan stale observations: %w", err)
	}
	count, _ := res.RowsAffected()
	if count > 0 {
		cryptoPayLogf("marked %d unfinalized %s observations orphaned", count, network)
	}
	return count, nil
}

// CryptoPayCreditOutcome is the result of one credit attempt.
type CryptoPayCreditOutcome struct {
	DepositID  int64  `json:"deposit_id"`
	Status     string `json:"status"`
	HoldReason string `json:"hold_reason"`
	Credited   bool   `json:"credited"` // true only when this call moved money
}

// creditDeposit is the single crediting path: lock deposit → hold checks →
// insert ledger row (unique deposit_id) → balance += amount (numeric) →
// mark credited → audit → commit → invalidate caches.
func (s *CryptoPayService) creditDeposit(ctx context.Context, depositID int64, operator, mode string, st *CryptoPaySettings) (*CryptoPayCreditOutcome, error) {
	out := &CryptoPayCreditOutcome{DepositID: depositID}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var d struct {
		status, network, contract, holdReason string
		userID                                int64
		amount                                decimal.Decimal
	}
	err = tx.QueryRowContext(ctx, `
		SELECT d.status, d.network, d.asset_contract, d.hold_reason, d.user_id, d.amount
		FROM crypto_pay_deposits d WHERE d.id = $1 FOR UPDATE`, depositID).
		Scan(&d.status, &d.network, &d.contract, &d.holdReason, &d.userID, &d.amount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, infraerrors.NotFound("CRYPTO_PAY_DEPOSIT_NOT_FOUND", "deposit not found")
	}
	if err != nil {
		return nil, fmt.Errorf("lock deposit: %w", err)
	}
	out.Status, out.HoldReason = d.status, d.holdReason
	switch {
	case d.status == cryptoDepositCredited:
		return out, nil // idempotent: already credited
	case d.status == cryptoDepositFinalized:
	case d.status == cryptoDepositHeld && mode == cryptoCreditRelease:
	case d.status == cryptoDepositHeld:
		return out, nil // held rows wait for an admin release
	default:
		return nil, infraerrors.Conflict("CRYPTO_PAY_DEPOSIT_NOT_CREDITABLE", "deposit is not finalized")
	}
	var userStatus string
	var userDeleted bool
	err = tx.QueryRowContext(ctx, `SELECT status, deleted_at IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, d.userID).
		Scan(&userStatus, &userDeleted)
	if errors.Is(err, sql.ErrNoRows) {
		userDeleted = true
	} else if err != nil {
		return nil, fmt.Errorf("lock deposit user: %w", err)
	}
	if mode == cryptoCreditRelease {
		if userDeleted {
			return nil, infraerrors.Conflict("CRYPTO_PAY_USER_DELETED", "the deposit's user is deleted")
		}
	} else if reason := s.holdReason(d.network, d.contract, d.amount, userStatus, userDeleted, st); reason != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE crypto_pay_deposits SET status = 'held', hold_reason = $2, updated_at = NOW() WHERE id = $1`,
			depositID, reason); err != nil {
			return nil, fmt.Errorf("hold deposit: %w", err)
		}
		if err := writeCryptoPayAudit(ctx, tx, "DEPOSIT_HELD", &depositID, &d.userID, nil, operator,
			map[string]any{"reason": reason, "amount": d.amount.String(), "mode": mode}); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		cryptoPayLogf("deposit %d held: %s", depositID, reason)
		out.Status, out.HoldReason = cryptoDepositHeld, reason
		return out, nil
	}
	var creditID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO crypto_pay_credits (deposit_id, user_id, amount, operator) VALUES ($1, $2, $3::numeric, $4)
		ON CONFLICT (deposit_id) DO NOTHING RETURNING id`, depositID, d.userID, d.amount.String(), operator).Scan(&creditID)
	if errors.Is(err, sql.ErrNoRows) {
		// Ledger row exists but the deposit was not marked: repair the status only.
		cryptoPayLogf("ALERT deposit %d already has a credit row; marking credited without balance change", depositID)
		if _, err := tx.ExecContext(ctx, `UPDATE crypto_pay_deposits SET status = 'credited', credited_at = COALESCE(credited_at, NOW()), updated_at = NOW() WHERE id = $1`, depositID); err != nil {
			return nil, err
		}
		if err := writeCryptoPayAudit(ctx, tx, "DEPOSIT_STATUS_REPAIRED", &depositID, &d.userID, nil, operator,
			map[string]any{"from": d.status, "mode": mode, "reason": "credit row already exists"}); err != nil {
			return nil, err
		}
		out.Status = cryptoDepositCredited
		return out, tx.Commit()
	}
	if err != nil {
		return nil, fmt.Errorf("insert credit: %w", err)
	}
	var balanceAfter string
	err = tx.QueryRowContext(ctx, `
		UPDATE users SET balance = balance + $1::numeric, updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL RETURNING balance::text`, d.amount.String(), d.userID).Scan(&balanceAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, infraerrors.Conflict("CRYPTO_PAY_USER_DELETED", "the deposit's user is deleted")
	}
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "22003" {
			return nil, ErrBalanceOutOfRange
		}
		return nil, fmt.Errorf("add balance: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE crypto_pay_credits SET balance_after = $2::numeric WHERE id = $1`, creditID, balanceAfter); err != nil {
		return nil, fmt.Errorf("record balance after: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE crypto_pay_deposits SET status = 'credited', credited_at = NOW(), updated_at = NOW() WHERE id = $1`, depositID); err != nil {
		return nil, fmt.Errorf("mark deposit credited: %w", err)
	}
	if err := writeCryptoPayAudit(ctx, tx, "DEPOSIT_CREDITED", &depositID, &d.userID, nil, operator,
		map[string]any{"amount": d.amount.String(), "balance_after": balanceAfter, "mode": mode, "from": d.status, "hold_reason": d.holdReason}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit credit: %w", err)
	}
	s.invalidateBalanceCaches(ctx, d.userID)
	out.Status, out.Credited = cryptoDepositCredited, true
	return out, nil
}

// holdReason returns why a deposit must be held instead of credited:
// the asset is no longer whitelisted, the user is deleted/not active, or
// amount >= hold_threshold_amount (inclusive). "" = credit it.
func (s *CryptoPayService) holdReason(network, contract string, amount decimal.Decimal, userStatus string, userDeleted bool, st *CryptoPaySettings) string {
	n, ok := s.byNetwork[chain.Network(network)]
	if !ok {
		return CryptoPayHoldAssetNotWhitelisted
	}
	if _, ok := n.assetByContractAnyProgram(contract); !ok {
		return CryptoPayHoldAssetNotWhitelisted
	}
	if userDeleted || userStatus != StatusActive {
		return CryptoPayHoldUserInactive
	}
	if amount.GreaterThanOrEqual(st.HoldThresholdAmount) {
		return CryptoPayHoldAmountOverThreshold
	}
	return ""
}

func (n *cryptoPayNetwork) assetByContractAnyProgram(contract string) (chain.Asset, bool) {
	for _, a := range n.Assets {
		if a.Contract == contract { // EVM contracts are stored lowercase; Solana mints are case-sensitive
			return a, true
		}
	}
	return chain.Asset{}, false
}

func (s *CryptoPayService) invalidateBalanceCaches(ctx context.Context, userID int64) {
	if s.authCache != nil {
		s.authCache.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.balanceCache == nil {
		return
	}
	go func() {
		cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.balanceCache.InvalidateUserBalance(cacheCtx, userID); err != nil {
			cryptoPayLogf("invalidate balance cache user=%d failed: %v", userID, err)
		}
	}()
}

// CreditFinalizedBacklog credits finalized deposits left behind by a crash or
// by a paused auto credit. No-op unless auto_credit_enabled.
func (s *CryptoPayService) CreditFinalizedBacklog(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	st, err := s.GetSettings(ctx)
	if err != nil || !st.AutoCreditEnabled {
		return 0, err
	}
	// Oldest-touched first: a row that keeps failing is touched below and
	// rotates to the back, so it cannot monopolize the batch.
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM crypto_pay_deposits WHERE status = 'finalized' ORDER BY updated_at, id LIMIT $1`, cryptoPayCreditBacklogBatch)
	if err != nil {
		return 0, fmt.Errorf("list finalized deposits: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	credited := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		// Per-row budget: one row stuck on a lock cannot eat the whole pass.
		rowCtx, cancel := context.WithTimeout(ctx, cryptoPayBacklogRowTimeout)
		outcome, err := s.creditDeposit(rowCtx, id, cryptoPayOperatorSystem, cryptoCreditAuto, st)
		cancel()
		if err != nil {
			cryptoPayLogf("backlog credit deposit %d failed: %v", id, err)
			touchCtx, touchCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if _, touchErr := s.db.ExecContext(touchCtx, `UPDATE crypto_pay_deposits SET updated_at = NOW()
				WHERE id = (SELECT id FROM crypto_pay_deposits WHERE id = $1 AND status = 'finalized' FOR UPDATE SKIP LOCKED)`, id); touchErr != nil {
				cryptoPayLogf("backlog rotate deposit %d failed: %v", id, touchErr)
			}
			touchCancel()
			continue
		}
		if outcome.Credited {
			credited++
		}
	}
	return credited, nil
}

// ReleaseDeposit credits a held (or finalized, e.g. while auto credit is
// paused) deposit through the same crediting transaction, skipping hold rules.
func (s *CryptoPayService) ReleaseDeposit(ctx context.Context, depositID int64, operator string) (*CryptoPayCreditOutcome, error) {
	if !s.Enabled() {
		// Deployments without crypto pay (saiai.top, abapi) never move money.
		return nil, infraerrors.ServiceUnavailable(CryptoPayReasonDisabled, "crypto top-up is disabled on this deployment")
	}
	st, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	return s.creditDeposit(ctx, depositID, operator, cryptoCreditRelease, st)
}

// CryptoPayRescanResult is the outcome of an admin re-scan of one transaction.
type CryptoPayRescanResult struct {
	Observations int                      `json:"observations"`
	Ignored      int                      `json:"ignored"`
	Results      []CryptoPayCreditOutcome `json:"results"`
}

// RescanTransaction re-reads one finalized transaction from the chain and runs
// its inflows to known addresses through the same ingestion and crediting
// path (hold rules apply; amounts always come from the chain). On Solana the
// worker only walks each user's USDC/USDT associated token accounts; inflows
// to any other token account owned by a deposit address (positive net balance
// change of that account in the tx) are picked up only through this path, and
// are accepted as long as the asset (mint + token program) is whitelisted.
func (s *CryptoPayService) RescanTransaction(ctx context.Context, networkName, txHash, operator string) (*CryptoPayRescanResult, error) {
	if !s.Enabled() {
		return nil, infraerrors.ServiceUnavailable(CryptoPayReasonDisabled, "crypto top-up is disabled on this deployment")
	}
	n, ok := s.network(networkName)
	if !ok {
		return nil, infraerrors.BadRequest(CryptoPayReasonNetworkUnsupported, "unsupported network")
	}
	txHash = strings.TrimSpace(txHash)
	if n.Family == chain.FamilyEVM {
		txHash = strings.ToLower(txHash)
		if !cryptoPayEVMTxHashRe.MatchString(txHash) {
			return nil, infraerrors.BadRequest("CRYPTO_PAY_TX_INVALID", "invalid transaction hash")
		}
	} else if !cryptoPaySolSigRe.MatchString(txHash) {
		return nil, infraerrors.BadRequest("CRYPTO_PAY_TX_INVALID", "invalid transaction signature")
	}
	addresses, err := s.networkAddresses(ctx, n.Network)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return &CryptoPayRescanResult{Results: []CryptoPayCreditOutcome{}}, nil
	}
	var obs []chain.Observation
	switch n.Family {
	case chain.FamilyEVM:
		sc := s.evmScanner()
		if sc == nil {
			return nil, infraerrors.ServiceUnavailable("CRYPTO_PAY_SCANNER_UNAVAILABLE", "chain scanner is not available")
		}
		obs, err = sc.ObservationsForTx(ctx, txHash, addresses, true)
	case chain.FamilySolana:
		sc := s.solanaScanner()
		if sc == nil {
			return nil, infraerrors.ServiceUnavailable("CRYPTO_PAY_SCANNER_UNAVAILABLE", "chain scanner is not available")
		}
		obs, err = sc.ObservationsForTx(ctx, txHash, addresses, "finalized")
	}
	if err != nil {
		if s.deps.IsRetryLater != nil && s.deps.IsRetryLater(err) {
			return nil, infraerrors.Conflict(CryptoPayReasonTxNotReady, "transaction is not finalized or not found yet; retry later").WithCause(err)
		}
		return nil, infraerrors.ServiceUnavailable("CRYPTO_PAY_CHAIN_ERROR", "chain lookup failed").WithCause(err)
	}
	// Only finalized inflows of exactly this transaction are accepted.
	accepted := make([]chain.Observation, 0, len(obs))
	for _, o := range obs {
		got := strings.TrimSpace(o.TxHash)
		if n.Family == chain.FamilyEVM {
			got = strings.ToLower(got) // EVM hashes are hex; Solana signatures are case-sensitive
		}
		if o.Finalized && o.Network == n.Network && got == txHash {
			accepted = append(accepted, o)
		}
	}
	res, err := s.ingest(ctx, accepted)
	if err != nil {
		return nil, err
	}
	st, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := &CryptoPayRescanResult{Observations: len(obs), Ignored: res.Ignored + len(obs) - len(accepted), Results: []CryptoPayCreditOutcome{}}
	for _, id := range res.DepositIDs {
		outcome, credErr := s.creditDeposit(ctx, id, operator, cryptoCreditRescan, st)
		if credErr != nil {
			return nil, credErr
		}
		out.Results = append(out.Results, *outcome)
	}
	if err := s.writeAuditDirect(ctx, "TX_RESCANNED", nil, nil, nil, operator,
		map[string]any{"network": n.Network, "tx_hash": txHash, "observations": len(obs), "deposits": res.DepositIDs}); err != nil {
		cryptoPayLogf("audit rescan failed: %v", err)
	}
	return out, nil
}

func (s *CryptoPayService) networkAddresses(ctx context.Context, network chain.Network) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT address FROM crypto_pay_addresses WHERE network = $1 ORDER BY id`, string(network))
	if err != nil {
		return nil, fmt.Errorf("list network addresses: %w", err)
	}
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}
