// Command cryptopay-sweep is the ONLINE half of the stablecoin sweep tool.
// It never sees a mnemonic or private key of the deposit tree:
//
//	prepare     read balances / nonces / fees and write an unsigned plan.json
//	broadcast   send a signed.json produced offline, in order, verifying each step
//	nonce-setup create Solana durable nonce accounts (authority = treasury)
//	            using a random temporary online key
//
// Everything it writes is untrusted input for the offline signer
// (cmd/cryptopay-offline sweep-sign / send-sign). See that README.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/tools/cryptopay/sweep"
)

const (
	defaultArbRPC = "https://arb1.arbitrum.io/rpc"
	defaultSolRPC = "https://api.mainnet-beta.solana.com"
)

const usage = `cryptopay-sweep <command> [flags]   (online machine; holds no keys)

  prepare   --network arbitrum-one|solana-mainnet --treasury ADDR --out plan.json
            sweep: --source INDEX:ADDRESS ... | --sources-file F   (Solana: --nonce-account A ...)
            send:  --kind send --asset USDC|USDT --dest ADDR       (Solana: --nonce-account A)
  broadcast --signed signed.json [--finality-timeout 40m]
            sends each signed tx at most once per run, in order, stopping on any anomaly;
            re-running may resend the same signed bytes (same signature: executes at most once)
  nonce-setup --treasury SOLANA_TREASURY --key-out tmp-key.json [--count 1] | --resume tmp-key.json

RPC: --rpc URL | --rpc-file F | --helius-key-file F (Solana) | $CRYPTOPAY_ARB_RPC / $CRYPTOPAY_SOL_RPC.
The RPC URL is never printed.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stdout, stderr := scrubWriter{os.Stdout}, scrubWriter{os.Stderr}
	if err := run(ctx, os.Args[1:], stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "error:", scrubAll(err.Error()))
		os.Exit(1)
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

type rpcFlags struct{ url, file, helius string }

func addRPCFlags(fs *flag.FlagSet) *rpcFlags {
	r := &rpcFlags{}
	fs.StringVar(&r.url, "rpc", "", "RPC URL (prefer --rpc-file: URLs with keys leak via shell history)")
	fs.StringVar(&r.file, "rpc-file", "", "file containing the RPC URL")
	fs.StringVar(&r.helius, "helius-key-file", "", "Solana: file containing only a Helius API key")
	return r
}

func (r *rpcFlags) client(n chain.Network, log io.Writer) (*rpcClient, error) {
	env, def := "CRYPTOPAY_ARB_RPC", defaultArbRPC
	if n == chain.SolanaMainnet {
		env, def = "CRYPTOPAY_SOL_RPC", defaultSolRPC
	} else if r.helius != "" {
		return nil, errors.New("--helius-key-file is for Solana only")
	}
	u, src, err := resolveRPC(r.url, r.file, r.helius, env, def)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "RPC: %s\n", src)
	return newRPC(u)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return errors.New("missing command")
	}
	switch args[0] {
	case "prepare":
		return cmdPrepare(ctx, args[1:], stdout, stderr)
	case "broadcast":
		return cmdBroadcast(ctx, args[1:], stdout, stderr)
	case "nonce-setup":
		return cmdNonceSetup(ctx, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(stdout, usage)
		return nil
	}
	_, _ = fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

func parseSources(list []string, file string, network chain.Network) ([]sourceArg, error) {
	var lines []string
	lines = append(lines, list...)
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(string(data), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			lines = append(lines, strings.Join(strings.Fields(strings.ReplaceAll(l, ",", " ")), ":"))
		}
	}
	var out []sourceArg
	seen := map[uint32]bool{}
	for _, l := range lines {
		i, a, ok := strings.Cut(l, ":")
		if !ok {
			return nil, fmt.Errorf("source %q: want INDEX:ADDRESS", l)
		}
		idx, err := strconv.ParseUint(i, 10, 32)
		if err != nil || idx >= 1<<31 {
			return nil, fmt.Errorf("source %q: bad index", l)
		}
		if seen[uint32(idx)] {
			return nil, fmt.Errorf("source index %d given twice", idx)
		}
		seen[uint32(idx)] = true
		if network == chain.ArbitrumOne {
			if a, err = chain.NormalizeEVMAddress(a); err != nil {
				return nil, err
			}
		} else if _, err := chain.DecodeSolanaPubkey(a); err != nil {
			return nil, err
		}
		out = append(out, sourceArg{index: uint32(idx), address: a})
	}
	if len(out) == 0 {
		return nil, errors.New("no sources (--source INDEX:ADDRESS or --sources-file)")
	}
	return out, nil
}

func cmdPrepare(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("prepare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rf := addRPCFlags(fs)
	network := fs.String("network", "", "arbitrum-one | solana-mainnet")
	kind := fs.String("kind", "sweep", "sweep | send")
	treasury := fs.String("treasury", "", "treasury address printed by `cryptopay-offline treasury-address`")
	out := fs.String("out", "", "plan.json to write (must not exist)")
	var sources, nonces multiFlag
	fs.Var(&sources, "source", "INDEX:ADDRESS of a deposit address (repeatable)")
	sourcesFile := fs.String("sources-file", "", "file with one 'INDEX ADDRESS' per line")
	fs.Var(&nonces, "nonce-account", "Solana durable nonce account (repeatable, max 5)")
	asset := fs.String("asset", "", "send: USDC | USDT")
	dest := fs.String("dest", "", "send: destination address (only a cross-check hint; the offline side uses its own registry)")
	cu := fs.Uint64("cu-price", 0, "Solana compute unit price in micro-lamports (0 = from recent fees, capped)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || *treasury == "" {
		return errors.New("--out and --treasury are required")
	}
	if _, err := os.Lstat(*out); err == nil {
		return fmt.Errorf("%s already exists", *out)
	}
	n := chain.Network(*network)
	c, err := rf.client(n, stderr)
	if err != nil {
		return err
	}
	var plan *sweep.Plan
	switch {
	case n == chain.ArbitrumOne && *kind == "sweep":
		t, err := chain.NormalizeEVMAddress(*treasury)
		if err != nil {
			return err
		}
		src, err := parseSources(sources, *sourcesFile, n)
		if err != nil {
			return err
		}
		plan, err = prepareEVMSweep(ctx, evmRPC{c}, t, src, stderr)
		if err != nil {
			return err
		}
	case n == chain.ArbitrumOne && *kind == "send":
		t, err := chain.NormalizeEVMAddress(*treasury)
		if err != nil {
			return err
		}
		if plan, err = prepareEVMSend(ctx, evmRPC{c}, t, *asset, *dest, stderr); err != nil {
			return err
		}
	case n == chain.SolanaMainnet && *kind == "sweep":
		src, err := parseSources(sources, *sourcesFile, n)
		if err != nil {
			return err
		}
		if plan, err = prepareSolanaSweep(ctx, solRPC{c}, *treasury, src, nonces, *cu, stderr); err != nil {
			return err
		}
	case n == chain.SolanaMainnet && *kind == "send":
		if len(nonces) != 1 {
			return errors.New("send needs exactly one --nonce-account")
		}
		if plan, err = prepareSolanaSend(ctx, solRPC{c}, *treasury, *asset, *dest, nonces[0], *cu, stderr); err != nil {
			return err
		}
	default:
		return errors.New("--network must be arbitrum-one or solana-mainnet and --kind sweep or send")
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := sweep.DecodePlan(data); err != nil { // self-check: the offline side will accept the shape
		return fmt.Errorf("internal: generated plan does not decode: %w", err)
	}
	if err := sweep.AtomicWriteFile(*out, data, 0o644, false); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote %s — carry it to the offline machine and run sweep-sign / send-sign\n", *out)
	return err
}

// ---- broadcast result ----------------------------------------------------

type txResult struct {
	Hash      string `json:"hash"`
	Role      string `json:"role"`
	Submitted bool   `json:"submitted"`
	Status    string `json:"status,omitempty"` // executed | failed
	Block     string `json:"block,omitempty"`
	BlockHash string `json:"block_hash,omitempty"`
	Finalized bool   `json:"finalized"`
}

type result struct {
	OpID    string      `json:"op_id"`
	Network string      `json:"network"`
	Updated string      `json:"updated"`
	Stopped string      `json:"stopped,omitempty"`
	Txs     []*txResult `json:"txs"`
	path    string
	saveErr error
}

func (r *result) ensure(i int, hash, role string) {
	for len(r.Txs) <= i {
		r.Txs = append(r.Txs, &txResult{})
	}
	if r.Txs[i].Hash != hash {
		r.Txs[i] = &txResult{Hash: hash, Role: role}
	}
}

// save writes the result report (a report only: broadcast never reads it
// back; every run re-checks the chain). A failed write is remembered and
// turns the run into an error.
func (r *result) save() {
	r.Updated = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		err = sweep.AtomicWriteFile(r.path, append(data, '\n'), 0o644, true)
	}
	if err != nil && r.saveErr == nil {
		r.saveErr = err
	}
}

// stop records an anomaly and returns it as an error. Broadcast never tries
// to repair anything: recovery is prepare + offline signing again.
func (r *result) stop(i int, msg string) error {
	r.Stopped = scrubAll(fmt.Sprintf("tx %d: %s", i+1, msg))
	r.save()
	return fmt.Errorf("STOPPED at tx %d: %s", i+1, msg)
}

type bcastOpts struct {
	log             io.Writer
	poll            time.Duration
	receiptTimeout  time.Duration
	finalityTimeout time.Duration
}

func cmdBroadcast(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("broadcast", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rf := addRPCFlags(fs)
	signed := fs.String("signed", "", "signed.json from the offline machine")
	finality := fs.Duration("finality-timeout", 40*time.Minute, "how long to wait for finality (0 = do not wait)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *signed == "" {
		return errors.New("--signed is required")
	}
	f, err := os.Open(*signed)
	if err != nil {
		return err
	}
	data, err := sweep.ReadLimited(f)
	_ = f.Close()
	if err != nil {
		return err
	}
	b, err := sweep.DecodeBundle(data)
	if err != nil {
		return err
	}
	unlock, err := lockBroadcast(b)
	if err != nil {
		return err
	}
	defer unlock()
	res := &result{OpID: b.OpID, Network: string(b.Network), path: *signed + ".result.json"}
	c, err := rf.client(b.Network, stderr)
	if err != nil {
		return err
	}
	opts := bcastOpts{log: stdout, poll: 2 * time.Second, receiptTimeout: 5 * time.Minute, finalityTimeout: *finality}
	if b.Network == chain.ArbitrumOne {
		err = broadcastEVM(ctx, evmRPC{c}, b, res, opts)
	} else {
		err = broadcastSolana(ctx, solRPC{c}, b, res, opts)
	}
	res.save()
	if err == nil && res.saveErr != nil {
		err = fmt.Errorf("could not write the result report: %w", res.saveErr)
	}
	_, _ = fmt.Fprintf(stdout, "\nresult: %s\n", res.path)
	for i, t := range res.Txs {
		_, _ = fmt.Fprintf(stdout, "  #%d %-6s submitted=%v status=%s finalized=%v  %s\n", i+1, t.Role, t.Submitted, orDash(t.Status), t.Finalized, t.Hash)
	}
	if err == nil {
		_, _ = fmt.Fprintf(stdout, "\n用手机打开区块浏览器核对每笔交易后，在离线机运行: cryptopay-offline ledger close --ledger <账本> --op %s\n", b.OpID)
	}
	return err
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func cmdNonceSetup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("nonce-setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rf := addRPCFlags(fs)
	treasury := fs.String("treasury", "", "Solana treasury address (authority of the new nonce accounts)")
	keyOut := fs.String("key-out", "", "new file for the temporary keys (mode 0600)")
	resume := fs.String("resume", "", "resume with an existing temporary key file")
	count := fs.Int("count", 1, "number of nonce accounts (1..5)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *treasury == "" || (*keyOut == "") == (*resume == "") {
		return errors.New("--treasury and exactly one of --key-out / --resume are required")
	}
	c, err := rf.client(chain.SolanaMainnet, stderr)
	if err != nil {
		return err
	}
	kf := *keyOut
	if *resume != "" {
		kf = *resume
	}
	return cmdNonceSetupRun(ctx, solRPC{c}, *treasury, *count, kf, *resume != "", stdout)
}

// lockBroadcast serializes broadcasts per network and signer set (derived
// from the signed bytes, not from file names), using lock files in the
// user's cache directory, so two copies or two different bundles for the
// same treasury cannot be broadcast in parallel.
func lockBroadcast(b *sweep.SignedBundle) (func(), error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "cryptopay-sweep", "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	signers, err := bundleSigners(b)
	if err != nil {
		return nil, err
	}
	var held []string
	release := func() {
		for _, p := range held {
			_ = os.Remove(p)
		}
	}
	for _, s := range signers {
		p := filepath.Join(dir, string(b.Network)+"-"+s+".lock")
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			release()
			if os.IsExist(err) {
				return nil, fmt.Errorf("another broadcast for %s is running (or crashed): remove %s if not", s, p)
			}
			return nil, err
		}
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
		_ = f.Close()
		held = append(held, p)
	}
	return release, nil
}

// bundleSigners returns the sorted distinct signers recovered from the raw
// transactions (EVM senders; Solana fee payers).
func bundleSigners(b *sweep.SignedBundle) ([]string, error) {
	set := map[string]bool{}
	for i, t := range b.Txs {
		if b.Network == chain.ArbitrumOne {
			raw, err := hexutil.Decode(t.Raw)
			if err != nil {
				return nil, fmt.Errorf("tx %d: bad hex", i)
			}
			d, err := sweep.DecodeEVMSigned(raw)
			if err != nil {
				return nil, fmt.Errorf("tx %d: %w", i, err)
			}
			set[d.From] = true
			if d.IsTransfer {
				set[d.Recipient] = true // sweeps: the treasury even without a top-up
			}
		} else {
			raw, err := base64.StdEncoding.DecodeString(t.Raw)
			if err != nil {
				return nil, fmt.Errorf("tx %d: bad base64", i)
			}
			w, err := sweep.ParseSolanaWire(raw)
			if err != nil {
				return nil, fmt.Errorf("tx %d: %w", i, err)
			}
			set[w.Keys[0]] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}
