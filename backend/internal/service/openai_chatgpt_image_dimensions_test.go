package service

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatGPTImageDimensionsFromReturnedPNGJPEGAndWebPBytes(t *testing.T) {
	input := image.NewRGBA(image.Rect(0, 0, 2, 3))
	var pngBytes, jpegBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, input))
	require.NoError(t, jpeg.Encode(&jpegBytes, input, nil))
	png64 := base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	require.Equal(t, "2x3", chatGPTImageResultDimensions(png64))
	require.Equal(t, "2x3", chatGPTImageResultDimensions("data:image/png;base64,"+png64))
	require.Equal(t, "2x3", chatGPTImageResultDimensions("data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(jpegBytes.Bytes())))
	require.Equal(t, "2x3", chatGPTImageResultDimensions(base64.StdEncoding.EncodeToString(jpegBytes.Bytes())))
	for _, variant := range []string{"VP8X", "VP8 ", "VP8L"} {
		header := make([]byte, 32)
		copy(header[:4], "RIFF")
		binary.LittleEndian.PutUint32(header[4:8], 24)
		copy(header[8:12], "WEBP")
		copy(header[12:16], variant)
		binary.LittleEndian.PutUint32(header[16:20], 12)
		switch variant {
		case "VP8X":
			header[24], header[25] = 0xff, 7 // 2048 - 1
			header[27], header[28] = 0x7f, 4 // 1152 - 1
		case "VP8 ":
			copy(header[23:26], "\x9d\x01\x2a")
			binary.LittleEndian.PutUint16(header[26:28], 2048)
			binary.LittleEndian.PutUint16(header[28:30], 1152)
		case "VP8L":
			header[20] = 0x2f
			binary.LittleEndian.PutUint32(header[21:25], uint32(2047|(1151<<14)))
		}
		require.Equal(t, "2048x1152", chatGPTImageResultDimensions("data:image/webp;base64,"+base64.StdEncoding.EncodeToString(header)), variant)
		require.Equal(t, "2048x1152", chatGPTImageResultDimensions(base64.StdEncoding.EncodeToString(header)), variant)
	}
	for _, result := range []string{"sediment://PRIVATE_ASSET", "data:image/png,invalid", "data:image/png;base64,not-an-image", "data:image/webp;base64,AAAA"} {
		require.Empty(t, chatGPTImageResultDimensions(result))
	}
}
