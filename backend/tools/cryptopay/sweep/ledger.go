package sweep

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

// LedgerVersion is the only accepted ledger file version.
const LedgerVersion = 1

// MaxLedgerBytes bounds the offline ledger (it keeps every signed raw tx;
// ~10 KB per typical op). Save refuses to write a ledger Load would reject.
const MaxLedgerBytes = 64 << 20

// Op statuses.
const (
	StatusPending  = "pending"
	StatusReplaced = "replaced"
	StatusClosed   = "closed"
)

// Ledger is the offline signing ledger plus the offline registries. It lives
// only on the offline machine (mode 0600) and is the source of truth for
// "what have I already signed".
type Ledger struct {
	Version       int            `json:"version"`
	Ops           []LedgerOp     `json:"ops"`
	NonceAccounts []NonceAccount `json:"nonce_accounts"`
	Destinations  []Destination  `json:"destinations"`
}

// LedgerOp is one signing (sweep or send).
type LedgerOp struct {
	ID         string        `json:"id"`
	Network    chain.Network `json:"network"`
	Kind       Kind          `json:"kind"`
	Status     string        `json:"status"`
	ChainRoot  string        `json:"chain_root"` // first op of the replacement chain (itself if none)
	Replaces   string        `json:"replaces,omitempty"`
	CreatedAt  string        `json:"created_at"`
	ClosedAt   string        `json:"closed_at,omitempty"`
	Summary    string        `json:"summary"`
	Txs        []LedgerTx    `json:"txs"`
	Budget     Budget        `json:"budget"`     // this op alone
	Cumulative Budget        `json:"cumulative"` // whole replacement chain up to and including this op
}

// LedgerTx is one signed transaction of an op.
type LedgerTx struct {
	Role         string `json:"role"`
	Hash         string `json:"hash"`
	Signer       string `json:"signer"`
	Nonce        string `json:"nonce,omitempty"`         // EVM account nonce (decimal)
	NonceAccount string `json:"nonce_account,omitempty"` // Solana durable nonce account
	NonceValue   string `json:"nonce_value,omitempty"`   // Solana durable nonce value
	// Raw is the signed transaction (offline only), so a lost signed.json can
	// be re-exported without signing anything new (ledger export).
	Raw         string `json:"raw"`
	SourceIndex uint32 `json:"source_index,omitempty"`
}

// ExclusionKey identifies the chain resource a tx consumes: at most one tx
// per key can ever execute (EVM signer+nonce, Solana nonce account+value).
func (t LedgerTx) ExclusionKey() string {
	if t.NonceAccount != "" {
		return "sol:" + t.NonceAccount + ":" + t.NonceValue
	}
	return "evm:" + strings.ToLower(t.Signer) + ":" + t.Nonce
}

func (o *LedgerOp) keys() map[string]bool {
	m := make(map[string]bool, len(o.Txs))
	for _, t := range o.Txs {
		m[t.ExclusionKey()] = true
	}
	return m
}

// NonceAccount is a registered Solana durable nonce account.
type NonceAccount struct {
	Address      string `json:"address"`
	RegisteredAt string `json:"registered_at"`
}

// Destination is a registered external address (e.g. an exchange deposit
// address) that send-sign may pay.
type Destination struct {
	Label        string        `json:"label"`
	Network      chain.Network `json:"network"`
	Address      string        `json:"address"` // EVM lowercase 0x; Solana owner base58
	RegisteredAt string        `json:"registered_at"`
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{Version: LedgerVersion, Ops: []LedgerOp{}, NonceAccounts: []NonceAccount{}, Destinations: []Destination{}}
}

// ErrCorruptLedger wraps every structural problem found when loading.
var ErrCorruptLedger = errors.New("sweep: ledger is corrupt; refusing to sign. Never delete it. Restore it from a backup ONLY if you are sure nothing was signed after that backup; otherwise stop and verify every signer's nonce / nonce account on chain independently first")

