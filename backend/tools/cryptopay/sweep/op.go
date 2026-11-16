package sweep

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

// Operation is a validated sweep or send on one network.
type Operation struct {
	Network chain.Network
	Kind    Kind
	EVM     *EVMOp
	Solana  *SolanaOp
	// Dest is the registered destination of a send.
	Dest *Destination
}

// Budget returns the operation's own worst-case budget.
func (o *Operation) Budget() Budget {
	if o.EVM != nil {
		return o.EVM.Budget
	}
	return o.Solana.Budget
}

// Treasury returns the derived treasury address.
func (o *Operation) Treasury() string {
	if o.EVM != nil {
		return o.EVM.Treasury
	}
	return o.Solana.Treasury
}

// ConfirmTarget is the address whose last 6 characters the operator types:
// the treasury for sweeps, the registered destination for sends.
func (o *Operation) ConfirmTarget() string {
	if o.Kind == KindSend && o.Dest != nil {
		return DisplayAddress(o.Network, o.Dest.Address)
	}
	return DisplayAddress(o.Network, o.Treasury())
}

// DisplayAddress renders EVM addresses in EIP-55 checksum form.
func DisplayAddress(n chain.Network, addr string) string {
	if n.Family() == chain.FamilyEVM {
		return common.HexToAddress(addr).Hex()
	}
	return addr
}

// CheckCumulative enforces the caps on the cumulative budget of the chain.
func (o *Operation) CheckCumulative(cum Budget, caps EVMCaps) error {
	if o.Network.Family() == chain.FamilyEVM {
		return cum.CheckEVM(caps)
	}
	return cum.CheckSolana()
}

// Sign signs the operation and returns the bundle txs and ledger entries.
func (o *Operation) Sign(kr *Keyring) ([]SignedTx, []LedgerTx, error) {
	if o.EVM != nil {
		st, err := SignEVM(o.EVM, kr)
		if err != nil {
			return nil, nil, err
		}
		lt := make([]LedgerTx, len(st))
		for i, t := range st {
			lt[i] = LedgerTx{Role: t.Role, Hash: t.Hash, Signer: t.Signer, Nonce: strconv.FormatUint(t.Nonce, 10), Raw: t.Raw, SourceIndex: t.SourceIndex}
		}
		return st, lt, nil
	}
	st, err := SignSolana(o.Solana, kr)
	if err != nil {
		return nil, nil, err
	}
	lt := make([]LedgerTx, len(st))
	for i, t := range st {
		in := o.Solana.Txs[i]
		lt[i] = LedgerTx{Role: t.Role, Hash: t.Hash, Signer: t.Signer, NonceAccount: in.NonceAccount, NonceValue: in.NonceValue, Raw: t.Raw}
	}
	return st, lt, nil
}

// LedgerTxsPreview returns the exclusion data of the op before signing (so
// the ledger rules can be checked before the operator confirms).
func (o *Operation) LedgerTxsPreview() []LedgerTx {
	var out []LedgerTx
	if o.EVM != nil {
		for _, in := range o.EVM.Intents {
			out = append(out, LedgerTx{Role: in.Role, Hash: "?", Signer: in.From, Nonce: strconv.FormatUint(in.Nonce, 10)})
		}
		return out
	}
	for _, in := range o.Solana.Txs {
		out = append(out, LedgerTx{Role: in.Role, Hash: "?", Signer: in.Treasury, NonceAccount: in.NonceAccount, NonceValue: in.NonceValue})
	}
	return out
}

