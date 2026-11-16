package sweep

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"

	solana "github.com/gagliardetto/solana-go"
	ata "github.com/gagliardetto/solana-go/programs/associated-token-account"
	computebudget "github.com/gagliardetto/solana-go/programs/compute-budget"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/programs/token"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

// Well-known program and sysvar ids (base58). Checked against solana-go's
// constants in tests.
const (
	SystemProgramID          = "11111111111111111111111111111111"
	ComputeBudgetProgramID   = "ComputeBudget111111111111111111111111111111"
	SysvarRecentBlockhashes  = "SysvarRecentB1ockHashes11111111111111111111"
	SysvarRent               = "SysvarRent111111111111111111111111111111111"
	solanaTokenDecimals      = 6
	solanaNonceAccountLength = 80
)

// SolanaTransferIntent is one transferChecked in a transaction.
type SolanaTransferIntent struct {
	SourceIndex uint32 // sweep only
	Owner       string // signer (pool owner for sweeps, treasury for sends)
	SourceATA   string
	Asset       chain.Asset
	DestOwner   string // treasury (sweep) or registered destination (send)
	DestATA     string
	Amount      uint64
}

// SolanaTxIntent is one Solana transaction as decided offline.
type SolanaTxIntent struct {
	Role          string // RoleSweep | RoleSend
	Treasury      string // fee payer and nonce authority
	NonceAccount  string
	NonceValue    string
	CUPriceMicros uint64
	CreateATA     []chain.Asset // treasury ATAs to create (idempotent), paid by the treasury
	Transfers     []SolanaTransferIntent
}

// Signers returns the required signers: treasury first, then unique owners
// in order of appearance.
func (in *SolanaTxIntent) Signers() []string {
	out := []string{in.Treasury}
	seen := map[string]bool{in.Treasury: true}
	for _, t := range in.Transfers {
		if !seen[t.Owner] {
			seen[t.Owner] = true
			out = append(out, t.Owner)
		}
	}
	return out
}

// FeeLamports is the maximum fee (signatures + priority).
func (in *SolanaTxIntent) FeeLamports() *big.Int {
	return SolanaTxFeeLamports(len(in.Signers()), in.CUPriceMicros)
}

// RentLamports is the ATA creation rent this tx may pay.
func (in *SolanaTxIntent) RentLamports() *big.Int {
	return new(big.Int).Mul(big.NewInt(int64(len(in.CreateATA))), new(big.Int).SetUint64(SolanaATARentLamports))
}

// SolanaOp is a validated Solana operation ready to sign.
type SolanaOp struct {
	Kind     Kind
	Treasury string
	Txs      []SolanaTxIntent
	Budget   Budget
	Totals   map[string]*big.Int
}

func parseCUPrice(s string) (uint64, error) {
	v, err := ParseUint64(s)
	if err != nil {
		return 0, fmt.Errorf("cu_price_micro_lamports: %w", err)
	}
	if v > SolanaMaxCUPriceMicros {
		return 0, fmt.Errorf("sweep: compute unit price %d exceeds the cap %d micro-lamports", v, SolanaMaxCUPriceMicros)
	}
	return v, nil
}

func parseNonceValue(s string) error {
	// A durable nonce value is a 32-byte hash in canonical base58.
	if _, err := chain.DecodeSolanaPubkey(s); err != nil {
		return fmt.Errorf("sweep: invalid nonce value %s", clip(s))
	}
	return nil
}

func parseTokenAmountU64(s string) (uint64, error) {
	v, err := ParsePositiveUint(s)
	if err != nil {
		return 0, err
	}
	if !v.IsUint64() {
		return 0, errors.New("sweep: token amount exceeds u64")
	}
	return v.Uint64(), nil
}

func checkSolanaTreasury(p *SolanaPlan, kr *Keyring) (*SolanaKey, error) {
	t, err := kr.SolanaTreasury()
	if err != nil {
		return nil, err
	}
	if p.Treasury != t.Address {
		return nil, fmt.Errorf("sweep: plan treasury %s is not this mnemonic's treasury %s", clip(p.Treasury), t.Address)
	}
	return t, nil
}

