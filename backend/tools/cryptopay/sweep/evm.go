package sweep

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

// ArbitrumOneChainID is the only accepted EVM chain id.
var ArbitrumOneChainID = big.NewInt(42161)

// erc20TransferSelector = keccak256("transfer(address,uint256)")[:4].
var erc20TransferSelector = []byte{0xa9, 0x05, 0x9c, 0xbb}

// Tx roles.
const (
	RoleTopUp  = "topup" // EVM treasury -> source, ETH for gas
	RoleSweep  = "sweep" // source -> treasury (EVM token transfer / Solana tx)
	RoleSend   = "send"  // treasury -> registered destination
	RoleNonce  = "nonce" // reserved
	maxEVMData = 68
)

// EVMIntent is one EVM transaction as decided offline.
type EVMIntent struct {
	Role        string
	SourceIndex uint32 // sweep/topup: the deposit index involved
	From        string // lowercase 0x signer
	Nonce       uint64
	To          string   // tx.to: the source (topup) or the token contract
	Value       *big.Int // wei (topup), else 0
	Asset       *chain.Asset
	Recipient   string   // token recipient (treasury for sweeps, destination for send)
	Amount      *big.Int // token units
	GasLimit    uint64
	MaxFee      *big.Int
	MaxPriority *big.Int
}

// MaxFeeWei = GasLimit * MaxFee.
func (in *EVMIntent) MaxFeeWei() *big.Int {
	return new(big.Int).Mul(new(big.Int).SetUint64(in.GasLimit), in.MaxFee)
}

// Data returns the calldata (empty for top-ups).
func (in *EVMIntent) Data() []byte {
	if in.Asset == nil {
		return nil
	}
	return erc20TransferData(in.Recipient, in.Amount)
}

func erc20TransferData(recipient string, amount *big.Int) []byte {
	d := make([]byte, 0, maxEVMData)
	d = append(d, erc20TransferSelector...)
	d = append(d, common.LeftPadBytes(common.HexToAddress(recipient).Bytes(), 32)...)
	d = append(d, common.LeftPadBytes(amount.Bytes(), 32)...)
	return d
}

// EVMOp is a fully validated EVM operation ready to sign.
type EVMOp struct {
	Kind     Kind
	Treasury string
	Intents  []EVMIntent // broadcast order
	Budget   Budget
	// Summary: per-symbol totals.
	Totals map[string]*big.Int
}

func parseFees(e *EVMPlan, caps EVMCaps) (maxFee, prio *big.Int, err error) {
	if maxFee, err = ParsePositiveUint(e.MaxFeePerGas); err != nil {
		return nil, nil, fmt.Errorf("max_fee_per_gas: %w", err)
	}
	if prio, err = ParseCanonicalUint(e.MaxPriorityFeePerGas); err != nil {
		return nil, nil, fmt.Errorf("max_priority_fee_per_gas: %w", err)
	}
	if prio.Cmp(maxFee) > 0 {
		return nil, nil, errors.New("sweep: max_priority_fee_per_gas exceeds max_fee_per_gas")
	}
	if maxFee.Cmp(caps.MaxFeePerGas) > 0 {
		return nil, nil, fmt.Errorf("sweep: max_fee_per_gas %s wei exceeds the cap %s wei (use --allow-high-fee for up to %s wei)", maxFee, caps.MaxFeePerGas, EVMAbsoluteMaxFeePerGas)
	}
	return maxFee, prio, nil
}

func parseGasLimit(s string) (uint64, error) {
	g, err := ParseUint64(s)
	if err != nil {
		return 0, err
	}
	if g < EVMMinGasLimit || g > EVMMaxGasLimit {
		return 0, fmt.Errorf("sweep: gas limit %d outside [%d, %d]", g, EVMMinGasLimit, EVMMaxGasLimit)
	}
	return g, nil
}

func checkChainID(e *EVMPlan) error {
	id, err := ParseCanonicalUint(e.ChainID)
	if err != nil || id.Cmp(ArbitrumOneChainID) != 0 {
		return fmt.Errorf("sweep: chain_id must be %s", ArbitrumOneChainID)
	}
	return nil
}

