package openai

import (
	"bytes"
	"io"
	"math/rand"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestEncodeCodexRequestZstdFrameHeaderAndRoundTrip(t *testing.T) {
	// Rust zstd 0.13.3 / libzstd 1.5.7 encode_all level 3 header: magic,
	// FHD 0x00 (no single segment, checksum, content size or dictionary)
	// and a 2 MiB window descriptor.
	wantHeader := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00, 0x58}
	rng := rand.New(rand.NewSource(1))
	for _, size := range []int{100, 64 << 10, 3 << 20} {
		data := make([]byte, size)
		for i := range data {
			// Mixed compressible/incompressible JSON-ish content.
			if i%7 == 0 {
				data[i] = byte(rng.Intn(256))
			} else {
				data[i] = "{\"input\":\"abc\"}"[i%15]
			}
		}
		encoded, err := EncodeCodexRequestZstd(data)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(encoded[:6], wantHeader) {
			t.Fatalf("size %d: header % X, want % X", size, encoded[:6], wantHeader)
		}
		decoder, err := zstd.NewReader(bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := io.ReadAll(decoder)
		decoder.Close()
		if err != nil || !bytes.Equal(decoded, data) {
			t.Fatalf("size %d: round trip failed: %v", size, err)
		}
	}
}
