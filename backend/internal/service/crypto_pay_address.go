package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/lib/pq"
)

const (
	cryptoPayEVMPathTemplate     = "m/44'/60'/0'/0/{i}"
	cryptoPayMaxEVMIndex         = 1<<31 - 1 // non-hardened child indexes only
	cryptoPayMaxPoolEntries      = 100000
	cryptoPayPoolInsertChunk     = 1000
	cryptoPayFinalizedRPCTimeout = 10 * time.Second
)

type cryptoPayAddressRow struct {
	ID         int64
	UserID     int64
	Network    chain.Network
	Address    string
	StartBlock sql.NullInt64
}

func cryptoPayAddressUnavailable(msg string) error {
	return infraerrors.Conflict(CryptoPayReasonAddressUnavailable, msg)
}

func (s *CryptoPayService) findAddress(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, userID int64, network chain.Network) (*cryptoPayAddressRow, error) {
	r := &cryptoPayAddressRow{}
	var net string
	err := q.QueryRowContext(ctx, `SELECT id, user_id, network, address, start_block FROM crypto_pay_addresses WHERE user_id = $1 AND network = $2`,
		userID, string(network)).Scan(&r.ID, &r.UserID, &net, &r.Address, &r.StartBlock)
	if err != nil {
		return nil, err
	}
	r.Network = chain.Network(net)
	return r, nil
}

// EnsureAddress returns the user's fixed address on the network, allocating
// one if needed. Addresses are never reused or reassigned. Callers check the
// allow_new_addresses switch; this function only allocates.
func (s *CryptoPayService) EnsureAddress(ctx context.Context, userID int64, network chain.Network) (*cryptoPayAddressRow, error) {
	n, ok := s.byNetwork[network]
	if !ok {
		return nil, infraerrors.BadRequest(CryptoPayReasonNetworkUnsupported, "unsupported network")
	}
	existing, err := s.findAddress(ctx, s.db, userID, network)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get crypto pay address: %w", err)
	}
	var allocated bool
	switch n.Family {
	case chain.FamilyEVM:
		allocated, err = s.allocateEVMAddress(ctx, userID, network)
	case chain.FamilySolana:
		allocated, err = s.allocateSolanaAddress(ctx, userID, network)
	default:
		return nil, cryptoPayAddressUnavailable("unsupported network family")
	}
	if err != nil {
		return nil, err
	}
	_ = allocated // false = a concurrent request allocated it first; read it back either way
	row, err := s.findAddress(ctx, s.db, userID, network)
	if err != nil {
		return nil, fmt.Errorf("read allocated crypto pay address: %w", err)
	}
	return row, nil
}

// allocateEVMAddress takes the next index from the active keyset's high-water
// mark (row-locked UPDATE ... RETURNING, never MAX+1) and derives the address.
func (s *CryptoPayService) allocateEVMAddress(ctx context.Context, userID int64, network chain.Network) (bool, error) {
	derive := s.deps.DeriveEVMAddress
	if derive == nil {
		return false, cryptoPayAddressUnavailable("address derivation is not available")
	}
	// The scan start height comes from RPC, outside the transaction. Without
	// a ready scanner there is no provable start height: refuse (an address
	// handed out now could never be watched from the right block).
	var startBlock sql.NullInt64
	if s.evmScanner() == nil {
		return false, cryptoPayAddressUnavailable("no deposit address is available right now; retry later")
	}
	if sc := s.evmScanner(); sc != nil {
		rpcCtx, cancel := context.WithTimeout(ctx, cryptoPayFinalizedRPCTimeout)
		height, err := sc.FinalizedBlock(rpcCtx)
		cancel()
		if err != nil {
			// Without a provable scan start the address could never be
			// scanned; refuse rather than hand out an unwatched address.
			cryptoPayLogf("finalized height for new %s address unavailable: %v", network, err)
			return false, cryptoPayAddressUnavailable("no deposit address is available right now; retry later")
		}
		startBlock = sql.NullInt64{Int64: int64(height), Valid: true}
	}
	// Solana pool addresses need no start height: their token accounts are
	// walked from the full signature history (see scanSolana).
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var keysetID, index int64
	var version int
	var xpub, pathTemplate string
	err = tx.QueryRowContext(ctx, `
		UPDATE crypto_pay_keysets SET next_index = next_index + 1, updated_at = NOW()
		WHERE network = $1 AND family = 'evm' AND active AND next_index <= $2
		RETURNING id, version, xpub, path_template, next_index - 1`, string(network), cryptoPayMaxEVMIndex,
	).Scan(&keysetID, &version, &xpub, &pathTemplate, &index)
	if errors.Is(err, sql.ErrNoRows) {
		cryptoPayLogf("no active EVM keyset with free indexes for %s", network)
		return false, cryptoPayAddressUnavailable("no deposit address is available")
	}
	if err != nil {
		return false, fmt.Errorf("reserve EVM index: %w", err)
	}
	address, err := derive(xpub, uint32(index))
	address = strings.ToLower(strings.TrimSpace(address))
	if err != nil || !cryptoPayEVMAddressRe.MatchString(address) {
		cryptoPayLogf("derive EVM address keyset=%d index=%d failed: %v", keysetID, index, err)
		return false, cryptoPayAddressUnavailable("no deposit address is available")
	}
	path := strings.ReplaceAll(pathTemplate, "{i}", strconv.FormatInt(index, 10))
	return s.insertAddress(ctx, tx, userID, network, address, keysetID, version, index, path, startBlock)
}