func addNonce(n uint64, k int) (uint64, error) {
	if n > ^uint64(0)-uint64(k) {
		return 0, errors.New("sweep: nonce overflow")
	}
	return n + uint64(k), nil
}

// BuildEVMSweep validates an EVM sweep plan against keys derived offline and
// returns the operation. Every source must re-derive from its index; the
// token recipient is always the derived treasury.
func BuildEVMSweep(p *Plan, kr *Keyring, caps EVMCaps) (*EVMOp, error) {
	if p.Kind != KindSweep || p.Network != chain.ArbitrumOne || p.EVM == nil {
		return nil, errors.New("sweep: not an arbitrum-one sweep plan")
	}
	e := p.EVM
	if err := checkChainID(e); err != nil {
		return nil, err
	}
	treasury, err := kr.EVMTreasury()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(e.Treasury, treasury.Address) {
		return nil, fmt.Errorf("sweep: plan treasury %s is not this mnemonic's treasury %s", clip(e.Treasury), treasury.Address)
	}
	maxFee, prio, err := parseFees(e, caps)
	if err != nil {
		return nil, err
	}
	topUpGas, err := parseGasLimit(e.TopUpGasLimit)
	if err != nil {
		return nil, fmt.Errorf("topup_gas_limit: %w", err)
	}
	if len(e.Sources) == 0 || len(e.Sources) > EVMMaxSources {
		return nil, fmt.Errorf("sweep: %d sources; allowed 1..%d", len(e.Sources), EVMMaxSources)
	}
	op := &EVMOp{Kind: KindSweep, Treasury: treasury.Address, Totals: map[string]*big.Int{}}
	seenIdx := map[uint32]bool{}
	seenAddr := map[string]bool{}
	var topUps, sweeps []EVMIntent
	treasuryNonce := e.TreasuryNonce
	treasurySpend, burn := new(big.Int), new(big.Int)
	for si, s := range e.Sources {
		if seenIdx[s.Index] {
			return nil, fmt.Errorf("sweep: duplicate source index %d", s.Index)
		}
		seenIdx[s.Index] = true
		key, err := kr.EVMDeposit(s.Index)
		if err != nil {
			return nil, err
		}
		addr, err := chain.NormalizeEVMAddress(s.Address)
		if err != nil || addr != key.Address {
			return nil, fmt.Errorf("sweep: source %d address %s does not derive from index %d of this mnemonic", si, clip(s.Address), s.Index)
		}
		if seenAddr[addr] {
			return nil, fmt.Errorf("sweep: duplicate source address %s", addr)
		}
		seenAddr[addr] = true
		if addr == treasury.Address {
			return nil, errors.New("sweep: source equals treasury")
		}
		declaredETH, err := ParseCanonicalUint(s.ETHBalance)
		if err != nil {
			return nil, fmt.Errorf("source %d eth_balance: %w", s.Index, err)
		}
		if len(s.Tokens) == 0 {
			return nil, fmt.Errorf("sweep: source %d has no tokens (a top-up needs at least one sweep)", s.Index)
		}
		seenSym := map[string]bool{}
		need := new(big.Int)
		for k, t := range s.Tokens {
			asset, err := assetBySymbol(chain.ArbitrumOne, t.Symbol, t.Contract)
			if err != nil {
				return nil, err
			}
			if seenSym[asset.Symbol] {
				return nil, fmt.Errorf("sweep: source %d lists %s twice", s.Index, asset.Symbol)
			}
			seenSym[asset.Symbol] = true
			amt, err := ParsePositiveUint(t.Amount)
			if err != nil {
				return nil, fmt.Errorf("source %d %s amount: %w", s.Index, t.Symbol, err)
			}
			gas, err := parseGasLimit(t.GasLimit)
			if err != nil {
				return nil, fmt.Errorf("source %d %s gas_limit: %w", s.Index, t.Symbol, err)
			}
			nonce, err := addNonce(s.Nonce, k)
			if err != nil {
				return nil, err
			}
			a := asset
			in := EVMIntent{
				Role: RoleSweep, SourceIndex: s.Index, From: addr, Nonce: nonce, To: asset.Contract,
				Value: new(big.Int), Asset: &a, Recipient: treasury.Address, Amount: amt,
				GasLimit: gas, MaxFee: maxFee, MaxPriority: prio,
			}
			fee := in.MaxFeeWei()
			need.Add(need, fee)
			burn.Add(burn, fee)
			if op.Totals[asset.Symbol] == nil {
				op.Totals[asset.Symbol] = new(big.Int)
			}
			op.Totals[asset.Symbol].Add(op.Totals[asset.Symbol], amt)
			sweeps = append(sweeps, in)
		}
		// A.value = max(0, Σ B budgets − declared source ETH)
		if need.Cmp(declaredETH) > 0 {
			value := new(big.Int).Sub(need, declaredETH)
			in := EVMIntent{
				Role: RoleTopUp, SourceIndex: s.Index, From: treasury.Address, Nonce: treasuryNonce, To: addr,
				Value: value, GasLimit: topUpGas, MaxFee: maxFee, MaxPriority: prio,
			}
			if treasuryNonce, err = addNonce(treasuryNonce, 1); err != nil {
				return nil, err
			}
			fee := in.MaxFeeWei()
			treasurySpend.Add(treasurySpend, value)
			treasurySpend.Add(treasurySpend, fee)
			burn.Add(burn, fee)
			topUps = append(topUps, in)
		}
	}
	// Broadcast order: per source, its top-up (if any) before its sweeps.
	for _, s := range e.Sources {
		for _, in := range topUps {
			if in.SourceIndex == s.Index {
				op.Intents = append(op.Intents, in)
			}
		}
		for _, in := range sweeps {
			if in.SourceIndex == s.Index {
				op.Intents = append(op.Intents, in)
			}
		}
	}
	op.Budget = Budget{EVMTreasurySpendWei: treasurySpend.String(), EVMBurnWei: burn.String()}
	if err := op.Budget.CheckEVM(caps); err != nil {
		return nil, err
	}
	return op, nil
}

