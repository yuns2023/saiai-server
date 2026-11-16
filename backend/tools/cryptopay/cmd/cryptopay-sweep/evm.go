package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/tools/cryptopay/sweep"
)

type evmRPC struct{ *rpcClient }

func (e evmRPC) bigCall(ctx context.Context, method string, params ...any) (*big.Int, error) {
	var s string
	if err := e.call(ctx, method, params, &s); err != nil {
		return nil, err
	}
	v, err := hexutil.DecodeBig(s)
	if err != nil {
		return nil, fmt.Errorf("%s: bad quantity", method)
	}
	return v, nil
}

func (e evmRPC) chainID(ctx context.Context) (*big.Int, error) { return e.bigCall(ctx, "eth_chainId") }

func (e evmRPC) nonce(ctx context.Context, addr, tag string) (uint64, error) {
	v, err := e.bigCall(ctx, "eth_getTransactionCount", addr, tag)
	if err != nil {
		return 0, err
	}
	if !v.IsUint64() {
		return 0, errors.New("nonce overflow")
	}
	return v.Uint64(), nil
}

// stableNonce requires latest == pending (no transaction of addr in flight).
func (e evmRPC) stableNonce(ctx context.Context, addr string) (uint64, error) {
	l, err := e.nonce(ctx, addr, "latest")
	if err != nil {
		return 0, err
	}
	p, err := e.nonce(ctx, addr, "pending")
	if err != nil {
		return 0, err
	}
	if l != p {
		return 0, fmt.Errorf("%s has transactions in flight (nonce latest %d, pending %d); wait and retry", addr, l, p)
	}
	return l, nil
}

func (e evmRPC) balance(ctx context.Context, addr string) (*big.Int, error) {
	return e.bigCall(ctx, "eth_getBalance", addr, "latest")
}

func (e evmRPC) tokenBalance(ctx context.Context, contract, owner string) (*big.Int, error) {
	data := "0x70a08231" + strings.Repeat("0", 24) + strings.TrimPrefix(owner, "0x")
	var s string
	if err := e.call(ctx, "eth_call", []any{map[string]string{"to": contract, "data": data}, "latest"}, &s); err != nil {
		return nil, err
	}
	b, err := hexutil.Decode(s)
	if err != nil || len(b) != 32 {
		return nil, errors.New("balanceOf: bad return")
	}
	return new(big.Int).SetBytes(b), nil
}

type evmBlock struct {
	Number        string `json:"number"`
	Hash          string `json:"hash"`
	BaseFeePerGas string `json:"baseFeePerGas"`
}

func (e evmRPC) block(ctx context.Context, tag string) (*evmBlock, error) {
	var b *evmBlock
	if err := e.call(ctx, "eth_getBlockByNumber", []any{tag, false}, &b); err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("block %s not found", tag)
	}
	return b, nil
}

// estimate runs eth_estimateGas with a balance state override for from (so a
// zero-ETH deposit address can be estimated). On Arbitrum the estimate
// includes the L1 data component. An RPC without state-override support is
// an error, never a guess.
func (e evmRPC) estimate(ctx context.Context, from, to string, value *big.Int, data []byte, maxFee *big.Int) (uint64, error) {
	call := map[string]string{
		"from":                 from,
		"to":                   to,
		"maxFeePerGas":         hexutil.EncodeBig(maxFee),
		"maxPriorityFeePerGas": "0x0",
		"value":                hexutil.EncodeBig(value),
	}
	if len(data) > 0 {
		call["data"] = hexutil.Encode(data)
	}
	override := map[string]map[string]string{from: {"balance": "0x56bc75e2d63100000"}} // 100 ETH
	v, err := e.bigCall(ctx, "eth_estimateGas", call, "latest", override)
	if err != nil {
		return 0, fmt.Errorf("eth_estimateGas with state override failed (the RPC must support the 3rd stateOverride parameter): %w", err)
	}
	if !v.IsUint64() {
		return 0, errors.New("estimate overflow")
	}
	return v.Uint64(), nil
}

// withMargin is ×1.5 rounded up; absurd estimates saturate (the offline
// gas cap then refuses them).
func withMargin(g uint64) uint64 {
	if g > 1<<62 {
		return ^uint64(0)
	}
	return g + (g+1)/2
}

func erc20Transfer(to string, amount *big.Int) []byte {
	d, _ := hex.DecodeString("a9059cbb")
	d = append(d, common.LeftPadBytes(common.HexToAddress(to).Bytes(), 32)...)
	return append(d, common.LeftPadBytes(amount.Bytes(), 32)...)
}

