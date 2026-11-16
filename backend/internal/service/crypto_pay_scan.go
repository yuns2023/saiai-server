package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/lib/pq"
)

// Scan scheduling: ONE fair queue per network. A scan unit is an EVM address
// batch or a Solana token account. Its next due time is
// last_attempt_at + interval, where interval is 0 for active addresses (an
// order or inflow within cryptoPayActiveWindow) and for EVM cursors that have
// not yet reached the finalized head, and cryptoPayLongInterval otherwise.
// Every attempt (success, failure, timeout, cursor-write failure) sets
// last_attempt_at, so it moves to the back of the queue. A pass runs at most
// scan_units_per_pass units per network in due order, EACH with the full
// scan_batch_timeout; there is no shared pass deadline, so no unit is ever
// given a shortened slice (the advisory lock prevents overlapping passes).
// There is no category priority.
const (
	cryptoPayActiveWindow       = 24 * time.Hour
	cryptoPayLongInterval       = 23 * time.Hour // idle units are revisited about daily (subject to units-per-pass capacity)
	cryptoPayEVMBatchAddresses  = 100
	cryptoPayEVMGroupSpread     = 10_000 // addresses whose next block differs by less share a batch
	cryptoPayEVMOrphanWindow    = 2 * time.Hour
	cryptoPaySolanaOrphanWindow = 30 * time.Minute
	cryptoPayCommitGrace        = 5 * time.Second
)

var errCryptoPayCursorConflict = errors.New("crypto pay scan cursor changed concurrently")

type cryptoPayCursor struct {
	scope       string
	position    string
	version     int64
	lastScanned sql.NullTime // last time the cursor reached the head
	lastAttempt sql.NullTime
	exists      bool
}

// dueAt is the unit's place in the fair queue (zero = never attempted).
func (c *cryptoPayCursor) dueAt(active bool) time.Time {
	if !c.lastAttempt.Valid {
		return time.Time{}
	}
	if active || !c.lastScanned.Valid {
		return c.lastAttempt.Time
	}
	return c.lastAttempt.Time.Add(cryptoPayLongInterval)
}

func cryptoPayEVMCursorScope(network chain.Network, address string) string {
	return "evm:" + string(network) + ":fin:" + address
}

