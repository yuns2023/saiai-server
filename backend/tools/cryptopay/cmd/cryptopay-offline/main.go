// Command cryptopay-offline is the air-gapped key tool for stablecoin deposits.
//
// It creates the BIP39 mnemonic, exports the watch-only EVM account xpub and
// the signed Solana owner-address pool that the server imports. The server
// never sees the mnemonic or any private key. See README.md.
package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mr-tron/base58"
	"github.com/tyler-smith/go-bip39"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/hdkey"
)

// DefaultSignerIndex is the SLIP-10 account index of the manifest signing key
// (m/44'/501'/K'/0'), far outside any realistic pool range.
const DefaultSignerIndex uint32 = 2147483000

const usage = `cryptopay-offline <command> [flags]

Commands (run on the air-gapped machine):
  gen-mnemonic   --out FILE | --print         new 24-word BIP39 mnemonic (crypto/rand)
  evm-xpub       --mnemonic-file F            xpub at m/44'/60'/0' + first 3 addresses
  solana-pool    --mnemonic-file F --start N --count M [--signer-index K] [--network solana-mainnet] --out manifest.json
  signer-pubkey  --mnemonic-file F [--signer-index K]
  verify-evm     --mnemonic-file F --index i  print EVM address m/44'/60'/0'/0/i
  verify-solana  --mnemonic-file F --index i  print Solana owner m/44'/501'/i'/0'

Sweep (see README.md):
  treasury-address --mnemonic-file F          EVM + Solana treasury addresses (fund them with a little ETH / SOL)
  ledger init|list|close|export --ledger L [--op ID] [--out F]  offline signing ledger
  nonce-register --ledger L --address A       register a Solana durable nonce account (authority = treasury)
  dest-register  --ledger L --network N --label X   register an external address (typed twice)
  sweep-sign     --mnemonic-file F --ledger L --plan plan.json --out signed.json [--allow-high-fee]
  send-sign      --mnemonic-file F --ledger L --plan plan.json --dest LABEL --amount 2.7 --out signed.json [--allow-high-fee]
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return errors.New("missing command")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "gen-mnemonic":
		return cmdGenMnemonic(rest, stdout, stderr)
	case "evm-xpub":
		return cmdEVMXpub(rest, stdout, stderr)
	case "solana-pool":
		return cmdSolanaPool(rest, stdout, stderr)
	case "signer-pubkey":
		return cmdSignerPubkey(rest, stdout, stderr)
	case "verify-evm":
		return cmdVerifyEVM(rest, stdout, stderr)
	case "verify-solana":
		return cmdVerifySolana(rest, stdout, stderr)
	case "treasury-address":
		return cmdTreasuryAddress(rest, stdout, stderr)
	case "ledger":
		return cmdLedger(rest, stdout, stderr)
	case "nonce-register":
		return cmdNonceRegister(rest, stdout, stderr)
	case "dest-register":
		return cmdDestRegister(rest, stdout, stderr)
	case "sweep-sign":
		return cmdSweepSign(rest, stdout, stderr)
	case "send-sign":
		return cmdSendSign(rest, stdout, stderr)
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(stdout, usage)
		return nil
	default:
		_, _ = fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func cmdGenMnemonic(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("gen-mnemonic", stderr)
	out := fs.String("out", "", "write the mnemonic to this NEW file (mode 0600)")
	printFlag := fs.Bool("print", false, "print the mnemonic to stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*out == "") == !*printFlag {
		return errors.New("choose exactly one of --out FILE or --print")
	}
	entropy, err := bip39.NewEntropy(256)
	if err != nil {
		return err
	}
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return err
	}
	if *printFlag {
		_, err = fmt.Fprintln(stdout, mnemonic)
		return err
	}
	if err := writeNewFile(*out, []byte(mnemonic+"\n"), 0o600); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote 24-word mnemonic to %s (mode 0600). Back it up on paper; never copy it to an online machine.\n", *out)
	return err
}

func cmdEVMXpub(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("evm-xpub", stderr)
	mf := fs.String("mnemonic-file", "", "mnemonic file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	seed, err := loadSeed(*mf, stderr)
	if err != nil {
		return err
	}
	xpub, addrs, err := evmAccountXpub(seed, 3)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "path:  %s\nxpub:  %s\n", chain.EVMAccountPath, xpub); err != nil {
		return err
	}
	for i, a := range addrs {
		if _, err := fmt.Fprintf(stdout, "%s/0/%d  %s\n", chain.EVMAccountPath, i, a); err != nil {
			return err
		}
	}
	return nil
}

// evmAccountXpub returns the account xpub and the first n receive addresses.
// Each address is derived twice (private path from the seed, public path from
// the exported xpub with the server's code) and must agree.
func evmAccountXpub(seed []byte, n uint32) (string, []string, error) {
	master, err := hdkey.NewMasterKey(seed)
	if err != nil {
		return "", nil, err
	}
	acct, err := master.DerivePath(chain.EVMAccountPath)
	if err != nil {
		return "", nil, err
	}
	xpub := acct.Neuter().String()
	addrs := make([]string, 0, n)
	for i := uint32(0); i < n; i++ {
		priv, err := evmAddressPrivate(master, i)
		if err != nil {
			return "", nil, err
		}
		pub, err := chain.EVMAddressFromXpub(xpub, i)
		if err != nil {
			return "", nil, err
		}
		if pub != priv {
			return "", nil, fmt.Errorf("internal mismatch at index %d: xpub path %s != private path %s", i, pub, priv)
		}
		addrs = append(addrs, pub)
	}
	return xpub, addrs, nil
}

func evmAddressPrivate(master *hdkey.ExtendedPrivateKey, index uint32) (string, error) {
	if index >= hdkey.HardenedOffset {
		return "", errors.New("index must be < 2^31")
	}
	k, err := master.DerivePath(chain.EVMAccountPath + "/0/" + strconv.FormatUint(uint64(index), 10))
	if err != nil {
		return "", err
	}
	return chain.EVMAddressFromPubKey(k.Neuter().PubKey), nil
}

func solanaPath(i uint32) string {
	return strings.Replace(chain.SolanaPoolPathTemplate, "{i}", strconv.FormatUint(uint64(i), 10), 1)
}

func solanaKey(seed []byte, i uint32) (*hdkey.SLIP10Ed25519Key, error) {
	if i >= hdkey.HardenedOffset {
		return nil, errors.New("index must be < 2^31")
	}
	return hdkey.NewSLIP10Ed25519Master(seed).DerivePath(solanaPath(i))
}

func solanaAddress(seed []byte, i uint32) (string, error) {
	k, err := solanaKey(seed, i)
	if err != nil {
		return "", err
	}
	return base58.Encode(k.PublicKey()), nil
}

// buildSolanaPool creates and signs the manifest, then verifies it with the
// server's verifier before returning it.
func buildSolanaPool(seed []byte, network chain.Network, start, count, signerIndex uint32) ([]byte, string, error) {
	if count == 0 {
		return nil, "", errors.New("--count must be > 0")
	}
	if uint64(start)+uint64(count) > uint64(hdkey.HardenedOffset) {
		return nil, "", errors.New("start+count exceeds 2^31")
	}
	if count > chain.MaxSolanaPoolEntries {
		return nil, "", fmt.Errorf("--count must be <= %d", chain.MaxSolanaPoolEntries)
	}
	if signerIndex >= start && uint64(signerIndex) < uint64(start)+uint64(count) {
		return nil, "", fmt.Errorf("signer index %d lies inside the pool range", signerIndex)
	}
	if network.Family() != chain.FamilySolana {
		return nil, "", fmt.Errorf("network %q is not a Solana network", network)
	}
	signer, err := solanaKey(seed, signerIndex)
	if err != nil {
		return nil, "", err
	}
	signerPub := base58.Encode(signer.PublicKey())
	m := chain.SolanaPoolManifest{
		Version:      chain.SolanaPoolManifestVersion,
		Network:      network,
		PathTemplate: chain.SolanaPoolPathTemplate,
		Start:        start,
		Entries:      make([]chain.SolanaPoolEntry, 0, count),
		SignerPubkey: signerPub,
	}
	master := hdkey.NewSLIP10Ed25519Master(seed)
	for i := start; i < start+count; i++ {
		k, err := master.DerivePath(solanaPath(i))
		if err != nil {
			return nil, "", err
		}
		m.Entries = append(m.Entries, chain.SolanaPoolEntry{Index: i, Address: base58.Encode(k.PublicKey())})
	}
	msg, err := chain.SolanaPoolManifestSigningBytes(&m)
	if err != nil {
		return nil, "", err
	}
	m.Signature = base58.Encode(ed25519.Sign(signer.PrivateKey(), msg))
	out, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		return nil, "", err
	}
	out = append(out, '\n')
	if _, err := chain.VerifySolanaPoolManifest(out, signerPub); err != nil {
		return nil, "", fmt.Errorf("self-verification failed: %w", err)
	}
	return out, signerPub, nil
}

func cmdSolanaPool(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("solana-pool", stderr)
	mf := fs.String("mnemonic-file", "", "mnemonic file")
	start := fs.Uint("start", 0, "first account index i")
	count := fs.Uint("count", 0, "number of addresses")
	signerIndex := fs.Uint("signer-index", uint(DefaultSignerIndex), "SLIP-10 index K of the manifest signer m/44'/501'/K'/0'")
	network := fs.String("network", string(chain.SolanaMainnet), "solana-mainnet | solana-devnet")
	out := fs.String("out", "", "manifest output file (must not exist)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("--out is required")
	}
	if *start >= uint(hdkey.HardenedOffset) || *count >= uint(hdkey.HardenedOffset) || *signerIndex >= uint(hdkey.HardenedOffset) {
		return errors.New("indexes must be < 2^31")
	}
	seed, err := loadSeed(*mf, stderr)
	if err != nil {
		return err
	}
	raw, signerPub, err := buildSolanaPool(seed, chain.Network(*network), uint32(*start), uint32(*count), uint32(*signerIndex))
	if err != nil {
		return err
	}
	if err := writeNewFile(*out, raw, 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote %d addresses [%d..%d] to %s\nsigner_pubkey: %s (configure this as the trusted signer on the server)\n",
		*count, *start, uint64(*start)+uint64(*count)-1, *out, signerPub)
	return err
}

func cmdSignerPubkey(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("signer-pubkey", stderr)
	mf := fs.String("mnemonic-file", "", "mnemonic file")
	signerIndex := fs.Uint("signer-index", uint(DefaultSignerIndex), "signer index K")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *signerIndex >= uint(hdkey.HardenedOffset) {
		return errors.New("index must be < 2^31")
	}
	seed, err := loadSeed(*mf, stderr)
	if err != nil {
		return err
	}
	addr, err := solanaAddress(seed, uint32(*signerIndex))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s  %s\n", solanaPath(uint32(*signerIndex)), addr)
	return err
}

func cmdVerifyEVM(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("verify-evm", stderr)
	mf := fs.String("mnemonic-file", "", "mnemonic file")
	index := fs.Uint("index", 0, "address index i")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *index >= uint(hdkey.HardenedOffset) {
		return errors.New("index must be < 2^31")
	}
	seed, err := loadSeed(*mf, stderr)
	if err != nil {
		return err
	}
	master, err := hdkey.NewMasterKey(seed)
	if err != nil {
		return err
	}
	addr, err := evmAddressPrivate(master, uint32(*index))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s/0/%d  %s\n", chain.EVMAccountPath, *index, addr)
	return err
}

func cmdVerifySolana(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("verify-solana", stderr)
	mf := fs.String("mnemonic-file", "", "mnemonic file")
	index := fs.Uint("index", 0, "account index i")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *index >= uint(hdkey.HardenedOffset) {
		return errors.New("index must be < 2^31")
	}
	seed, err := loadSeed(*mf, stderr)
	if err != nil {
		return err
	}
	addr, err := solanaAddress(seed, uint32(*index))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s  %s\n", solanaPath(uint32(*index)), addr)
	return err
}

// loadSeed reads and checksum-validates a mnemonic file and returns the BIP39
// seed (empty passphrase, as Phantom and MetaMask use by default).
func loadSeed(path string, stderr io.Writer) ([]byte, error) {
	if path == "" {
		return nil, errors.New("--mnemonic-file is required")
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		_, _ = fmt.Fprintf(stderr, "warning: %s is readable by group/others (mode %v); chmod 600 it\n", path, st.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return seedFromMnemonic(string(raw))
}

func seedFromMnemonic(raw string) ([]byte, error) {
	mnemonic := strings.Join(strings.Fields(strings.ToLower(raw)), " ")
	if n := len(strings.Fields(mnemonic)); n != 12 && n != 15 && n != 18 && n != 21 && n != 24 {
		return nil, fmt.Errorf("mnemonic has %d words; want 12/15/18/21/24", n)
	}
	seed, err := bip39.NewSeedWithErrorChecking(mnemonic, "")
	if err != nil {
		return nil, fmt.Errorf("invalid mnemonic (bad word or checksum): %w", err)
	}
	return seed, nil
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
