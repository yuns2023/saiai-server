package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/tools/cryptopay/sweep"
)

// stdin is the operator's keyboard; tests replace it.
var stdin io.Reader = os.Stdin

// now is the clock; tests replace it.
var now = time.Now

type lineReader struct{ r *bufio.Reader }

var input *lineReader

func prompt(stdout io.Writer, msg string) (string, error) {
	if input == nil {
		input = &lineReader{r: bufio.NewReader(stdin)}
	}
	if _, err := fmt.Fprint(stdout, msg); err != nil {
		return "", err
	}
	line, err := input.r.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", errors.New("aborted: no input")
	}
	return strings.TrimSpace(line), nil
}

func keyringFromFile(mf string, stderr io.Writer) (*sweep.Keyring, error) {
	seed, err := loadSeed(mf, stderr)
	if err != nil {
		return nil, err
	}
	return sweep.NewKeyring(seed)
}

func cmdTreasuryAddress(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("treasury-address", stderr)
	mf := fs.String("mnemonic-file", "", "mnemonic file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	kr, err := keyringFromFile(*mf, stderr)
	if err != nil {
		return err
	}
	e, err := kr.EVMTreasury()
	if err != nil {
		return err
	}
	s, err := kr.SolanaTreasury()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout,
		"EVM 金库 (Arbitrum One)  %s\n    %s\n    往这里充少量 ETH（Arbitrum One 网络）付 gas；归集的 USDC/USDT 也到这里。\n"+
			"Solana 金库              %s\n    %s\n    往这里充少量 SOL 付手续费；归集的 USDC/USDT 到它的 token 账户。\n",
		sweep.EVMTreasuryPath, common.HexToAddress(e.Address).Hex(), sweep.SolanaTreasuryPath, s.Address)
	return err
}

func cmdLedger(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ledger init|list|close|export --ledger FILE [--op ID] [--out F]")
	}
	sub, rest := args[0], args[1:]
	fs := newFlags("ledger "+sub, stderr)
	path := fs.String("ledger", "", "offline ledger file")
	opID := fs.String("op", "", "op id (close, export)")
	out := fs.String("out", "", "export: signed.json to write (must not exist)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("--ledger is required")
	}
	switch sub {
	case "init":
		if err := sweep.InitLedger(*path); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "created empty ledger %s (mode 0600). Back it up together with the mnemonic backup procedure; never delete or recreate it.\n", *path)
		return err
	case "list":
		st := &sweep.LedgerStore{Path: *path}
		l, err := st.Load()
		if err != nil {
			return err
		}
		printLedger(stdout, l)
		return nil
	case "close":
		return ledgerClose(*path, *opID, stdout)
	case "export":
		if *opID == "" || *out == "" {
			return errors.New("ledger export needs --op and --out")
		}
		st := &sweep.LedgerStore{Path: *path}
		if err := st.Lock(); err != nil {
			return err
		}
		defer st.Unlock()
		l, err := st.Load()
		if err != nil {
			return err
		}
		// Re-persist (fsync file + directory) before releasing any signature:
		// what we read may be a rename whose directory fsync failed.
		if err := st.Save(l); err != nil {
			return fmt.Errorf("ledger could not be made durable; nothing exported: %w", err)
		}
		data, err := sweep.ExportBundle(l, *opID)
		if err != nil {
			return err
		}
		if err := sweep.AtomicWriteFile(*out, data, 0o644, false); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "wrote %s (the transactions signed for op %s; nothing new was signed)\n", *out, *opID)
		return err
	default:
		return fmt.Errorf("unknown ledger subcommand %q", sub)
	}
}

func explorerTx(n chain.Network, h string) string {
	if n == chain.SolanaMainnet {
		return "https://solscan.io/tx/" + h
	}
	return "https://arbiscan.io/tx/" + h
}

func explorerAddr(n chain.Network, a string) string {
	if n == chain.SolanaMainnet {
		return "https://solscan.io/account/" + a
	}
	return "https://arbiscan.io/address/" + a
}

