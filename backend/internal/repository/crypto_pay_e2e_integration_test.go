//go:build integration

package repository

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/hdkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/mr-tron/base58"
	"github.com/stretchr/testify/require"
	"github.com/tyler-smith/go-bip39"
)

// e2eRPC is a minimal JSON-RPC server driven by a handler.
func e2eRPC(t *testing.T, handle func(method string, params []json.RawMessage) any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": handle(req.Method, req.Params)})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func e2eHex(n uint64) string       { return "0x" + strconv.FormatUint(n, 16) }
func e2eBlockHash(n uint64) string { return fmt.Sprintf("0x%064x", n+0xb10c) }

func testAccountXpub(t *testing.T) string {
	t.Helper()
	seed, err := bip39.NewSeedWithErrorChecking("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about", "")
	require.NoError(t, err)
	master, err := hdkey.NewMasterKey(seed)
	require.NoError(t, err)
	acct, err := master.DerivePath(chain.EVMAccountPath)
	require.NoError(t, err)
	return acct.Neuter().String()
}

func newRealChainService(t *testing.T, cfg config.CryptoPayConfig) (*service.CryptoPayService, service.SettingRepository) {
	t.Helper()
	resetCryptoTables(t)
	t.Cleanup(func() {
		resetCryptoTables(t)
		_, _ = integrationDB.Exec(`DELETE FROM users WHERE email LIKE 'crypto-%@example.test'`)
	})
	settings := NewSettingRepository(integrationEntClient)
	for k, v := range map[string]string{service.SettingCryptoPayAllowNewAddresses: "true",
		service.SettingCryptoPayScanningEnabled: "true", service.SettingCryptoPayAutoCreditEnabled: "true"} {
		require.NoError(t, settings.Set(context.Background(), k, v))
	}
	cfg.Enabled, cfg.ScanIntervalSeconds, cfg.ScanBatchTimeoutSeconds, cfg.EVMBlocksPerScan, cfg.ScanUnitsPerPass, cfg.SolanaPageSize, cfg.ConfirmingUnitsPerPass = true, 30, 20, 2000, 20, 200, 5
	deps := service.BuildCryptoPayChainDeps(cfg, nil)
	return service.NewCryptoPayService(integrationDB, settings, cfg, deps, nil, nil), settings
}

func TestCryptoPayE2E_ArbitrumRealScannerCreditsExactly(t *testing.T) {
	ctx := context.Background()
	usdc := chain.MainnetAssets()[0].Contract
	var mu sync.Mutex
	finalized, latest := uint64(1000), uint64(1000)
	var deposit *struct {
		to    string
		block uint64
	}
	srv := e2eRPC(t, func(method string, p []json.RawMessage) any {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "eth_blockNumber":
			return e2eHex(latest)
		case "eth_getBlockByNumber":
			var tag string
			_ = json.Unmarshal(p[0], &tag)
			n := finalized
			switch tag {
			case "finalized":
			case "latest":
				n = latest
			default:
				n, _ = strconv.ParseUint(strings.TrimPrefix(tag, "0x"), 16, 64)
			}
			return map[string]any{"number": e2eHex(n), "hash": e2eBlockHash(n), "timestamp": e2eHex(1_800_000_000 + n)}
		case "eth_getLogs":
			var f []struct {
				FromBlock string `json:"fromBlock"`
				ToBlock   string `json:"toBlock"`
				Topics    []any  `json:"topics"`
			}
			_ = json.Unmarshal(append(append([]byte("["), p[0]...), ']'), &f)
			from, _ := strconv.ParseUint(strings.TrimPrefix(f[0].FromBlock, "0x"), 16, 64)
			to, _ := strconv.ParseUint(strings.TrimPrefix(f[0].ToBlock, "0x"), 16, 64)
			out := []map[string]any{}
			if deposit != nil && deposit.block >= from && deposit.block <= to {
				topic2 := "0x000000000000000000000000" + deposit.to[2:]
				if list, ok := f[0].Topics[2].([]any); ok {
					for _, x := range list {
						if x == topic2 {
							out = append(out, map[string]any{
								"address": usdc, "topics": []string{chain.TransferTopic0, "0x000000000000000000000000" + strings.Repeat("9", 40), topic2},
								"data": fmt.Sprintf("0x%064x", 1_234_567), "blockNumber": e2eHex(deposit.block), "blockHash": e2eBlockHash(deposit.block),
								"transactionHash": fmt.Sprintf("0x%064x", 0xfeed), "logIndex": "0x3", "removed": false,
							})
						}
					}
				}
			}
			return out
		}
		return nil
	})
	svc, _ := newRealChainService(t, config.CryptoPayConfig{EVMNetwork: "arbitrum-one", EVMRPCURL: srv.URL})
	_, err := svc.ImportEVMKeyset(ctx, "arbitrum-one", testAccountXpub(t), "admin:e2e")
	require.NoError(t, err)
	user := createCryptoUser(t)
	order, err := svc.CreateOrder(ctx, user, "arbitrum-one", "USDC", "1.23")
	require.NoError(t, err)
	require.Equal(t, "0x9858effd232b4033e47d90003d41ec34ecaeda94", order.Address, "index 0 of the test xpub")
	var start int64
	require.NoError(t, integrationDB.QueryRow(`SELECT start_block FROM crypto_pay_addresses WHERE user_id = $1`, user).Scan(&start))
	require.Equal(t, int64(1000), start, "start height = finalized head at allocation")

	mu.Lock()
	deposit = &struct {
		to    string
		block uint64
	}{to: order.Address, block: 1005}
	finalized, latest = 1010, 1012
	mu.Unlock()
	svc.RunWorkerPass(ctx)
	require.Equal(t, "1.23456700", userBalance(t, user))
	order, err = svc.GetUserOrder(ctx, user, order.ID)
	require.NoError(t, err)
	require.Equal(t, "1.234567", order.CreditedAmount)
	require.Equal(t, "paid", order.Status)
	require.Equal(t, "log:3", func() string {
		var k string
		require.NoError(t, integrationDB.QueryRow(`SELECT event_key FROM crypto_pay_deposits`).Scan(&k))
		return k
	}())
	svc.RunWorkerPass(ctx)
	require.Equal(t, "1.23456700", userBalance(t, user), "second tick credits nothing more")
}

