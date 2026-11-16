package chain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/mr-tron/base58"
)

// Solana commitments accepted by the scanner.
const (
	CommitmentFinalized = "finalized"
	CommitmentConfirmed = "confirmed"
)

// SolanaScannerConfig configures a Solana JSON-RPC scanner.
type SolanaScannerConfig struct {
	Network Network
	RPC     RPCOptions
	// Assets is the token whitelist. nil = MainnetAssets() rows of Network.
	// Required (non-nil) for networks without mainnet assets (e.g. devnet).
	Assets []Asset
	// PageLimit for getSignaturesForAddress (1..1000, default 1000).
	PageLimit int
	// MaxPages bounds one ScanTokenAccount walk (default 1000 pages); hitting
	// it is an error, never a truncated success.
	MaxPages int
	// MaxSupportedTransactionVersion passed to getTransaction (default 1).
	// Mainnet already carries version-1 transactions (verified 2026-11-16);
	// with 0 the node rejects them with -32015 and the scan would stall.
	MaxSupportedTransactionVersion *int
}

// SolanaRPCScanner implements SolanaScanner over plain JSON-RPC.
type SolanaRPCScanner struct {
	network    Network
	rpc        *rpcClient
	assets     []Asset
	pageLimit  int
	maxPages   int
	maxVersion int
}

var _ SolanaScanner = (*SolanaRPCScanner)(nil)

// NewSolanaScanner builds a Solana scanner.
func NewSolanaScanner(cfg SolanaScannerConfig) (*SolanaRPCScanner, error) {
	if cfg.Network.Family() != FamilySolana {
		return nil, fmt.Errorf("chain: %s is not a Solana network", clip(string(cfg.Network)))
	}
	assets := cfg.Assets
	if assets == nil {
		assets = assetsForNetwork(MainnetAssets(), cfg.Network)
	}
	if len(assets) == 0 {
		return nil, fmt.Errorf("chain: no whitelisted assets for %s", cfg.Network)
	}
	seen := map[string]struct{}{}
	for _, a := range assets {
		if a.Network != cfg.Network {
			return nil, fmt.Errorf("chain: asset %s is for %s, scanner is %s", a.Symbol, a.Network, cfg.Network)
		}
		if _, err := DecodeSolanaPubkey(a.Contract); err != nil {
			return nil, fmt.Errorf("chain: asset %s mint: %w", a.Symbol, err)
		}
		if _, err := DecodeSolanaPubkey(a.TokenProgram); err != nil {
			return nil, fmt.Errorf("chain: asset %s token program: %w", a.Symbol, err)
		}
		if _, dup := seen[a.Contract]; dup {
			return nil, fmt.Errorf("chain: duplicate asset mint %s", a.Contract)
		}
		seen[a.Contract] = struct{}{}
	}
	s := &SolanaRPCScanner{
		network:    cfg.Network,
		assets:     append([]Asset(nil), assets...),
		pageLimit:  cfg.PageLimit,
		maxPages:   cfg.MaxPages,
		maxVersion: 1,
	}
	if s.pageLimit <= 0 || s.pageLimit > 1000 {
		s.pageLimit = 1000
	}
	if s.maxPages <= 0 {
		s.maxPages = 1000
	}
	if cfg.MaxSupportedTransactionVersion != nil {
		s.maxVersion = *cfg.MaxSupportedTransactionVersion
	}
	rpc, err := newRPCClient(cfg.RPC)
	if err != nil {
		return nil, err
	}
	s.rpc = rpc
	return s, nil
}

// Network implements SolanaScanner.
func (s *SolanaRPCScanner) Network() Network { return s.network }

func (s *SolanaRPCScanner) whitelisted(mint, program string) (Asset, bool) {
	for _, a := range s.assets {
		if a.Contract == mint && a.TokenProgram == program {
			return a, true
		}
	}
	return Asset{}, false
}

func validateCommitment(c string) error {
	if c != CommitmentFinalized && c != CommitmentConfirmed {
		return fmt.Errorf("chain: unsupported commitment %s (want finalized or confirmed)", clip(c))
	}
	return nil
}

