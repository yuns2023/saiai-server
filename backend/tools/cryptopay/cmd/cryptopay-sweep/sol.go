package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"sort"
	"time"

	solana "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/mr-tron/base58"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/tools/cryptopay/sweep"
)

type solRPC struct{ *rpcClient }

type solAccountInfo struct {
	Lamports uint64          `json:"lamports"`
	Owner    string          `json:"owner"`
	Data     json.RawMessage `json:"data"`
}

func (s solRPC) accountInfo(ctx context.Context, addr, encoding string) (*solAccountInfo, error) {
	var r struct {
		Value *solAccountInfo `json:"value"`
	}
	if err := s.call(ctx, "getAccountInfo", []any{addr, map[string]string{"encoding": encoding, "commitment": "confirmed"}}, &r); err != nil {
		return nil, err
	}
	return r.Value, nil
}

func (s solRPC) balance(ctx context.Context, addr string) (uint64, error) {
	var r struct {
		Value uint64 `json:"value"`
	}
	if err := s.call(ctx, "getBalance", []any{addr, map[string]string{"commitment": "confirmed"}}, &r); err != nil {
		return 0, err
	}
	return r.Value, nil
}

func (s solRPC) rentExempt(ctx context.Context, size int) (uint64, error) {
	var v uint64
	err := s.call(ctx, "getMinimumBalanceForRentExemption", []any{size}, &v)
	return v, err
}

// tokenAccount returns the raw amount of an SPL token account after checking
// its program owner, mint, owner and state; nil if it does not exist.
func (s solRPC) tokenAccount(ctx context.Context, ata, owner string, asset chain.Asset) (*big.Int, error) {
	info, err := s.accountInfo(ctx, ata, "jsonParsed")
	if err != nil || info == nil {
		return nil, err
	}
	if info.Owner != asset.TokenProgram {
		return nil, fmt.Errorf("%s is not owned by the token program", ata)
	}
	var d struct {
		Parsed struct {
			Type string `json:"type"`
			Info struct {
				Mint        string `json:"mint"`
				Owner       string `json:"owner"`
				State       string `json:"state"`
				TokenAmount struct {
					Amount string `json:"amount"`
				} `json:"tokenAmount"`
			} `json:"info"`
		} `json:"parsed"`
	}
	if err := json.Unmarshal(info.Data, &d); err != nil || d.Parsed.Type != "account" {
		return nil, fmt.Errorf("%s: not a parsed token account", ata)
	}
	i := d.Parsed.Info
	if i.Mint != asset.Contract || i.Owner != owner {
		return nil, fmt.Errorf("%s: mint/owner mismatch", ata)
	}
	if i.State != "initialized" {
		return nil, fmt.Errorf("%s: token account state %q", ata, i.State)
	}
	v, ok := new(big.Int).SetString(i.TokenAmount.Amount, 10)
	if !ok || v.Sign() < 0 {
		return nil, fmt.Errorf("%s: bad amount", ata)
	}
	return v, nil
}

type nonceState struct {
	authority string
	value     string
}

// parseNonceAccount decodes a System nonce account (Versions::Current,
// State::Initialized): u32 version, u32 state, authority[32], nonce[32], fee u64.
func parseNonceAccount(info *solAccountInfo) (*nonceState, error) {
	if info == nil {
		return nil, errors.New("nonce account does not exist")
	}
	if info.Owner != sweep.SystemProgramID {
		return nil, errors.New("nonce account is not owned by the System program")
	}
	var arr []string
	if err := json.Unmarshal(info.Data, &arr); err != nil || len(arr) != 2 || arr[1] != "base64" {
		return nil, errors.New("nonce account data: want base64")
	}
	b, err := base64.StdEncoding.DecodeString(arr[0])
	if err != nil || len(b) != 80 {
		return nil, errors.New("nonce account data is not 80 bytes")
	}
	if binary.LittleEndian.Uint32(b[0:4]) != 1 || binary.LittleEndian.Uint32(b[4:8]) != 1 {
		return nil, errors.New("nonce account is not initialized (current version)")
	}
	return &nonceState{authority: base58.Encode(b[8:40]), value: base58.Encode(b[40:72])}, nil
}

