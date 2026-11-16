package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// dustChain builds n signatures (oldest first: d0..d{n-1}) in ONE slot, each
// +1 raw unit to the ATA; every 7th fails in the listing and every 11th has
// meta.err; both emit nothing.
func dustChain(owner, ata string, n int) (*solChain, []string) {
	c := &solChain{honorUntil: true, txs: map[string]fakeSolTx{}}
	var credited []string
	bal := 0
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("d%d", i)
		tx := fakeSolTx{slot: 4242, pre: []tb{{ata, sUSDC, owner, "", fmt.Sprint(bal)}}, post: []tb{{ata, sUSDC, owner, "", fmt.Sprint(bal + 1)}}}
		switch {
		case i%7 == 0:
			tx.listErr, tx.failed = true, true
		case i%11 == 0:
			tx.failed = true
		default:
			bal++
			credited = append(credited, sSig(name))
		}
		c.order = append([]string{sSig(name)}, c.order...)
		c.txs[sSig(name)] = tx
	}
	return c, credited
}

func addNewest(c *solChain, ata, owner, name string) {
	c.order = append([]string{sSig(name)}, c.order...)
	c.txs[sSig(name)] = fakeSolTx{slot: 9000, pre: []tb{{ata, sUSDC, owner, "", "1"}}, post: []tb{{ata, sUSDC, owner, "", "2"}}}
}

type windowHarness struct {
	t        *testing.T
	s        *SolanaRPCScanner
	c        *solChain
	owner    string
	ata      string
	failList int    // fail the n-th getSignaturesForAddress call (1-based), 0 = never
	failSig  string // fail getTransaction of this signature
	listN    int
	txN      int
}

func newWindowHarness(t *testing.T, n int) (*windowHarness, []string) {
	h := &windowHarness{t: t, owner: sKey(fmt.Sprintf("win-%d", n))}
	h.ata, _ = SolanaATA(h.owner, sUSDC, SolanaTokenProgram)
	var credited []string
	h.c, credited = dustChain(h.owner, h.ata, n)
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		switch method {
		case "getSignaturesForAddress":
			h.listN++
			if h.failList == h.listN {
				return nil, &RPCError{Code: -32000, Message: "node behind"}
			}
		case "getTransaction":
			h.txN++
			if h.failSig != "" && strings.Contains(string(params), h.failSig) {
				return nil, &RPCError{Code: -32000, Message: "unavailable"}
			}
		}
		return h.c.handle(method, params)
	})
	h.s, _ = NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}})
	return h, credited
}

func (h *windowHarness) call(w SolanaWindow, maxTx int) ([]Observation, SolanaWindow, error) {
	return h.s.ScanTokenAccountWindow(context.Background(), h.owner, h.ata, usdcAsset(), w, CommitmentFinalized, maxTx)
}

// walk calls until the first window completes; returns tx hashes and #calls.
func (h *windowHarness) walk(w SolanaWindow, maxTx int) ([]string, SolanaWindow, int) {
	var got []string
	for calls := 1; calls < 1000; calls++ {
		obs, next, err := h.call(w, maxTx)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, o := range obs {
			if o.Units.Int64() != 1 || o.EventKey != "acct:"+h.ata || o.ToAddress != h.owner {
				h.t.Fatalf("bad obs %+v", o)
			}
			got = append(got, o.TxHash)
		}
		w = next
		if w.Anchor == "" {
			return got, w, calls
		}
	}
	h.t.Fatal("window never completed")
	return nil, w, 0
}

func sameSet(t *testing.T, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	wnt := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(wnt)
	if strings.Join(g, ",") != strings.Join(wnt, ",") {
		t.Fatalf("inflow set mismatch: got %d, want %d", len(got), len(want))
	}
	for i := 1; i < len(g); i++ {
		if g[i] == g[i-1] {
			t.Fatalf("duplicate %s", g[i])
		}
	}
}

func TestSolanaWindow2500(t *testing.T) {
	h, credited := newWindowHarness(t, 2500)
	got, w, calls := h.walk(SolanaWindow{}, 200)
	if calls != 13 { // ceil(2500/200); the 13th page is short (100)
		t.Fatalf("calls = %d, want 13", calls)
	}
	sameSet(t, got, credited)
	if w != (SolanaWindow{Until: sSig("d2499")}) {
		t.Fatalf("next window %+v", w)
	}
	// listing-failed signatures are never fetched: 2500 - ceil(2500/7)
	if h.txN != 2500-358 {
		t.Fatalf("getTransaction calls %d", h.txN)
	}
	// Nothing new: window unchanged, no transactions fetched.
	before := h.txN
	obs, next, err := h.call(w, 200)
	if err != nil || obs != nil || next != w || h.txN != before {
		t.Fatalf("idle call: %v %+v %v", obs, next, err)
	}
}