type rpcSignatureInfo struct {
	Signature string          `json:"signature"`
	Slot      uint64          `json:"slot"`
	Err       json.RawMessage `json:"err"`
}

func (s *SolanaRPCScanner) prepareTokenAccountScan(owner, tokenAccount string, asset Asset, commitment string) (Asset, error) {
	if err := validateCommitment(commitment); err != nil {
		return Asset{}, err
	}
	if _, err := DecodeSolanaPubkey(owner); err != nil {
		return Asset{}, err
	}
	if _, err := DecodeSolanaPubkey(tokenAccount); err != nil {
		return Asset{}, err
	}
	if asset.Network != s.network {
		return Asset{}, fmt.Errorf("chain: asset network %s != scanner %s", asset.Network, s.network)
	}
	wl, ok := s.whitelisted(asset.Contract, asset.TokenProgram)
	if !ok {
		return Asset{}, fmt.Errorf("chain: asset %s/%s is not whitelisted", clip(asset.Contract), clip(asset.TokenProgram))
	}
	return wl, nil
}

// listSignaturesSince returns every signature of tokenAccount newer than
// untilSig (exclusive), newest first, exactly in the node's order (same-slot
// order preserved). Listing is cheap; transactions are not fetched here.
func (s *SolanaRPCScanner) listSignaturesSince(ctx context.Context, tokenAccount, untilSig, commitment string) ([]rpcSignatureInfo, error) {
	var sigs []rpcSignatureInfo
	before := ""
	for page := 0; ; page++ {
		if page >= s.maxPages {
			return nil, fmt.Errorf("chain: %s has more than %d signature pages; raise MaxPages", tokenAccount, s.maxPages)
		}
		opts := map[string]any{"limit": s.pageLimit, "commitment": commitment}
		if before != "" {
			opts["before"] = before
		}
		if untilSig != "" {
			opts["until"] = untilSig
		}
		raw, err := s.rpc.call(ctx, "getSignaturesForAddress", []any{tokenAccount, opts})
		if err != nil {
			return nil, err
		}
		if isJSONNull(raw) {
			// null is not "end of history": failing here keeps the caller's
			// watermark where it was instead of skipping older signatures.
			return nil, errors.New("chain: getSignaturesForAddress returned null")
		}
		var batch []rpcSignatureInfo
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, fmt.Errorf("chain: getSignaturesForAddress: %w", err)
		}
		reachedUntil := false
		for _, si := range batch {
			if si.Signature == "" {
				return nil, errors.New("chain: getSignaturesForAddress returned an empty signature")
			}
			if untilSig != "" && si.Signature == untilSig {
				reachedUntil = true
				break
			}
			sigs = append(sigs, si)
		}
		if reachedUntil || len(batch) < s.pageLimit {
			return sigs, nil
		}
		before = batch[len(batch)-1].Signature
	}
}

// scanSignature fetches one listed signature and returns the inflow to
// tokenAccount it caused, or nil.
func (s *SolanaRPCScanner) scanSignature(ctx context.Context, si rpcSignatureInfo, owner, tokenAccount string, wl Asset, commitment string) (*Observation, error) {
	if !isJSONNull(si.Err) {
		return nil, nil // failed transaction
	}
	tx, err := s.getTransaction(ctx, si.Signature, commitment)
	if err != nil {
		return nil, err
	}
	if !isJSONNull(tx.Meta.Err) {
		return nil, nil
	}
	deltas, err := tokenDeltas(tx)
	if err != nil {
		return nil, fmt.Errorf("chain: tx %s: %w", si.Signature, err)
	}
	d, ok := deltas[tokenAccount]
	if ok && d.bad != nil {
		return nil, fmt.Errorf("chain: tx %s: %w", si.Signature, d.bad)
	}
	if !ok || d.mint != wl.Contract || d.program != wl.TokenProgram || d.owner != owner || d.delta.Sign() <= 0 {
		return nil, nil
	}
	o := s.observation(tx, si.Signature, wl, owner, tokenAccount, d, deltas, commitment)
	return &o, nil
}