func TestCryptoPayE2E_SolanaRealScannerCreditsExactly(t *testing.T) {
	ctx := context.Background()
	usdc := chain.MainnetAssets()[2]
	signerPub, signerPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	m := chain.SolanaPoolManifest{Version: chain.SolanaPoolManifestVersion, Network: chain.SolanaMainnet,
		PathTemplate: chain.SolanaPoolPathTemplate, Start: 0, SignerPubkey: base58.Encode(signerPub)}
	for i := 0; i < 2; i++ {
		seed := sha256.Sum256([]byte(fmt.Sprintf("pool-%d", i)))
		pub := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey) //nolint:forcetypeassert // stdlib contract
		m.Entries = append(m.Entries, chain.SolanaPoolEntry{Index: uint32(i), Address: base58.Encode(pub)})
	}
	msg, err := chain.SolanaPoolManifestSigningBytes(&m)
	require.NoError(t, err)
	m.Signature = base58.Encode(ed25519.Sign(signerPriv, msg))
	raw, err := json.Marshal(m)
	require.NoError(t, err)

	sigA := sha256.Sum256([]byte("sig-a"))
	sigB := sha256.Sum256([]byte("sig-b"))
	sig := base58.Encode(append(sigA[:], sigB[:]...))
	var mu sync.Mutex
	var owner, ata string
	srv := e2eRPC(t, func(method string, p []json.RawMessage) any {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "getSignaturesForAddress":
			var account string
			var opts map[string]any
			_ = json.Unmarshal(p[0], &account)
			_ = json.Unmarshal(p[1], &opts)
			if account != ata || opts["before"] != nil || opts["until"] == sig {
				return []any{}
			}
			return []map[string]any{{"signature": sig, "slot": 777, "err": nil, "confirmationStatus": "finalized"}}
		case "getTransaction":
			bal := func(amount string) []map[string]any {
				return []map[string]any{{"accountIndex": 1, "mint": usdc.Contract, "owner": owner, "programId": usdc.TokenProgram,
					"uiTokenAmount": map[string]any{"amount": amount, "decimals": 6}}}
			}
			return map[string]any{
				"slot": 777, "blockTime": 1_800_000_000, "version": 0,
				"meta": map[string]any{"err": nil, "preTokenBalances": bal("0"), "postTokenBalances": bal("1234567")},
				"transaction": map[string]any{"signatures": []string{sig}, "message": map[string]any{
					"accountKeys": []map[string]any{
						{"pubkey": base58.Encode(signerPub), "signer": true, "writable": true, "source": "transaction"},
						{"pubkey": ata, "signer": false, "writable": true, "source": "transaction"},
					}, "recentBlockhash": base58.Encode(sigA[:])}},
			}
		}
		return nil
	})
	svc, _ := newRealChainService(t, config.CryptoPayConfig{SolanaNetwork: "solana-mainnet", SolanaRPCURL: srv.URL,
		SolanaPoolSignerPubkey: base58.Encode(signerPub)})
	_, err = svc.ImportSolanaKeyset(ctx, raw, "admin:e2e")
	require.NoError(t, err)
	user := createCryptoUser(t)
	order, err := svc.CreateOrder(ctx, user, "solana-mainnet", "USDC", "1")
	require.NoError(t, err)
	require.Equal(t, m.Entries[0].Address, order.Address)
	mu.Lock()
	owner = order.Address
	ata, err = chain.SolanaATA(owner, usdc.Contract, usdc.TokenProgram)
	mu.Unlock()
	require.NoError(t, err)

	svc.RunWorkerPass(ctx)
	require.Equal(t, "1.23456700", userBalance(t, user))
	var eventKey string
	require.NoError(t, integrationDB.QueryRow(`SELECT event_key FROM crypto_pay_deposits`).Scan(&eventKey))
	require.Equal(t, "acct:"+ata, eventKey)
	svc.RunWorkerPass(ctx)
	require.Equal(t, "1.23456700", userBalance(t, user), "cursor resumes after the signature; no double credit")
}