// cryptoPaySolanaCursorScope binds a persisted window to (network, mint,
// token account): a window is only ever resumed for the same triple.
func cryptoPaySolanaCursorScope(network chain.Network, mint, tokenAccount string) string {
	return "sol:" + string(network) + ":" + mint + ":" + tokenAccount
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
		SELECT scope_key, position, version, last_scanned_at, last_attempt_at
		FROM crypto_pay_scan_cursors WHERE scope_key = ANY($1)`, pq.Array(scopes))
	if err != nil {
		return nil, fmt.Errorf("load scan cursors: %w", err)
	}
	for rows.Next() {
		c := &cryptoPayCursor{exists: true}
		if err := rows.Scan(&c.scope, &c.position, &c.version, &c.lastScanned, &c.lastAttempt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[c.scope] = c
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// advanceCursor stores a new position with compare-and-swap on version. It is
// called only after the scanned range was fully ingested, so a lost race or a
// crash merely repeats an idempotent scan. caughtUp=false (chunk below the
// finalized head) leaves last_scanned_at NULL so the unit stays due.
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
				last_scanned_at = CASE WHEN $4 THEN NOW() END, updated_at = NOW()
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

// markAttempt records that the units were tried (moving them to the back of
// the queue). outcome nil = success (clears last_error); a CAS conflict is
// benign (logged, last_error kept); anything else is stored in last_error and
// shows up in reconciliation. It uses its own budget so it still runs when
// the attempt used up its deadline.
func (s *CryptoPayService) markAttempt(ctx context.Context, network chain.Network, scopes []string, outcome error) {
	if len(scopes) == 0 {
		return
	}
	var msg sql.NullString
	switch {
	case outcome == nil:
		msg = sql.NullString{String: "", Valid: true}
	case errors.Is(outcome, errCryptoPayCursorConflict):
		cryptoPayLogf("scan cursor conflict on %d %s unit(s); another writer advanced it", len(scopes), network)
	default:
		text := outcome.Error()
		if len(text) > 500 {
			text = text[:500]
		}
		msg = sql.NullString{String: text, Valid: true}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO crypto_pay_scan_cursors (scope_key, network, last_attempt_at, last_error)
		SELECT unnest($1::text[]), $2, NOW(), COALESCE($3, '')
		ON CONFLICT (scope_key) DO UPDATE SET last_attempt_at = NOW(),
			last_error = COALESCE($3, crypto_pay_scan_cursors.last_error)`,
		pq.Array(scopes), string(network), msg); err != nil {
		cryptoPayLogf("record scan attempt for %s failed: %v", network, err)
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

// ScanOnce runs one scan pass over every configured network that has a scanner.
func (s *CryptoPayService) ScanOnce(ctx context.Context) error {
	return s.scanNetworks(ctx)
}

// scanNetworks scans each network: at most unitsPerPass units, each with the
// full unit timeout. ctx only ends on shutdown.
func (s *CryptoPayService) scanNetworks(ctx context.Context) error {
	var errs []error
	orphan := func(network chain.Network, window time.Duration) {
		octx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, err := s.OrphanStaleObservations(octx, network, window); err != nil {
			errs = append(errs, err)
		}
	}
	if sc := s.evmScanner(); sc != nil {
		if err := s.scanEVM(ctx, sc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sc.Network(), err))
		}
		orphan(sc.Network(), cryptoPayEVMOrphanWindow)
	}
	if sc := s.solanaScanner(); sc != nil {
		if err := s.scanSolana(ctx, sc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sc.Network(), err))
		}
		orphan(sc.Network(), cryptoPaySolanaOrphanWindow)
	}
	return errors.Join(errs...)
}

// runUnit runs one scan unit with the full unit timeout and records the
// attempt, unless the pass itself was cancelled (shutdown): an interrupted
// unit did not get its full slice and keeps its place in the queue.
//
// fn gets two contexts: unitCtx for chain calls and ingestion, and commitCtx
// for cursor writes. commitCtx is one shared budget per unit: it outlives the
// unit deadline by cryptoPayCommitGrace so a write after a successful ingest
// is not lost to that deadline, and on shutdown (parent cancelled) it ends
// at most cryptoPayCommitGrace later, so a unit delays shutdown by at most the
// grace. Cursors not written in time are simply rescanned (ingestion is
// idempotent).
func (s *CryptoPayService) runUnit(ctx context.Context, network chain.Network, scopes []string, fn func(unitCtx, commitCtx context.Context) error) error {
	deadline := time.Now().Add(s.batchTimeout)
	unitCtx, cancel := context.WithDeadline(ctx, deadline)
	commitCtx, commitCancel := context.WithDeadline(context.WithoutCancel(ctx), deadline.Add(cryptoPayCommitGrace))
	var graceTimer *time.Timer
	var graceMu sync.Mutex
	stopWatch := context.AfterFunc(ctx, func() {
		graceMu.Lock()
		defer graceMu.Unlock()
		graceTimer = time.AfterFunc(cryptoPayCommitGrace, commitCancel)
	})
	err := fn(unitCtx, commitCtx)
	stopWatch()
	graceMu.Lock()
	if graceTimer != nil {
		graceTimer.Stop()
	}
	graceMu.Unlock()
	commitCancel()
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.markAttempt(ctx, network, scopes, err)
	if errors.Is(err, errCryptoPayCursorConflict) {
		return nil
	}
	return err
}

type cryptoPayEVMScanItem struct {
	addr   cryptoPayScanAddress
	cursor *cryptoPayCursor
	from   uint64 // next block to scan
	due    time.Time
}

type cryptoPayEVMBatch struct {
	items []*cryptoPayEVMScanItem
	due   time.Time
}