// allocateSolanaAddress assigns the next free pool entry (FOR UPDATE SKIP LOCKED).
func (s *CryptoPayService) allocateSolanaAddress(ctx context.Context, userID int64, network chain.Network) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var entryID, keysetID, index int64
	var version int
	var address, pathTemplate string
	err = tx.QueryRowContext(ctx, `
		SELECT e.id, e.keyset_id, e.derivation_index, e.address, k.version, k.path_template
		FROM crypto_pay_solana_pool_entries e
		JOIN crypto_pay_keysets k ON k.id = e.keyset_id
		WHERE e.network = $1 AND k.active AND e.assigned_user_id IS NULL
		ORDER BY e.keyset_id, e.derivation_index
		LIMIT 1
		FOR UPDATE OF e SKIP LOCKED`, string(network)).Scan(&entryID, &keysetID, &index, &address, &version, &pathTemplate)
	if errors.Is(err, sql.ErrNoRows) {
		cryptoPayLogf("ALERT Solana address pool exhausted for %s", network)
		return false, cryptoPayAddressUnavailable("no deposit address is available")
	}
	if err != nil {
		return false, fmt.Errorf("reserve Solana pool entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE crypto_pay_solana_pool_entries SET assigned_user_id = $2, assigned_at = NOW()
		WHERE id = $1 AND assigned_user_id IS NULL`, entryID, userID); err != nil {
		return false, fmt.Errorf("assign Solana pool entry: %w", err)
	}
	path := strings.ReplaceAll(pathTemplate, "{i}", strconv.FormatInt(index, 10))
	return s.insertAddress(ctx, tx, userID, network, address, keysetID, version, index, path, sql.NullInt64{})
}

// insertAddress inserts the address row and commits. If the user already got
// an address concurrently, the transaction is rolled back (the reserved index
// or pool entry was never exposed) and false is returned.
func (s *CryptoPayService) insertAddress(ctx context.Context, tx *sql.Tx, userID int64, network chain.Network, address string,
	keysetID int64, version int, index int64, path string, startBlock sql.NullInt64) (bool, error) {
	var addressID int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO crypto_pay_addresses (user_id, network, address, keyset_id, keyset_version, derivation_index, derivation_path, start_block)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id, network) DO NOTHING
		RETURNING id`, userID, string(network), address, keysetID, version, index, path, startBlock).Scan(&addressID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			cryptoPayLogf("ALERT address collision on %s keyset=%d index=%d: %s", network, keysetID, index, pqErr.Constraint)
			return false, cryptoPayAddressUnavailable("no deposit address is available")
		}
		return false, fmt.Errorf("insert crypto pay address: %w", err)
	}
	if err := writeCryptoPayAudit(ctx, tx, "ADDRESS_ALLOCATED", nil, &userID, &keysetID, cryptoPayOperatorSystem,
		map[string]any{"network": network, "address": address, "index": index, "path": path}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit crypto pay address: %w", err)
	}
	return true, nil
}

// ---- keysets ----