// ScanTokenAccount implements SolanaScanner. Observations are returned oldest
// first. newestSig is the newest signature returned by the node at this
// commitment; when there is nothing newer than untilSig it is untilSig itself
// (so storing it unconditionally never rewinds a watermark), and "" only when
// the account has no history at all and untilSig was "".
//
// Prefer ScanTokenAccountWindow for production scanning: this walk processes
// every new signature in one call and can be stalled by dust spam.
func (s *SolanaRPCScanner) ScanTokenAccount(ctx context.Context, owner, tokenAccount string, asset Asset, untilSig string, commitment string) ([]Observation, string, error) {
	wl, err := s.prepareTokenAccountScan(owner, tokenAccount, asset, commitment)
	if err != nil {
		return nil, "", err
	}
	sigs, err := s.listSignaturesSince(ctx, tokenAccount, untilSig, commitment)
	if err != nil {
		return nil, "", err
	}
	newest := untilSig
	if len(sigs) > 0 {
		newest = sigs[0].Signature
	}
	var out []Observation
	for i := len(sigs) - 1; i >= 0; i-- { // oldest first
		o, err := s.scanSignature(ctx, sigs[i], owner, tokenAccount, wl, commitment)
		if err != nil {
			return nil, "", err
		}
		if o != nil {
			out = append(out, *o)
		}
	}
	return out, newest, nil
}

// ScanTokenAccountWindow implements SolanaScanner: a stateless, bounded,
// caller-persisted walk. See SolanaWindow. Intended for commitment
// "finalized" (the window is anchored on signatures that must not vanish);
// other commitments are rejected.
//
// One call = one getSignaturesForAddress page (limit=maxTx, until=w.Until,
// before=w.Before) plus one getTransaction per listed signature that did not
// already fail in the listing. Within the window the walk goes newest to
// older; observations of one call are returned oldest first.
//
//   - Empty page, no anchor: nothing new; next == w.
//   - Empty page with an anchor: the window is complete; next = {Until: Anchor}.
//   - Any non-empty page (full or short): next = {Until: w.Until,
//     Anchor: anchor, Before: last sig}. A short page does not close the
//     window, because nodes may return fewer than `limit` signatures while
//     older ones exist; the following (empty) page closes it.
//   - Any error: (nil, w, err); the caller keeps its window.
func (s *SolanaRPCScanner) ScanTokenAccountWindow(ctx context.Context, owner, tokenAccount string, asset Asset, w SolanaWindow, commitment string, maxTx int) ([]Observation, SolanaWindow, error) {
	if commitment != CommitmentFinalized {
		return nil, w, fmt.Errorf("chain: ScanTokenAccountWindow requires commitment %q", CommitmentFinalized)
	}
	if maxTx <= 0 || maxTx > 1000 {
		return nil, w, fmt.Errorf("chain: maxTx must be in 1..1000, got %d", maxTx)
	}
	if (w.Anchor == "") != (w.Before == "") {
		return nil, w, errors.New("chain: window must have both Anchor and Before, or neither")
	}
	for _, f := range []string{w.Until, w.Anchor, w.Before} {
		if f == "" {
			continue
		}
		if _, err := decodeSignature(f); err != nil {
			return nil, w, fmt.Errorf("chain: window field: %w", err)
		}
	}
	wl, err := s.prepareTokenAccountScan(owner, tokenAccount, asset, commitment)
	if err != nil {
		return nil, w, err
	}
	opts := map[string]any{"limit": maxTx, "commitment": commitment}
	if w.Until != "" {
		opts["until"] = w.Until
	}
	if w.Before != "" {
		opts["before"] = w.Before
	}
	raw, err := s.rpc.call(ctx, "getSignaturesForAddress", []any{tokenAccount, opts})
	if err != nil {
		return nil, w, err
	}
	if isJSONNull(raw) {
		return nil, w, errors.New("chain: getSignaturesForAddress returned null")
	}
	var page []rpcSignatureInfo
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, w, fmt.Errorf("chain: getSignaturesForAddress: %w", err)
	}
	if len(page) > maxTx {
		return nil, w, fmt.Errorf("chain: getSignaturesForAddress returned %d signatures for limit %d", len(page), maxTx)
	}
	seen := make(map[string]struct{}, len(page))
	for _, si := range page {
		if si.Signature == "" {
			return nil, w, errors.New("chain: getSignaturesForAddress returned an empty signature")
		}
		if si.Signature == w.Until || si.Signature == w.Before || si.Signature == w.Anchor {
			return nil, w, fmt.Errorf("chain: getSignaturesForAddress returned window bound %s inside the page", clip(si.Signature))
		}
		if _, dup := seen[si.Signature]; dup {
			return nil, w, fmt.Errorf("chain: getSignaturesForAddress returned %s twice", clip(si.Signature))
		}
		seen[si.Signature] = struct{}{}
	}
	if len(page) == 0 {
		if w.Anchor == "" {
			return nil, w, nil
		}
		return nil, SolanaWindow{Until: w.Anchor}, nil
	}
	anchor := w.Anchor
	if anchor == "" {
		anchor = page[0].Signature
	}
	var out []Observation
	for i := len(page) - 1; i >= 0; i-- { // oldest first within the page
		o, err := s.scanSignature(ctx, page[i], owner, tokenAccount, wl, commitment)
		if err != nil {
			return nil, w, err
		}
		if o != nil {
			out = append(out, *o)
		}
	}
	// A short page does NOT close the window: nodes may return fewer than
	// `limit` while older signatures still exist. Only an empty page closes it.
	return out, SolanaWindow{Until: w.Until, Anchor: anchor, Before: page[len(page)-1].Signature}, nil
}