type evmFees struct{ maxFee, prio *big.Int }

func (e evmRPC) fees(ctx context.Context) (*evmFees, error) {
	b, err := e.block(ctx, "latest")
	if err != nil {
		return nil, err
	}
	base, err := hexutil.DecodeBig(b.BaseFeePerGas)
	if err != nil {
		return nil, errors.New("latest block has no baseFeePerGas")
	}
	minBase := big.NewInt(10_000_000) // Arbitrum minimum base fee 0.01 gwei
	if base.Cmp(minBase) < 0 {
		base = minBase
	}
	maxFee := new(big.Int).Mul(base, big.NewInt(2))
	if maxFee.Cmp(sweep.EVMAbsoluteMaxFeePerGas) > 0 {
		return nil, fmt.Errorf("network base fee %s gwei is above what the offline signer will ever accept; wait", sweep.FormatUnits(base, 9))
	}
	return &evmFees{maxFee: maxFee, prio: new(big.Int)}, nil
}

type sourceArg struct {
	index   uint32
	address string
}

func prepareEVMSweep(ctx context.Context, rpc evmRPC, treasury string, sources []sourceArg, log io.Writer) (*sweep.Plan, error) {
	if err := checkEVMChain(ctx, rpc); err != nil {
		return nil, err
	}
	fees, err := rpc.fees(ctx)
	if err != nil {
		return nil, err
	}
	tNonce, err := rpc.stableNonce(ctx, treasury)
	if err != nil {
		return nil, err
	}
	tBal, err := rpc.balance(ctx, treasury)
	if err != nil {
		return nil, err
	}
	plan := &sweep.Plan{
		Version: sweep.PlanVersion, Kind: sweep.KindSweep, Network: chain.ArbitrumOne, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		EVM: &sweep.EVMPlan{
			ChainID: sweep.ArbitrumOneChainID.String(), Treasury: treasury, TreasuryNonce: tNonce,
			MaxFeePerGas: fees.maxFee.String(), MaxPriorityFeePerGas: fees.prio.String(),
		},
	}
	assets := assetsOf(chain.ArbitrumOne)
	spend := new(big.Int)
	var maxTopUpGas uint64
	for _, s := range sources {
		addr, err := chain.NormalizeEVMAddress(s.address)
		if err != nil {
			return nil, err
		}
		src := sweep.EVMSource{Index: s.index, Address: addr}
		need := new(big.Int)
		for _, a := range assets {
			bal, err := rpc.tokenBalance(ctx, a.Contract, addr)
			if err != nil {
				return nil, err
			}
			if bal.Sign() == 0 {
				continue
			}
			gas, err := rpc.estimate(ctx, addr, a.Contract, new(big.Int), erc20Transfer(treasury, bal), fees.maxFee)
			if err != nil {
				return nil, err
			}
			gl := withMargin(gas)
			if gl > sweep.EVMMaxGasLimit {
				return nil, fmt.Errorf("source %d %s: gas %d exceeds the offline cap", s.index, a.Symbol, gl)
			}
			need.Add(need, new(big.Int).Mul(new(big.Int).SetUint64(gl), fees.maxFee))
			src.Tokens = append(src.Tokens, sweep.EVMToken{Symbol: a.Symbol, Contract: a.Contract, Amount: bal.String(), GasLimit: fmt.Sprint(gl)})
		}
		if len(src.Tokens) == 0 {
			_, _ = fmt.Fprintf(log, "skip source %d %s: no USDC/USDT balance\n", s.index, addr)
			continue
		}
		if src.Nonce, err = rpc.stableNonce(ctx, addr); err != nil {
			return nil, err
		}
		eth, err := rpc.balance(ctx, addr)
		if err != nil {
			return nil, err
		}
		src.ETHBalance = eth.String()
		if need.Cmp(eth) > 0 {
			g, err := rpc.estimate(ctx, treasury, addr, new(big.Int).Sub(need, eth), nil, fees.maxFee)
			if err != nil {
				return nil, err
			}
			if gl := withMargin(g); gl > maxTopUpGas {
				maxTopUpGas = gl
			}
			spend.Add(spend, new(big.Int).Sub(need, eth))
		}
		plan.EVM.Sources = append(plan.EVM.Sources, src)
		_, _ = fmt.Fprintf(log, "source %d %s: %d token(s), nonce %d, ETH %s\n", s.index, addr, len(src.Tokens), src.Nonce, sweep.FormatUnits(eth, 18))
	}
	if len(plan.EVM.Sources) == 0 {
		return nil, errors.New("nothing to sweep")
	}
	if len(plan.EVM.Sources) > sweep.EVMMaxSources {
		return nil, fmt.Errorf("%d sources; at most %d per plan", len(plan.EVM.Sources), sweep.EVMMaxSources)
	}
	if maxTopUpGas == 0 {
		maxTopUpGas = withMargin(21_000)
	}
	if maxTopUpGas < sweep.EVMMinGasLimit {
		maxTopUpGas = sweep.EVMMinGasLimit
	}
	plan.EVM.TopUpGasLimit = fmt.Sprint(maxTopUpGas)
	// Same budget the offline side will compute.
	nTop := 0
	for _, s := range plan.EVM.Sources {
		need := new(big.Int)
		for _, t := range s.Tokens {
			g, _ := new(big.Int).SetString(t.GasLimit, 10)
			need.Add(need, new(big.Int).Mul(g, fees.maxFee))
		}
		e, _ := new(big.Int).SetString(s.ETHBalance, 10)
		if need.Cmp(e) > 0 {
			nTop++
		}
	}
	spend.Add(spend, new(big.Int).Mul(big.NewInt(int64(nTop)), new(big.Int).Mul(new(big.Int).SetUint64(maxTopUpGas), fees.maxFee)))
	_, _ = fmt.Fprintf(log, "treasury %s nonce %d ETH %s; this plan may spend up to %s ETH\n", treasury, tNonce, sweep.FormatUnits(tBal, 18), sweep.FormatUnits(spend, 18))
	if spend.Cmp(tBal) > 0 {
		return nil, fmt.Errorf("treasury ETH %s < needed %s: send at least the difference to %s on Arbitrum One first",
			sweep.FormatUnits(tBal, 18), sweep.FormatUnits(spend, 18), common.HexToAddress(treasury).Hex())
	}
	if spend.Cmp(sweep.EVMDefaultMaxTreasurySpend) > 0 {
		_, _ = fmt.Fprintln(log, "WARNING: above the default offline cap; sweep-sign will need --allow-high-fee (or sweep fewer sources)")
	}
	return plan, nil
}