// BuildEVMSend validates an EVM send plan. dest is the offline-registered
// destination (lowercase 0x) and amount the offline-entered token amount.
func BuildEVMSend(p *Plan, kr *Keyring, caps EVMCaps, dest Destination, amount *big.Int) (*EVMOp, error) {
	if p.Kind != KindSend || p.Network != chain.ArbitrumOne || p.EVM == nil || p.EVM.Send == nil {
		return nil, errors.New("sweep: not an arbitrum-one send plan")
	}
	if dest.Network != chain.ArbitrumOne {
		return nil, fmt.Errorf("sweep: destination %s is registered for %s", dest.Label, dest.Network)
	}
	destAddr, err := chain.NormalizeEVMAddress(dest.Address)
	if err != nil {
		return nil, err
	}
	e := p.EVM
	if err := checkChainID(e); err != nil {
		return nil, err
	}
	treasury, err := kr.EVMTreasury()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(e.Treasury, treasury.Address) {
		return nil, fmt.Errorf("sweep: plan treasury %s is not this mnemonic's treasury %s", clip(e.Treasury), treasury.Address)
	}
	if destAddr == treasury.Address {
		return nil, errors.New("sweep: destination is the treasury itself")
	}
	if e.Send.DestHint != "" {
		h, err := chain.NormalizeEVMAddress(e.Send.DestHint)
		if err != nil || h != destAddr {
			return nil, fmt.Errorf("sweep: plan dest_hint %s differs from registered destination %s (%s)", clip(e.Send.DestHint), destAddr, dest.Label)
		}
	}
	maxFee, prio, err := parseFees(e, caps)
	if err != nil {
		return nil, err
	}
	asset, err := assetBySymbol(chain.ArbitrumOne, e.Send.Symbol, e.Send.Contract)
	if err != nil {
		return nil, err
	}
	gas, err := parseGasLimit(e.Send.GasLimit)
	if err != nil {
		return nil, fmt.Errorf("send gas_limit: %w", err)
	}
	if _, err := ParseCanonicalUint(e.Send.TreasuryBalance); err != nil {
		return nil, fmt.Errorf("send treasury_balance: %w", err)
	}
	if amount == nil || amount.Sign() <= 0 || amount.BitLen() > 256 {
		return nil, errors.New("sweep: send amount must be > 0")
	}
	in := EVMIntent{
		Role: RoleSend, From: treasury.Address, Nonce: e.TreasuryNonce, To: asset.Contract, Value: new(big.Int),
		Asset: &asset, Recipient: destAddr, Amount: new(big.Int).Set(amount),
		GasLimit: gas, MaxFee: maxFee, MaxPriority: prio,
	}
	fee := in.MaxFeeWei()
	op := &EVMOp{
		Kind: KindSend, Treasury: treasury.Address, Intents: []EVMIntent{in},
		Budget: Budget{EVMTreasurySpendWei: fee.String(), EVMBurnWei: fee.String()},
		Totals: map[string]*big.Int{asset.Symbol: new(big.Int).Set(amount)},
	}
	if err := op.Budget.CheckEVM(caps); err != nil {
		return nil, err
	}
	return op, nil
}