// CryptoPayKeysetInfo is the admin view of a keyset (the xpub is never returned).
type CryptoPayKeysetInfo struct {
	ID            int64     `json:"id"`
	Network       string    `json:"network"`
	Family        string    `json:"family"`
	Version       int       `json:"version"`
	PathTemplate  string    `json:"path_template"`
	Fingerprint   string    `json:"fingerprint"`
	SignerPubkey  string    `json:"signer_pubkey"`
	Active        bool      `json:"active"`
	Allocated     int64     `json:"allocated"`
	PoolSize      int64     `json:"pool_size"`
	PoolRemaining int64     `json:"pool_remaining"`
	FirstAddress  string    `json:"first_address"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
}

func cryptoPayFingerprint(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ImportEVMKeyset stores a new account-level xpub and makes it the active
// keyset for the network (the previous one keeps serving its addresses).
func (s *CryptoPayService) ImportEVMKeyset(ctx context.Context, networkName, xpub, operator string) (*CryptoPayKeysetInfo, error) {
	n, ok := s.network(networkName)
	if !ok || n.Family != chain.FamilyEVM {
		return nil, infraerrors.BadRequest(CryptoPayReasonNetworkUnsupported, "network is not a configured EVM network")
	}
	if s.deps.DeriveEVMAddress == nil {
		return nil, infraerrors.ServiceUnavailable("CRYPTO_PAY_DERIVATION_UNAVAILABLE", "address derivation is not available")
	}
	xpub = strings.TrimSpace(xpub)
	if xpub == "" || len(xpub) > 200 {
		return nil, infraerrors.BadRequest("CRYPTO_PAY_XPUB_INVALID", "invalid xpub")
	}
	first, err := s.deps.DeriveEVMAddress(xpub, 0)
	first = strings.ToLower(strings.TrimSpace(first))
	if err != nil || !cryptoPayEVMAddressRe.MatchString(first) {
		return nil, infraerrors.BadRequest("CRYPTO_PAY_XPUB_INVALID", "xpub cannot derive addresses")
	}
	second, err := s.deps.DeriveEVMAddress(xpub, 1)
	if err != nil || strings.EqualFold(second, first) {
		return nil, infraerrors.BadRequest("CRYPTO_PAY_XPUB_INVALID", "xpub cannot derive addresses")
	}
	fingerprint := cryptoPayFingerprint([]byte(xpub))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	version, err := nextCryptoPayKeysetVersion(ctx, tx, n.Network)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE crypto_pay_keysets SET active = FALSE, updated_at = NOW() WHERE network = $1 AND family = 'evm' AND active`,
		string(n.Network)); err != nil {
		return nil, fmt.Errorf("deactivate EVM keyset: %w", err)
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO crypto_pay_keysets (network, family, version, xpub, path_template, fingerprint, active, created_by)
		VALUES ($1, 'evm', $2, $3, $4, $5, TRUE, $6) RETURNING id`,
		string(n.Network), version, xpub, cryptoPayEVMPathTemplate, fingerprint, operator).Scan(&id)
	if err != nil {
		return nil, cryptoPayKeysetInsertError(err)
	}
	if err := writeCryptoPayAudit(ctx, tx, "KEYSET_IMPORTED", nil, nil, &id, operator,
		map[string]any{"network": n.Network, "family": "evm", "version": version, "fingerprint": fingerprint, "first_address": first}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getKeyset(ctx, id)
}

// ImportSolanaKeyset verifies an offline-signed pool manifest against the
// configured trusted signer and stores its entries as a new active pool.
func (s *CryptoPayService) ImportSolanaKeyset(ctx context.Context, raw []byte, operator string) (*CryptoPayKeysetInfo, error) {
	if s.deps.VerifySolanaManifest == nil {
		return nil, infraerrors.ServiceUnavailable("CRYPTO_PAY_MANIFEST_VERIFIER_UNAVAILABLE", "manifest verification is not available")
	}
	if s.signerPubkey == "" {
		return nil, infraerrors.ServiceUnavailable("CRYPTO_PAY_SIGNER_NOT_CONFIGURED", "trusted manifest signer is not configured")
	}
	manifest, err := s.deps.VerifySolanaManifest(raw, s.signerPubkey)
	if err != nil || manifest == nil {
		return nil, infraerrors.BadRequest("CRYPTO_PAY_MANIFEST_INVALID", "manifest verification failed")
	}
	n, ok := s.byNetwork[manifest.Network]
	if !ok || n.Family != chain.FamilySolana {
		return nil, infraerrors.BadRequest(CryptoPayReasonNetworkUnsupported, "manifest network is not a configured Solana network")
	}
	if len(manifest.Entries) == 0 || len(manifest.Entries) > cryptoPayMaxPoolEntries {
		return nil, infraerrors.BadRequest("CRYPTO_PAY_MANIFEST_INVALID", "manifest has no entries or too many entries")
	}
	path := strings.TrimSpace(manifest.PathTemplate)
	if path == "" || len(path) > 100 || !strings.Contains(path, "{i}") {
		return nil, infraerrors.BadRequest("CRYPTO_PAY_MANIFEST_INVALID", "invalid path template")
	}
	seenIdx := make(map[uint32]bool, len(manifest.Entries))
	seenAddr := make(map[string]bool, len(manifest.Entries))
	for _, e := range manifest.Entries {
		if !cryptoPayBase58Re.MatchString(e.Address) || seenIdx[e.Index] || seenAddr[e.Address] {
			return nil, infraerrors.BadRequest("CRYPTO_PAY_MANIFEST_INVALID", "manifest has an invalid or duplicate entry")
		}
		seenIdx[e.Index], seenAddr[e.Address] = true, true
	}
	fingerprint := cryptoPayFingerprint(raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	version, err := nextCryptoPayKeysetVersion(ctx, tx, n.Network)
	if err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO crypto_pay_keysets (network, family, version, path_template, fingerprint, signer_pubkey, pool_size, active, created_by)
		VALUES ($1, 'solana', $2, $3, $4, $5, $6, TRUE, $7) RETURNING id`,
		string(n.Network), version, path, fingerprint, manifest.SignerPubkey, len(manifest.Entries), operator).Scan(&id)
	if err != nil {
		return nil, cryptoPayKeysetInsertError(err)
	}
	for start := 0; start < len(manifest.Entries); start += cryptoPayPoolInsertChunk {
		end := min(start+cryptoPayPoolInsertChunk, len(manifest.Entries))
		var sb strings.Builder
		_, _ = sb.WriteString(`INSERT INTO crypto_pay_solana_pool_entries (keyset_id, network, derivation_index, address) VALUES `)
		args := make([]any, 0, (end-start)*2+2)
		args = append(args, id, string(n.Network))
		for i, e := range manifest.Entries[start:end] {
			if i > 0 {
				_, _ = sb.WriteString(",")
			}
			fmt.Fprintf(&sb, "($1, $2, $%d, $%d)", len(args)+1, len(args)+2)
			args = append(args, int64(e.Index), e.Address)
		}
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return nil, cryptoPayKeysetInsertError(err)
		}
	}
	if err := writeCryptoPayAudit(ctx, tx, "KEYSET_IMPORTED", nil, nil, &id, operator,
		map[string]any{"network": n.Network, "family": "solana", "version": version, "fingerprint": fingerprint,
			"entries": len(manifest.Entries), "signer": manifest.SignerPubkey}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getKeyset(ctx, id)
}