func (b *cryptoPayEVMBatch) scopes() []string {
	out := make([]string, len(b.items))
	for i, it := range b.items {
		out[i] = it.cursor.scope
	}
	return out
}

// scanEVM runs the fair queue of finalized-log batches, then the unfinalized
// tail for active addresses with whatever budget is left (display only).
func (s *CryptoPayService) scanEVM(ctx context.Context, sc chain.EVMScanner) error {
	network := sc.Network()
	head := func() (uint64, uint64, error) {
		hctx, cancel := context.WithTimeout(ctx, s.batchTimeout)
		defer cancel()
		finalized, err := sc.FinalizedBlock(hctx)
		if err != nil {
			return 0, 0, fmt.Errorf("finalized block: %w", err)
		}
		latest, err := sc.LatestBlock(hctx)
		if err != nil {
			return 0, 0, fmt.Errorf("latest block: %w", err)
		}
		return finalized, latest, nil
	}
	finalized, latest, err := head()
	if err != nil {
		return err
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
	var items []*cryptoPayEVMScanItem
	var active []string
	for i, a := range addrs {
		c := cursors[scopes[i]]
		if a.active {
			active = append(active, a.address)
		}
		due := c.dueAt(a.active)
		if due.After(now) {
			continue
		}
		var from uint64
		switch {
		case c.position != "":
			last, perr := strconv.ParseUint(c.position, 10, 64)
			if perr != nil {
				cryptoPayLogf("ALERT invalid EVM cursor %s=%q; address skipped", c.scope, c.position)
				continue
			}
			from = last + 1
		case a.startBlock.Valid:
			from = uint64(a.startBlock.Int64)
		case s.evmScanFromBlock > 0:
			from = s.evmScanFromBlock
		default:
			cryptoPayLogf("ALERT %s address %s has no scan start height; set crypto_pay.evm_scan_from_block", network, a.address)
			continue
		}
		items = append(items, &cryptoPayEVMScanItem{addr: a, cursor: c, from: from, due: due})
	}
	// Group addresses with close start blocks, then order batches by due time.
	sort.Slice(items, func(i, j int) bool { return items[i].from < items[j].from })
	var batches []*cryptoPayEVMBatch
	for start := 0; start < len(items); {
		end := start + 1
		for end < len(items) && end-start < cryptoPayEVMBatchAddresses && items[end].from-items[start].from < cryptoPayEVMGroupSpread {
			end++
		}
		b := &cryptoPayEVMBatch{items: items[start:end], due: items[start].due}
		for _, it := range b.items {
			if it.due.Before(b.due) {
				b.due = it.due
			}
		}
		batches = append(batches, b)
		start = end
	}
	sort.SliceStable(batches, func(i, j int) bool { return batches[i].due.Before(batches[j].due) })
	if len(batches) > s.unitsPerPass {
		batches = batches[:s.unitsPerPass]
	}
	var errs []error
	for _, b := range batches {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := s.runUnit(ctx, network, b.scopes(), func(unitCtx, commitCtx context.Context) error {
			return s.scanEVMBatch(unitCtx, commitCtx, sc, b, finalized)
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	// Unfinalized tail for active addresses: display only ("confirming"). At
	// most confirmingPerPass groups of 100 per pass, rotated round-robin.
	if latest > finalized && len(active) > 0 {
		groups := (len(active) + cryptoPayEVMBatchAddresses - 1) / cryptoPayEVMBatchAddresses
		first := s.confirmingOffset(network, groups)
		attempted := 0
		defer func() { s.advanceConfirmingOffset(network, groups, attempted) }()
		for g := 0; g < min(groups, s.confirmingPerPass) && ctx.Err() == nil; g++ {
			attempted++
			start := ((first + g) % groups) * cryptoPayEVMBatchAddresses
			end := min(start+cryptoPayEVMBatchAddresses, len(active))
			tailCtx, cancel := context.WithTimeout(ctx, s.batchTimeout)
			scanErr := s.scanEVMConfirmingTail(tailCtx, sc, active[start:end], finalized, latest)
			cancel()
			if scanErr != nil {
				// A failing group must not keep the others from their turn.
				errs = append(errs, fmt.Errorf("unfinalized tail: %w", scanErr))
			}
		}
	}
	return errors.Join(errs...)
}

// cryptoPayEVMConfirmingMaxBlocks caps the unfinalized window shown as
// "confirming" (about 2 h of Arbitrum blocks). Arbitrum's finalized tag lags
// latest by ~20 min (≈5000 blocks), so the window must span far more than one
// getLogs chunk or new transfers stay invisible until shortly before credit.
const cryptoPayEVMConfirmingMaxBlocks = 30_000

// scanEVMConfirmingTail records display-only observations for (finalized,
// latest], newest window capped at cryptoPayEVMConfirmingMaxBlocks, in chunks
// of evmBlocksPerScan walking from latest backwards so a timeout still shows
// the newest transfers. It never moves cursors; credit still waits for the
// finalized scan.
func (s *CryptoPayService) scanEVMConfirmingTail(ctx context.Context, sc chain.EVMScanner, addresses []string, finalized, latest uint64) error {
	if latest <= finalized {
		return nil
	}
	low := finalized + 1
	if latest-finalized > cryptoPayEVMConfirmingMaxBlocks {
		low = latest - cryptoPayEVMConfirmingMaxBlocks + 1
	}
	step := s.evmBlocksPerScan
	if step == 0 {
		step = 1
	}
	to := latest
	for {
		from := low
		if to-low >= step {
			from = to - step + 1
		}
		obs, err := sc.ScanRange(ctx, from, to, addresses, false)
		if err != nil {
			return err
		}
		for i := range obs {
			obs[i].Finalized = false
		}
		if _, err := s.IngestObservations(ctx, obs); err != nil {
			return err
		}
		if from == low {
			return nil
		}
		to = from - 1
	}
}

// scanEVMBatch walks one batch forward in chunks of evmBlocksPerScan blocks.
// Each chunk is ingested and its cursors advanced before the next one, so a
// timeout keeps every completed chunk and the next attempt resumes after it.
func (s *CryptoPayService) scanEVMBatch(ctx, commitCtx context.Context, sc chain.EVMScanner, b *cryptoPayEVMBatch, finalized uint64) error {
	network := sc.Network()
	for {
		from := uint64(0)
		first := true
		for _, it := range b.items {
			if first || it.from < from {
				from, first = it.from, false
			}
		}
		if from > finalized {
			return nil // at the finalized head
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		span := s.adaptiveSize(b.scopes(), s.evmBlocksPerScan)
		to := finalized
		if to-from >= span {
			to = from + span - 1
		}
		var list []string
		for _, it := range b.items {
			if it.from <= to {
				list = append(list, it.addr.address)
			}
		}
		obs, err := sc.ScanRange(ctx, from, to, list, true)
		if err != nil {
			s.adaptiveFeedback(b.scopes(), s.evmBlocksPerScan, cryptoPayTimeoutCause(ctx, err))
			return fmt.Errorf("scan %d-%d: %w", from, to, err)
		}
		for i := range obs {
			if !obs[i].Finalized {
				return fmt.Errorf("scanner returned an unfinalized log for finalized range %d-%d", from, to)
			}
		}
		if _, err := s.IngestObservations(ctx, obs); err != nil {
			s.adaptiveFeedback(b.scopes(), s.evmBlocksPerScan, cryptoPayTimeoutCause(ctx, err))
			return fmt.Errorf("ingest %d-%d: %w", from, to, err)
		}
		s.adaptiveFeedback(b.scopes(), s.evmBlocksPerScan, nil)
		position := strconv.FormatUint(to, 10)
		for _, it := range b.items {
			if it.from > to {
				continue
			}
			if err := s.advanceCursor(commitCtx, network, it.cursor, position, to == finalized); err != nil {
				return err
			}
			it.from = to + 1
		}
	}
}

type cryptoPaySolanaUnit struct {
	owner, account, scope string
	asset                 chain.Asset
	active                bool
	cursor                *cryptoPayCursor
	due                   time.Time
}

// scanSolana runs the fair queue of the users' USDC/USDT associated token
// accounts; each unit is one window walk (see scanSolanaAccount).
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
	var units []*cryptoPaySolanaUnit
	var scopes []string
	for _, a := range addrs {
		for _, asset := range n.Assets {
			account, aerr := ata(a.address, asset.Contract, asset.TokenProgram)
			if aerr != nil || !cryptoPayBase58Re.MatchString(account) {
				cryptoPayLogf("ALERT ATA for %s/%s failed: %v", a.address, asset.Symbol, aerr)
				continue
			}
			scope := cryptoPaySolanaCursorScope(network, asset.Contract, account)
			units = append(units, &cryptoPaySolanaUnit{owner: a.address, account: account, asset: asset, active: a.active, scope: scope})
			scopes = append(scopes, scope)
		}
	}
	cursors, err := s.loadCursors(ctx, scopes)
	if err != nil {
		return err
	}
	now := s.now()
	due := units[:0]
	for _, u := range units {
		u.cursor = cursors[u.scope]
		u.due = u.cursor.dueAt(u.active)
		if !u.due.After(now) {
			due = append(due, u)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].due.Before(due[j].due) })
	if len(due) > s.unitsPerPass {
		due = due[:s.unitsPerPass]
	}
	var errs []error
	for _, u := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := s.runUnit(ctx, network, []string{u.scope}, func(unitCtx, commitCtx context.Context) error {
			return s.scanSolanaAccount(unitCtx, commitCtx, sc, u)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u.account, err))
		}
	}
	return errors.Join(errs...)
}

// cryptoPayParseSolanaWindow reads a persisted Solana cursor. The feature has
// never been deployed with another format: position is always the JSON of a
// chain.SolanaWindow ("" = no window yet). Anything else is an error that
// stays visible in last_error; it is never silently rescanned from scratch.
func cryptoPayParseSolanaWindow(position string) (chain.SolanaWindow, error) {
	var w chain.SolanaWindow
	if position == "" {
		return w, nil
	}
	if strings.TrimSpace(position) == "null" {
		return w, errors.New("invalid Solana scan window cursor: null")
	}
	dec := json.NewDecoder(strings.NewReader(position))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return w, fmt.Errorf("invalid Solana scan window cursor: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return w, errors.New("invalid Solana scan window cursor: trailing data")
	}
	return w, nil
}

func cryptoPayEncodeSolanaWindow(w chain.SolanaWindow) (string, error) {
	b, err := json.Marshal(w)
	return string(b), err
}

// scanSolanaAccount walks the account with chain.ScanTokenAccountWindow: one
// signature page (at most solanaPageSize transactions) per call. After each
// successful call the observations are ingested first, then the returned
// window is persisted verbatim with CAS, so a restart or another instance
// resumes exactly there. The loop stops when a call changes nothing (window
// closed, no new signatures: caught up) or the unit timeout ends; a timeout
// after progress is not a failure (the unit stays due and resumes next pass).
func (s *CryptoPayService) scanSolanaAccount(ctx, commitCtx context.Context, sc chain.SolanaScanner, u *cryptoPaySolanaUnit) error {
	c := u.cursor
	w, err := cryptoPayParseSolanaWindow(c.position)
	if err != nil {
		return err
	}
	progressed := false
	scopes := []string{u.scope}
	defSize := uint64(s.solanaPageSize)
	// Only the expected pause at the unit deadline, after progress, is benign.
	// A page that could not complete even with the full slice halves the page
	// size for this account, so a slow database cannot replay it forever.
	paused := func(err error) error {
		if progressed && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil
		}
		s.adaptiveFeedback(scopes, defSize, cryptoPayTimeoutCause(ctx, err))
		return err
	}
	for {
		pageSize := int(s.adaptiveSize(scopes, defSize))
		obs, next, err := sc.ScanTokenAccountWindow(ctx, u.owner, u.account, u.asset, w, "finalized", pageSize)
		if err != nil {
			return paused(fmt.Errorf("scan: %w", err))
		}
		for i := range obs {
			if !obs[i].Finalized {
				return fmt.Errorf("scanner returned an unfinalized inflow at finalized commitment")
			}
		}
		if _, err := s.IngestObservations(ctx, obs); err != nil {
			return paused(fmt.Errorf("ingest: %w", err))
		}
		caughtUp := next == w && next.Anchor == ""
		position, err := cryptoPayEncodeSolanaWindow(next)
		if err != nil {
			return err
		}
		// A cursor write failure is always returned (recorded, visible).
		if err := s.advanceCursor(commitCtx, sc.Network(), c, position, caughtUp); err != nil {
			return err
		}
		s.adaptiveFeedback(scopes, defSize, nil)
		progressed = true
		w = next
		if caughtUp {
			break
		}
	}
	if !u.active {
		return nil
	}
	// Confirmed-but-not-final inflows: display only; failures do not matter
	// for crediting and are retried next attempt.
	pending, _, err := sc.ScanTokenAccount(ctx, u.owner, u.account, u.asset, w.Until, "confirmed")
	if err != nil {
		cryptoPayLogf("confirmed scan of %s failed: %v", u.account, err)
		return nil
	}
	for i := range pending {
		pending[i].Finalized = false
	}
	if _, err := s.IngestObservations(ctx, pending); err != nil {
		cryptoPayLogf("ingest confirmed inflows of %s failed: %v", u.account, err)
	}
	return nil
}