// DecodeLedger strictly decodes and validates a ledger.
func DecodeLedger(data []byte) (*Ledger, error) {
	var l Ledger
	if err := decodeStrictLimit(data, &l, MaxLedgerBytes); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptLedger, err)
	}
	if err := l.validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptLedger, err)
	}
	return &l, nil
}

func (l *Ledger) validate() error {
	if l.Version != LedgerVersion {
		return fmt.Errorf("version %d", l.Version)
	}
	if l.Ops == nil || l.NonceAccounts == nil || l.Destinations == nil {
		return errors.New("missing section")
	}
	ids := map[string]*LedgerOp{}
	pending := map[chain.Network]int{}
	for i := range l.Ops {
		o := &l.Ops[i]
		if o.ID == "" || ids[o.ID] != nil {
			return fmt.Errorf("op id %s empty or duplicate", clip(o.ID))
		}
		ids[o.ID] = o
		if o.Network != chain.ArbitrumOne && o.Network != chain.SolanaMainnet {
			return fmt.Errorf("op %s network", o.ID)
		}
		if o.Kind != KindSweep && o.Kind != KindSend {
			return fmt.Errorf("op %s kind", o.ID)
		}
		switch o.Status {
		case StatusPending:
			pending[o.Network]++
		case StatusReplaced, StatusClosed:
		default:
			return fmt.Errorf("op %s status %s", o.ID, clip(o.Status))
		}
		if len(o.Txs) == 0 {
			return fmt.Errorf("op %s has no txs", o.ID)
		}
		for _, t := range o.Txs {
			if t.Hash == "" || t.Signer == "" || t.Raw == "" {
				return fmt.Errorf("op %s tx without hash/signer/raw", o.ID)
			}
			if (t.NonceAccount == "") == (t.Nonce == "") {
				return fmt.Errorf("op %s tx needs exactly one of nonce / nonce_account", o.ID)
			}
			if t.NonceAccount != "" && t.NonceValue == "" {
				return fmt.Errorf("op %s tx without nonce_value", o.ID)
			}
			if t.Nonce != "" {
				if _, err := ParseUint64(t.Nonce); err != nil {
					return fmt.Errorf("op %s tx nonce: %v", o.ID, err)
				}
			}
		}
		for _, b := range []Budget{o.Budget, o.Cumulative} {
			if _, err := b.Add(Budget{}); err != nil {
				return fmt.Errorf("op %s budget: %v", o.ID, err)
			}
		}
	}
	for n, c := range pending {
		if c > 1 {
			return fmt.Errorf("%d pending ops on %s", c, n)
		}
	}
	// Replacement chains: linear, ordered (an op appears after the op it
	// replaces), same network, each successor covers every exclusion key of
	// its predecessor, cumulative budgets recomputed exactly, and no
	// exclusion key shared between different chains or within one op.
	replacedBy := map[string]string{}
	keyChain := map[string]string{}
	for i := range l.Ops {
		o := &l.Ops[i]
		root := ids[o.ChainRoot]
		if root == nil || root.Network != o.Network || root.ChainRoot != root.ID || root.Replaces != "" {
			return fmt.Errorf("op %s chain_root invalid", o.ID)
		}
		own := map[string]bool{}
		for _, t := range o.Txs {
			k := t.ExclusionKey()
			if own[k] {
				return fmt.Errorf("op %s uses %s twice", o.ID, k)
			}
			own[k] = true
			if c, ok := keyChain[k]; ok && c != o.ChainRoot {
				return fmt.Errorf("op %s reuses %s of chain %s", o.ID, k, c)
			}
			keyChain[k] = o.ChainRoot
		}
		if o.Replaces == "" {
			if o.ChainRoot != o.ID {
				return fmt.Errorf("op %s has a chain_root but replaces nothing", o.ID)
			}
			if o.Cumulative != o.Budget {
				return fmt.Errorf("op %s cumulative budget != its budget", o.ID)
			}
			continue
		}
		if o.Network.Family() == chain.FamilySolana {
			return fmt.Errorf("op %s: Solana ops are never replaced", o.ID)
		}
		r := ids[o.Replaces]
		if r == nil || r.Status != StatusReplaced || r.ChainRoot != o.ChainRoot || r.Network != o.Network {
			return fmt.Errorf("op %s replaces invalid", o.ID)
		}
		if _, dup := replacedBy[r.ID]; dup {
			return fmt.Errorf("op %s replaced twice", r.ID)
		}
		replacedBy[r.ID] = o.ID
		if idx := opIndex(l, r.ID); idx < 0 || idx >= i {
			return fmt.Errorf("op %s replaces a later op", o.ID)
		}
		for k := range r.keys() {
			if !own[k] {
				return fmt.Errorf("op %s does not cover %s of the op it replaces", o.ID, k)
			}
		}
		want, err := o.Budget.Add(r.Cumulative)
		if err != nil || want != o.Cumulative {
			return fmt.Errorf("op %s cumulative budget does not equal predecessor + own budget", o.ID)
		}
	}
	for i := range l.Ops {
		o := &l.Ops[i]
		if o.Status == StatusReplaced && replacedBy[o.ID] == "" {
			return fmt.Errorf("op %s replaced by nothing", o.ID)
		}
		if o.Status != StatusReplaced && replacedBy[o.ID] != "" {
			return fmt.Errorf("op %s is replaced but not marked so", o.ID)
		}
	}
	seenN := map[string]bool{}
	for _, n := range l.NonceAccounts {
		if _, err := chain.DecodeSolanaPubkey(n.Address); err != nil || seenN[n.Address] {
			return fmt.Errorf("nonce account %s invalid or duplicate", clip(n.Address))
		}
		seenN[n.Address] = true
	}
	if len(l.NonceAccounts) > SolanaMaxRegisteredNonces {
		return errors.New("too many nonce accounts")
	}
	seenD := map[string]bool{}
	for _, d := range l.Destinations {
		if err := validateDestination(d); err != nil || seenD[d.Label] {
			return fmt.Errorf("destination %s invalid or duplicate", clip(d.Label))
		}
		seenD[d.Label] = true
	}
	return nil
}

