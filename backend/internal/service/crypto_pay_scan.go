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
	updatedAt   sql.NullTime // last success or failure; rotates failing scopes back
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
		SELECT scope_key, position, version, last_scanned_at, updated_at FROM crypto_pay_scan_cursors WHERE scope_key = ANY($1)`, pq.Array(scopes))
	if err != nil {
		return nil, fmt.Errorf("load scan cursors: %w", err)
	}
	for rows.Next() {
		c := &cryptoPayCursor{exists: true}
		if err := rows.Scan(&c.scope, &c.position, &c.version, &c.lastScanned, &c.updatedAt); err != nil {
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
// caughtUp=false (range capped below the finalized head) leaves
// last_scanned_at NULL so the cursor is scanned again next tick instead of
// waiting for the daily sweep.
func (s *CryptoPayService) advanceCursor(ctx context.Context, network chain.Network, c *cryptoPayCursor, position string, caughtUp bool) error {
	var res sql.Result
	var err error
	if !c.exists {
		res, err = s.db.ExecContext(ctx, `
			INSERT INTO crypto_pay_scan_cursors (scope_key, network, position, version, last_scanned_at)
			VALUES ($1, $2, $3, 1, CASE WHEN $4 THEN NOW() END) ON CONFLICT (scope_key) DO NOTHING`, c.scope, string(network), position, caughtUp)
	} else {
		res, err = s.db.ExecContext(ctx, `
			UPDATE crypto_pay_scan_cursors SET position = $2, version = version + 1,
				last_scanned_at = CASE WHEN $4 THEN NOW() END, last_error = '', updated_at = NOW()
			WHERE scope_key = $1 AND version = $3`, c.scope, position, c.version, caughtUp)
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
	// Own budget: the error must be recorded (and the scope rotated) even
	// when the scan itself used up the caller's deadline.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
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
	return s.scanNetworks(ctx, 0)
}

// scanNetworks scans each network with its own time budget (0 = parent ctx
// only) so a slow chain cannot starve the other one.
func (s *CryptoPayService) scanNetworks(parent context.Context, budget time.Duration) error {
	phase := func() (context.Context, context.CancelFunc) {
		if budget <= 0 {
			return context.WithCancel(parent)
		}
		return context.WithTimeout(parent, budget)
	}
	var errs []error
	if sc := s.evmScanner(); sc != nil {
		ctx, cancel := phase()
		defer cancel()
		if err := s.scanEVM(ctx, sc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sc.Network(), err))
		}
		if _, err := s.OrphanStaleObservations(ctx, sc.Network(), cryptoPayEVMOrphanWindow); err != nil {
			errs = append(errs, err)
		}
	}
	if sc := s.solanaScanner(); sc != nil {
		ctx, cancel := phase()
		defer cancel()
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
		// A cursor that has not reached the finalized head yet has a NULL
		// last_scanned_at (see advanceCursor), so it stays due every tick.
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
	// Group addresses whose next block is close into batches, then run the
	// least recently attempted batches first. Success and failure both touch
	// the cursors' updated_at, so a batch that keeps failing or timing out
	// rotates behind healthy ones instead of starving them.
	sort.Slice(items, func(i, j int) bool { return items[i].from < items[j].from })
	type evmBatch struct {
		items       []cryptoPayEVMScanItem
		lastAttempt time.Time // zero = never attempted
	}
	var batchList []evmBatch
	for start := 0; start < len(items); {
		end := start + 1
		for end < len(items) && end-start < cryptoPayEVMBatchAddresses && items[end].from-items[start].from < cryptoPayEVMGroupSpread {
			end++
		}
		b := evmBatch{items: items[start:end]}
		for _, it := range b.items {
			if it.cursor.updatedAt.Valid && it.cursor.updatedAt.Time.After(b.lastAttempt) {
				b.lastAttempt = it.cursor.updatedAt.Time
			}
		}
		batchList = append(batchList, b)
		start = end
	}
	sort.SliceStable(batchList, func(i, j int) bool { return batchList[i].lastAttempt.Before(batchList[j].lastAttempt) })
	if len(batchList) > cryptoPayEVMMaxBatchesTick {
		batchList = batchList[:cryptoPayEVMMaxBatchesTick]
	}
	var errs []error
	for _, b := range batchList {
		if ctx.Err() != nil {
			break
		}
		batch := b.items
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
		batchErr := func() error {
			batchCtx, cancel := context.WithTimeout(ctx, s.batchTimeout)
			defer cancel()
			obs, scanErr := sc.ScanRange(batchCtx, from, to, list, true)
			if scanErr != nil {
				return fmt.Errorf("scan %d-%d: %w", from, to, scanErr)
			}
			for i := range obs {
				if !obs[i].Finalized {
					return fmt.Errorf("scanner returned an unfinalized log for finalized range %d-%d", from, to)
				}
			}
			if _, err := s.IngestObservations(batchCtx, obs); err != nil {
				return fmt.Errorf("ingest %d-%d: %w", from, to, err)
			}
			return nil
		}()
		if batchErr != nil {
			// Cursors stay put (the range is retried); the error is visible
			// in reconciliation and the batch rotates to the back.
			for _, it := range batch {
				s.recordCursorError(ctx, network, it.cursor.scope, batchErr)
			}
			errs = append(errs, batchErr)
			continue
		}
		position := strconv.FormatUint(to, 10)
		for _, it := range batch {
			if it.from > to+1 {
				continue // this address was already past the range
			}
			if err := s.advanceCursor(ctx, network, it.cursor, position, to == finalized); err != nil {
				errs = append(errs, fmt.Errorf("advance %s: %w", it.cursor.scope, err))
			}
		}
	}
	// Unfinalized tail for active addresses: display only ("confirming").
	if latest > finalized && len(active) > 0 {
		for start := 0; start < len(active); start += cryptoPayEVMBatchAddresses {
			end := min(start+cryptoPayEVMBatchAddresses, len(active))
			tailCtx, cancel := context.WithTimeout(ctx, s.batchTimeout)
			obs, scanErr := sc.ScanRange(tailCtx, finalized+1, latest, active[start:end], false)
			cancel()
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
		// Least recently attempted first (success or failure both touch
		// updated_at), so failing accounts rotate behind healthy ones.
		ci, cj := cursors[items[i].scope], cursors[items[j].scope]
		if ci.updatedAt.Valid != cj.updatedAt.Valid {
			return !ci.updatedAt.Valid
		}
		return ci.updatedAt.Time.Before(cj.updatedAt.Time)
	})
	// Overdue inactive accounts get a reserved half of the budget so a large
	// active set can never starve them; active accounts fill the rest.
	var overdue, active []item
	for _, it := range items {
		if !cursors[it.scope].due(now, it.active) {
			continue
		}
		if it.active {
			active = append(active, it)
		} else {
			overdue = append(overdue, it)
		}
	}
	reserved := min(len(overdue), cryptoPaySolanaMaxScansTick/2)
	selected := append([]item{}, overdue[:reserved]...)
	selected = append(selected, active[:min(len(active), cryptoPaySolanaMaxScansTick-reserved)]...)
	if room := cryptoPaySolanaMaxScansTick - len(selected); room > 0 {
		selected = append(selected, overdue[reserved:reserved+min(room, len(overdue)-reserved)]...)
	}
	var errs []error
	for _, it := range selected {
		c := cursors[it.scope]
		if ctx.Err() != nil {
			break
		}
		accountCtx, cancel := context.WithTimeout(ctx, s.batchTimeout)
		obs, newest, scanErr := sc.ScanTokenAccount(accountCtx, it.owner, it.account, it.asset, c.position, "finalized")
		cancel()
		if scanErr != nil {
			s.recordCursorError(ctx, network, it.scope, scanErr)
			errs = append(errs, fmt.Errorf("scan %s: %w", it.account, scanErr))
			continue
		}
		var ingestErr error
		for i := range obs {
			if !obs[i].Finalized {
				ingestErr = fmt.Errorf("scanner returned an unfinalized inflow at finalized commitment")
			}
		}
		if ingestErr == nil {
			_, ingestErr = s.IngestObservations(ctx, obs)
		}
		if ingestErr != nil {
			s.recordCursorError(ctx, network, it.scope, ingestErr)
			errs = append(errs, fmt.Errorf("ingest %s: %w", it.account, ingestErr))
			continue
		}
		position := c.position
		if newest != "" {
			position = newest
		}
		if err := s.advanceCursor(ctx, network, c, position, true); err != nil {
			errs = append(errs, fmt.Errorf("advance %s: %w", it.scope, err))
			continue
		}
		if !it.active {
			continue
		}
		pendingCtx, pendingCancel := context.WithTimeout(ctx, s.batchTimeout)
		pending, _, pendErr := sc.ScanTokenAccount(pendingCtx, it.owner, it.account, it.asset, position, "confirmed")
		pendingCancel()
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
