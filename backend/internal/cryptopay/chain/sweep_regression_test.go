package chain

import (
	"context"
	"testing"
)

// A multi-source sweep transaction (tools/cryptopay sweep-sign): several
// deposit owners' ATAs are emptied into the treasury ATA in one transaction;
// a source ATA may even be closed (absent from postTokenBalances). The
// scanner must report no inflow for any deposit owner and no error.
func TestSolanaMultiSourceSweepYieldsNoObservations(t *testing.T) {
	ownerA, ownerB, ownerC := sKey("sweep-a"), sKey("sweep-b"), sKey("sweep-c")
	treasury := sKey("treasury")
	ataA, _ := SolanaATA(ownerA, sUSDT, SolanaTokenProgram)
	ataB, _ := SolanaATA(ownerB, sUSDT, SolanaTokenProgram)
	ataC, _ := SolanaATA(ownerC, sUSDC, SolanaTokenProgram)
	tATAt, _ := SolanaATA(treasury, sUSDT, SolanaTokenProgram)
	tATAc, _ := SolanaATA(treasury, sUSDC, SolanaTokenProgram)
	sweep := fakeSolTx{slot: 50,
		pre: []tb{
			{ataA, sUSDT, ownerA, "", "4300000"},
			{ataB, sUSDT, ownerB, "", "1"},
			{ataC, sUSDC, ownerC, "", "7"},
			{tATAc, sUSDC, treasury, "", "100"},
		},
		post: []tb{
			{ataA, sUSDT, ownerA, "", "0"},
			// ataB closed: no post entry
			{ataC, sUSDC, ownerC, "", "0"},
			{tATAt, sUSDT, treasury, "", "4300001"}, // created in this tx
			{tATAc, sUSDC, treasury, "", "107"},
		},
	}
	c := &solChain{honorUntil: true, txs: map[string]fakeSolTx{sSig("sweep"): sweep}, order: []string{sSig("sweep")}}
	s := newTestSolScanner(t, c, 10)
	for _, x := range []struct{ owner, ata string }{{ownerA, ataA}, {ownerB, ataB}} {
		obs, newest, err := s.ScanTokenAccount(context.Background(), x.owner, x.ata, MainnetAssets()[3], "", CommitmentFinalized)
		if err != nil || len(obs) != 0 || newest != sSig("sweep") {
			t.Fatalf("%s: obs=%v newest=%q err=%v", x.ata, obs, newest, err)
		}
		w := SolanaWindow{}
		for i := 0; i < 3 && w.Until == ""; i++ {
			obs, next, err := s.ScanTokenAccountWindow(context.Background(), x.owner, x.ata, MainnetAssets()[3], w, CommitmentFinalized, 10)
			if err != nil || len(obs) != 0 {
				t.Fatalf("window %s: obs=%v err=%v", x.ata, obs, err)
			}
			w = next
		}
		if w.Until != sSig("sweep") {
			t.Fatalf("window %s did not complete: %+v", x.ata, w)
		}
	}
	obs, _, err := s.ScanTokenAccount(context.Background(), ownerC, ataC, usdcAsset(), "", CommitmentFinalized)
	if err != nil || len(obs) != 0 {
		t.Fatalf("USDC source: %v %v", obs, err)
	}
	obs, err = s.ObservationsForTx(context.Background(), sSig("sweep"), []string{ownerA, ownerB, ownerC}, CommitmentFinalized)
	if err != nil || len(obs) != 0 {
		t.Fatalf("ObservationsForTx: %v %v", obs, err)
	}
}