// SignedTx is one signed transaction in a bundle.
type SignedTx struct {
	Role        string `json:"role"`
	Raw         string `json:"raw"`  // EVM: 0x hex; Solana: base64 wire transaction
	Hash        string `json:"hash"` // EVM tx hash; Solana first signature
	Signer      string `json:"signer"`
	Nonce       uint64 `json:"nonce,omitempty"`        // EVM
	SourceIndex uint32 `json:"source_index,omitempty"` // informational
}

// SignEVM signs every intent with keys derived from kr, then decodes each
// signed transaction and verifies it equals the intent (VerifyEVMSigned).
func SignEVM(op *EVMOp, kr *Keyring) ([]SignedTx, error) {
	treasury, err := kr.EVMTreasury()
	if err != nil {
		return nil, err
	}
	out := make([]SignedTx, 0, len(op.Intents))
	for i := range op.Intents {
		in := &op.Intents[i]
		var priv []byte
		switch in.From {
		case treasury.Address:
			if in.Role != RoleTopUp && in.Role != RoleSend {
				return nil, errors.New("sweep: internal: treasury signs only top-ups and sends")
			}
			priv = treasury.Private
		default:
			if in.Role != RoleSweep {
				return nil, errors.New("sweep: internal: sources sign only sweeps")
			}
			k, err := kr.EVMDeposit(in.SourceIndex)
			if err != nil {
				return nil, err
			}
			if k.Address != in.From {
				return nil, errors.New("sweep: internal: source key mismatch")
			}
			if in.Recipient != treasury.Address {
				return nil, errors.New("sweep: internal: sweep recipient is not the treasury")
			}
			priv = k.Private
		}
		raw, hash, err := signEVMIntent(in, priv)
		if err != nil {
			return nil, err
		}
		if err := VerifyEVMSigned(raw, in); err != nil {
			return nil, fmt.Errorf("sweep: post-sign verification failed: %w", err)
		}
		out = append(out, SignedTx{Role: in.Role, Raw: "0x" + hex.EncodeToString(raw), Hash: hash, Signer: in.From, Nonce: in.Nonce, SourceIndex: in.SourceIndex})
	}
	return out, nil
}

func signEVMIntent(in *EVMIntent, priv []byte) ([]byte, string, error) {
	to := common.HexToAddress(in.To)
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   new(big.Int).Set(ArbitrumOneChainID),
		Nonce:     in.Nonce,
		GasTipCap: new(big.Int).Set(in.MaxPriority),
		GasFeeCap: new(big.Int).Set(in.MaxFee),
		Gas:       in.GasLimit,
		To:        &to,
		Value:     new(big.Int).Set(in.Value),
		Data:      in.Data(),
	})
	key, err := crypto.ToECDSA(priv)
	if err != nil {
		return nil, "", err
	}
	signed, err := types.SignTx(tx, types.NewLondonSigner(ArbitrumOneChainID), key)
	if err != nil {
		return nil, "", err
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		return nil, "", err
	}
	return raw, signed.Hash().Hex(), nil
}

