package hdkey

import (
	"golang.org/x/crypto/ripemd160" //nolint:staticcheck // BIP32 fingerprints are defined with RIPEMD-160; not used for security.
)

func ripemd160Sum(b []byte) []byte {
	h := ripemd160.New()
	_, _ = h.Write(b)
	return h.Sum(nil)
}