func opIndex(l *Ledger, id string) int {
	for i := range l.Ops {
		if l.Ops[i].ID == id {
			return i
		}
	}
	return -1
}

func validateDestination(d Destination) error {
	if d.Label == "" || len(d.Label) > 40 {
		return errors.New("label must be 1..40 chars")
	}
	for _, c := range d.Label {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return errors.New("label may only contain letters, digits, - _ .")
		}
	}
	switch d.Network {
	case chain.ArbitrumOne:
		a, err := chain.NormalizeEVMAddress(d.Address)
		if err != nil || a != d.Address {
			return errors.New("EVM address must be lowercase 0x")
		}
		if a == "0x0000000000000000000000000000000000000000" {
			return errors.New("zero address")
		}
	case chain.SolanaMainnet:
		b, err := chain.DecodeSolanaPubkey(d.Address)
		if err != nil {
			return err
		}
		if !chain.IsOnEd25519Curve(b) {
			return errors.New("Solana destination must be a wallet (owner) address, not a token account / PDA")
		}
	default:
		return errors.New("network")
	}
	return nil
}

// Pending returns the pending op of network, or nil.
func (l *Ledger) Pending(network chain.Network) *LedgerOp {
	for i := range l.Ops {
		if l.Ops[i].Network == network && l.Ops[i].Status == StatusPending {
			return &l.Ops[i]
		}
	}
	return nil
}

// Find returns the op with id, or nil.
func (l *Ledger) Find(id string) *LedgerOp {
	for i := range l.Ops {
		if l.Ops[i].ID == id {
			return &l.Ops[i]
		}
	}
	return nil
}

// AdmitResult tells the caller what admitting an op would do.
type AdmitResult struct {
	Replaces   *LedgerOp // the pending op being replaced, or nil
	Cumulative Budget    // budget of the whole replacement chain incl. the new op
}

