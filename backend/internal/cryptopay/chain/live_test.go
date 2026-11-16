//go:build live

// Read-only smoke tests against public mainnet RPC. Run manually:
//
//	go test -tags live -run Live -v ./internal/cryptopay/chain/
//
// Override endpoints with CRYPTOPAY_LIVE_ARB_RPC / CRYPTOPAY_LIVE_SOL_RPC.
package chain

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func liveURL(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

func TestLiveArbitrumOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := NewEVMScanner(EVMScannerConfig{Network: ArbitrumOne, RPC: RPCOptions{URL: liveURL("CRYPTOPAY_LIVE_ARB_RPC", "https://arb1.arbitrum.io/rpc")}})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestBlock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fin, err := s.FinalizedBlock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("latest %d finalized %d (lag %d blocks)", latest, fin, latest-fin)
	from := fin - 200

	// Independent raw query: all USDC/USDT Transfer logs in the range, no topic2 filter.
	raw, err := s.rpc.call(ctx, "eth_getLogs", []any{map[string]any{
		"fromBlock": "0x" + strconv.FormatUint(from, 16), "toBlock": "0x" + strconv.FormatUint(fin, 16),
		"address": s.contracts, "topics": []any{TransferTopic0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var logs []rpcLog
	if err := json.Unmarshal(raw, &logs); err != nil {
		t.Fatal(err)
	}
	recipients := map[string]int{}
	var order []string
	for _, l := range logs {
		if len(l.Topics) != 3 || l.Removed {
			continue
		}
		to, err := topicToAddress(l.Topics[2])
		if err != nil {
			t.Fatal(err)
		}
		u, err := parseUint256Data(l.Data)
		if err != nil {
			t.Fatal(err)
		}
		if u.Sign() <= 0 {
			continue
		}
		if _, ok := recipients[to]; !ok {
			order = append(order, to)
		}
		recipients[to]++
	}
	if len(order) == 0 {
		t.Skip("no USDC/USDT transfers in range")
	}
	if len(order) > 3 {
		order = order[:3]
	}
	want := 0
	for _, a := range order {
		want += recipients[a]
	}
	obs, err := s.ScanRange(ctx, from, fin, order, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != want {
		t.Fatalf("ScanRange returned %d observations, raw query says %d", len(obs), want)
	}
	for _, o := range obs[:min(3, len(obs))] {
		t.Logf("%s %s %s units=%s to=%s block=%d time=%s", o.Asset.Symbol, o.TxHash, o.EventKey, o.Units, o.ToAddress, o.BlockNumber, o.BlockTime.Format(time.RFC3339))
	}
	o := obs[0]
	byTx, err := s.ObservationsForTx(ctx, o.TxHash, []string{o.ToAddress}, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range byTx {
		if b.EventKey == o.EventKey && b.Units.Cmp(o.Units) == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("ObservationsForTx did not reproduce %+v: %+v", o, byTx)
	}
	t.Logf("scanned [%d,%d]: %d raw transfers, %d observations for %d recipients; ObservationsForTx ok", from, fin, len(logs), len(obs), len(order))
}

func TestLiveSolanaMainnet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// Owner A7hAgC... and its USDC ATA, confirmed via getTokenAccountsByOwner on 2026-11-16.
	const owner = "A7hAgCzFw14fejgCp387JUJRMNyz4j89JKnhtKU8piqW"
	ata, err := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-16: api.mainnet-beta.solana.com answers 403 "Your IP or provider is
	// blocked" to Go's default TLS ClientHello (X25519MLKEM768 key share) while
	// curl works; dropping ML-KEM from the curve list gets through. Production
	// should use a dedicated RPC provider; this is only for the smoke test.
	tr := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12, CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256}},
	}
	s, err := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{
		URL:        liveURL("CRYPTOPAY_LIVE_SOL_RPC", "https://api.mainnet-beta.solana.com"),
		HTTPClient: &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.rpc.call(ctx, "getSignaturesForAddress", []any{ata, map[string]any{"limit": 6, "commitment": "finalized"}})
	if err != nil {
		t.Fatal(err)
	}
	var sigs []rpcSignatureInfo
	if err := json.Unmarshal(raw, &sigs); err != nil {
		t.Fatal(err)
	}
	if len(sigs) < 2 {
		t.Skip("not enough history")
	}
	until := sigs[len(sigs)-1].Signature
	obs, newest, err := s.ScanTokenAccount(ctx, owner, ata, usdcAsset(), until, CommitmentFinalized)
	if err != nil {
		if strings.Contains(err.Error(), "429") {
			t.Skipf("rate limited: %v", err)
		}
		t.Fatal(err)
	}
	t.Logf("ATA %s: %d newer signatures, %d inflows, newest %s", ata, len(sigs)-1, len(obs), newest)
	// Window walk over the same range, 2 signatures per call.
	var windowed []string
	w := SolanaWindow{Until: until}
	for i := 0; i < 10; i++ {
		start := time.Now()
		var page []Observation
		page, w, err = s.ScanTokenAccountWindow(ctx, owner, ata, usdcAsset(), w, CommitmentFinalized, 2)
		t.Logf("  window call %d: %d obs, next %+v, %s, err %v", i, len(page), w, time.Since(start).Round(time.Millisecond), err)
		if err != nil {
			if strings.Contains(err.Error(), "http=429") {
				t.Logf("public RPC rate limited; stopping the window check")
				break
			}
			t.Fatal(err)
		}
		for _, o := range page {
			windowed = append(windowed, o.TxHash)
		}
		if w.Anchor == "" && i > 0 {
			break // first window complete
		}
	}
	t.Logf("window walk: %d inflows", len(windowed))
	for _, o := range obs {
		t.Logf("  %s +%s USDC units from %s slot %d", o.TxHash, o.Units, o.FromAddress, o.BlockNumber)
		byTx, err := s.ObservationsForTx(ctx, o.TxHash, []string{owner}, CommitmentFinalized)
		if err != nil {
			t.Fatal(err)
		}
		if len(byTx) == 0 || byTx[0].Units.Cmp(o.Units) != 0 {
			t.Fatalf("ObservationsForTx mismatch %+v", byTx)
		}
	}
}
