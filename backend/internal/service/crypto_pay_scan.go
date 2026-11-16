package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/lib/pq"
)

// Scan scheduling. Active addresses (an order or an inflow in the last
// activeWindow) are scanned every tick; every other address is scanned at
// least once per fullSweepInterval.
const (
	cryptoPayActiveWindow       = 24 * time.Hour
	cryptoPayFullSweepInterval  = 23 * time.Hour // < 24h so each address is covered within a day
	cryptoPayEVMBatchAddresses  = 100
	cryptoPayEVMGroupSpread     = 10_000  // addresses whose next block differs by less are scanned together
	cryptoPayEVMMaxSpan         = 200_000 // blocks per ScanRange call per tick
	cryptoPayEVMMaxBatchesTick  = 50
	cryptoPaySolanaMaxScansTick = 300
	cryptoPayEVMOrphanWindow    = 2 * time.Hour
	cryptoPaySolanaOrphanWindow = 30 * time.Minute
)

var errCryptoPayCursorConflict = errors.New("crypto pay scan cursor changed concurrently")

type cryptoPayCursor struct {
	scope       string
	position    string
	version     int64
	lastScanned sql.NullTime
	exists      bool
}

func (c *cryptoPayCursor) due(now time.Time, active bool) bool {
	return active || !c.lastScanned.Valid || c.lastScanned.Time.Before(now.Add(-cryptoPayFullSweepInterval))
}

func cryptoPayEVMCursorScope(network chain.Network, address string) string {
	return "evm:" + string(network) + ":fin:" + address
}

func cryptoPaySolanaCursorScope(network chain.Network, symbol, tokenAccount string) string {
	return "sol:" + string(network) + ":" + symbol + ":" + tokenAccount
}

func (s *CryptoPayService) loadCursors(ctx context.Context, scopes []string) (map[string]*cryptoPayCursor, error) {
	out := make(map[string]*cryptoPayCursor, len(scopes))
	for _, sc := range scopes {
		out[sc] = &cryptoPayCursor{scope: sc}
	}
	if len(scopes) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope_key, position, version, last_scanned_at FROM crypto_pay_scan_cursors WHERE scope_key = ANY($1)`, pq.Array(scopes))
	if err != nil {
		return nil, fmt.Errorf("load scan cursors: %w", err)
	}
	for rows.Next() {
		c := &cryptoPayCursor{exists: true}
		if err := rows.Scan(&c.scope, &c.position, &c.version, &c.lastScanned); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[c.scope] = c
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// advanceCursor stores a new position with compare-and-swap on version. It is
// called only after the scanned range was fully ingested, so a lost race or a
// crash merely repeats an idempotent scan.
func (s *CryptoPayService) advanceCursor(ctx context.Context, network chain.Network, c *cryptoPayCursor, position string) error {
	var res sql.Result
	var err error
	if !c.exists {
		res, err = s.db.ExecContext(ctx, `
			INSERT INTO crypto_pay_scan_cursors (scope_key, network, position, version, last_scanned_at)
			VALUES ($1, $2, $3, 1, NOW()) ON CONFLICT (scope_key) DO NOTHING`, c.scope, string(network), position)
	} else {
		res, err = s.db.ExecContext(ctx, `
			UPDATE crypto_pay_scan_cursors SET position = $2, version = version + 1, last_scanned_at = NOW(), last_error = '', updated_at = NOW()
			WHERE scope_key = $1 AND version = $3`, c.scope, position, c.version)
	}
	if err != nil {
		return fmt.Errorf("advance scan cursor: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errCryptoPayCursorConflict
	}
	if c.exists {
		c.version++
	} else {
		c.exists, c.version = true, 1
	}
	c.position = position
	return nil
}

func (s *CryptoPayService) recordCursorError(ctx context.Context, network chain.Network, scope string, cause error) {
	msg := cause.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO crypto_pay_scan_cursors (scope_key, network, last_error) VALUES ($1, $2, $3)
		ON CONFLICT (scope_key) DO UPDATE SET last_error = EXCLUDED.last_error, updated_at = NOW()`, scope, string(network), msg); err != nil {
		cryptoPayLogf("record cursor error %s failed: %v", scope, err)
	}
}

type cryptoPayScanAddress struct {
	id         int64
	address    string
	startBlock sql.NullInt64
	active     bool
}