// DecodedEVMTx is the semantic content of a signed EVM transaction.
type DecodedEVMTx struct {
	Hash        string
	From        string
	Nonce       uint64
	To          string
	Value       *big.Int
	Data        []byte
	GasLimit    uint64
	MaxFee      *big.Int
	MaxPriority *big.Int
	// For ERC-20 transfer calldata:
	IsTransfer bool
	Recipient  string
	Amount     *big.Int
}

// DecodeEVMSigned parses a signed EIP-1559 Arbitrum One transaction and
// recovers its sender. Anything else (legacy, blobs, access lists, other
// chain ids, unexpected calldata) is rejected.
func DecodeEVMSigned(raw []byte) (*DecodedEVMTx, error) {
	var tx types.Transaction
	if err := tx.UnmarshalBinary(raw); err != nil {
		return nil, fmt.Errorf("sweep: decode EVM tx: %w", err)
	}
	if tx.Type() != types.DynamicFeeTxType {
		return nil, fmt.Errorf("sweep: EVM tx type %d, want 2", tx.Type())
	}
	if tx.ChainId() == nil || tx.ChainId().Cmp(ArbitrumOneChainID) != 0 {
		return nil, errors.New("sweep: EVM tx chain id is not 42161")
	}
	if len(tx.AccessList()) != 0 || tx.To() == nil {
		return nil, errors.New("sweep: EVM tx has an access list or no recipient")
	}
	from, err := types.Sender(types.NewLondonSigner(ArbitrumOneChainID), &tx)
	if err != nil {
		return nil, fmt.Errorf("sweep: recover EVM sender: %w", err)
	}
	d := &DecodedEVMTx{
		Hash: tx.Hash().Hex(), From: strings.ToLower(from.Hex()), Nonce: tx.Nonce(),
		To: strings.ToLower(tx.To().Hex()), Value: tx.Value(), Data: tx.Data(), GasLimit: tx.Gas(),
		MaxFee: tx.GasFeeCap(), MaxPriority: tx.GasTipCap(),
	}
	switch {
	case len(d.Data) == 0:
	case len(d.Data) == maxEVMData && bytes.Equal(d.Data[:4], erc20TransferSelector) && bytes.Equal(d.Data[4:16], make([]byte, 12)):
		d.IsTransfer = true
		d.Recipient = strings.ToLower(common.BytesToAddress(d.Data[16:36]).Hex())
		d.Amount = new(big.Int).SetBytes(d.Data[36:68])
	default:
		return nil, errors.New("sweep: EVM tx calldata is neither empty nor an ERC-20 transfer")
	}
	return d, nil
}

// VerifyEVMSigned decodes raw and checks every field equals the intent.
func VerifyEVMSigned(raw []byte, in *EVMIntent) error {
	d, err := DecodeEVMSigned(raw)
	if err != nil {
		return err
	}
	switch {
	case d.From != in.From:
		return fmt.Errorf("signer %s, want %s", d.From, in.From)
	case d.Nonce != in.Nonce:
		return fmt.Errorf("nonce %d, want %d", d.Nonce, in.Nonce)
	case d.To != strings.ToLower(in.To):
		return fmt.Errorf("to %s, want %s", d.To, in.To)
	case d.Value.Cmp(in.Value) != 0:
		return errors.New("value mismatch")
	case d.GasLimit != in.GasLimit || d.MaxFee.Cmp(in.MaxFee) != 0 || d.MaxPriority.Cmp(in.MaxPriority) != 0:
		return errors.New("fee fields mismatch")
	}
	if in.Asset == nil {
		if len(d.Data) != 0 {
			return errors.New("top-up carries calldata")
		}
		return nil
	}
	if !d.IsTransfer || d.Recipient != in.Recipient || d.Amount.Cmp(in.Amount) != 0 || d.To != in.Asset.Contract || d.Value.Sign() != 0 {
		return errors.New("token transfer mismatch")
	}
	return nil
}