// ObservationsForTx implements SolanaScanner: every whitelisted token account
// owned by one of owners whose balance increased in the transaction.
func (s *SolanaRPCScanner) ObservationsForTx(ctx context.Context, signature string, owners []string, commitment string) ([]Observation, error) {
	if err := validateCommitment(commitment); err != nil {
		return nil, err
	}
	ownerSet := make(map[string]struct{}, len(owners))
	for _, o := range owners {
		if _, err := DecodeSolanaPubkey(o); err != nil {
			return nil, err
		}
		ownerSet[o] = struct{}{}
	}
	tx, err := s.getTransaction(ctx, signature, commitment)
	if err != nil {
		return nil, err
	}
	if !isJSONNull(tx.Meta.Err) {
		return nil, nil
	}
	deltas, err := tokenDeltas(tx)
	if err != nil {
		return nil, fmt.Errorf("chain: tx %s: %w", signature, err)
	}
	accounts := make([]string, 0, len(deltas))
	for acct := range deltas {
		accounts = append(accounts, acct)
	}
	sort.Strings(accounts)
	var out []Observation
	for _, acct := range accounts {
		d := deltas[acct]
		if d.bad != nil && !d.identityChanged {
			// Malformed RPC metadata (missing fields, bad amount, duplicates)
			// cannot be produced by a sender; fail so the caller retries
			// instead of silently reporting "no inflow".
			return nil, fmt.Errorf("chain: tx %s: %w", signature, d.bad)
		}
		if _, ok := ownerSet[d.owner]; !ok {
			continue
		}
		asset, ok := s.whitelisted(d.mint, d.program)
		if !ok {
			continue
		}
		if d.bad != nil {
			// mint/owner/program changed inside the tx. Fatal for the owner's
			// canonical ATA; any other account (e.g. one an attacker
			// re-assigned to our owner in the same tx) is not a deposit and
			// must not block the real inflows of this transaction.
			if ata, err := SolanaATA(d.owner, d.mint, d.program); err == nil && ata == acct {
				return nil, fmt.Errorf("chain: tx %s: %w", signature, d.bad)
			}
			continue
		}
		if d.delta.Sign() <= 0 {
			continue
		}
		out = append(out, s.observation(tx, signature, asset, d.owner, acct, d, deltas, commitment))
	}
	return out, nil
}

