package service

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"
)

// Inspect only bytes already returned in a completed image call. Never fetch
// assets, retain image bytes or use requested dimensions as output evidence.
func chatGPTImageResultDimensions(result string) string {
	encoded := result
	if strings.HasPrefix(result, "data:image/") {
		prefix, data, ok := strings.Cut(result, ";base64,")
		if !ok || strings.Contains(prefix, ",") {
			return ""
		}
		encoded = data
	} else if !strings.HasPrefix(result, "iVBORw0KGgo") && !strings.HasPrefix(result, "/9j/") && !strings.HasPrefix(result, "UklGR") {
		return ""
	}
	header, err := io.ReadAll(io.LimitReader(base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded)), 64*1024))
	if err != nil {
		return ""
	}
	defer clear(header)
	if len(header) >= 30 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP" {
		switch string(header[12:16]) {
		case "VP8X":
			width := 1 + int(header[24]) + (int(header[25]) << 8) + (int(header[26]) << 16)
			height := 1 + int(header[27]) + (int(header[28]) << 8) + (int(header[29]) << 16)
			return ChatGPTImageDimensions(width, height)
		case "VP8 ":
			if string(header[23:26]) == "\x9d\x01\x2a" && header[20]&1 == 0 {
				return ChatGPTImageDimensions(int(binary.LittleEndian.Uint16(header[26:28])&0x3fff), int(binary.LittleEndian.Uint16(header[28:30])&0x3fff))
			}
		case "VP8L":
			if header[20] == 0x2f {
				bits := binary.LittleEndian.Uint32(header[21:25])
				if bits>>29 == 0 {
					return ChatGPTImageDimensions(int(bits&0x3fff)+1, int(bits>>14&0x3fff)+1)
				}
			}
		}
		return ""
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(header))
	if err != nil {
		return ""
	}
	return ChatGPTImageDimensions(config.Width, config.Height)
}
