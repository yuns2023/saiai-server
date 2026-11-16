package sweep

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/mr-tron/base58"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

// This file is an independent (no solana-go) parser of legacy Solana wire
// transactions. The offline signer re-parses every signed transaction with
// it and checks the result against the intent, so a bug or surprise in the
// library's message compiler cannot silently change what was signed.

// SolanaAccountRef is one account of an instruction with message-level flags.
type SolanaAccountRef struct {
	Key      string
	Signer   bool
	Writable bool
}

// SolanaInstruction is one parsed instruction.
type SolanaInstruction struct {
	Program  string
	Accounts []SolanaAccountRef
	Data     []byte
}

// SolanaWire is a parsed legacy transaction.
type SolanaWire struct {
	Signatures   [][]byte
	Message      []byte
	Keys         []string
	NumSigners   int
	Signer       map[string]bool
	Writable     map[string]bool
	Blockhash    string
	Instructions []SolanaInstruction
	// Signature is the first signature in base58 (the transaction id).
	Signature string
}

type wireReader struct {
	b   []byte
	off int
}

func (r *wireReader) byte1() (byte, error) {
	if r.off >= len(r.b) {
		return 0, errors.New("truncated")
	}
	v := r.b[r.off]
	r.off++
	return v, nil
}

func (r *wireReader) take(n int) ([]byte, error) {
	if n < 0 || r.off+n > len(r.b) {
		return nil, errors.New("truncated")
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v, nil
}

// compactU16 decodes Solana's short_vec length, rejecting alias encodings.
func (r *wireReader) compactU16() (int, error) {
	var v int
	for i := 0; i < 3; i++ {
		c, err := r.byte1()
		if err != nil {
			return 0, err
		}
		v |= int(c&0x7f) << (7 * i)
		if c&0x80 == 0 {
			if i > 0 && c == 0 {
				return 0, errors.New("non-canonical compact-u16")
			}
			if i == 2 && c > 0x03 {
				return 0, errors.New("compact-u16 overflow")
			}
			return v, nil
		}
	}
	return 0, errors.New("compact-u16 too long")
}

// ParseSolanaWire parses a signed legacy transaction (versioned messages are
// rejected) and verifies every signature over the message bytes.
func ParseSolanaWire(raw []byte) (*SolanaWire, error) {
	if len(raw) > SolanaMaxPacketBytes {
		return nil, fmt.Errorf("transaction is %d bytes > %d", len(raw), SolanaMaxPacketBytes)
	}
	r := &wireReader{b: raw}
	nsig, err := r.compactU16()
	if err != nil {
		return nil, err
	}
	if nsig == 0 || nsig > 12 {
		return nil, fmt.Errorf("%d signatures", nsig)
	}
	w := &SolanaWire{Signer: map[string]bool{}, Writable: map[string]bool{}}
	for i := 0; i < nsig; i++ {
		s, err := r.take(64)
		if err != nil {
			return nil, err
		}
		w.Signatures = append(w.Signatures, s)
	}
	msgStart := r.off
	h0, err := r.byte1()
	if err != nil {
		return nil, err
	}
	if h0&0x80 != 0 {
		return nil, errors.New("versioned message not allowed")
	}
	roSigned, err := r.byte1()
	if err != nil {
		return nil, err
	}
	roUnsigned, err := r.byte1()
	if err != nil {
		return nil, err
	}
	nkeys, err := r.compactU16()
	if err != nil {
		return nil, err
	}
	if int(h0) != nsig || int(roSigned) >= int(h0) || int(h0)+int(roUnsigned) > nkeys {
		return nil, errors.New("inconsistent message header")
	}
	seen := map[string]bool{}
	for i := 0; i < nkeys; i++ {
		k, err := r.take(32)
		if err != nil {
			return nil, err
		}
		ks := base58.Encode(k)
		if seen[ks] {
			return nil, errors.New("duplicate account key")
		}
		seen[ks] = true
		w.Keys = append(w.Keys, ks)
		if i < int(h0) {
			w.Signer[ks] = true
			w.Writable[ks] = i < int(h0)-int(roSigned)
		} else {
			w.Writable[ks] = i < nkeys-int(roUnsigned)
		}
	}
	w.NumSigners = int(h0)
	bh, err := r.take(32)
	if err != nil {
		return nil, err
	}
	w.Blockhash = base58.Encode(bh)
	nix, err := r.compactU16()
	if err != nil {
		return nil, err
	}
	for i := 0; i < nix; i++ {
		pi, err := r.byte1()
		if err != nil {
			return nil, err
		}
		if int(pi) >= nkeys || pi == 0 {
			return nil, errors.New("bad program index")
		}
		na, err := r.compactU16()
		if err != nil {
			return nil, err
		}
		ix := SolanaInstruction{Program: w.Keys[pi]}
		for j := 0; j < na; j++ {
			ai, err := r.byte1()
			if err != nil {
				return nil, err
			}
			if int(ai) >= nkeys {
				return nil, errors.New("bad account index")
			}
			k := w.Keys[ai]
			ix.Accounts = append(ix.Accounts, SolanaAccountRef{Key: k, Signer: w.Signer[k], Writable: w.Writable[k]})
		}
		dl, err := r.compactU16()
		if err != nil {
			return nil, err
		}
		d, err := r.take(dl)
		if err != nil {
			return nil, err
		}
		ix.Data = append([]byte(nil), d...)
		w.Instructions = append(w.Instructions, ix)
	}
	if r.off != len(raw) {
		return nil, errors.New("trailing bytes after message")
	}
	w.Message = raw[msgStart:]
	for i := 0; i < nsig; i++ {
		pub, err := chain.DecodeSolanaPubkey(w.Keys[i])
		if err != nil {
			return nil, err
		}
		if !ed25519.Verify(ed25519.PublicKey(pub), w.Message, w.Signatures[i]) {
			return nil, fmt.Errorf("signature %d does not verify for %s", i, w.Keys[i])
		}
	}
	w.Signature = base58.Encode(w.Signatures[0])
	return w, nil
}

// AdvanceNonceAccount returns the nonce account if the first instruction is
// a System AdvanceNonceAccount, else "".
func (w *SolanaWire) AdvanceNonceAccount() string {
	if len(w.Instructions) == 0 {
		return ""
	}
	ix := w.Instructions[0]
	if ix.Program != SystemProgramID || !bytes.Equal(ix.Data, u32le(4)) || len(ix.Accounts) != 3 {
		return ""
	}
	return ix.Accounts[0].Key
}

func u32le(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func u64le(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}

type wantIx struct {
	program  string
	accounts []string
	data     []byte
}

// VerifySolanaSigned parses raw and checks that it is exactly the intent:
// fee payer, signer set, writable set, blockhash = nonce value, and the
// instruction list (programs, accounts, data) byte for byte.
func VerifySolanaSigned(raw []byte, in *SolanaTxIntent) (*SolanaWire, error) {
	w, err := ParseSolanaWire(raw)
	if err != nil {
		return nil, err
	}
	if w.Keys[0] != in.Treasury {
		return nil, fmt.Errorf("fee payer %s, want treasury %s", w.Keys[0], in.Treasury)
	}
	if w.Blockhash != in.NonceValue {
		return nil, errors.New("recent blockhash is not the durable nonce value")
	}
	wantSigners := in.Signers()
	if w.NumSigners != len(wantSigners) {
		return nil, fmt.Errorf("%d signers, want %d", w.NumSigners, len(wantSigners))
	}
	for _, s := range wantSigners {
		if !w.Signer[s] {
			return nil, fmt.Errorf("missing signer %s", s)
		}
	}
	wantWritable := map[string]bool{in.Treasury: true, in.NonceAccount: true}
	want := []wantIx{
		{SystemProgramID, []string{in.NonceAccount, SysvarRecentBlockhashes, in.Treasury}, u32le(4)},
		{ComputeBudgetProgramID, nil, append([]byte{2}, u32le(SolanaComputeUnitLimit)...)},
		{ComputeBudgetProgramID, nil, append([]byte{3}, u64le(in.CUPriceMicros)...)},
	}
	for _, a := range in.CreateATA {
		ataAddr, err := chain.SolanaATA(in.Treasury, a.Contract, a.TokenProgram)
		if err != nil {
			return nil, err
		}
		wantWritable[ataAddr] = true
		want = append(want, wantIx{chain.AssociatedTokenProgram, []string{in.Treasury, ataAddr, in.Treasury, a.Contract, SystemProgramID, a.TokenProgram}, []byte{1}})
	}
	for _, t := range in.Transfers {
		src, err := chain.SolanaATA(t.Owner, t.Asset.Contract, t.Asset.TokenProgram)
		if err != nil {
			return nil, err
		}
		dst, err := chain.SolanaATA(t.DestOwner, t.Asset.Contract, t.Asset.TokenProgram)
		if err != nil {
			return nil, err
		}
		if src != t.SourceATA || dst != t.DestATA {
			return nil, errors.New("intent ATA mismatch")
		}
		wantWritable[src] = true
		wantWritable[dst] = true
		data := append(append([]byte{12}, u64le(t.Amount)...), solanaTokenDecimals)
		want = append(want, wantIx{t.Asset.TokenProgram, []string{src, t.Asset.Contract, dst, t.Owner}, data})
	}
	if len(w.Instructions) != len(want) {
		return nil, fmt.Errorf("%d instructions, want %d", len(w.Instructions), len(want))
	}
	for i, wi := range want {
		got := w.Instructions[i]
		if got.Program != wi.program || !bytes.Equal(got.Data, wi.data) || len(got.Accounts) != len(wi.accounts) {
			return nil, fmt.Errorf("instruction %d differs from intent", i)
		}
		for j, k := range wi.accounts {
			if got.Accounts[j].Key != k {
				return nil, fmt.Errorf("instruction %d account %d differs from intent", i, j)
			}
		}
	}
	for _, k := range w.Keys {
		if w.Writable[k] != wantWritable[k] {
			return nil, fmt.Errorf("account %s writable=%v, want %v", k, w.Writable[k], wantWritable[k])
		}
	}
	return w, nil
}