func (s *SolanaRPCScanner) observation(tx *rpcSolanaTx, sig string, asset Asset, owner, tokenAccount string, d tokenDelta, all map[string]tokenDelta, commitment string) Observation {
	o := Observation{
		Network:      s.network,
		Asset:        asset,
		TxHash:       sig,
		EventKey:     "acct:" + tokenAccount,
		ToAddress:    owner,
		TokenAccount: tokenAccount,
		FromAddress:  senderOwner(all, asset),
		Units:        new(big.Int).Set(d.delta),
		BlockNumber:  tx.Slot,
		Finalized:    commitment == CommitmentFinalized,
	}
	if tx.BlockTime != nil {
		o.BlockTime = time.Unix(*tx.BlockTime, 0).UTC()
	}
	return o
}

// senderOwner picks the owner with the largest decrease of the same mint
// (informational only).
func senderOwner(all map[string]tokenDelta, asset Asset) string {
	best := ""
	var bestDelta *big.Int
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d := all[k]
		if d.bad != nil || d.mint != asset.Contract || d.program != asset.TokenProgram || d.delta.Sign() >= 0 {
			continue
		}
		if bestDelta == nil || d.delta.Cmp(bestDelta) < 0 {
			best, bestDelta = d.owner, d.delta
		}
	}
	return best
}

type rpcSolanaTx struct {
	Slot      uint64 `json:"slot"`
	BlockTime *int64 `json:"blockTime"`
	Meta      *struct {
		Err               json.RawMessage   `json:"err"`
		PreTokenBalances  []rpcTokenBalance `json:"preTokenBalances"`
		PostTokenBalances []rpcTokenBalance `json:"postTokenBalances"`
	} `json:"meta"`
	Transaction struct {
		Signatures []string `json:"signatures"`
		Message    struct {
			AccountKeys []struct {
				Pubkey string `json:"pubkey"`
			} `json:"accountKeys"`
		} `json:"message"`
	} `json:"transaction"`
}

