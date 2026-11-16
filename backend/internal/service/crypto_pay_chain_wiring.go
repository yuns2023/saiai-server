package service

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

// BuildCryptoPayChainDeps wires the chain package. Derivation and manifest
// verification are pure functions and always available. Scanners are built
// only when the deployment master switch is on, the configuration is valid,
// the network is configured and has an RPC URL; otherwise they stay nil and
// the worker idles. Constructing a scanner makes no network request.
//
// RPC URLs may embed API keys: they are passed to the chain package only and
// never logged (logs name the network). httpClient is optional (tests).
func BuildCryptoPayChainDeps(cfg config.CryptoPayConfig, httpClient *http.Client) CryptoPayChainDeps {
	deps := CryptoPayChainDeps{
		DeriveEVMAddress:     chain.EVMAddressFromXpub,
		VerifySolanaManifest: chain.VerifySolanaPoolManifest,
		SolanaATA:            chain.SolanaATA,
		IsRetryLater:         cryptoPayIsRetryLater,
	}
	networks, _ := parseCryptoPayNetworks(cfg) // warnings are logged by NewCryptoPayService
	for _, n := range networks {
		switch n.Family {
		case chain.FamilyEVM:
			url := strings.TrimSpace(cfg.EVMRPCURL)
			if url == "" {
				cryptoPayLogf("%s scanner not started: crypto_pay.evm_rpc_url is empty", n.Network)
				continue
			}
			sc, err := chain.NewEVMScanner(chain.EVMScannerConfig{
				Network: n.Network, Assets: n.Assets,
				RPC: chain.RPCOptions{URL: url, HTTPClient: httpClient},
			})
			if err != nil {
				cryptoPayLogf("%s scanner not started: invalid configuration", n.Network)
				continue
			}
			deps.EVMScanner = sc
		case chain.FamilySolana:
			url := strings.TrimSpace(cfg.SolanaRPCURL)
			if url == "" {
				cryptoPayLogf("%s scanner not started: crypto_pay.solana_rpc_url is empty", n.Network)
				continue
			}
			sc, err := chain.NewSolanaScanner(chain.SolanaScannerConfig{
				Network: n.Network, Assets: n.Assets,
				RPC: chain.RPCOptions{URL: url, HTTPClient: httpClient},
			})
			if err != nil {
				cryptoPayLogf("%s scanner not started: invalid configuration", n.Network)
				continue
			}
			deps.SolanaScanner = sc
		}
	}
	return deps
}

// cryptoPayIsRetryLater: "not finalized yet", "tx unknown" and transient
// RPC failures mean retry later, never "no deposit".
func cryptoPayIsRetryLater(err error) bool {
	if errors.Is(err, chain.ErrNotFinalized) || errors.Is(err, chain.ErrTxNotFound) {
		return true
	}
	var callErr *chain.RPCCallError
	if errors.As(err, &callErr) {
		switch callErr.Kind {
		case chain.RPCKindTransport, chain.RPCKindTimeout, chain.RPCKindHTTPStatus:
			return true
		}
	}
	return false
}
