package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// dustChain builds n signatures (oldest first: d0..d{n-1}) in ONE slot, each
// +1 raw unit to the ATA; every 7th is failed in the listing and every 11th
// has meta.err. They still count toward maxTx but emit nothing.
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

func TestSolanaScanTokenAccountPageResumes(t *testing.T) {
	owner := sKey("dust-victim")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	c, credited := dustChain(owner, ata, 2500)
	txCalls := 0
	failSig := ""
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		if method == "getTransaction" {
			if failSig != "" && strings.Contains(string(params), failSig) {
				return nil, &RPCError{Code: -32000, Message: "unavailable"}
			}
			txCalls++
		}
		return c.handle(method, params)
	})
	s, _ := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}, PageLimit: 1000})
	ctx := context.Background()

	var got []string
	cursor := ""
	var dones []bool
	for call := 0; call < 10; call++ {
		if call == 1 {
			// tx #700 of the second page fails: no cursor, nothing to persist.
			failSig = sSig("d1699")
			before := txCalls
			obs, cur, done, err := s.ScanTokenAccountPage(ctx, owner, ata, usdcAsset(), cursor, CommitmentFinalized, 1000)
			if err == nil || obs != nil || cur != "" || done {
				t.Fatalf("failure at tx #700: %v %q %v %v", len(obs), cur, done, err)
			}
			failSig = ""
			txCalls = before // the retry below re-fetches the same prefix
		}
		obs, cur, done, err := s.ScanTokenAccountPage(ctx, owner, ata, usdcAsset(), cursor, CommitmentFinalized, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range obs {
			if o.Units.Int64() != 1 || o.EventKey != "acct:"+ata {
				t.Fatalf("bad obs %+v", o)
			}
			got = append(got, o.TxHash)
		}
		cursor = cur
		dones = append(dones, done)
		if done {
			break
		}
	}
	if len(dones) != 3 || dones[0] || dones[1] || !dones[2] {
		t.Fatalf("want 3 calls (done only on the last), got %v", dones)
	}
	if cursor != sSig("d2499") {
		t.Fatalf("final cursor %s", cursor)
	}
	if strings.Join(got, ",") != strings.Join(credited, ",") {
		t.Fatalf("gap/duplicate/order mismatch: got %d obs, want %d", len(got), len(credited))
	}
	// listing-failed signatures are never fetched: 2500 - ceil(2500/7)
	if want := 2500 - 358; txCalls != want {
		t.Fatalf("getTransaction calls %d, want %d", txCalls, want)
	}
	// Up to date: nothing newer than the cursor.
	obs, cur, done, err := s.ScanTokenAccountPage(ctx, owner, ata, usdcAsset(), cursor, CommitmentFinalized, 1000)
	if err != nil || obs != nil || cur != cursor || !done {
		t.Fatalf("untilSig == newest: %v %q %v %v", obs, cur, done, err)
	}
}

func TestSolanaScanTokenAccountPageCursorBoundaries(t *testing.T) {
	owner := sKey("dust-victim2")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	c, _ := dustChain(owner, ata, 25)
	failSig := ""
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		if method == "getTransaction" && failSig != "" && strings.Contains(string(params), failSig) {
			return nil, &RPCError{Code: -32000, Message: "unavailable"}
		}
		return c.handle(method, params)
	})
	s, _ := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}, PageLimit: 4})
	ctx := context.Background()

	// First page: d0..d9 processed (d0, d7 failed in listing still count).
	obs, cur, done, err := s.ScanTokenAccountPage(ctx, owner, ata, usdcAsset(), "", CommitmentFinalized, 10)
	if err != nil || done || cur != sSig("d9") {
		t.Fatalf("page 1: %q %v %v", cur, done, err)
	}
	if obs[0].TxHash != sSig("d1") || obs[len(obs)-1].TxHash != sSig("d9") {
		t.Fatalf("page 1 must be the oldest prefix: %s..%s", obs[0].TxHash, obs[len(obs)-1].TxHash)
	}
	// A failure in the middle of the next page gives no cursor at all.
	failSig = sSig("d16")
	obs, cur, done, err = s.ScanTokenAccountPage(ctx, owner, ata, usdcAsset(), sSig("d9"), CommitmentFinalized, 10)
	if err == nil || obs != nil || cur != "" || done {
		t.Fatalf("failure mid-page: %v %q %v %v", obs, cur, done, err)
	}
	failSig = ""
	// New signatures land between calls; resuming from d9 sees d10..d24 then them.
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("new%d", i)
		c.order = append([]string{sSig(name)}, c.order...)
		c.txs[sSig(name)] = fakeSolTx{slot: 5000, pre: []tb{{ata, sUSDC, owner, "", "100"}}, post: []tb{{ata, sUSDC, owner, "", "105"}}}
	}
	var seen []string
	cursor := sSig("d9")
	// The backlog listed before the new signatures arrived is finished first
	// (done=true relative to that listing); the next call lists the new ones.
	for round := 0; round < 2; round++ {
		done = false
		for i := 0; i < 5 && !done; i++ {
			obs, cursor, done, err = s.ScanTokenAccountPage(ctx, owner, ata, usdcAsset(), cursor, CommitmentFinalized, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range obs {
				seen = append(seen, o.TxHash)
			}
		}
	}
	if !done || cursor != sSig("new2") {
		t.Fatalf("did not finish: cursor %q done %v", cursor, done)
	}
	if seen[0] != sSig("d10") || seen[len(seen)-1] != sSig("new2") {
		t.Fatalf("resume order wrong: first %s last %s", seen[0], seen[len(seen)-1])
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] == seen[i-1] {
			t.Fatal("duplicate observation")
		}
	}
	if _, _, _, err := s.ScanTokenAccountPage(ctx, owner, ata, usdcAsset(), "", CommitmentFinalized, 0); err == nil {
		t.Fatal("maxTx 0 must be rejected")
	}
}

// A backlog larger than MaxListPagesPerCall is listed across calls (no cursor
// movement, no error), then processed; the listing is done once in total.
func TestSolanaScanTokenAccountPageBoundedListing(t *testing.T) {
	owner := sKey("dust-victim3")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	c, credited := dustChain(owner, ata, 95)
	listCalls := 0
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		if method == "getSignaturesForAddress" {
			listCalls++
		}
		return c.handle(method, params)
	})
	s, _ := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}, PageLimit: 10, MaxListPagesPerCall: 3})
	ctx := context.Background()
	cursor := ""
	var got []string
	var shapes []string
	for i := 0; i < 20; i++ {
		obs, cur, done, err := s.ScanTokenAccountPage(ctx, owner, ata, usdcAsset(), cursor, CommitmentFinalized, 40)
		if err != nil {
			t.Fatal(err)
		}
		shapes = append(shapes, fmt.Sprintf("%d/%v", len(obs), done))
		for _, o := range obs {
			got = append(got, o.TxHash)
		}
		if cur != cursor && len(obs) == 0 && cur != "" && !done && i < 3 {
			// listing calls must not move the cursor
			t.Fatalf("listing call moved cursor: %q", cur)
		}
		cursor = cur
		if done {
			break
		}
	}
	if strings.Join(got, ",") != strings.Join(credited, ",") {
		t.Fatalf("mismatch: got %d want %d (%v)", len(got), len(credited), shapes)
	}
	// 95 sigs / 10 per page = 10 pages listed once (3+3+3+1), never re-listed.
	if listCalls != 10 {
		t.Fatalf("getSignaturesForAddress calls = %d, want 10 (%v)", listCalls, shapes)
	}
}