func printOp(w io.Writer, o *sweep.LedgerOp) {
	_, _ = fmt.Fprintf(w, "%s  [%s]  %s  %s  创建 %s", o.ID, o.Status, o.Network, o.Summary, o.CreatedAt)
	if o.Replaces != "" {
		_, _ = fmt.Fprintf(w, "  替换 %s", o.Replaces)
	}
	if o.ClosedAt != "" {
		_, _ = fmt.Fprintf(w, "  关闭 %s", o.ClosedAt)
	}
	_, _ = fmt.Fprintln(w)
	for _, t := range o.Txs {
		if t.NonceAccount != "" {
			_, _ = fmt.Fprintf(w, "    %-6s nonce账户 %s 值 %s\n           %s\n", t.Role, t.NonceAccount, t.NonceValue, explorerTx(o.Network, t.Hash))
		} else {
			_, _ = fmt.Fprintf(w, "    %-6s %s nonce %s\n           %s\n", t.Role, sweep.DisplayAddress(o.Network, t.Signer), t.Nonce, explorerTx(o.Network, t.Hash))
		}
	}
}

func printLedger(w io.Writer, l *sweep.Ledger) {
	_, _ = fmt.Fprintf(w, "已登记 nonce 账户 (%d/%d):\n", len(l.NonceAccounts), sweep.SolanaMaxRegisteredNonces)
	for _, n := range l.NonceAccounts {
		_, _ = fmt.Fprintf(w, "    %s  (%s)\n", n.Address, n.RegisteredAt)
	}
	_, _ = fmt.Fprintf(w, "已登记外部收款地址:\n")
	for _, d := range l.Destinations {
		_, _ = fmt.Fprintf(w, "    %-16s %s  %s\n", d.Label, d.Network, sweep.DisplayAddress(d.Network, d.Address))
	}
	_, _ = fmt.Fprintf(w, "签名记录（新→旧）:\n")
	for _, o := range l.SortedOps() {
		printOp(w, &o)
	}
}

func ledgerClose(path, opID string, stdout io.Writer) error {
	if opID == "" {
		return errors.New("--op is required")
	}
	st := &sweep.LedgerStore{Path: path}
	if err := st.Lock(); err != nil {
		return err
	}
	defer st.Unlock()
	l, err := st.Load()
	if err != nil {
		return err
	}
	o := l.Find(opID)
	if o == nil {
		return fmt.Errorf("no op %q", opID)
	}
	if o.Status != sweep.StatusPending {
		return fmt.Errorf("op %s is %s; only a pending op can be closed", opID, o.Status)
	}
	_, _ = fmt.Fprintln(stdout, "要关闭的操作及其整条替换链（全部签过的交易）:")
	for i := range l.Ops {
		if l.Ops[i].ChainRoot == o.ChainRoot {
			printOp(stdout, &l.Ops[i])
		}
	}
	if o.Network == chain.SolanaMainnet {
		_, _ = fmt.Fprintf(stdout, `
Solana 关闭条件（不要只看 broadcast 的结果文件，用手机或另一台设备打开上面的链接）:
  A. 上面每一笔交易在浏览器里都显示 Finalized（成功或失败都算）。外发（send）只能用 A。
  B. 仅限归集（sweep）：签名已超过 %d 分钟（工具按本机时钟检查），并且每个 nonce 账户在浏览器里显示的
     当前 nonce 值（finalized）都已经不是上面列出的值。B 不能证明旧签名一定失效（例如网络曾停摆），
     所以只用于归集：旧归集签名即使之后上链，也只是把充值地址的钱转进金库，手续费在已确认的预算内。
     B 不撤销旧签名，也不释放它的费用授权：旧签名之后上链时，它的预算会在新操作的预算之外额外花掉。
都不满足时不要关闭：把原 signed 文件再 broadcast（文件丢了用 ledger export 导出），等它 finalized。
`, int(sweep.SolanaCloseExpiryWait/time.Minute))
		ans, err := prompt(stdout, "选择 A 或 B: ")
		if err != nil {
			return err
		}
		switch strings.ToUpper(ans) {
		case "A":
		case "B":
			if err := sweep.SolanaCloseByExpiryAllowed(o, now()); err != nil {
				return err
			}
		default:
			return errors.New("nothing closed")
		}
	} else {
		_, _ = fmt.Fprint(stdout, `
关闭前请用手机或另一台设备打开上面的区块浏览器链接逐笔核对（不要只看 broadcast 的结果文件）:
  - 每笔交易要么“成功”，要么确认失败/永远不会上链；
  - 打开每个签名地址，确认其当前 nonce（已发送交易数）已经大于上面列出的最大 nonce。
如果某笔旧签名其实还没上链：不要关闭，先把原来的 signed 文件广播出去（签名一直有效，
文件丢了用 ledger export 导出），或者重新 prepare 同一批源地址做替换签名。
`)
	}
	_, _ = fmt.Fprintln(stdout, "关闭后才能对这条链签新的操作；账本永远不允许再用这些 nonce 签名。")
	ans, err := prompt(stdout, "核对无误请输入要关闭的 op id: ")
	if err != nil {
		return err
	}
	if ans != opID {
		return errors.New("op id did not match; nothing closed")
	}
	if err := l.Close(opID, now()); err != nil {
		return err
	}
	if err := st.Save(l); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "closed %s\n", opID)
	return err
}