// CheckAdmit applies the v3 rules without mutating the ledger:
//   - at most one pending op per network;
//   - Solana: no replacement at all while an op is pending (see below);
//   - EVM: with a pending op P, the new op must be a mutually exclusive
//     replacement: its exclusion keys must include every key of P (so for
//     every tx of P a tx of the new op consumes the same nonce);
//   - no exclusion key may appear in any op outside P's replacement chain
//     (global signer+nonce / nonce account+value dedupe);
//   - the new op's own keys are unique.
func (l *Ledger) CheckAdmit(network chain.Network, txs []LedgerTx, budget Budget) (*AdmitResult, error) {
	if len(txs) == 0 {
		return nil, errors.New("sweep: nothing to sign")
	}
	newKeys := map[string]bool{}
	for _, t := range txs {
		k := t.ExclusionKey()
		if newKeys[k] {
			return nil, fmt.Errorf("sweep: two transactions of this op use the same nonce (%s)", k)
		}
		newKeys[k] = true
	}
	p := l.Pending(network)
	if p != nil && network.Family() == chain.FamilySolana {
		// No Solana replacement at all: the offline side cannot prove that a
		// plan's nonce_value is the account's stored durable nonce. An
		// ordinary recent blockhash passes as nonce_value, and two different
		// messages carrying it can both execute (AdvanceNonceAccount does not
		// compare it with the stored nonce). Re-export the original
		// signatures instead, or close: every tx finalized (path A), or
		// signing >= SolanaCloseExpiryWait ago AND every stored nonce moved
		// past the signed value (path B, see ledger close).
		return nil, fmt.Errorf("sweep: op %s is still pending on %s. Solana ops can never be replaced by a new signature.\n"+
			"If the signed file was lost: `ledger export --op %s`. Otherwise broadcast it and run `ledger close --op %s` only when the phone explorer\n"+
			"shows every tx finalized (sweeps only: or 30+ min after signing with every nonce account's stored value different from the signed value)", p.ID, network, p.ID, p.ID)
	}
	for i := range l.Ops {
		o := &l.Ops[i]
		if p != nil && o.ChainRoot == p.ChainRoot {
			continue
		}
		for k := range o.keys() {
			if newKeys[k] {
				return nil, fmt.Errorf("sweep: nonce %s was already used by op %s (%s). If that signature never landed, broadcast its signed file (it stays valid); otherwise re-run prepare to read the current chain state", k, o.ID, o.Status)
			}
		}
	}
	res := &AdmitResult{Cumulative: budget}
	if p != nil {
		for k := range p.keys() {
			if !newKeys[k] {
				return nil, fmt.Errorf("sweep: op %s is still pending on %s and this plan does not replace all of its nonces (missing %s).\n"+
					"Either broadcast it and run `ledger close %s` after checking the explorer, or prepare a plan that reuses the same nonces/sources", p.ID, network, k, p.ID)
			}
		}
		var err error
		if res.Cumulative, err = budget.Add(p.Cumulative); err != nil {
			return nil, err
		}
		res.Replaces = p
	}
	return res, nil
}

// Admit records a new pending op (after CheckAdmit succeeded on the same
// ledger state). It returns the stored op.
func (l *Ledger) Admit(network chain.Network, kind Kind, summary string, txs []LedgerTx, budget Budget, now time.Time) (*LedgerOp, error) {
	res, err := l.CheckAdmit(network, txs, budget)
	if err != nil {
		return nil, err
	}
	id, err := newOpID(network, now)
	if err != nil {
		return nil, err
	}
	op := LedgerOp{
		ID: id, Network: network, Kind: kind, Status: StatusPending, ChainRoot: id,
		CreatedAt: now.UTC().Format(time.RFC3339), Summary: summary, Txs: txs,
		Budget: budget, Cumulative: res.Cumulative,
	}
	if res.Replaces != nil {
		op.ChainRoot = res.Replaces.ChainRoot
		op.Replaces = res.Replaces.ID
		res.Replaces.Status = StatusReplaced
	}
	l.Ops = append(l.Ops, op)
	if err := l.validate(); err != nil {
		return nil, fmt.Errorf("sweep: internal ledger invariant: %w", err)
	}
	return &l.Ops[len(l.Ops)-1], nil
}