func (s solRPC) nonce(ctx context.Context, addr string) (*nonceState, error) {
	info, err := s.accountInfo(ctx, addr, "base64")
	if err != nil {
		return nil, err
	}
	return parseNonceAccount(info)
}

func (s solRPC) cuPrice(ctx context.Context, writable []string) (uint64, error) {
	var fees []struct {
		Fee uint64 `json:"prioritizationFee"`
	}
	if err := s.call(ctx, "getRecentPrioritizationFees", []any{writable}, &fees); err != nil {
		return 0, err
	}
	var v []uint64
	for _, f := range fees {
		v = append(v, f.Fee)
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	p := uint64(0)
	if len(v) > 0 {
		p = v[len(v)*3/4]
	}
	if p < 1000 {
		p = 1000
	}
	if p > sweep.SolanaMaxCUPriceMicros {
		p = sweep.SolanaMaxCUPriceMicros
	}
	return p, nil
}

func prepareSolanaSweep(ctx context.Context, rpc solRPC, treasury string, sources []sourceArg, nonceAccts []string, cuOverride uint64, log io.Writer) (*sweep.Plan, error) {
	if len(nonceAccts) == 0 || len(nonceAccts) > sweep.SolanaMaxTxsPerOp {
		return nil, fmt.Errorf("give 1..%d --nonce-account", sweep.SolanaMaxTxsPerOp)
	}
	if _, err := chain.DecodeSolanaPubkey(treasury); err != nil {
		return nil, err
	}
	type pending struct {
		src   sourceArg
		asset chain.Asset
		amt   *big.Int
	}
	var todo []pending
	assets := assetsOf(chain.SolanaMainnet)
	for _, s := range sources {
		if s.index >= sweep.SolanaPoolIndexLimit {
			return nil, fmt.Errorf("source index %d is reserved", s.index)
		}
		for _, a := range assets {
			ata, err := chain.SolanaATA(s.address, a.Contract, a.TokenProgram)
			if err != nil {
				return nil, err
			}
			amt, err := rpc.tokenAccount(ctx, ata, s.address, a)
			if err != nil {
				return nil, err
			}
			if amt == nil || amt.Sign() == 0 {
				continue
			}
			todo = append(todo, pending{s, a, amt})
			_, _ = fmt.Fprintf(log, "source %d %s: %s %s\n", s.index, s.address, sweep.FormatUnits(amt, 6), a.Symbol)
		}
	}
	if len(todo) == 0 {
		return nil, errors.New("nothing to sweep")
	}
	treasuryATAExists := map[string]bool{}
	for _, a := range assets {
		ata, _ := chain.SolanaATA(treasury, a.Contract, a.TokenProgram)
		amt, err := rpc.tokenAccount(ctx, ata, treasury, a)
		if err != nil {
			return nil, err
		}
		treasuryATAExists[a.Contract] = amt != nil
	}
	cu := cuOverride
	if cu == 0 {
		var err error
		if cu, err = rpc.cuPrice(ctx, []string{treasury}); err != nil {
			return nil, err
		}
	}
	plan := &sweep.Plan{
		Version: sweep.PlanVersion, Kind: sweep.KindSweep, Network: chain.SolanaMainnet, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Solana: &sweep.SolanaPlan{Treasury: treasury, CUPriceMicros: fmt.Sprint(cu)},
	}
	created := map[string]bool{}
	next := 0
	for _, na := range nonceAccts {
		if next >= len(todo) {
			break
		}
		ns, err := rpc.nonce(ctx, na)
		if err != nil {
			return nil, fmt.Errorf("nonce account %s: %w", na, err)
		}
		if ns.authority != treasury {
			return nil, fmt.Errorf("nonce account %s authority %s is not the treasury", na, ns.authority)
		}
		tx := sweep.SolanaTxPlan{NonceAccount: na, NonceValue: ns.value}
		for next < len(todo) {
			p := todo[next]
			cand := tx
			cand.Transfers = append(append([]sweep.SolanaTransfer(nil), tx.Transfers...), sweep.SolanaTransfer{
				Index: p.src.index, Owner: p.src.address, Symbol: p.asset.Symbol, Mint: p.asset.Contract, Amount: p.amt.String(),
			})
			cand.CreateTreasuryATA = append([]string(nil), tx.CreateTreasuryATA...)
			if !treasuryATAExists[p.asset.Contract] && !created[p.asset.Contract] && !contains(cand.CreateTreasuryATA, p.asset.Symbol) {
				cand.CreateTreasuryATA = append(cand.CreateTreasuryATA, p.asset.Symbol)
			}
			size, err := solTxPlanSize(treasury, cu, cand)
			if err != nil {
				return nil, err
			}
			if size > sweep.SolanaMaxPacketBytes {
				if len(tx.Transfers) == 0 {
					return nil, errors.New("a single transfer does not fit in a transaction")
				}
				break
			}
			tx = cand
			next++
		}
		for _, sym := range tx.CreateTreasuryATA {
			a, _ := sweep.AssetBySymbol(chain.SolanaMainnet, sym)
			created[a.Contract] = true
		}
		plan.Solana.Txs = append(plan.Solana.Txs, tx)
	}
	if next < len(todo) {
		_, _ = fmt.Fprintf(log, "NOTE: %d token account(s) did not fit into %d transaction(s); sweep them in the next round\n", len(todo)-next, len(plan.Solana.Txs))
	}
	// Treasury SOL check: fees + rent, and the treasury must stay rent-exempt.
	fee, rent := new(big.Int), new(big.Int)
	for _, t := range plan.Solana.Txs {
		owners := map[string]bool{}
		for _, tr := range t.Transfers {
			owners[tr.Owner] = true
		}
		fee.Add(fee, sweep.SolanaTxFeeLamports(1+len(owners), cu))
		rent.Add(rent, new(big.Int).SetUint64(uint64(len(t.CreateTreasuryATA))*sweep.SolanaATARentLamports))
	}
	if err := checkTreasurySOL(ctx, rpc, treasury, new(big.Int).Add(fee, rent), log); err != nil {
		return nil, err
	}
	return plan, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func checkTreasurySOL(ctx context.Context, rpc solRPC, treasury string, cost *big.Int, log io.Writer) error {
	bal, err := rpc.balance(ctx, treasury)
	if err != nil {
		return err
	}
	minBal, err := rpc.rentExempt(ctx, 0)
	if err != nil {
		return err
	}
	need := new(big.Int).Add(cost, new(big.Int).SetUint64(minBal))
	_, _ = fmt.Fprintf(log, "treasury %s SOL %s; this plan may cost up to %s SOL (+ %s SOL must stay as rent reserve)\n",
		treasury, sweep.FormatSOL(fmt.Sprint(bal)), sweep.FormatSOL(cost.String()), sweep.FormatSOL(fmt.Sprint(minBal)))
	if new(big.Int).SetUint64(bal).Cmp(need) < 0 {
		return fmt.Errorf("treasury SOL too low: have %s, need %s; send SOL to %s first", sweep.FormatSOL(fmt.Sprint(bal)), sweep.FormatSOL(need.String()), treasury)
	}
	return nil
}

// solTxPlanSize compiles a plan tx exactly as the offline side would (without
// keys) to measure its wire size.
func solTxPlanSize(treasury string, cu uint64, tp sweep.SolanaTxPlan) (int, error) {
	in := sweep.SolanaTxIntent{Role: sweep.RoleSweep, Treasury: treasury, NonceAccount: tp.NonceAccount, NonceValue: tp.NonceValue, CUPriceMicros: cu}
	for _, sym := range tp.CreateTreasuryATA {
		a, err := sweep.AssetBySymbol(chain.SolanaMainnet, sym)
		if err != nil {
			return 0, err
		}
		in.CreateATA = append(in.CreateATA, a)
	}
	for _, t := range tp.Transfers {
		a, err := sweep.AssetBySymbol(chain.SolanaMainnet, t.Symbol)
		if err != nil {
			return 0, err
		}
		src, err := chain.SolanaATA(t.Owner, a.Contract, a.TokenProgram)
		if err != nil {
			return 0, err
		}
		dst, err := chain.SolanaATA(treasury, a.Contract, a.TokenProgram)
		if err != nil {
			return 0, err
		}
		amt, _ := new(big.Int).SetString(t.Amount, 10)
		in.Transfers = append(in.Transfers, sweep.SolanaTransferIntent{SourceIndex: t.Index, Owner: t.Owner, SourceATA: src, Asset: a, DestOwner: treasury, DestATA: dst, Amount: amt.Uint64()})
	}
	return sweep.SolanaTxSize(&in)
}

func prepareSolanaSend(ctx context.Context, rpc solRPC, treasury, symbol, dest, nonceAcct string, cuOverride uint64, log io.Writer) (*sweep.Plan, error) {
	asset, err := sweep.AssetBySymbol(chain.SolanaMainnet, symbol)
	if err != nil {
		return nil, err
	}
	if _, err := chain.DecodeSolanaPubkey(dest); err != nil {
		return nil, err
	}
	srcATA, _ := chain.SolanaATA(treasury, asset.Contract, asset.TokenProgram)
	bal, err := rpc.tokenAccount(ctx, srcATA, treasury, asset)
	if err != nil {
		return nil, err
	}
	if bal == nil || bal.Sign() == 0 {
		return nil, fmt.Errorf("treasury holds no %s", symbol)
	}
	dstATA, err := chain.SolanaATA(dest, asset.Contract, asset.TokenProgram)
	if err != nil {
		return nil, err
	}
	if d, err := rpc.tokenAccount(ctx, dstATA, dest, asset); err != nil {
		return nil, err
	} else if d == nil {
		return nil, fmt.Errorf("destination %s has no %s token account (%s); send-sign never creates one. Check the address / network in the exchange app", dest, symbol, dstATA)
	}
	ns, err := rpc.nonce(ctx, nonceAcct)
	if err != nil {
		return nil, err
	}
	if ns.authority != treasury {
		return nil, fmt.Errorf("nonce account authority %s is not the treasury", ns.authority)
	}
	cu := cuOverride
	if cu == 0 {
		if cu, err = rpc.cuPrice(ctx, []string{treasury, srcATA}); err != nil {
			return nil, err
		}
	}
	if err := checkTreasurySOL(ctx, rpc, treasury, sweep.SolanaTxFeeLamports(1, cu), log); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "treasury holds %s %s\n", sweep.FormatUnits(bal, 6), symbol)
	return &sweep.Plan{
		Version: sweep.PlanVersion, Kind: sweep.KindSend, Network: chain.SolanaMainnet, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Solana: &sweep.SolanaPlan{Treasury: treasury, CUPriceMicros: fmt.Sprint(cu), Send: &sweep.SolanaSendPlan{
			NonceAccount: nonceAcct, NonceValue: ns.value, Symbol: asset.Symbol, Mint: asset.Contract, TreasuryBalance: bal.String(), DestHint: dest,
		}},
	}, nil
}

// ---- broadcast -----------------------------------------------------------

type sigStatus struct {
	Err                json.RawMessage `json:"err"`
	ConfirmationStatus string          `json:"confirmationStatus"`
	Slot               uint64          `json:"slot"`
}

func (s solRPC) status(ctx context.Context, sig string) (*sigStatus, error) {
	var r struct {
		Value []*sigStatus `json:"value"`
	}
	if err := s.call(ctx, "getSignatureStatuses", []any{[]string{sig}, map[string]bool{"searchTransactionHistory": true}}, &r); err != nil {
		return nil, err
	}
	if len(r.Value) != 1 {
		return nil, errors.New("getSignatureStatuses: bad response")
	}
	return r.Value[0], nil
}

func isNullErr(raw json.RawMessage) bool { return len(raw) == 0 || string(raw) == "null" }

type tokenBal struct {
	AccountIndex  int    `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	UiTokenAmount struct {
		Amount string `json:"amount"`
	} `json:"uiTokenAmount"`
}

// verifySolanaEffects checks via getTransaction that every TransferChecked of
// the wire tx moved exactly its amount (source -amount, destination +amount).
func verifySolanaEffects(ctx context.Context, rpc solRPC, w *sweep.SolanaWire) error {
	var tx *struct {
		Meta *struct {
			Err               json.RawMessage `json:"err"`
			PreTokenBalances  []tokenBal      `json:"preTokenBalances"`
			PostTokenBalances []tokenBal      `json:"postTokenBalances"`
		} `json:"meta"`
		Transaction struct {
			Message struct {
				AccountKeys []string `json:"accountKeys"`
			} `json:"message"`
		} `json:"transaction"`
	}
	if err := rpc.call(ctx, "getTransaction", []any{w.Signature, map[string]any{"encoding": "json", "commitment": "confirmed", "maxSupportedTransactionVersion": 0}}, &tx); err != nil {
		return err
	}
	if tx == nil || tx.Meta == nil {
		return errors.New("getTransaction returned null")
	}
	if !isNullErr(tx.Meta.Err) {
		return errors.New("transaction failed on chain")
	}
	keys := tx.Transaction.Message.AccountKeys
	if len(keys) != len(w.Keys) {
		return errors.New("account keys differ from the signed transaction")
	}
	for i := range keys {
		if keys[i] != w.Keys[i] {
			return errors.New("account keys differ from the signed transaction")
		}
	}
	var parseErr error
	bal := func(list []tokenBal, acct string) *big.Int {
		found := new(big.Int)
		n := 0
		for _, b := range list {
			if b.AccountIndex < 0 || b.AccountIndex >= len(keys) {
				parseErr = errors.New("token balance index out of range")
				continue
			}
			if keys[b.AccountIndex] != acct {
				continue
			}
			v, ok := new(big.Int).SetString(b.UiTokenAmount.Amount, 10)
			if !ok || v.Sign() < 0 {
				parseErr = errors.New("token balance amount malformed")
				continue
			}
			found, n = v, n+1
		}
		if n > 1 {
			parseErr = errors.New("duplicate token balance entries")
		}
		return found
	}
	delta := map[string]*big.Int{}
	for _, ix := range w.Instructions {
		if ix.Program != chain.SolanaTokenProgram || len(ix.Data) != 10 || ix.Data[0] != 12 {
			continue
		}
		amt := new(big.Int).SetUint64(binary.LittleEndian.Uint64(ix.Data[1:9]))
		src, dst := ix.Accounts[0].Key, ix.Accounts[2].Key
		if delta[src] == nil {
			delta[src] = new(big.Int)
		}
		if delta[dst] == nil {
			delta[dst] = new(big.Int)
		}
		delta[src].Sub(delta[src], amt)
		delta[dst].Add(delta[dst], amt)
	}
	for acct, want := range delta {
		got := new(big.Int).Sub(bal(tx.Meta.PostTokenBalances, acct), bal(tx.Meta.PreTokenBalances, acct))
		if parseErr != nil {
			return parseErr
		}
		if got.Cmp(want) != 0 {
			return fmt.Errorf("token account %s changed by %s, expected %s", acct, got, want)
		}
	}
	return nil
}

func broadcastSolana(ctx context.Context, rpc solRPC, b *sweep.SignedBundle, res *result, opts bcastOpts) error {
	wires := make([]*sweep.SolanaWire, len(b.Txs))
	for i, t := range b.Txs {
		raw, err := base64.StdEncoding.DecodeString(t.Raw)
		if err != nil {
			return fmt.Errorf("tx %d: bad base64", i)
		}
		w, err := sweep.ParseSolanaWire(raw)
		if err != nil {
			return fmt.Errorf("tx %d: %w", i, err)
		}
		if w.Signature != t.Hash {
			return fmt.Errorf("tx %d: hash field does not match the raw transaction", i)
		}
		if w.AdvanceNonceAccount() == "" {
			return fmt.Errorf("tx %d is not a durable-nonce transaction", i)
		}
		wires[i] = w
		res.ensure(i, w.Signature, t.Role)
	}
	for i, w := range wires {
		if res.saveErr != nil {
			return fmt.Errorf("stopping: could not write the result report: %w", res.saveErr)
		}
		st := res.Txs[i]
		s, err := rpc.status(ctx, w.Signature)
		if err != nil {
			return err
		}
		if s == nil {
			ns, err := rpc.nonce(ctx, w.AdvanceNonceAccount())
			if err != nil {
				return err
			}
			if ns.value != w.Blockhash {
				return res.stop(i, "the nonce account has advanced and this signature is not on chain: it can never land. Check the explorer, ledger close offline, prepare again")
			}
			raw, _ := base64.StdEncoding.DecodeString(b.Txs[i].Raw)
			send := func() error {
				var sig string
				err := rpc.call(ctx, "sendTransaction", []any{base64.StdEncoding.EncodeToString(raw), map[string]any{
					"encoding": "base64", "skipPreflight": false, "preflightCommitment": "confirmed", "maxRetries": 0,
				}}, &sig)
				if err == nil && sig != w.Signature {
					return errors.New("RPC returned a different signature")
				}
				return err
			}
			if err := send(); err != nil {
				return res.stop(i, "send failed (simulation or RPC): "+err.Error())
			}
			st.Submitted = true
			res.save()
			_, _ = fmt.Fprintf(opts.log, "tx %d submitted %s\n", i+1, w.Signature)
			deadline := time.Now().Add(opts.receiptTimeout)
			for s == nil || s.ConfirmationStatus == "processed" {
				if time.Now().After(deadline) {
					return res.stop(i, "submitted but not confirmed before timeout. Nothing is re-sent automatically: run broadcast again later; it re-checks the signature and the nonce first")
				}
				if err := sleepCtx(ctx, opts.poll); err != nil {
					return err
				}
				if s, err = rpc.status(ctx, w.Signature); err != nil {
					return err
				}
			}
		}
		st.Submitted = true
		if !isNullErr(s.Err) {
			st.Status = "failed"
			return res.stop(i, "transaction failed on chain (nonce advanced, fee paid): "+string(s.Err))
		}
		if err := verifySolanaEffects(ctx, rpc, w); err != nil {
			return res.stop(i, err.Error())
		}
		st.Status = "executed"
		st.Block = fmt.Sprint(s.Slot)
		res.save()
		_, _ = fmt.Fprintf(opts.log, "tx %d executed (slot %d)  https://solscan.io/tx/%s\n", i+1, s.Slot, w.Signature)
	}
	if opts.finalityTimeout <= 0 {
		return nil
	}
	deadline := time.Now().Add(opts.finalityTimeout)
	for {
		all := true
		for i, w := range wires {
			if res.Txs[i].Finalized {
				continue
			}
			s, err := rpc.status(ctx, w.Signature)
			if err != nil {
				return err
			}
			if s == nil || !isNullErr(s.Err) {
				return res.stop(i, "status disappeared or failed before finality; check explorer")
			}
			if s.ConfirmationStatus == "finalized" {
				res.Txs[i].Finalized = true
				res.save()
			} else {
				all = false
			}
		}
		if all {
			_, _ = fmt.Fprintln(opts.log, "all transactions finalized")
			return nil
		}
		if time.Now().After(deadline) {
			_, _ = fmt.Fprintln(opts.log, "not all finalized before timeout; run broadcast again later to re-check")
			return nil
		}
		if err := sleepCtx(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

// ---- nonce-setup ---------------------------------------------------------

type ephemeralFile struct {
	Version      int      `json:"version"`
	Payer        string   `json:"payer_secret_base58"`
	NonceSecrets []string `json:"nonce_secrets_base58"`
	Treasury     string   `json:"treasury"`
}

func cmdNonceSetupRun(ctx context.Context, rpc solRPC, treasury string, count int, keyFile string, resume bool, log io.Writer) error {
	if _, err := chain.DecodeSolanaPubkey(treasury); err != nil {
		return err
	}
	var ef ephemeralFile
	if resume {
		data, err := os.ReadFile(keyFile)
		if err != nil {
			return err
		}
		if err := sweep.DecodeStrict(data, &ef); err != nil {
			return err
		}
		if ef.Treasury != treasury {
			return errors.New("--treasury differs from the key file")
		}
	} else {
		if count < 1 || count > sweep.SolanaMaxRegisteredNonces {
			return fmt.Errorf("--count must be 1..%d", sweep.SolanaMaxRegisteredNonces)
		}
		// Random online keys, NOT from the mnemonic: the payer only ever holds
		// the few cents the operator sends it; nonce account keys are needed
		// only to sign their own creation.
		ef = ephemeralFile{Version: 1, Treasury: treasury}
		_, p, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		ef.Payer = base58.Encode(p)
		for i := 0; i < count; i++ {
			_, k, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return err
			}
			ef.NonceSecrets = append(ef.NonceSecrets, base58.Encode(k))
		}
		data, _ := json.MarshalIndent(&ef, "", "  ")
		if err := sweep.AtomicWriteFile(keyFile, append(data, '\n'), 0o600, false); err != nil {
			return err
		}
	}
	payer, err := solana.PrivateKeyFromBase58(ef.Payer)
	if err != nil {
		return err
	}
	rent, err := rpc.rentExempt(ctx, 80)
	if err != nil {
		return err
	}
	if ef.Version != 1 || len(ef.NonceSecrets) < 1 || len(ef.NonceSecrets) > sweep.SolanaMaxRegisteredNonces {
		return errors.New("bad temporary key file")
	}
	missing := 0
	seenNK := map[string]bool{}
	for _, ns := range ef.NonceSecrets {
		nk, err := solana.PrivateKeyFromBase58(ns)
		if err != nil {
			return err
		}
		addr := nk.PublicKey().String()
		if seenNK[addr] {
			return errors.New("duplicate nonce key in the temporary key file")
		}
		seenNK[addr] = true
		info, err := rpc.accountInfo(ctx, addr, "base64")
		if err != nil {
			return err
		}
		if info == nil {
			missing++
		}
	}
	need := uint64(missing)*(rent+10_000) + 10_000
	_, _ = fmt.Fprintf(log, "临时付款地址（随机生成，私钥在 %s，不来自助记词）:\n    %s\n请从交易所往它提 至少 %s SOL（Solana 网络）。等待到账...\n",
		keyFile, payer.PublicKey(), sweep.FormatSOL(fmt.Sprint(need)))
	for missing > 0 {
		bal, err := rpc.balance(ctx, payer.PublicKey().String())
		if err != nil {
			return err
		}
		if bal >= need {
			break
		}
		if err := sleepCtx(ctx, 5*time.Second); err != nil {
			return err
		}
	}
	treasuryPK := solana.MustPublicKeyFromBase58(treasury)
	for _, ns := range ef.NonceSecrets {
		nk, err := solana.PrivateKeyFromBase58(ns)
		if err != nil {
			return err
		}
		addr := nk.PublicKey().String()
		if info, err := rpc.accountInfo(ctx, addr, "base64"); err != nil {
			return err
		} else if info != nil {
			st, err := parseNonceAccount(info)
			if err != nil || st.authority != treasury {
				return fmt.Errorf("account %s exists but is not a nonce account of the treasury", addr)
			}
			_, _ = fmt.Fprintf(log, "nonce account %s already exists\n", addr)
			continue
		}
		bh, err := latestBlockhash(ctx, rpc)
		if err != nil {
			return err
		}
		tx, err := solana.NewTransaction([]solana.Instruction{
			system.NewCreateAccountInstruction(rent, 80, solana.SystemProgramID, payer.PublicKey(), nk.PublicKey()).Build(),
			system.NewInitializeNonceAccountInstruction(treasuryPK, nk.PublicKey(), solana.SysVarRecentBlockHashesPubkey, solana.SysVarRentPubkey).Build(),
		}, bh, solana.TransactionPayer(payer.PublicKey()))
		if err != nil {
			return err
		}
		if err := signSendConfirm(ctx, rpc, tx, map[solana.PublicKey]solana.PrivateKey{payer.PublicKey(): payer, nk.PublicKey(): nk}, log); err != nil {
			return err
		}
		st, err := rpc.nonce(ctx, addr)
		if err != nil {
			return err
		}
		if st.authority != treasury {
			return fmt.Errorf("created nonce account %s has authority %s", addr, st.authority)
		}
		_, _ = fmt.Fprintf(log, "nonce account created: %s  (authority = treasury)\n    https://solscan.io/account/%s\n", addr, addr)
	}
	// Return the remainder to the treasury.
	bal, err := rpc.balance(ctx, payer.PublicKey().String())
	if err != nil {
		return err
	}
	if bal > 5000 {
		amt := bal - 5000
		tbal, err := rpc.balance(ctx, treasury)
		if err != nil {
			return err
		}
		minBal, err := rpc.rentExempt(ctx, 0)
		if err != nil {
			return err
		}
		if tbal == 0 && amt < minBal {
			_, _ = fmt.Fprintf(log, "remaining %s SOL is below the rent minimum for an empty treasury; left on the temporary address (key file kept)\n", sweep.FormatSOL(fmt.Sprint(amt)))
		} else {
			bh, err := latestBlockhash(ctx, rpc)
			if err != nil {
				return err
			}
			tx, err := solana.NewTransaction([]solana.Instruction{
				system.NewTransferInstruction(amt, payer.PublicKey(), treasuryPK).Build(),
			}, bh, solana.TransactionPayer(payer.PublicKey()))
			if err != nil {
				return err
			}
			if err := signSendConfirm(ctx, rpc, tx, map[solana.PublicKey]solana.PrivateKey{payer.PublicKey(): payer}, log); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(log, "sent remaining %s SOL to the treasury\n", sweep.FormatSOL(fmt.Sprint(amt)))
		}
	}
	_, _ = fmt.Fprintln(log, "下一步: 用手机打开上面的 solscan 链接，核对 Authority = 你的 Solana 金库地址，然后在离线机运行 nonce-register。")
	return nil
}

func latestBlockhash(ctx context.Context, rpc solRPC) (solana.Hash, error) {
	var r struct {
		Value struct {
			Blockhash string `json:"blockhash"`
		} `json:"value"`
	}
	if err := rpc.call(ctx, "getLatestBlockhash", []any{map[string]string{"commitment": "confirmed"}}, &r); err != nil {
		return solana.Hash{}, err
	}
	return solana.HashFromBase58(r.Value.Blockhash)
}

func signSendConfirm(ctx context.Context, rpc solRPC, tx *solana.Transaction, keys map[solana.PublicKey]solana.PrivateKey, log io.Writer) error {
	if _, err := tx.Sign(func(k solana.PublicKey) *solana.PrivateKey {
		if v, ok := keys[k]; ok {
			return &v
		}
		return nil
	}); err != nil {
		return err
	}
	raw, err := tx.MarshalBinary()
	if err != nil {
		return err
	}
	sig := tx.Signatures[0].String()
	var got string
	if err := rpc.call(ctx, "sendTransaction", []any{base64.StdEncoding.EncodeToString(raw), map[string]any{"encoding": "base64", "preflightCommitment": "confirmed"}}, &got); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(log, "submitted %s\n", sig)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return err
		}
		s, err := rpc.status(ctx, sig)
		if err != nil {
			return err
		}
		if s != nil && !isNullErr(s.Err) {
			return fmt.Errorf("transaction %s failed: %s", sig, string(s.Err))
		}
		if s != nil && (s.ConfirmationStatus == "confirmed" || s.ConfirmationStatus == "finalized") {
			return nil
		}
	}
	return fmt.Errorf("transaction %s not confirmed in time; re-run with --resume", sig)
}