func cmdNonceRegister(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("nonce-register", stderr)
	path := fs.String("ledger", "", "offline ledger file")
	addr := fs.String("address", "", "durable nonce account address (from nonce-setup)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" || *addr == "" {
		return errors.New("--ledger and --address are required")
	}
	st := &sweep.LedgerStore{Path: *path}
	if err := st.Lock(); err != nil {
		return err
	}
	defer st.Unlock()
	l, err := st.Load()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "登记前请在手机上打开 %s\n确认它是 Nonce 账户，且 authority（授权人）= 你的 Solana 金库地址（treasury-address 显示的那个）。\n", explorerAddr(chain.SolanaMainnet, *addr))
	ans, err := prompt(stdout, "已核对 authority = 金库？输入 yes 登记: ")
	if err != nil {
		return err
	}
	if ans != "yes" {
		return errors.New("not confirmed; nothing registered")
	}
	if err := l.RegisterNonceAccount(*addr, now()); err != nil {
		return err
	}
	if err := st.Save(l); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "registered nonce account %s\n", *addr)
	return err
}

// normalizeTypedAddress validates an address typed by the operator. Mixed-case
// EVM addresses must carry a valid EIP-55 checksum.
func normalizeTypedAddress(n chain.Network, s string) (string, error) {
	s = strings.TrimSpace(s)
	switch n {
	case chain.ArbitrumOne:
		a, err := chain.NormalizeEVMAddress(s)
		if err != nil {
			return "", err
		}
		body := s[2:]
		if strings.ToLower(body) != body && strings.ToUpper(body) != body {
			if common.HexToAddress(a).Hex() != "0x"+body {
				return "", errors.New("EIP-55 checksum mismatch: a character was mistyped")
			}
		}
		return a, nil
	case chain.SolanaMainnet:
		if _, err := chain.DecodeSolanaPubkey(s); err != nil {
			return "", err
		}
		return s, nil
	}
	return "", fmt.Errorf("unsupported network %q", n)
}