func nextCryptoPayKeysetVersion(ctx context.Context, tx *sql.Tx, network chain.Network) (int, error) {
	// Serialize imports per network so versions are gapless and unique.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('crypto_pay_keyset:' || $1))`, string(network)); err != nil {
		return 0, fmt.Errorf("lock keyset import: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM crypto_pay_keysets WHERE network = $1`,
		string(network)).Scan(&version); err != nil {
		return 0, fmt.Errorf("next keyset version: %w", err)
	}
	return version, nil
}

func cryptoPayKeysetInsertError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return infraerrors.Conflict("CRYPTO_PAY_KEYSET_CONFLICT", "keyset or one of its addresses was already imported")
	}
	return fmt.Errorf("import keyset: %w", err)
}

const cryptoPayKeysetSelect = `
	SELECT k.id, k.network, k.family, k.version, k.path_template, k.fingerprint, k.signer_pubkey, k.active,
	       k.next_index, k.pool_size, k.created_by, k.created_at, k.xpub,
	       (SELECT COUNT(*) FROM crypto_pay_solana_pool_entries e WHERE e.keyset_id = k.id AND e.assigned_user_id IS NULL),
	       (SELECT COUNT(*) FROM crypto_pay_addresses a WHERE a.keyset_id = k.id)
	FROM crypto_pay_keysets k`

func (s *CryptoPayService) scanKeyset(scan func(dest ...any) error) (*CryptoPayKeysetInfo, error) {
	var k CryptoPayKeysetInfo
	var nextIndex int64
	var xpub string
	if err := scan(&k.ID, &k.Network, &k.Family, &k.Version, &k.PathTemplate, &k.Fingerprint, &k.SignerPubkey, &k.Active,
		&nextIndex, &k.PoolSize, &k.CreatedBy, &k.CreatedAt, &xpub, &k.PoolRemaining, &k.Allocated); err != nil {
		return nil, err
	}
	if k.Family == string(chain.FamilyEVM) {
		k.PoolSize, k.PoolRemaining = 0, 0
		if s.deps.DeriveEVMAddress != nil {
			if first, err := s.deps.DeriveEVMAddress(xpub, 0); err == nil {
				k.FirstAddress = strings.ToLower(first)
			}
		}
	}
	return &k, nil
}

func (s *CryptoPayService) getKeyset(ctx context.Context, id int64) (*CryptoPayKeysetInfo, error) {
	return s.scanKeyset(s.db.QueryRowContext(ctx, cryptoPayKeysetSelect+` WHERE k.id = $1`, id).Scan)
}

// ListKeysets returns all keysets with allocation counts and pool remaining.
func (s *CryptoPayService) ListKeysets(ctx context.Context) ([]CryptoPayKeysetInfo, error) {
	rows, err := s.db.QueryContext(ctx, cryptoPayKeysetSelect+` ORDER BY k.network, k.version`)
	if err != nil {
		return nil, fmt.Errorf("list keysets: %w", err)
	}
	out := []CryptoPayKeysetInfo{}
	for rows.Next() {
		k, scanErr := s.scanKeyset(rows.Scan)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		out = append(out, *k)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return out, nil
}
