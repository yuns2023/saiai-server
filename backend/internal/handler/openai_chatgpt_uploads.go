package handler

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

// Native upload control is separate from model turns: no token/fixed-turn
// billing, Responses adaptation, prompt/body editing or model-slot retention.
func (h *OpenAIGatewayHandler) ChatGPTUpload(c *gin.Context) {
	key, _, scope, ok := h.chatGPTDeliveryContext(c)
	if !ok {
		return
	}
	cache, err := h.gatewayService.ChatGPTUploadCache()
	if err != nil {
		h.errorResponse(c, 503, "upload_context_unavailable", "Chat upload ownership is unavailable")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, service.MaxChatGPTUploadBytes+1))
	if err != nil || len(body) == 0 {
		h.errorResponse(c, 400, "invalid_request_error", "Invalid Chat upload body")
		return
	}
	if len(body) > service.MaxChatGPTUploadBytes {
		h.errorResponse(c, 413, "request_too_large", "Chat uploads exceed the supported size")
		return
	}
	path := c.Request.URL.Path
	create := path == "/chatgpt/backend-api/files"
	var owner int64
	if create {
		var metadata struct {
			FileSize int64 `json:"file_size"`
		}
		if json.Unmarshal(body, &metadata) != nil || metadata.FileSize <= 0 || metadata.FileSize > service.MaxChatGPTUploadBytes {
			h.errorResponse(c, 400, "invalid_request_error", "Invalid Chat upload size")
			return
		}
		device := c.GetHeader("OAI-Device-ID")
		if len(device) > 512 {
			h.errorResponse(c, 400, "invalid_request_error", "Invalid Chat device identity")
			return
		}
		sessionKey := scope.UploadSessionKey(device)
		owner, err = cache.GetChatGPTUploadOwner(c.Request.Context(), sessionKey)
		if err == nil && owner == 0 {
			selection, _, selectErr := h.gatewayService.SelectChatGPTOAuthAccount(c.Request.Context(), key.GroupID, "", "")
			if selectErr != nil || selection == nil || selection.Account == nil {
				h.errorResponse(c, 503, "service_unavailable", "No available OpenAI OAuth account")
				return
			}
			candidateID := selection.Account.ID
			releaseChatGPTControlSelection(selection)
			owner, err = cache.ClaimChatGPTUploadOwner(c.Request.Context(), sessionKey, candidateID, service.ChatGPTUploadSessionTTL)
		}
	} else if path == "/chatgpt/backend-api/files/process_upload_stream" {
		var metadata struct {
			FileID string `json:"file_id"`
		}
		if json.Unmarshal(body, &metadata) != nil || !service.ValidChatGPTUploadFileID(metadata.FileID) {
			h.errorResponse(c, 400, "invalid_request_error", "Invalid Chat upload identity")
			return
		}
		owner, err = cache.GetChatGPTUploadOwner(c.Request.Context(), scope.UploadedFileKey(metadata.FileID))
	} else if path == "/chatgpt/backend-api/estuary/upload_content_bytes" || path == "/chatgpt/api/estuary/upload_content_bytes" {
		capability, parseErr := chatGPTMultipartUploadCapability(c.GetHeader("Content-Type"), body)
		if parseErr != nil {
			h.errorResponse(c, 400, "invalid_request_error", "Invalid Chat upload form")
			return
		}
		if values, exists := c.Request.URL.Query()["upload_url"]; exists && (len(values) != 1 || values[0] != capability) {
			h.errorResponse(c, 400, "invalid_request_error", "Chat upload capability mismatch")
			return
		}
		owner, err = cache.GetChatGPTUploadOwner(c.Request.Context(), scope.UploadURLKey(capability))
	} else {
		h.errorResponse(c, 404, "not_found_error", "Unsupported Chat upload endpoint")
		return
	}
	if err != nil {
		h.errorResponse(c, 503, "upload_context_unavailable", "Chat upload ownership is unavailable")
		return
	}
	if owner == 0 {
		h.errorResponse(c, 404, "upload_context_unavailable", "Chat upload ownership is unavailable")
		return
	}
	account, err := h.gatewayService.GetChatGPTBoundAccount(c.Request.Context(), key.GroupID, owner)
	if err != nil {
		h.errorResponse(c, 503, "service_unavailable", "Chat upload account is unavailable")
		return
	}
	setOpsSelectedAccount(c, owner, service.PlatformOpenAI)
	response, err := h.gatewayService.ForwardChatGPTConversation(c.Request.Context(), c, account, body, c.Request.URL.RequestURI())
	if err != nil {
		h.errorResponse(c, 502, "upstream_error", "Chat upload request failed")
		return
	}
	defer func() { _ = response.Body.Close() }()
	var buffered []byte
	if create && response.StatusCode >= 200 && response.StatusCode < 300 {
		buffered, err = io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
		if err != nil || len(buffered) > 64*1024 {
			h.errorResponse(c, 502, "upstream_error", "Chat upload metadata is invalid")
			return
		}
		metadata, decodeErr := decodeChatGPTUploadMetadata(buffered, response.Header.Get("Content-Encoding"))
		if decodeErr != nil {
			h.errorResponse(c, 502, "upstream_error", "Chat upload metadata is invalid")
			return
		}
		keys, observeErr := service.ChatGPTUploadResponseKeys(scope, metadata)
		if observeErr != nil {
			h.errorResponse(c, 502, "upstream_error", "Chat upload metadata is invalid")
			return
		}
		if len(keys) > 0 {
			err = cache.BindChatGPTUploadOwner(c.Request.Context(), keys, owner, service.ChatGPTUploadTTL)
		}
		if err != nil {
			h.errorResponse(c, 503, "upload_context_unavailable", "Chat upload ownership is unavailable")
			return
		}
	}
	for name, values := range response.Header {
		if shouldCopyChatGPTResponseHeader(name) {
			for _, value := range values {
				c.Writer.Header().Add(name, value)
			}
		}
	}
	c.Header("Cache-Control", "no-store")
	c.Status(response.StatusCode)
	if buffered != nil {
		_, _ = c.Writer.Write(buffered)
		return
	}
	// Processing emits readiness events before its body closes. Flush each
	// original chunk so the official upload resolver can consume those events.
	c.Writer.Flush()
	_, _ = io.Copy(chatGPTUploadFlushWriter{c.Writer}, response.Body)
}