func prepareEVMSend(ctx context.Context, rpc evmRPC, treasury, symbol, dest string, log io.Writer) (*sweep.Plan, error) {
	if err := checkEVMChain(ctx, rpc); err != nil {
		return nil, err
	}
	asset, err := sweep.AssetBySymbol(chain.ArbitrumOne, symbol)
	if err != nil {
		return nil, err
	}
	destAddr, err := chain.NormalizeEVMAddress(dest)
	if err != nil {
		return nil, err
	}
	fees, err := rpc.fees(ctx)
	if err != nil {
		return nil, err
	}
	n, err := rpc.stableNonce(ctx, treasury)
	if err != nil {
		return nil, err
	}
	bal, err := rpc.tokenBalance(ctx, asset.Contract, treasury)
	if err != nil {
		return nil, err
	}
	if bal.Sign() == 0 {
		return nil, fmt.Errorf("treasury holds no %s", symbol)
	}
	gas, err := rpc.estimate(ctx, treasury, asset.Contract, new(big.Int), erc20Transfer(destAddr, bal), fees.maxFee)
	if err != nil {
		return nil, err
	}
	gl := withMargin(gas)
	eth, err := rpc.balance(ctx, treasury)
	if err != nil {
		return nil, err
	}
	fee := new(big.Int).Mul(new(big.Int).SetUint64(gl), fees.maxFee)
	if eth.Cmp(fee) < 0 {
		return nil, fmt.Errorf("treasury ETH %s < fee cap %s", sweep.FormatUnits(eth, 18), sweep.FormatUnits(fee, 18))
	}
	_, _ = fmt.Fprintf(log, "treasury %s holds %s %s; nonce %d; fee cap %s ETH\n", treasury, sweep.FormatUnits(bal, 6), symbol, n, sweep.FormatUnits(fee, 18))
	return &sweep.Plan{
		Version: sweep.PlanVersion, Kind: sweep.KindSend, Network: chain.ArbitrumOne, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		EVM: &sweep.EVMPlan{
			ChainID: sweep.ArbitrumOneChainID.String(), Treasury: treasury, TreasuryNonce: n,
			MaxFeePerGas: fees.maxFee.String(), MaxPriorityFeePerGas: fees.prio.String(),
			Send: &sweep.EVMSendPlan{Symbol: asset.Symbol, Contract: asset.Contract, GasLimit: fmt.Sprint(gl), TreasuryBalance: bal.String(), DestHint: destAddr},
		},
	}, nil
}