// Close marks the pending op id closed (operator confirmed on an explorer).
func (l *Ledger) Close(id string, now time.Time) error {
	o := l.Find(id)
	if o == nil {
		return fmt.Errorf("sweep: no op %s", clip(id))
	}
	if o.Status != StatusPending {
		return fmt.Errorf("sweep: op %s is %s; only the pending op of a chain can be closed", id, o.Status)
	}
	o.Status = StatusClosed
	o.ClosedAt = now.UTC().Format(time.RFC3339)
	return nil
}

// SolanaCloseExpiryWait is how long after signing a Solana sweep may be
// closed on path B. It does NOT prove that an ordinary blockhash expired (the
// network may have halted); B is accepted only because a late old sweep
// signature can only move deposit funds into the treasury.
const SolanaCloseExpiryWait = 30 * time.Minute

// SolanaCloseByExpiryAllowed gates close path B. Elapsed wall time cannot
// prove that an ordinary blockhash expired (the network may have halted), so
// B is only offered for sweeps: if an old sweep signature still lands after
// the close, it only moves deposit funds to the treasury (fees stay within the
// confirmed budgets). A send can only be closed once finalized (path A).
func SolanaCloseByExpiryAllowed(o *LedgerOp, now time.Time) error {
	if o.Kind != KindSweep {
		return fmt.Errorf("sweep: op %s is a send; it can only be closed after every tx shows Finalized (path A)", o.ID)
	}
	created, err := time.Parse(time.RFC3339, o.CreatedAt)
	if err != nil {
		return fmt.Errorf("sweep: op %s has no valid signing time", o.ID)
	}
	if wait := created.Add(SolanaCloseExpiryWait).Sub(now); wait > 0 {
		return fmt.Errorf("sweep: op %s was signed %s ago; path B needs %s (wait %s more, or use path A once every tx is finalized)",
			o.ID, now.Sub(created).Round(time.Second), SolanaCloseExpiryWait, wait.Round(time.Second))
	}
	return nil
}

// RegisterNonceAccount adds a Solana durable nonce account.
func (l *Ledger) RegisterNonceAccount(addr string, now time.Time) error {
	if _, err := chain.DecodeSolanaPubkey(addr); err != nil {
		return err
	}
	for _, n := range l.NonceAccounts {
		if n.Address == addr {
			return fmt.Errorf("sweep: nonce account %s already registered", addr)
		}
	}
	if len(l.NonceAccounts) >= SolanaMaxRegisteredNonces {
		return fmt.Errorf("sweep: at most %d nonce accounts", SolanaMaxRegisteredNonces)
	}
	l.NonceAccounts = append(l.NonceAccounts, NonceAccount{Address: addr, RegisteredAt: now.UTC().Format(time.RFC3339)})
	return nil
}

// IsNonceRegistered reports whether addr is a registered nonce account.
func (l *Ledger) IsNonceRegistered(addr string) bool {
	for _, n := range l.NonceAccounts {
		if n.Address == addr {
			return true
		}
	}
	return false
}

// RegisterDestination adds a labelled external destination.
func (l *Ledger) RegisterDestination(d Destination) error {
	if err := validateDestination(d); err != nil {
		return fmt.Errorf("sweep: destination: %w", err)
	}
	for _, x := range l.Destinations {
		if x.Label == d.Label {
			return fmt.Errorf("sweep: label %s already registered", d.Label)
		}
	}
	l.Destinations = append(l.Destinations, d)
	return nil
}

// Destination returns the registered destination with label.
func (l *Ledger) Destination(label string) (Destination, error) {
	for _, d := range l.Destinations {
		if d.Label == label {
			return d, nil
		}
	}
	return Destination{}, fmt.Errorf("sweep: no registered destination %s (run dest-register first)", clip(label))
}

func newOpID(network chain.Network, now time.Time) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	short := "arb"
	if network == chain.SolanaMainnet {
		short = "sol"
	}
	return short + "-" + now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:]), nil
}