func TestSolanaWindowExactMultiple(t *testing.T) {
	h, credited := newWindowHarness(t, 400)
	got, w, calls := h.walk(SolanaWindow{}, 200)
	// Two full pages, then an empty page closes the window.
	if calls != 3 || w.Until != sSig("d399") {
		t.Fatalf("calls %d, window %+v", calls, w)
	}
	sameSet(t, got, credited)
}

func TestSolanaWindowNewSignaturesGoToNextWindow(t *testing.T) {
	h, credited := newWindowHarness(t, 450)
	w := SolanaWindow{}
	var got []string
	for i := 0; i < 2; i++ { // two full pages of the first window
		obs, next, err := h.call(w, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range obs {
			got = append(got, o.TxHash)
		}
		w = next
	}
	if w.Anchor != sSig("d449") {
		t.Fatalf("anchor %+v", w)
	}
	for i := 0; i < 5; i++ { // arrive mid-window
		addNewest(h.c, h.ata, h.owner, fmt.Sprintf("late%d", i))
	}
	rest, w, _ := h.walk(w, 200)
	got = append(got, rest...)
	sameSet(t, got, credited) // first window: exactly the original set
	if w.Until != sSig("d449") {
		t.Fatalf("first window must end at its anchor: %+v", w)
	}
	late, w, _ := h.walk(w, 200)
	var want []string
	for i := 0; i < 5; i++ {
		want = append(want, sSig(fmt.Sprintf("late%d", i)))
	}
	sameSet(t, late, want)
	if w.Until != sSig("late4") {
		t.Fatalf("second window end %+v", w)
	}
}

func TestSolanaWindowErrorsLeaveWindowAndRetryIsExact(t *testing.T) {
	h, credited := newWindowHarness(t, 1000)
	w := SolanaWindow{}
	var got []string
	step := func() SolanaWindow {
		obs, next, err := h.call(w, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range obs {
			got = append(got, o.TxHash)
		}
		return next
	}
	w = step()
	w = step()

	// Listing failure on page 3.
	h.failList = h.listN + 1
	obs, next, err := h.call(w, 200)
	if err == nil || obs != nil || next != w {
		t.Fatalf("listing failure: %v %+v %v", obs, next, err)
	}
	h.failList = 0
	// getTransaction failure inside page 3 (tx d450 is in it: page 3 = d599..d400).
	h.failSig = sSig("d450")
	obs, next, err = h.call(w, 200)
	if err == nil || obs != nil || next != w {
		t.Fatalf("tx failure: %v %+v %v", obs, next, err)
	}
	h.failSig = ""
	// Restart safety: same inputs, same outputs.
	o1, n1, e1 := h.call(w, 200)
	o2, n2, e2 := h.call(w, 200)
	if e1 != nil || e2 != nil || n1 != n2 || len(o1) != len(o2) {
		t.Fatalf("not deterministic: %+v %+v %v %v", n1, n2, e1, e2)
	}
	for i := range o1 {
		if o1[i].TxHash != o2[i].TxHash || o1[i].Units.Cmp(o2[i].Units) != 0 {
			t.Fatal("repeat call differs")
		}
	}
	for _, o := range o1 {
		got = append(got, o.TxHash)
	}
	rest, _, _ := h.walk(n1, 200)
	got = append(got, rest...)
	sameSet(t, got, credited)
}

func TestSolanaWindowRejectsBadPagesAndInputs(t *testing.T) {
	h, _ := newWindowHarness(t, 30)
	// A node that ignores `until` returns the Until signature inside the page.
	h.c.honorUntil = false
	w := SolanaWindow{Until: sSig("d10")}
	if obs, next, err := h.call(w, 50); err == nil || obs != nil || next != w {
		t.Fatalf("Until inside page must be rejected: %v %+v %v", obs, next, err)
	}
	h.c.honorUntil = true
	if _, _, err := h.s.ScanTokenAccountWindow(context.Background(), h.owner, h.ata, usdcAsset(), SolanaWindow{}, CommitmentConfirmed, 10); err == nil {
		t.Fatal("confirmed must be rejected")
	}
	for _, bad := range []int{0, 1001} {
		if _, _, err := h.call(SolanaWindow{}, bad); err == nil {
			t.Fatalf("maxTx %d must be rejected", bad)
		}
	}
	if _, _, err := h.call(SolanaWindow{Before: sSig("d3")}, 10); err == nil {
		t.Fatal("Before without Anchor must be rejected")
	}
	// Duplicate signature within a page.
	dupH, _ := newWindowHarness(t, 31)
	dupH.c.order = append([]string{dupH.c.order[0]}, dupH.c.order...)
	if _, _, err := dupH.call(SolanaWindow{}, 50); err == nil {
		t.Fatal("duplicate signature in page must be rejected")
	}
}