func checkEVMChain(ctx context.Context, rpc evmRPC) error {
	id, err := rpc.chainID(ctx)
	if err != nil {
		return err
	}
	if id.Cmp(sweep.ArbitrumOneChainID) != 0 {
		return fmt.Errorf("RPC chain id %s is not Arbitrum One (42161)", id)
	}
	return nil
}

func assetsOf(n chain.Network) []chain.Asset {
	var out []chain.Asset
	for _, a := range chain.MainnetAssets() {
		if a.Network == n {
			out = append(out, a)
		}
	}
	return out
}

// ---- broadcast -----------------------------------------------------------

type evmReceipt struct {
	Status      string `json:"status"`
	BlockNumber string `json:"blockNumber"`
	BlockHash   string `json:"blockHash"`
	TxHash      string `json:"transactionHash"`
	Logs        []struct {
		Address string   `json:"address"`
		Topics  []string `json:"topics"`
		Data    string   `json:"data"`
	} `json:"logs"`
}

func (e evmRPC) receipt(ctx context.Context, h string) (*evmReceipt, error) {
	var r *evmReceipt
	if err := e.call(ctx, "eth_getTransactionReceipt", []any{h}, &r); err != nil {
		return nil, err
	}
	if r != nil && !strings.EqualFold(r.TxHash, h) {
		return nil, errors.New("receipt hash mismatch")
	}
	return r, nil
}

// checkTransferEvent requires exactly one Transfer(from, to, amount) log of
// the token contract in the receipt.
func checkTransferEvent(r *evmReceipt, d *sweep.DecodedEVMTx) error {
	want := 0
	for _, l := range r.Logs {
		if !strings.EqualFold(l.Address, d.To) || len(l.Topics) != 3 || !strings.EqualFold(l.Topics[0], chain.TransferTopic0) {
			continue
		}
		t1, t2 := strings.ToLower(strings.TrimPrefix(l.Topics[1], "0x")), strings.ToLower(strings.TrimPrefix(l.Topics[2], "0x"))
		if len(t1) != 64 || len(t2) != 64 {
			return errors.New("Transfer log topics malformed")
		}
		from, to := "0x"+t1[24:], "0x"+t2[24:]
		b, err := hexutil.Decode(l.Data)
		if err != nil || len(b) != 32 {
			return errors.New("Transfer log data undecodable")
		}
		amt := new(big.Int).SetBytes(b)
		if from == d.From && to == d.Recipient && amt.Cmp(d.Amount) == 0 {
			want++
		}
	}
	if want != 1 {
		return fmt.Errorf("expected exactly one matching Transfer event, found %d", want)
	}
	return nil
}