type chatGPTUploadFlushWriter struct{ gin.ResponseWriter }

func (w chatGPTUploadFlushWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.Flush()
	return n, err
}

func chatGPTMultipartUploadCapability(contentType string, body []byte) (string, error) {
	media, parameters, err := mime.ParseMediaType(contentType)
	if err != nil || media != "multipart/form-data" || parameters["boundary"] == "" {
		return "", errors.New("invalid upload form")
	}
	r := multipart.NewReader(bytes.NewReader(body), parameters["boundary"])
	capability := ""
	files := 0
	for n := 0; n < 32; n++ {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			if capability != "" && files == 1 {
				return capability, nil
			}
			return "", errors.New("incomplete upload form")
		}
		if err != nil {
			return "", err
		}
		if part.FormName() == "upload_url" {
			value, err := io.ReadAll(io.LimitReader(part, 32*1024+1))
			if err != nil || len(value) == 0 || len(value) > 32*1024 || capability != "" || part.FileName() != "" {
				return "", errors.New("invalid upload capability")
			}
			capability = string(value)
		} else if part.FormName() == "file" {
			files++
		}
		if _, err := io.Copy(io.Discard, part); err != nil {
			return "", err
		}
		_ = part.Close()
	}
	return "", errors.New("too many upload fields")
}

func decodeChatGPTUploadMetadata(raw []byte, encoding string) ([]byte, error) {
	var reader io.Reader = bytes.NewReader(raw)
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
	case "gzip":
		decoder, err := gzip.NewReader(reader)
		if err != nil {
			return nil, err
		}
		defer func() { _ = decoder.Close() }()
		reader = decoder
	case "deflate":
		decoder, err := zlib.NewReader(reader)
		if err != nil {
			return nil, err
		}
		defer func() { _ = decoder.Close() }()
		reader = decoder
	case "br":
		reader = brotli.NewReader(reader)
	case "zstd":
		decoder, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1024*1024))
		if err != nil {
			return nil, err
		}
		defer decoder.Close()
		reader = decoder
	default:
		return nil, errors.New("unsupported upload metadata encoding")
	}
	decoded, err := io.ReadAll(io.LimitReader(reader, 64*1024+1))
	if err != nil || len(decoded) > 64*1024 {
		return nil, errors.New("invalid upload metadata size")
	}
	return decoded, nil
}

func (h *OpenAIGatewayHandler) chatGPTAttachmentOwner(ctx context.Context, scope service.ChatGPTTurnScope, turns service.ChatGPTTurnCache, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	cache, err := h.gatewayService.ChatGPTUploadCache()
	if err != nil {
		return 0, err
	}
	var owner int64
	for _, id := range ids {
		accountID, err := cache.GetChatGPTUploadOwner(ctx, scope.UploadedFileKey(id))
		if err != nil {
			return 0, err
		}
		if accountID == 0 {
			for _, digest := range service.ChatGPTAssetLookupHashes(id) {
				turn, err := turns.GetChatGPTResume(ctx, scope.AssetKey(digest))
				if err != nil {
					return 0, err
				}
				if turn != nil {
					accountID = turn.AccountID
					break
				}
			}
		}
		if accountID == 0 || owner != 0 && owner != accountID {
			return 0, errors.New("native Chat attachment ownership unavailable or conflicting")
		}
		owner = accountID
	}
	return owner, nil
}