func groupAddr(a string) string {
	var b strings.Builder
	for i, c := range a {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func cmdDestRegister(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("dest-register", stderr)
	path := fs.String("ledger", "", "offline ledger file")
	network := fs.String("network", "", "arbitrum-one | solana-mainnet")
	label := fs.String("label", "", "short label, e.g. binance-arb")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" || *network == "" || *label == "" {
		return errors.New("--ledger, --network and --label are required (the address is typed interactively)")
	}
	n := chain.Network(*network)
	if n != chain.ArbitrumOne && n != chain.SolanaMainnet {
		return fmt.Errorf("unsupported network %q", *network)
	}
	st := &sweep.LedgerStore{Path: *path}
	if err := st.Lock(); err != nil {
		return err
	}
	defer st.Unlock()
	l, err := st.Load()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "请在手机交易所 App 里打开 %s 网络的 USDC/USDT 充值地址，照着手动输入（不要从联网电脑复制）。\n", n)
	a1, err := prompt(stdout, "输入地址: ")
	if err != nil {
		return err
	}
	a2, err := prompt(stdout, "再输入一次: ")
	if err != nil {
		return err
	}
	if a1 != a2 {
		return errors.New("the two entries differ; nothing registered")
	}
	addr, err := normalizeTypedAddress(n, a1)
	if err != nil {
		return err
	}
	shown := sweep.DisplayAddress(n, addr)
	_, _ = fmt.Fprintf(stdout, "\n将登记 %q (%s):\n    %s\n    %s\n请与手机 App 上的地址逐组核对。\n", *label, n, shown, groupAddr(shown))
	ans, err := prompt(stdout, "一致请输入 yes: ")
	if err != nil {
		return err
	}
	if ans != "yes" {
		return errors.New("not confirmed; nothing registered")
	}
	if err := l.RegisterDestination(sweep.Destination{Label: *label, Network: n, Address: addr, RegisteredAt: now().UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	if err := st.Save(l); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "registered %s\n", *label)
	return err
}

func readPlan(path string) (*sweep.Plan, error) {
	if path == "" {
		return nil, errors.New("--plan is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := sweep.ReadLimited(f)
	if err != nil {
		return nil, err
	}
	return sweep.DecodePlan(data)
}

type signFlags struct {
	mnemonic, ledger, plan, out string
	highFee                     bool
}

func cmdSweepSign(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("sweep-sign", stderr)
	var f signFlags
	fs.StringVar(&f.mnemonic, "mnemonic-file", "", "mnemonic file")
	fs.StringVar(&f.ledger, "ledger", "", "offline ledger file")
	fs.StringVar(&f.plan, "plan", "", "plan.json from `cryptopay-sweep prepare`")
	fs.StringVar(&f.out, "out", "", "signed output file (must not exist)")
	fs.BoolVar(&f.highFee, "allow-high-fee", false, "EVM: raise fee caps from default to absolute (asks for a second confirmation)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return signCommon(f, sweep.KindSweep, "", "", stdout, stderr)
}

func cmdSendSign(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("send-sign", stderr)
	var f signFlags
	fs.StringVar(&f.mnemonic, "mnemonic-file", "", "mnemonic file")
	fs.StringVar(&f.ledger, "ledger", "", "offline ledger file")
	fs.StringVar(&f.plan, "plan", "", "send plan.json from `cryptopay-sweep prepare --kind send`")
	fs.StringVar(&f.out, "out", "", "signed output file (must not exist)")
	fs.BoolVar(&f.highFee, "allow-high-fee", false, "EVM: raise fee caps from default to absolute (asks for a second confirmation)")
	dest := fs.String("dest", "", "label of a destination registered with dest-register")
	amount := fs.String("amount", "", "token amount, e.g. 2.7 (typed by you, never taken from the plan)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dest == "" || *amount == "" {
		return errors.New("--dest and --amount are required")
	}
	return signCommon(f, sweep.KindSend, *dest, *amount, stdout, stderr)
}

func signCommon(f signFlags, kind sweep.Kind, destLabel, amountStr string, stdout, stderr io.Writer) error {
	if f.ledger == "" || f.out == "" {
		return errors.New("--ledger and --out are required")
	}
	p, err := readPlan(f.plan)
	if err != nil {
		return err
	}
	if p.Kind != kind {
		return fmt.Errorf("plan kind is %q; use %s-sign", p.Kind, p.Kind)
	}
	kr, err := keyringFromFile(f.mnemonic, stderr)
	if err != nil {
		return err
	}
	st := &sweep.LedgerStore{Path: f.ledger}
	if err := st.Lock(); err != nil {
		return err
	}
	defer st.Unlock()
	l, err := st.Load()
	if err != nil {
		return err
	}
	caps := sweep.EVMCapsFor(f.highFee)
	op := &sweep.Operation{Network: p.Network, Kind: p.Kind}
	if kind == sweep.KindSend {
		d, err := l.Destination(destLabel)
		if err != nil {
			return err
		}
		if d.Network != p.Network {
			return fmt.Errorf("destination %s is for %s, plan is %s", d.Label, d.Network, p.Network)
		}
		asset, err := sendAsset(p)
		if err != nil {
			return err
		}
		amount, err := sweep.ParseTokenAmount(amountStr, int(asset.Decimals))
		if err != nil {
			return err
		}
		op.Dest = &d
		if p.Network == chain.ArbitrumOne {
			if op.EVM, err = sweep.BuildEVMSend(p, kr, caps, d, amount); err != nil {
				return err
			}
		} else {
			if op.Solana, err = sweep.BuildSolanaSend(p, kr, l.IsNonceRegistered, d, amount); err != nil {
				return err
			}
		}
		if bal, err := sweep.ParseCanonicalUint(sendBalance(p)); err == nil && bal.Cmp(amount) < 0 {
			_, _ = fmt.Fprintf(stdout, "警告: plan 报告的金库余额 %s 小于外发金额（plan 不可信，仅供参考；余额不足时交易会失败）\n", sweep.FormatUnits(bal, 6))
		}
	} else {
		if p.Network == chain.ArbitrumOne {
			if op.EVM, err = sweep.BuildEVMSweep(p, kr, caps); err != nil {
				return err
			}
		} else {
			if op.Solana, err = sweep.BuildSolanaSweep(p, kr, l.IsNonceRegistered); err != nil {
				return err
			}
		}
	}
	admit, err := l.CheckAdmit(op.Network, op.LedgerTxsPreview(), op.Budget())
	if err != nil {
		return err
	}
	if err := op.CheckCumulative(admit.Cumulative, caps); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "\n================ 请逐项核对 ================")
	_, _ = fmt.Fprint(stdout, op.Describe(admit))
	_, _ = fmt.Fprintln(stdout, "===========================================")
	target := op.ConfirmTarget()
	ans, err := prompt(stdout, fmt.Sprintf("确认收款地址无误后，输入它的最后 6 位: "))
	if err != nil {
		return err
	}
	if !strings.EqualFold(ans, target[len(target)-6:]) {
		return errors.New("confirmation did not match; nothing signed")
	}
	if f.highFee {
		ans, err := prompt(stdout, "你使用了 --allow-high-fee（上限提高到 1 gwei / 0.005 ETH）。输入 HIGH-FEE 继续: ")
		if err != nil {
			return err
		}
		if ans != "HIGH-FEE" {
			return errors.New("high-fee confirmation did not match; nothing signed")
		}
	}
	signed, ltxs, err := op.Sign(kr)
	if err != nil {
		return err
	}
	rec, err := sweep.CommitSigned(st, l, op, signed, ltxs, f.out, now())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "\n已签名 %d 笔交易，写入 %s\n账本记录 op %s（pending）。广播并在浏览器核对后，在离线机运行 ledger close --op %s\n",
		len(signed), f.out, rec.ID, rec.ID)
	return err
}

func sendAsset(p *sweep.Plan) (chain.Asset, error) {
	if p.EVM != nil && p.EVM.Send != nil {
		return sweep.AssetBySymbol(p.Network, p.EVM.Send.Symbol)
	}
	if p.Solana != nil && p.Solana.Send != nil {
		return sweep.AssetBySymbol(p.Network, p.Solana.Send.Symbol)
	}
	return chain.Asset{}, errors.New("not a send plan")
}

func sendBalance(p *sweep.Plan) string {
	if p.EVM != nil && p.EVM.Send != nil {
		return p.EVM.Send.TreasuryBalance
	}
	if p.Solana != nil && p.Solana.Send != nil {
		return p.Solana.Send.TreasuryBalance
	}
	return ""
}