func broadcastEVM(ctx context.Context, rpc evmRPC, b *sweep.SignedBundle, res *result, opts bcastOpts) error {
	if err := checkEVMChain(ctx, rpc); err != nil {
		return err
	}
	decoded := make([]*sweep.DecodedEVMTx, len(b.Txs))
	raws := make([][]byte, len(b.Txs))
	for i, t := range b.Txs {
		raw, err := hexutil.Decode(t.Raw)
		if err != nil {
			return fmt.Errorf("tx %d: bad hex", i)
		}
		d, err := sweep.DecodeEVMSigned(raw)
		if err != nil {
			return fmt.Errorf("tx %d: %w", i, err)
		}
		if !strings.EqualFold(d.Hash, t.Hash) {
			return fmt.Errorf("tx %d: hash field does not match the raw transaction", i)
		}
		decoded[i], raws[i] = d, raw
		res.ensure(i, d.Hash, t.Role)
	}
	for i, d := range decoded {
		if res.saveErr != nil {
			return fmt.Errorf("stopping: could not write the result report: %w", res.saveErr)
		}
		st := res.Txs[i]
		r, err := rpc.receipt(ctx, d.Hash)
		if err != nil {
			return err
		}
		if r == nil {
			chainNonce, err := rpc.nonce(ctx, d.From, "latest")
			if err != nil {
				return err
			}
			switch {
			case chainNonce > d.Nonce:
				return res.stop(i, fmt.Sprintf("nonce %d of %s was consumed by a different transaction; do NOT retry. Check the explorer, `ledger close` offline, then prepare again", d.Nonce, d.From))
			case chainNonce < d.Nonce:
				return res.stop(i, fmt.Sprintf("an earlier nonce of %s (chain %d < %d) has not executed", d.From, chainNonce, d.Nonce))
			}
			need := new(big.Int).Add(d.Value, new(big.Int).Mul(new(big.Int).SetUint64(d.GasLimit), d.MaxFee))
			bal, err := rpc.balance(ctx, d.From)
			if err != nil {
				return err
			}
			if bal.Cmp(need) < 0 {
				return res.stop(i, fmt.Sprintf("%s has %s ETH < %s needed for this tx (top-up missing?)", d.From, sweep.FormatUnits(bal, 18), sweep.FormatUnits(need, 18)))
			}
			var h string
			if err := rpc.call(ctx, "eth_sendRawTransaction", []any{hexutil.Encode(raws[i])}, &h); err != nil {
				if !strings.Contains(strings.ToLower(err.Error()), "already known") {
					return res.stop(i, "send failed: "+err.Error())
				}
			} else if !strings.EqualFold(h, d.Hash) {
				return res.stop(i, "RPC returned a different tx hash")
			}
			st.Submitted = true
			res.save()
			_, _ = fmt.Fprintf(opts.log, "tx %d submitted %s\n", i+1, d.Hash)
			deadline := time.Now().Add(opts.receiptTimeout)
			for r == nil {
				if time.Now().After(deadline) {
					return res.stop(i, "submitted but no receipt before timeout; run broadcast again later: it re-checks the chain and may resend the same signed bytes once (same signature, executes at most once)")
				}
				if err := sleepCtx(ctx, opts.poll); err != nil {
					return err
				}
				if r, err = rpc.receipt(ctx, d.Hash); err != nil {
					return err
				}
			}
		}
		st.Submitted = true
		if _, err := hexutil.DecodeUint64(r.BlockNumber); err != nil || len(r.BlockHash) != 66 || !strings.HasPrefix(r.BlockHash, "0x") {
			return res.stop(i, "receipt has a malformed block number/hash")
		}
		if _, err := hexutil.Decode(r.BlockHash); err != nil {
			return res.stop(i, "receipt has a malformed block hash")
		}
		st.Block = r.BlockNumber
		st.BlockHash = r.BlockHash
		if r.Status != "0x1" {
			st.Status = "failed"
			return res.stop(i, "receipt status is not 1 (reverted); nonce consumed, fee burned. Stop: check explorer, ledger close, prepare again")
		}
		if d.IsTransfer {
			if err := checkTransferEvent(r, d); err != nil {
				return res.stop(i, err.Error())
			}
		}
		st.Status = "executed"
		res.save()
		_, _ = fmt.Fprintf(opts.log, "tx %d executed in block %s  https://arbiscan.io/tx/%s\n", i+1, r.BlockNumber, d.Hash)
	}
	if opts.finalityTimeout <= 0 {
		return nil
	}
	_, _ = fmt.Fprintln(opts.log, "waiting for Arbitrum finality (usually 15-30 min)...")
	deadline := time.Now().Add(opts.finalityTimeout)
	for {
		fb, err := rpc.block(ctx, "finalized")
		if err != nil {
			return err
		}
		fin, err := hexutil.DecodeBig(fb.Number)
		if err != nil {
			return errors.New("bad finalized block number")
		}
		all := true
		for i, d := range decoded {
			st := res.Txs[i]
			if st.Finalized {
				continue
			}
			bn, _ := hexutil.DecodeBig(st.Block)
			if bn == nil || bn.Cmp(fin) > 0 {
				all = false
				continue
			}
			r, err := rpc.receipt(ctx, d.Hash)
			if err != nil {
				return err
			}
			if r == nil || r.BlockHash != st.BlockHash || r.Status != "0x1" {
				return res.stop(i, "receipt changed before finality (reorg); check explorer")
			}
			st.Finalized = true
			res.save()
		}
		if all {
			_, _ = fmt.Fprintln(opts.log, "all transactions finalized")
			return nil
		}
		if time.Now().After(deadline) {
			_, _ = fmt.Fprintln(opts.log, "not all finalized before timeout; run broadcast again later to re-check")
			return nil
		}
		if err := sleepCtx(ctx, 15*time.Second); err != nil {
			return err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