// SortedOps returns ops newest first (for listing).
func (l *Ledger) SortedOps() []LedgerOp {
	out := append([]LedgerOp(nil), l.Ops...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// ---- file storage -------------------------------------------------------

// LedgerStore is the on-disk ledger with an exclusive lock file. Writes are
// atomic: temp file + fsync + rename + directory fsync.
type LedgerStore struct {
	Path string
	lock *os.File
}

// ErrLedgerLocked means another signing holds the lock (or a crash left it).
var ErrLedgerLocked = errors.New("sweep: ledger is locked by another cryptopay-offline process")

func (s *LedgerStore) lockPath() string { return s.Path + ".lock" }

// InitLedger creates a new empty ledger file; it refuses to overwrite.
func InitLedger(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("sweep: %s already exists", path)
	}
	st := &LedgerStore{Path: path}
	if err := st.Lock(); err != nil {
		return err
	}
	defer st.Unlock()
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("sweep: %s already exists", path)
	}
	return st.Save(NewLedger())
}

// Lock takes the exclusive lock (O_EXCL lock file; portable to Windows).
func (s *LedgerStore) Lock() error {
	f, err := os.OpenFile(s.lockPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%w (%s exists; if no other process is running, a previous run crashed: check `ledger list`, then delete the lock file)", ErrLedgerLocked, s.lockPath())
		}
		return err
	}
	_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	_ = f.Sync()
	s.lock = f
	return nil
}

// Unlock releases the lock.
func (s *LedgerStore) Unlock() {
	if s.lock != nil {
		_ = s.lock.Close()
		_ = os.Remove(s.lockPath())
		s.lock = nil
	}
}

// Load reads and validates the ledger. A missing file is an error: an empty
// ledger must be created explicitly with InitLedger, so a mistyped path can
// never silently drop the signing history.
func (s *LedgerStore) Load() (*Ledger, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("sweep: ledger %s does not exist (create it once with `ledger init`; never recreate it after signing. If it was lost, restore a backup only if nothing was signed after it; otherwise stop and verify on chain independently)", s.Path)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := readLimit(f, MaxLedgerBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptLedger, err)
	}
	return DecodeLedger(data)
}

// Save atomically replaces the ledger file. The caller must hold the lock.
func (s *LedgerStore) Save(l *Ledger) error {
	if s.lock == nil {
		return errors.New("sweep: internal: Save without lock")
	}
	if err := l.validate(); err != nil {
		return fmt.Errorf("sweep: refusing to save invalid ledger: %w", err)
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxLedgerBytes {
		return fmt.Errorf("sweep: ledger would exceed %d bytes; refusing to save; nothing was signed. Ask the maintainer: a new ledger loses the nonce history of this one", MaxLedgerBytes)
	}
	return AtomicWriteFile(s.Path, data, 0o600, true)
}

// AtomicWriteFile durably writes data to path.
//
// replace=true (the ledger): temp file in the same directory, fsync, rename
// over path, fsync the directory. Any durability error is returned, so a
// caller never proceeds as if the write had been persisted.
//
// replace=false (plans, signed bundles, key files): created with O_EXCL (never
// overwrites, no check-then-rename race), fsynced, then the directory is
// fsynced. Not atomic: a crash can leave a truncated file, which every reader
// rejects (strict JSON). Works on FAT/exFAT USB sticks (no hard links needed).
func AtomicWriteFile(path string, data []byte, mode os.FileMode, replace bool) error {
	dir := filepath.Dir(path)
	if !replace {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			if os.IsExist(err) {
				return fmt.Errorf("sweep: %s already exists", path)
			}
			return err
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return syncDir(dir)
	}
	var rb [6]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(rb[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(tmp) }
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return err
	}
	return syncDir(dir)
}

// syncDir fsyncs a directory so a create/rename is durable. Windows cannot
// fsync directory handles (NTFS journals metadata itself), so it is skipped
// there; on every other OS a failure is an error.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("sweep: open directory for fsync: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("sweep: fsync directory %s: %w (refusing to continue: the write may not survive a power loss)", dir, err)
	}
	return d.Close()
}
