package handler

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestParseOpenAIImageGenerationRequest(t *testing.T) {
	model, size, err := parseOpenAIImageRequest(service.OpenAIImageEndpointGenerations, "application/json; charset=utf-8", []byte(`{
		"model":"custom/image-model-v2","prompt":"hello","unknown_future_field":{"keep":true},"size":"1024x1536"
	}`))
	require.NoError(t, err)
	require.Equal(t, "custom/image-model-v2", model)
	require.Equal(t, "1024x1536", size)
}

func TestParseOpenAIImageGenerationRejectsStreaming(t *testing.T) {
	_, _, err := parseOpenAIImageRequest(service.OpenAIImageEndpointGenerations, "application/json", []byte(`{"model":"gpt-image-2","stream":true}`))
	require.ErrorContains(t, err, "streaming is not supported")
}

func TestParseOpenAIImageEditMultipart(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("model", "gpt-image-2"))
	require.NoError(t, w.WriteField("size", "1536x1024"))
	imagePart, err := w.CreateFormFile("image[]", "source.png")
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("fake-png-data"))
	require.NoError(t, err)
	require.NoError(t, w.WriteField("future_field", "preserved"))
	require.NoError(t, w.Close())

	model, size, err := parseOpenAIImageRequest(service.OpenAIImageEndpointEdits, w.FormDataContentType(), body.Bytes())
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", model)
	require.Equal(t, "1536x1024", size)
}