// BuildSolanaSweep validates a Solana sweep plan. isNonceRegistered must
// consult the offline registry.
func BuildSolanaSweep(p *Plan, kr *Keyring, isNonceRegistered func(string) bool) (*SolanaOp, error) {
	if p.Kind != KindSweep || p.Network != chain.SolanaMainnet || p.Solana == nil {
		return nil, errors.New("sweep: not a solana-mainnet sweep plan")
	}
	s := p.Solana
	treasury, err := checkSolanaTreasury(s, kr)
	if err != nil {
		return nil, err
	}
	cuPrice, err := parseCUPrice(s.CUPriceMicros)
	if err != nil {
		return nil, err
	}
	if len(s.Txs) == 0 || len(s.Txs) > SolanaMaxTxsPerOp {
		return nil, fmt.Errorf("sweep: %d transactions; allowed 1..%d", len(s.Txs), SolanaMaxTxsPerOp)
	}
	op := &SolanaOp{Kind: KindSweep, Treasury: treasury.Address, Totals: map[string]*big.Int{}}
	seenNonce := map[string]bool{}
	seenOwnerMint := map[string]bool{}
	seenCreate := map[string]bool{}
	seenIndexOwner := map[uint32]string{}
	fee, rent := new(big.Int), new(big.Int)
	for ti, tp := range s.Txs {
		if !isNonceRegistered(tp.NonceAccount) {
			return nil, fmt.Errorf("sweep: tx %d nonce account %s is not registered offline (nonce-register)", ti, clip(tp.NonceAccount))
		}
		if seenNonce[tp.NonceAccount] {
			return nil, fmt.Errorf("sweep: nonce account %s used twice", tp.NonceAccount)
		}
		seenNonce[tp.NonceAccount] = true
		if err := parseNonceValue(tp.NonceValue); err != nil {
			return nil, err
		}
		if len(tp.Transfers) == 0 {
			return nil, fmt.Errorf("sweep: tx %d has no transfers", ti)
		}
		in := SolanaTxIntent{Role: RoleSweep, Treasury: treasury.Address, NonceAccount: tp.NonceAccount, NonceValue: tp.NonceValue, CUPriceMicros: cuPrice}
		mintsInTx := map[string]bool{}
		for _, tr := range tp.Transfers {
			key, err := kr.SolanaPool(tr.Index)
			if err != nil {
				return nil, err
			}
			if tr.Owner != key.Address {
				return nil, fmt.Errorf("sweep: owner %s does not derive from pool index %d of this mnemonic", clip(tr.Owner), tr.Index)
			}
			if prev, ok := seenIndexOwner[tr.Index]; ok && prev != tr.Owner {
				return nil, errors.New("sweep: internal: index/owner mismatch")
			}
			seenIndexOwner[tr.Index] = tr.Owner
			if tr.Owner == treasury.Address {
				return nil, errors.New("sweep: source equals treasury")
			}
			asset, err := assetBySymbol(chain.SolanaMainnet, tr.Symbol, tr.Mint)
			if err != nil {
				return nil, err
			}
			k := tr.Owner + "/" + asset.Contract
			if seenOwnerMint[k] {
				return nil, fmt.Errorf("sweep: source %d %s listed twice", tr.Index, asset.Symbol)
			}
			seenOwnerMint[k] = true
			amt, err := parseTokenAmountU64(tr.Amount)
			if err != nil {
				return nil, fmt.Errorf("source %d %s amount: %w", tr.Index, tr.Symbol, err)
			}
			srcATA, err := chain.SolanaATA(tr.Owner, asset.Contract, asset.TokenProgram)
			if err != nil {
				return nil, err
			}
			dstATA, err := chain.SolanaATA(treasury.Address, asset.Contract, asset.TokenProgram)
			if err != nil {
				return nil, err
			}
			in.Transfers = append(in.Transfers, SolanaTransferIntent{
				SourceIndex: tr.Index, Owner: tr.Owner, SourceATA: srcATA, Asset: asset,
				DestOwner: treasury.Address, DestATA: dstATA, Amount: amt,
			})
			mintsInTx[asset.Contract] = true
			if op.Totals[asset.Symbol] == nil {
				op.Totals[asset.Symbol] = new(big.Int)
			}
			op.Totals[asset.Symbol].Add(op.Totals[asset.Symbol], new(big.Int).SetUint64(amt))
		}
		for _, sym := range tp.CreateTreasuryATA {
			asset, err := AssetBySymbol(chain.SolanaMainnet, sym)
			if err != nil {
				return nil, err
			}
			if seenCreate[asset.Contract] {
				return nil, fmt.Errorf("sweep: treasury %s ATA creation requested twice", sym)
			}
			if !mintsInTx[asset.Contract] {
				return nil, fmt.Errorf("sweep: tx %d creates the treasury %s ATA but transfers no %s", ti, sym, sym)
			}
			seenCreate[asset.Contract] = true
			in.CreateATA = append(in.CreateATA, asset)
		}
		fee.Add(fee, in.FeeLamports())
		rent.Add(rent, in.RentLamports())
		op.Txs = append(op.Txs, in)
	}
	op.Budget = Budget{SolanaFeeLamports: fee.String(), SolanaRentLamports: rent.String()}
	if err := op.Budget.CheckSolana(); err != nil {
		return nil, err
	}
	return op, nil
}