func (s *CryptoPayService) scanAddresses(ctx context.Context, network chain.Network) ([]cryptoPayScanAddress, error) {
	since := s.now().Add(-cryptoPayActiveWindow)
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.address, a.start_block,
		       (a.last_order_at > $2 OR EXISTS (
		           SELECT 1 FROM crypto_pay_deposits d WHERE d.address_id = a.id AND d.observed_at > $2)) AS active
		FROM crypto_pay_addresses a WHERE a.network = $1 ORDER BY a.id`, string(network), since)
	if err != nil {
		return nil, fmt.Errorf("list scan addresses: %w", err)
	}
	var out []cryptoPayScanAddress
	for rows.Next() {
		var a cryptoPayScanAddress
		var active sql.NullBool
		if err := rows.Scan(&a.id, &a.address, &a.startBlock, &active); err != nil {
			_ = rows.Close()
			return nil, err
		}
		a.active = active.Valid && active.Bool
		out = append(out, a)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// ScanOnce runs one pass over every configured network that has a scanner.
func (s *CryptoPayService) ScanOnce(ctx context.Context) error {
	var errs []error
	if sc := s.evmScanner(); sc != nil {
		if err := s.scanEVM(ctx, sc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sc.Network(), err))
		}
		if _, err := s.OrphanStaleObservations(ctx, sc.Network(), cryptoPayEVMOrphanWindow); err != nil {
			errs = append(errs, err)
		}
	}
	if sc := s.solanaScanner(); sc != nil {
		if err := s.scanSolana(ctx, sc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sc.Network(), err))
		}
		if _, err := s.OrphanStaleObservations(ctx, sc.Network(), cryptoPaySolanaOrphanWindow); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type cryptoPayEVMScanItem struct {
	addr   cryptoPayScanAddress
	cursor *cryptoPayCursor
	from   uint64
}

// scanEVM scans finalized logs per address cursor (grouping addresses whose
// next block is close), then the unfinalized tail for active addresses.
func (s *CryptoPayService) scanEVM(ctx context.Context, sc chain.EVMScanner) error {
	network := sc.Network()
	finalized, err := sc.FinalizedBlock(ctx)
	if err != nil {
		return fmt.Errorf("finalized block: %w", err)
	}
	latest, err := sc.LatestBlock(ctx)
	if err != nil {
		return fmt.Errorf("latest block: %w", err)
	}
	addrs, err := s.scanAddresses(ctx, network)
	if err != nil {
		return err
	}
	scopes := make([]string, len(addrs))
	for i, a := range addrs {
		scopes[i] = cryptoPayEVMCursorScope(network, a.address)
	}
	cursors, err := s.loadCursors(ctx, scopes)
	if err != nil {
		return err
	}
	now := s.now()
	var items []cryptoPayEVMScanItem
	var active []string
	for i, a := range addrs {
		c := cursors[scopes[i]]
		if a.active {
			active = append(active, a.address)
		}
		if !c.due(now, a.active) {
			continue
		}
		var from uint64
		if c.exists && c.position != "" {
			last, perr := strconv.ParseUint(c.position, 10, 64)
			if perr != nil {
				cryptoPayLogf("ALERT invalid EVM cursor %s=%q; address skipped", c.scope, c.position)
				continue
			}
			from = last + 1
		} else if a.startBlock.Valid {
			from = uint64(a.startBlock.Int64)
		} else if s.evmScanFromBlock > 0 {
			from = s.evmScanFromBlock
		} else {
			cryptoPayLogf("ALERT %s address %s has no scan start height; set crypto_pay.evm_scan_from_block", network, a.address)
			continue
		}
		items = append(items, cryptoPayEVMScanItem{addr: a, cursor: c, from: from})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].from < items[j].from })
	batches := 0
	var errs []error
	for start := 0; start < len(items) && batches < cryptoPayEVMMaxBatchesTick; batches++ {
		end := start + 1
		for end < len(items) && end-start < cryptoPayEVMBatchAddresses && items[end].from-items[start].from < cryptoPayEVMGroupSpread {
			end++
		}
		batch := items[start:end]
		start = end
		from := batch[0].from
		if from > finalized {
			continue // already at the finalized head
		}
		to := finalized
		if to-from >= cryptoPayEVMMaxSpan {
			to = from + cryptoPayEVMMaxSpan - 1
		}
		list := make([]string, len(batch))
		for i, it := range batch {
			list[i] = it.addr.address
		}
		obs, scanErr := sc.ScanRange(ctx, from, to, list, true)
		if scanErr != nil {
			for _, it := range batch {
				s.recordCursorError(ctx, network, it.cursor.scope, scanErr)
			}
			errs = append(errs, fmt.Errorf("scan %d-%d: %w", from, to, scanErr))
			continue
		}
		for i := range obs {
			if !obs[i].Finalized {
				return fmt.Errorf("scanner returned an unfinalized log for a finalized range")
			}
		}
		if _, err := s.IngestObservations(ctx, obs); err != nil {
			return fmt.Errorf("ingest %d-%d: %w", from, to, err)
		}
		position := strconv.FormatUint(to, 10)
		for _, it := range batch {
			if it.from > to+1 {
				continue // this address was already past the range
			}
			if err := s.advanceCursor(ctx, network, it.cursor, position); err != nil {
				return err
			}
		}
	}
	// Unfinalized tail for active addresses: display only ("confirming").
	if latest > finalized && len(active) > 0 {
		for start := 0; start < len(active); start += cryptoPayEVMBatchAddresses {
			end := min(start+cryptoPayEVMBatchAddresses, len(active))
			obs, scanErr := sc.ScanRange(ctx, finalized+1, latest, active[start:end], false)
			if scanErr != nil {
				errs = append(errs, fmt.Errorf("scan unfinalized tail: %w", scanErr))
				break
			}
			for i := range obs {
				obs[i].Finalized = false
			}
			if _, err := s.IngestObservations(ctx, obs); err != nil {
				return fmt.Errorf("ingest unfinalized tail: %w", err)
			}
		}
	}
	return errors.Join(errs...)
}

// scanSolana walks each user's USDC/USDT associated token accounts.
func (s *CryptoPayService) scanSolana(ctx context.Context, sc chain.SolanaScanner) error {
	network := sc.Network()
	n := s.byNetwork[network]
	ata := s.deps.SolanaATA
	if n == nil || ata == nil {
		return fmt.Errorf("solana ATA derivation is not available")
	}
	addrs, err := s.scanAddresses(ctx, network)
	if err != nil {
		return err
	}
	type item struct {
		owner   string
		account string
		asset   chain.Asset
		active  bool
		scope   string
	}
	var items []item
	var scopes []string
	for _, a := range addrs {
		for _, asset := range n.Assets {
			account, aerr := ata(a.address, asset.Contract, asset.TokenProgram)
			if aerr != nil || !cryptoPayBase58Re.MatchString(account) {
				cryptoPayLogf("ALERT ATA for %s/%s failed: %v", a.address, asset.Symbol, aerr)
				continue
			}
			scope := cryptoPaySolanaCursorScope(network, asset.Symbol, account)
			items = append(items, item{owner: a.address, account: account, asset: asset, active: a.active, scope: scope})
			scopes = append(scopes, scope)
		}
	}
	cursors, err := s.loadCursors(ctx, scopes)
	if err != nil {
		return err
	}
	now := s.now()
	// Never-scanned and oldest first, so the per-tick budget rotates fairly.
	sort.SliceStable(items, func(i, j int) bool {
		ci, cj := cursors[items[i].scope], cursors[items[j].scope]
		if items[i].active != items[j].active {
			return items[i].active
		}
		if ci.lastScanned.Valid != cj.lastScanned.Valid {
			return !ci.lastScanned.Valid
		}
		return ci.lastScanned.Time.Before(cj.lastScanned.Time)
	})
	var errs []error
	scans := 0
	for _, it := range items {
		c := cursors[it.scope]
		if !c.due(now, it.active) {
			continue
		}
		if scans >= cryptoPaySolanaMaxScansTick {
			break
		}
		scans++
		obs, newest, scanErr := sc.ScanTokenAccount(ctx, it.owner, it.account, it.asset, c.position, "finalized")
		if scanErr != nil {
			s.recordCursorError(ctx, network, it.scope, scanErr)
			errs = append(errs, fmt.Errorf("scan %s: %w", it.account, scanErr))
			continue
		}
		for i := range obs {
			if !obs[i].Finalized {
				return fmt.Errorf("scanner returned an unfinalized inflow at finalized commitment")
			}
		}
		if _, err := s.IngestObservations(ctx, obs); err != nil {
			return fmt.Errorf("ingest %s: %w", it.account, err)
		}
		position := c.position
		if newest != "" {
			position = newest
		}
		if err := s.advanceCursor(ctx, network, c, position); err != nil {
			return err
		}
		if !it.active {
			continue
		}
		pending, _, pendErr := sc.ScanTokenAccount(ctx, it.owner, it.account, it.asset, position, "confirmed")
		if pendErr != nil {
			errs = append(errs, fmt.Errorf("scan confirmed %s: %w", it.account, pendErr))
			continue
		}
		for i := range pending {
			pending[i].Finalized = false
		}
		if _, err := s.IngestObservations(ctx, pending); err != nil {
			return fmt.Errorf("ingest confirmed %s: %w", it.account, err)
		}
	}
	return errors.Join(errs...)
}
