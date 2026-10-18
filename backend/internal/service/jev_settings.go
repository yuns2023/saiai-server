package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const JevDefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
const jevSettingsKey = "input_moderation_jev_config"

type JevSettings struct {
	Endpoint      string `json:"endpoint"`
	KeyConfigured bool   `json:"key_configured"`
	Source        string `json:"source"`
	Engine        string `json:"engine"`
	CanSave       bool   `json:"can_save"`
}
type JevSettingsUpdate struct {
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key"`
	ClearKey bool   `json:"clear_key"`
}
type storedJevSettings struct {
	Endpoint     string `json:"endpoint"`
	EncryptedKey string `json:"encrypted_key"`
}
type JevSettingsService struct {
	repo      SettingRepository
	encryptor SecretEncryptor
	cfg       *config.Config
	mu        sync.Mutex
}

func NewJevSettingsService(repo SettingRepository, encryptor SecretEncryptor, cfg *config.Config) *JevSettingsService {
	return &JevSettingsService{repo: repo, encryptor: encryptor, cfg: cfg}
}

// Resolve reads the database for each queued job: settings take effect across replicas
// without a restart. Database or decryption failures never fall back to stale credentials.
func (s *JevSettingsService) Resolve(ctx context.Context) (config.GatewayInputModerationConfig, string, error) {
	var c config.GatewayInputModerationConfig
	if s.cfg != nil {
		c = s.cfg.Gateway.InputModeration
	}
	if c.Engine == "" {
		c.Engine = "jev"
	}
	if c.Endpoint == "" && c.Engine == "jev" {
		c.Endpoint = JevDefaultEndpoint
	}
	raw, err := s.repo.GetValue(ctx, jevSettingsKey)
	if errors.Is(err, ErrSettingNotFound) {
		return c, "environment", nil
	}
	if err != nil {
		return c, "", fmt.Errorf("jev settings unavailable")
	}
	var stored storedJevSettings
	if json.Unmarshal([]byte(raw), &stored) != nil {
		return c, "", fmt.Errorf("invalid Jev settings")
	}
	c.Engine = "jev"
	c.Endpoint = stored.Endpoint
	c.APIKey = ""
	if stored.EncryptedKey != "" {
		if s.encryptor == nil {
			return c, "", fmt.Errorf("jev key decryption unavailable")
		}
		c.APIKey, err = s.encryptor.Decrypt(stored.EncryptedKey)
		if err != nil {
			return config.GatewayInputModerationConfig{}, "", fmt.Errorf("jev key decryption failed")
		}
	}
	return c, "admin", nil
}
func (s *JevSettingsService) Get(ctx context.Context) (*JevSettings, error) {
	c, source, err := s.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return &JevSettings{Endpoint: c.Endpoint, KeyConfigured: strings.TrimSpace(c.APIKey) != "", Source: source, Engine: c.Engine, CanSave: s.cfg != nil && s.cfg.Totp.EncryptionKeyConfigured && s.encryptor != nil}, nil
}
func (s *JevSettingsService) Update(ctx context.Context, u JevSettingsUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg == nil || !s.cfg.Totp.EncryptionKeyConfigured || s.encryptor == nil {
		return fmt.Errorf("a stable encryption key is required")
	}
	u.Endpoint = strings.TrimSpace(u.Endpoint)
	u.APIKey = strings.TrimSpace(u.APIKey)
	parsed, err := url.Parse(u.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || len(u.Endpoint) > 2048 {
		return fmt.Errorf("endpoint must be an HTTPS URL without credentials, query or fragment")
	}
	if len(u.APIKey) > 4096 || strings.ContainsAny(u.APIKey, "\r\n") || (u.ClearKey && u.APIKey != "") {
		return fmt.Errorf("invalid key update")
	}
	key := u.APIKey
	if !u.ClearKey && key == "" {
		current, _, resolveErr := s.Resolve(ctx)
		if resolveErr != nil {
			return resolveErr
		}
		key = current.APIKey
		if u.Endpoint != current.Endpoint && key != "" {
			return fmt.Errorf("changing the endpoint requires a new key or explicit key removal")
		}
	}
	// Explicit replacement/removal also repairs an unreadable old ciphertext.

	stored := storedJevSettings{Endpoint: u.Endpoint}
	if key != "" {
		stored.EncryptedKey, err = s.encryptor.Encrypt(key)
		if err != nil {
			return fmt.Errorf("jev key encryption failed")
		}
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode Jev settings")
	}
	if err = s.repo.Set(ctx, jevSettingsKey, string(raw)); err != nil {
		return fmt.Errorf("save Jev settings failed")
	}
	return nil
}