func sortedSymbols(m map[string]*big.Int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Describe renders the confirmation summary shown before signing.
func (o *Operation) Describe(admit *AdmitResult) string {
	var b strings.Builder
	kindZh := map[Kind]string{KindSweep: "归集 sweep（充值地址 → 金库）", KindSend: "外发 send（金库 → 已登记外部地址）"}[o.Kind]
	fmt.Fprintf(&b, "网络: %s\n操作: %s\n", o.Network, kindZh)
	if o.Kind == KindSend && o.Dest != nil {
		fmt.Fprintf(&b, "收款方（离线登记的标签 %q）:\n    %s\n", o.Dest.Label, o.ConfirmTarget())
		fmt.Fprintf(&b, "付款方（金库，离线派生）: %s\n", DisplayAddress(o.Network, o.Treasury()))
	} else {
		fmt.Fprintf(&b, "收款方（金库，离线从助记词派生，plan 无法指定）:\n    %s\n", o.ConfirmTarget())
	}
	var totals map[string]*big.Int
	if o.EVM != nil {
		totals = o.EVM.Totals
	} else {
		totals = o.Solana.Totals
	}
	for _, s := range sortedSymbols(totals) {
		fmt.Fprintf(&b, "合计 %s: %s\n", s, FormatUnits(totals[s], 6))
	}
	if o.EVM != nil {
		srcs := map[uint32]bool{}
		var nTop, nSweep int
		for _, in := range o.EVM.Intents {
			switch in.Role {
			case RoleTopUp:
				nTop++
			case RoleSweep:
				nSweep++
				srcs[in.SourceIndex] = true
			}
		}
		fmt.Fprintf(&b, "源地址数: %d   交易数: %d（补 gas %d 笔，代币转账 %d 笔）\n", len(srcs), len(o.EVM.Intents), nTop, nSweep+boolInt(o.Kind == KindSend))
		if len(o.EVM.Intents) > 0 {
			fmt.Fprintf(&b, "maxFeePerGas: %s gwei   priority: %s gwei\n", FormatUnits(o.EVM.Intents[0].MaxFee, 9), FormatUnits(o.EVM.Intents[0].MaxPriority, 9))
		}
		fmt.Fprintf(&b, "金库本操作最大支出（补 gas 金额 + 手续费上限）: %s ETH\n", FormatETH(o.EVM.Budget.EVMTreasurySpendWei))
		fmt.Fprintf(&b, "全部交易最大燃烧手续费: %s ETH\n", FormatETH(o.EVM.Budget.EVMBurnWei))
		for i, in := range o.EVM.Intents {
			switch in.Role {
			case RoleTopUp:
				fmt.Fprintf(&b, "  #%d 补gas  金库 nonce %d → 源#%d %s  %s ETH\n", i+1, in.Nonce, in.SourceIndex, DisplayAddress(o.Network, in.To), FormatUnits(in.Value, 18))
			case RoleSweep:
				fmt.Fprintf(&b, "  #%d 归集  源#%d %s nonce %d  %s %s → 金库\n", i+1, in.SourceIndex, DisplayAddress(o.Network, in.From), in.Nonce, FormatUnits(in.Amount, 6), in.Asset.Symbol)
			case RoleSend:
				fmt.Fprintf(&b, "  #%d 外发  金库 nonce %d  %s %s → %s\n", i+1, in.Nonce, FormatUnits(in.Amount, 6), in.Asset.Symbol, DisplayAddress(o.Network, in.Recipient))
			}
		}
	} else {
		srcs := map[uint32]bool{}
		for _, in := range o.Solana.Txs {
			for _, t := range in.Transfers {
				if in.Role == RoleSweep {
					srcs[t.SourceIndex] = true
				}
			}
		}
		fmt.Fprintf(&b, "源地址数: %d   交易数: %d\n", len(srcs), len(o.Solana.Txs))
		fmt.Fprintf(&b, "手续费上限（签名费 + 优先费）: %s SOL\n", FormatSOL(o.Solana.Budget.SolanaFeeLamports))
		fmt.Fprintf(&b, "金库 ATA 创建租金（资金占用，不是燃烧）: %s SOL\n", FormatSOL(o.Solana.Budget.SolanaRentLamports))
		fmt.Fprintf(&b, "（Solana 链上没有总费用字段强制上限；以上是按当前网络费率规则计算的上限）\n")
		for i, in := range o.Solana.Txs {
			fmt.Fprintf(&b, "  #%d nonce 账户 %s  优先费 %d micro-lamports/CU  创建 ATA %d 个\n", i+1, in.NonceAccount, in.CUPriceMicros, len(in.CreateATA))
			for _, t := range in.Transfers {
				if in.Role == RoleSweep {
					fmt.Fprintf(&b, "      源#%d %s  %s %s → 金库\n", t.SourceIndex, t.Owner, FormatUnits(new(big.Int).SetUint64(t.Amount), 6), t.Asset.Symbol)
				} else {
					fmt.Fprintf(&b, "      金库 → %s  %s %s（收款 token 账户 %s，离线计算）\n", t.DestOwner, FormatUnits(new(big.Int).SetUint64(t.Amount), 6), t.Asset.Symbol, t.DestATA)
				}
			}
		}
	}
	if admit != nil && admit.Replaces != nil {
		// Only EVM ops can be replaced (CheckAdmit refuses Solana).
		fmt.Fprintf(&b, "\n!! 这是对待处理操作 %s 的替换：它复用旧操作的全部 nonce，每个 nonce 链上只会被消费一次，\n!! 但新旧签名可能各成功一部分（归集都进金库）。费用按新旧全部签名累计计算：\n", admit.Replaces.ID)
		fmt.Fprintf(&b, "!! 本操作链累计最大授权（含旧签名）: 金库最大支出 %s ETH，全部交易最大燃烧 %s ETH\n", FormatETH(admit.Cumulative.EVMTreasurySpendWei), FormatETH(admit.Cumulative.EVMBurnWei))
	}
	if o.Network.Family() == chain.FamilySolana {
		fmt.Fprintf(&b, "\n注意: Solana 签名之后不能再被替换。签名文件丢了用 ledger export 重新导出；要签新的 Solana 操作，\n必须先在手机浏览器确认每笔已 finalized，再 ledger close（仅归集可选：签名 30 分钟后且 nonce 账户的值已变化，见 ledger close）。\n")
	}
	return b.String()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// BundleVersion is the signed.json version.
const BundleVersion = 1

// SignedBundle is signed.json: signed raw transactions in broadcast order.
// Online tools must decode Raw themselves and never trust the other fields.
type SignedBundle struct {
	Version  int           `json:"version"`
	OpID     string        `json:"op_id"`
	Network  chain.Network `json:"network"`
	Kind     Kind          `json:"kind"`
	Treasury string        `json:"treasury"`
	Txs      []SignedTx    `json:"txs"`
}

// DecodeBundle strictly decodes signed.json.
func DecodeBundle(data []byte) (*SignedBundle, error) {
	var b SignedBundle
	if err := DecodeStrict(data, &b); err != nil {
		return nil, err
	}
	if b.Version != BundleVersion || len(b.Txs) == 0 || (b.Network != chain.ArbitrumOne && b.Network != chain.SolanaMainnet) {
		return nil, errors.New("sweep: invalid signed bundle")
	}
	return &b, nil
}

// ExportBundle rebuilds signed.json for a recorded op from the raw
// transactions kept in the ledger. Nothing is signed.
func ExportBundle(l *Ledger, id string) ([]byte, error) {
	o := l.Find(id)
	if o == nil {
		return nil, fmt.Errorf("sweep: no op %s", clip(id))
	}
	b := SignedBundle{Version: BundleVersion, OpID: o.ID, Network: o.Network, Kind: o.Kind}
	for _, t := range o.Txs {
		st := SignedTx{Role: t.Role, Raw: t.Raw, Hash: t.Hash, Signer: t.Signer, SourceIndex: t.SourceIndex}
		if t.Nonce != "" {
			n, err := ParseUint64(t.Nonce)
			if err != nil {
				return nil, err
			}
			st.Nonce = n
		}
		b.Txs = append(b.Txs, st)
	}
	if len(o.Txs) > 0 && o.Kind == KindSweep {
		// treasury = recipient of sweeps: the signer of top-ups (EVM) or fee payer (Solana)
		for _, t := range o.Txs {
			if t.Role == RoleTopUp || t.NonceAccount != "" {
				b.Treasury = t.Signer
				break
			}
		}
	} else if len(o.Txs) > 0 {
		b.Treasury = o.Txs[0].Signer
	}
	data, err := json.MarshalIndent(&b, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// testHookAfterLedgerSave lets tests simulate a crash between the ledger
// write and the output write.
var testHookAfterLedgerSave func() error

// CommitSigned records the signed op in the ledger and only then writes the
// signed bundle. Order (crash safety): (1) refuse if outPath exists,
// (2) admit + atomically save the ledger, (3) atomically create outPath.
// A crash after (2) leaves a pending ledger entry without output (safe: the
// op can be replaced or closed); signed output can never exist without its
// pending ledger entry. The caller must hold the store lock and pass the
// ledger it loaded under that lock.
func CommitSigned(store *LedgerStore, l *Ledger, o *Operation, signed []SignedTx, ltxs []LedgerTx, outPath string, now time.Time) (*LedgerOp, error) {
	if _, err := os.Lstat(outPath); err == nil {
		return nil, fmt.Errorf("sweep: output %s already exists", outPath)
	}
	summary := fmt.Sprintf("%s %s %d tx", o.Kind, o.Network, len(signed))
	if o.Dest != nil {
		summary += " to " + o.Dest.Label
	}
	op, err := l.Admit(o.Network, o.Kind, summary, ltxs, o.Budget(), now)
	if err != nil {
		return nil, err
	}
	opCopy := *op
	if err := store.Save(l); err != nil {
		return nil, fmt.Errorf("sweep: ledger write failed, nothing was output: %w", err)
	}
	if testHookAfterLedgerSave != nil {
		if err := testHookAfterLedgerSave(); err != nil {
			return nil, err
		}
	}
	bundle := SignedBundle{Version: BundleVersion, OpID: opCopy.ID, Network: o.Network, Kind: o.Kind, Treasury: o.Treasury(), Txs: signed}
	data, err := json.MarshalIndent(&bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := AtomicWriteFile(outPath, append(data, '\n'), 0o644, false); err != nil {
		return nil, fmt.Errorf("sweep: ledger recorded op %s but writing %s failed: %w (run `ledger export --op %s` to write it again; nothing new is signed)", opCopy.ID, outPath, err, opCopy.ID)
	}
	return &opCopy, nil
}
