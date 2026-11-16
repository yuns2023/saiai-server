//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/stretchr/testify/require"
)

func TestBuildCryptoPayChainDepsMasterOffMakesNoRequests(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := config.CryptoPayConfig{Enabled: false, EVMNetwork: "arbitrum-one", EVMRPCURL: srv.URL,
		SolanaNetwork: "solana-mainnet", SolanaRPCURL: srv.URL, ScanIntervalSeconds: 1, ScanBatchTimeoutSeconds: 1}
	deps := BuildCryptoPayChainDeps(cfg, srv.Client())
	require.Nil(t, deps.EVMScanner)
	require.Nil(t, deps.SolanaScanner)
	svc := NewCryptoPayService(nil, &cryptoPayFakeSettings{values: map[string]string{
		SettingCryptoPayScanningEnabled: "true", SettingCryptoPayAutoCreditEnabled: "true"}}, cfg, deps, nil, nil)
	require.False(t, svc.Enabled())
	w := NewCryptoPayWorker(nil, svc, 10*time.Millisecond)
	w.Start()
	time.Sleep(50 * time.Millisecond)
	w.Stop()
	svc.RunWorkerPass(context.Background())
	require.Zero(t, atomic.LoadInt64(&hits), "master off: no RPC traffic at all")

	// Invalid config with master on is also fail-closed: no scanners.
	bad := cfg
	bad.Enabled, bad.SolanaNetwork = true, "solana-mainnt"
	deps = BuildCryptoPayChainDeps(bad, srv.Client())
	require.Nil(t, deps.EVMScanner)
	require.Nil(t, deps.SolanaScanner)

	// Master on: scanners exist, construction itself makes no request; a
	// network without URL stays idle.
	on := cfg
	on.Enabled, on.SolanaRPCURL = true, ""
	deps = BuildCryptoPayChainDeps(on, srv.Client())
	require.NotNil(t, deps.EVMScanner)
	require.Equal(t, chain.ArbitrumOne, deps.EVMScanner.Network())
	require.Nil(t, deps.SolanaScanner)
	require.NotNil(t, deps.DeriveEVMAddress)
	require.NotNil(t, deps.VerifySolanaManifest)
	require.NotNil(t, deps.SolanaATA)
	require.Zero(t, atomic.LoadInt64(&hits))
}

func TestCryptoPayIsRetryLater(t *testing.T) {
	require.True(t, cryptoPayIsRetryLater(fmt.Errorf("x: %w", chain.ErrNotFinalized)))
	require.True(t, cryptoPayIsRetryLater(fmt.Errorf("x: %w", chain.ErrTxNotFound)))
	for _, k := range []chain.RPCErrorKind{chain.RPCKindTransport, chain.RPCKindTimeout, chain.RPCKindHTTPStatus} {
		require.True(t, cryptoPayIsRetryLater(fmt.Errorf("wrapped: %w", &chain.RPCCallError{Method: "eth_getTransactionReceipt", Kind: k})), k)
	}
	require.False(t, cryptoPayIsRetryLater(&chain.RPCCallError{Method: "m", Kind: chain.RPCKindDecode}))
	require.False(t, cryptoPayIsRetryLater(errors.New("other")))
}