// Adaptive work size per scope (EVM span in blocks, Solana page size in
// transactions): a chunk/page that times out before completing halves it
// (min 1); cryptoPayEVMSpanResetStreak consecutive successes restore the
// configured value. A size-1 unit that still times out keeps failing visibly
// (last_error) and is retried every pass.
const cryptoPayEVMSpanResetStreak = 3

type cryptoPayEVMSpan struct {
	span   uint64
	streak int
}

func (s *CryptoPayService) adaptiveSize(scopes []string, def uint64) uint64 {
	s.spanMu.Lock()
	defer s.spanMu.Unlock()
	span := def
	for _, sc := range scopes {
		if st, ok := s.evmSpans[sc]; ok && st.span < span {
			span = st.span
		}
	}
	return span
}

func (s *CryptoPayService) adaptiveFeedback(scopes []string, def uint64, err error) {
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return // only timeouts shrink the span
	}
	s.spanMu.Lock()
	defer s.spanMu.Unlock()
	for _, sc := range scopes {
		st, ok := s.evmSpans[sc]
		if !ok {
			st = &cryptoPayEVMSpan{span: def}
		}
		if err != nil {
			st.span /= 2
			if st.span < 1 {
				st.span = 1
			}
			st.streak = 0
			s.evmSpans[sc] = st
			continue
		}
		if !ok {
			continue
		}
		st.streak++
		if st.streak >= cryptoPayEVMSpanResetStreak {
			delete(s.evmSpans, sc)
		}
	}
}

// cryptoPayTimeoutCause reports err as a timeout when the unit deadline hit,
// whatever error type the driver or RPC client surfaced for it.
func cryptoPayTimeoutCause(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}

// confirmingOffset is the group the confirming scan starts at (in-memory
// round-robin; display only, resets on restart).
func (s *CryptoPayService) confirmingOffset(network chain.Network, groups int) int {
	s.spanMu.Lock()
	defer s.spanMu.Unlock()
	return s.confirmOffsets[network] % groups
}

// advanceConfirmingOffset moves the round-robin past the groups actually
// attempted this pass (failed ones included), so every group gets a turn.
func (s *CryptoPayService) advanceConfirmingOffset(network chain.Network, groups, attempted int) {
	s.spanMu.Lock()
	defer s.spanMu.Unlock()
	s.confirmOffsets[network] = (s.confirmOffsets[network]%groups + attempted) % groups
}
