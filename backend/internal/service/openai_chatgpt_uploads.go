package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	MaxChatGPTUploadBytes   = 32 * 1024 * 1024
	MaxChatGPTUploadFiles   = 32
	ChatGPTUploadTTL        = time.Hour
	ChatGPTUploadSessionTTL = 5 * time.Minute
)

// Upload ownership is independent of model-turn billing. Only scoped digests
// and account IDs are stored, never file names, signed URLs or uploaded bytes.
type ChatGPTUploadCache interface {
	GetChatGPTUploadOwner(context.Context, string) (int64, error)
	ClaimChatGPTUploadOwner(context.Context, string, int64, time.Duration) (int64, error)
	BindChatGPTUploadOwner(context.Context, []string, int64, time.Duration) error
}

func (s *OpenAIGatewayService) ChatGPTUploadCache() (ChatGPTUploadCache, error) {
	turns, err := s.ChatGPTTurnCache()
	if err != nil {
		return nil, err
	}
	cache, ok := turns.(ChatGPTUploadCache)
	if !ok {
		return nil, errors.New("native Chat upload ownership unavailable")
	}
	return cache, nil
}

func (s ChatGPTTurnScope) uploadKey(kind, value string) string {
	digest := sha256.Sum256([]byte("chatgpt-upload-" + kind + "-v1\x00" + value))
	return fmt.Sprintf("chatgpt:upload:{%s}:%s:%x", s.namespace(), kind, digest)
}

func (s ChatGPTTurnScope) UploadedFileKey(id string) string { return s.uploadKey("file", id) }
func (s ChatGPTTurnScope) UploadURLKey(raw string) string   { return s.uploadKey("url", raw) }
func (s ChatGPTTurnScope) UploadSessionKey(device string) string {
	return s.uploadKey("session", device)
}

func ValidChatGPTUploadFileID(id string) bool {
	return len(id) >= 6 && len(id) <= 256 &&
		(strings.HasPrefix(id, "file_") || strings.HasPrefix(id, "file-")) &&
		ValidChatGPTMetadataValue(id, 256) && !strings.ContainsAny(id, ":/")
}

// Observe only file-creation metadata. The provider's response is forwarded
// unchanged. Estuary capability values are hashed before entering the cache.
func ChatGPTUploadResponseKeys(scope ChatGPTTurnScope, raw []byte) ([]string, error) {
	var metadata struct {
		Status    string `json:"status"`
		FileID    string `json:"file_id"`
		UploadURL string `json:"upload_url"`
	}
	if len(raw) > 64*1024 || json.Unmarshal(raw, &metadata) != nil {
		return nil, errors.New("invalid native Chat upload metadata")
	}
	// The native endpoint can return an error envelope with HTTP 200. Preserve
	// that response without manufacturing an upload identity or account binding.
	if metadata.Status == "error" {
		return nil, nil
	}
	if !ValidChatGPTUploadFileID(metadata.FileID) || len(metadata.UploadURL) == 0 || len(metadata.UploadURL) > 32*1024 {
		return nil, errors.New("invalid native Chat upload metadata")
	}
	u, err := url.Parse(metadata.UploadURL)
	if err != nil || u.User != nil || u.Fragment != "" || u.Host != "" && u.Scheme != "https" {
		return nil, errors.New("invalid native Chat upload URL")
	}
	keys := []string{scope.UploadedFileKey(metadata.FileID)}
	if u.Path == "/backend-api/estuary/upload_content_bytes" || u.Path == "/api/estuary/upload_content_bytes" {
		if u.Host != "" && u.Host != "chatgpt.com" {
			return nil, errors.New("invalid native Chat upload origin")
		}
		values := u.Query()["upload_url"]
		if len(values) != 1 || values[0] == "" || len(values[0]) > 32*1024 {
			return nil, errors.New("invalid native Chat upload capability")
		}
		keys = append(keys, scope.UploadURLKey(values[0]))
	} else if u.Host == "" || u.Scheme != "https" {
		return nil, errors.New("unsupported native Chat upload route")
	}
	return keys, nil
}

// Inspect only the new user's attachment identities. Prompt text and prior
// conversation branches are not scanned or changed.
func ChatGPTRequestUploadFileIDs(raw []byte) ([]string, error) {
	var envelope struct {
		Messages []struct {
			Author struct {
				Role string `json:"role"`
			} `json:"author"`
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
			Metadata struct {
				Attachments []struct {
					ID     string `json:"id"`
					FileID string `json:"file_id"`
				} `json:"attachments"`
			} `json:"metadata"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, errors.New("invalid native Chat attachment envelope")
	}
	if len(envelope.Messages) == 0 {
		return nil, nil
	}
	message := envelope.Messages[len(envelope.Messages)-1]
	if message.Author.Role != "user" {
		return nil, nil
	}
	ids := make([]string, 0)
	seen := make(map[string]bool)
	add := func(id string) error {
		if !ValidChatGPTUploadFileID(id) {
			return errors.New("invalid native Chat attachment identity")
		}
		if !seen[id] {
			if len(ids) >= MaxChatGPTUploadFiles {
				return errors.New("too many native Chat attachments")
			}
			ids = append(ids, id)
			seen[id] = true
		}
		return nil
	}
	for _, part := range message.Content.Parts {
		var image struct {
			Type    string `json:"content_type"`
			Pointer string `json:"asset_pointer"`
		}
		if json.Unmarshal(part, &image) != nil || image.Type != "image_asset_pointer" {
			continue
		}
		id := strings.TrimPrefix(strings.TrimPrefix(image.Pointer, "sediment://"), "file-service://")
		if err := add(id); err != nil {
			return nil, err
		}
	}
	for _, attachment := range message.Metadata.Attachments {
		id := attachment.ID
		if id == "" {
			id = attachment.FileID
		}
		if err := add(id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

type chatGPTMemoryUpload struct {
	accountID int64
	expires   time.Time
}

func (s *chatGPTMemoryTurnCache) GetChatGPTUploadOwner(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	return s.uploads[key].accountID, nil
}
func (s *chatGPTMemoryTurnCache) ClaimChatGPTUploadOwner(_ context.Context, key string, accountID int64, ttl time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	if prior, ok := s.uploads[key]; ok {
		return prior.accountID, nil
	}
	if accountID <= 0 || !ValidChatGPTUploadClaimTTL(key, ttl) || len(s.uploads) >= 4096 {
		return 0, errors.New("invalid native Chat upload claim")
	}
	s.uploads[key] = chatGPTMemoryUpload{accountID, time.Now().Add(ttl)}
	return accountID, nil
}
func (s *chatGPTMemoryTurnCache) BindChatGPTUploadOwner(_ context.Context, keys []string, accountID int64, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	if accountID <= 0 || ttl <= 0 || ttl > ChatGPTUploadTTL || len(keys) == 0 || len(keys) > 4 || len(s.uploads)+len(keys) > 4096 {
		return errors.New("invalid native Chat upload binding")
	}
	for _, key := range keys {
		if prior, ok := s.uploads[key]; ok && prior.accountID != accountID {
			return errors.New("native Chat upload ownership conflict")
		}
	}
	for _, key := range keys {
		if _, ok := s.uploads[key]; !ok {
			s.uploads[key] = chatGPTMemoryUpload{accountID, time.Now().Add(ttl)}
		}
	}
	return nil
}
