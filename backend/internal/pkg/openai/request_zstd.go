package openai

import (
	"bytes"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// EncodeCodexRequestZstd compresses a request body with a frame header shaped
// like Codex's (Rust zstd 0.13 / libzstd 1.5 `encode_all` level 3): a
// streaming frame with no single-segment flag, checksum, content size or
// dictionary, and a 2 MiB window (header bytes 28 B5 2F FD 00 58). Only the
// frame header shape is matched; the compressed blocks are not byte-identical
// to libzstd output.
func EncodeCodexRequestZstd(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(data)/3 + 64)
	encoder, err := zstd.NewWriter(&buf,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithWindowSize(2<<20),
		zstd.WithEncoderCRC(false),
		zstd.WithEncoderConcurrency(1),
		zstd.WithSingleSegment(false),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize zstd request encoder: %w", err)
	}
	if _, err := encoder.Write(data); err != nil {
		_ = encoder.Close()
		return nil, fmt.Errorf("zstd encode request body: %w", err)
	}
	// Flushing before Close writes the streaming header; a Close with all
	// input still buffered would take the one-shot path that records the
	// content size.
	if err := encoder.Flush(); err != nil {
		_ = encoder.Close()
		return nil, fmt.Errorf("zstd flush request body: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("zstd close request body: %w", err)
	}
	return buf.Bytes(), nil
}