// BuildSolanaSend validates a Solana send plan with an offline-registered
// destination and offline-entered amount.
func BuildSolanaSend(p *Plan, kr *Keyring, isNonceRegistered func(string) bool, dest Destination, amount *big.Int) (*SolanaOp, error) {
	if p.Kind != KindSend || p.Network != chain.SolanaMainnet || p.Solana == nil || p.Solana.Send == nil {
		return nil, errors.New("sweep: not a solana-mainnet send plan")
	}
	if dest.Network != chain.SolanaMainnet {
		return nil, fmt.Errorf("sweep: destination %s is registered for %s", dest.Label, dest.Network)
	}
	if err := validateDestination(dest); err != nil {
		return nil, err
	}
	s := p.Solana
	treasury, err := checkSolanaTreasury(s, kr)
	if err != nil {
		return nil, err
	}
	if dest.Address == treasury.Address {
		return nil, errors.New("sweep: destination is the treasury itself")
	}
	if s.Send.DestHint != "" && s.Send.DestHint != dest.Address {
		return nil, fmt.Errorf("sweep: plan dest_hint %s differs from registered destination %s (%s)", clip(s.Send.DestHint), dest.Address, dest.Label)
	}
	cuPrice, err := parseCUPrice(s.CUPriceMicros)
	if err != nil {
		return nil, err
	}
	sp := s.Send
	if !isNonceRegistered(sp.NonceAccount) {
		return nil, fmt.Errorf("sweep: nonce account %s is not registered offline (nonce-register)", clip(sp.NonceAccount))
	}
	if err := parseNonceValue(sp.NonceValue); err != nil {
		return nil, err
	}
	asset, err := assetBySymbol(chain.SolanaMainnet, sp.Symbol, sp.Mint)
	if err != nil {
		return nil, err
	}
	if _, err := ParseCanonicalUint(sp.TreasuryBalance); err != nil {
		return nil, fmt.Errorf("send treasury_balance: %w", err)
	}
	if amount == nil || amount.Sign() <= 0 || !amount.IsUint64() {
		return nil, errors.New("sweep: send amount must be > 0 and fit u64")
	}
	srcATA, err := chain.SolanaATA(treasury.Address, asset.Contract, asset.TokenProgram)
	if err != nil {
		return nil, err
	}
	dstATA, err := chain.SolanaATA(dest.Address, asset.Contract, asset.TokenProgram)
	if err != nil {
		return nil, err
	}
	in := SolanaTxIntent{
		Role: RoleSend, Treasury: treasury.Address, NonceAccount: sp.NonceAccount, NonceValue: sp.NonceValue, CUPriceMicros: cuPrice,
		Transfers: []SolanaTransferIntent{{
			Owner: treasury.Address, SourceATA: srcATA, Asset: asset, DestOwner: dest.Address, DestATA: dstATA, Amount: amount.Uint64(),
		}},
	}
	op := &SolanaOp{
		Kind: KindSend, Treasury: treasury.Address, Txs: []SolanaTxIntent{in},
		Budget: Budget{SolanaFeeLamports: in.FeeLamports().String(), SolanaRentLamports: "0"},
		Totals: map[string]*big.Int{asset.Symbol: new(big.Int).Set(amount)},
	}
	if err := op.Budget.CheckSolana(); err != nil {
		return nil, err
	}
	return op, nil
}

func pk(s string) (solana.PublicKey, error) {
	b, err := chain.DecodeSolanaPubkey(s)
	if err != nil {
		return solana.PublicKey{}, err
	}
	return solana.PublicKeyFromBytes(b), nil
}

func mustPK(s string) solana.PublicKey {
	p, err := pk(s)
	if err != nil {
		panic(err)
	}
	return p
}