type rpcTokenBalance struct {
	AccountIndex  int    `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	ProgramID     string `json:"programId"`
	UITokenAmount struct {
		Amount string `json:"amount"`
	} `json:"uiTokenAmount"`
}

func (s *SolanaRPCScanner) getTransaction(ctx context.Context, sig, commitment string) (*rpcSolanaTx, error) {
	if _, err := decodeSignature(sig); err != nil {
		return nil, err
	}
	raw, err := s.rpc.call(ctx, "getTransaction", []any{sig, map[string]any{
		"commitment":                     commitment,
		"maxSupportedTransactionVersion": s.maxVersion,
		"encoding":                       "jsonParsed",
	}})
	if err != nil {
		return nil, err
	}
	if isJSONNull(raw) {
		// Not yet visible at this commitment on this node: retry later. Never
		// treat as "no inflow".
		return nil, fmt.Errorf("%w: getTransaction(%s) returned null at %s", ErrTxNotFound, sig, commitment)
	}
	var tx rpcSolanaTx
	if err := json.Unmarshal(raw, &tx); err != nil {
		return nil, fmt.Errorf("chain: getTransaction(%s): %w", sig, err)
	}
	if tx.Meta == nil {
		return nil, fmt.Errorf("chain: getTransaction(%s): missing meta", sig)
	}
	if len(tx.Transaction.Signatures) == 0 || tx.Transaction.Signatures[0] != sig {
		return nil, fmt.Errorf("chain: getTransaction(%s): response is for a different transaction", sig)
	}
	if len(tx.Transaction.Message.AccountKeys) == 0 {
		return nil, fmt.Errorf("chain: getTransaction(%s): no account keys (jsonParsed expected)", sig)
	}
	return &tx, nil
}

type tokenDelta struct {
	mint, owner, program string
	delta                *big.Int
	// bad is set when this account's balance entries cannot be interpreted
	// (identity changed within the tx, missing fields, duplicates). It is only
	// an error when the account is one we care about, so an unrelated account
	// in the same transaction cannot stall our scan.
	bad error
	// identityChanged marks bad as "mint/owner/program differs between pre
	// and post" (a sender can cause this) as opposed to malformed metadata.
	identityChanged bool
}

// tokenDeltas returns post-pre per token account. An account missing from pre
// (created in this tx) counts as 0 before; missing from post (closed) counts
// as 0 after.
func tokenDeltas(tx *rpcSolanaTx) (map[string]tokenDelta, error) {
	// A missing/null balance list is not "no balances": treating a missing
	// pre list as all-zero would turn existing balances into fake inflows.
	if tx.Meta.PreTokenBalances == nil || tx.Meta.PostTokenBalances == nil {
		return nil, errors.New("token balance metadata missing")
	}
	keys := tx.Transaction.Message.AccountKeys
	type entry struct {
		mint, owner, program string
		amount               *big.Int
		bad                  error
	}
	read := func(list []rpcTokenBalance) (map[string]entry, error) {
		m := make(map[string]entry, len(list))
		for _, b := range list {
			if b.AccountIndex < 0 || b.AccountIndex >= len(keys) {
				return nil, fmt.Errorf("token balance accountIndex %d out of range (%d keys)", b.AccountIndex, len(keys))
			}
			acct := keys[b.AccountIndex].Pubkey
			e := entry{mint: b.Mint, owner: b.Owner, program: b.ProgramID}
			amt, ok := new(big.Int).SetString(b.UITokenAmount.Amount, 10)
			switch {
			case b.Mint == "" || b.Owner == "" || b.ProgramID == "":
				e.bad = fmt.Errorf("token balance for %s lacks mint/owner/programId", acct)
			case !ok || amt.Sign() < 0:
				e.bad = fmt.Errorf("token balance amount %s for %s is not a non-negative integer", clip(b.UITokenAmount.Amount), clip(acct))
			default:
				e.amount = amt
			}
			if prev, dup := m[acct]; dup {
				prev.bad = fmt.Errorf("duplicate token balance entries for %s", acct)
				m[acct] = prev
				continue
			}
			m[acct] = e
		}
		return m, nil
	}
	pre, err := read(tx.Meta.PreTokenBalances)
	if err != nil {
		return nil, err
	}
	post, err := read(tx.Meta.PostTokenBalances)
	if err != nil {
		return nil, err
	}
	out := make(map[string]tokenDelta, len(post)+len(pre))
	for acct, p := range post {
		d := tokenDelta{mint: p.mint, owner: p.owner, program: p.program, bad: p.bad}
		before := big.NewInt(0)
		if q, ok := pre[acct]; ok {
			switch {
			case p.bad != nil:
				// keep the post-side metadata error; never reclassify it
			case q.bad != nil:
				d.bad = q.bad
			case q.mint != p.mint || q.owner != p.owner || q.program != p.program:
				d.bad = fmt.Errorf("token account %s changed mint/owner/program within the transaction", acct)
				d.identityChanged = true
			default:
				before = q.amount
			}
		}
		if d.bad == nil {
			d.delta = new(big.Int).Sub(p.amount, before)
		}
		out[acct] = d
	}
	for acct, q := range pre {
		if _, ok := post[acct]; ok {
			continue
		}
		d := tokenDelta{mint: q.mint, owner: q.owner, program: q.program, bad: q.bad}
		if d.bad == nil {
			d.delta = new(big.Int).Neg(q.amount)
		}
		out[acct] = d
	}
	return out, nil
}

func decodeSignature(sig string) ([]byte, error) {
	if sig == "" || len(sig) > 90 {
		return nil, fmt.Errorf("chain: invalid Solana signature %s", clip(sig))
	}
	b, err := base58.Decode(sig)
	if err != nil || len(b) != 64 {
		return nil, fmt.Errorf("chain: invalid Solana signature %s", clip(sig))
	}
	return b, nil
}