// buildSolanaTx compiles the intent into an unsigned legacy transaction with
// solana-go. Instruction order: AdvanceNonceAccount (must be first for a
// durable-nonce transaction), compute budget, ATA creations, transfers.
func buildSolanaTx(in *SolanaTxIntent) (*solana.Transaction, error) {
	treasury, err := pk(in.Treasury)
	if err != nil {
		return nil, err
	}
	nonceAcct, err := pk(in.NonceAccount)
	if err != nil {
		return nil, err
	}
	nonceVal, err := pk(in.NonceValue)
	if err != nil {
		return nil, err
	}
	ixs := []solana.Instruction{
		system.NewAdvanceNonceAccountInstruction(nonceAcct, solana.SysVarRecentBlockHashesPubkey, treasury).Build(),
		computebudget.NewSetComputeUnitLimitInstruction(SolanaComputeUnitLimit).Build(),
		computebudget.NewSetComputeUnitPriceInstruction(in.CUPriceMicros).Build(),
	}
	for _, a := range in.CreateATA {
		ixs = append(ixs, ata.NewCreateIdempotentInstructionWithTokenProgram(treasury, treasury, mustPK(a.Contract), mustPK(a.TokenProgram)).Build())
	}
	for _, t := range in.Transfers {
		ix, err := token.NewTransferCheckedInstruction(t.Amount, solanaTokenDecimals,
			mustPK(t.SourceATA), mustPK(t.Asset.Contract), mustPK(t.DestATA), mustPK(t.Owner), nil).ValidateAndBuild()
		if err != nil {
			return nil, err
		}
		if t.Asset.TokenProgram != chain.SolanaTokenProgram {
			return nil, errors.New("sweep: only the classic SPL token program is supported")
		}
		ixs = append(ixs, ix)
	}
	return solana.NewTransaction(ixs, solana.Hash(nonceVal), solana.TransactionPayer(treasury))
}

// SolanaTxSize returns the serialized size of the intent's transaction with
// all signatures present (used by prepare to pack sources).
func SolanaTxSize(in *SolanaTxIntent) (int, error) {
	tx, err := buildSolanaTx(in)
	if err != nil {
		return 0, err
	}
	n := len(tx.Message.Signers())
	tx.Signatures = make([]solana.Signature, n)
	raw, err := tx.MarshalBinary()
	if err != nil {
		return 0, err
	}
	return len(raw), nil
}

// SignSolana signs every transaction of op with keys derived from kr, then
// re-parses each with the independent parser and verifies it equals the
// intent (VerifySolanaSigned).
func SignSolana(op *SolanaOp, kr *Keyring) ([]SignedTx, error) {
	treasury, err := kr.SolanaTreasury()
	if err != nil {
		return nil, err
	}
	if treasury.Address != op.Treasury {
		return nil, errors.New("sweep: internal: treasury mismatch")
	}
	out := make([]SignedTx, 0, len(op.Txs))
	for i := range op.Txs {
		in := &op.Txs[i]
		keys := map[solana.PublicKey]solana.PrivateKey{mustPK(treasury.Address): solana.PrivateKey(treasury.Private)}
		for _, t := range in.Transfers {
			if t.Owner == treasury.Address {
				if in.Role != RoleSend {
					return nil, errors.New("sweep: internal: treasury-owned transfer in a sweep")
				}
				continue
			}
			if in.Role != RoleSweep || t.DestOwner != treasury.Address {
				return nil, errors.New("sweep: internal: sweep must pay the treasury")
			}
			k, err := kr.SolanaPool(t.SourceIndex)
			if err != nil {
				return nil, err
			}
			if k.Address != t.Owner {
				return nil, errors.New("sweep: internal: pool key mismatch")
			}
			keys[mustPK(k.Address)] = solana.PrivateKey(k.Private)
		}
		tx, err := buildSolanaTx(in)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
			if k, ok := keys[key]; ok {
				return &k
			}
			return nil
		}); err != nil {
			return nil, err
		}
		raw, err := tx.MarshalBinary()
		if err != nil {
			return nil, err
		}
		if len(raw) > SolanaMaxPacketBytes {
			return nil, fmt.Errorf("sweep: tx %d is %d bytes > %d; prepare must split sources into more transactions", i, len(raw), SolanaMaxPacketBytes)
		}
		parsed, err := VerifySolanaSigned(raw, in)
		if err != nil {
			return nil, fmt.Errorf("sweep: post-sign verification failed: %w", err)
		}
		out = append(out, SignedTx{Role: in.Role, Raw: base64.StdEncoding.EncodeToString(raw), Hash: parsed.Signature, Signer: in.Treasury})
	}
	return out, nil
}
