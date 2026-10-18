package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
)

type jevTestEncryptor struct{}

func (jevTestEncryptor) Encrypt(s string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(s)), nil
}
func (jevTestEncryptor) Decrypt(s string) (string, error) {
	b, e := base64.StdEncoding.DecodeString(s)
	return string(b), e
}
func TestJevSettingsRedactionPreservationAndLiveUpdates(t *testing.T) {
	ctx := context.Background()
	repo := &chatGPTBillingSettingsRepo{values: map[string]string{}}
	cfg := &config.Config{}
	cfg.Totp.EncryptionKeyConfigured = true
	cfg.Gateway.InputModeration.APIKey = "TEST_ONLY_ENV_KEY"
	svc := NewJevSettingsService(repo, jevTestEncryptor{}, cfg)
	view, err := svc.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, JevDefaultEndpoint, view.Endpoint)
	require.True(t, view.KeyConfigured)
	require.Equal(t, "environment", view.Source)
	require.NoError(t, svc.Update(ctx, JevSettingsUpdate{Endpoint: JevDefaultEndpoint}))
	require.NotContains(t, repo.values[jevSettingsKey], "TEST_ONLY_ENV_KEY")
	resolved, source, err := svc.Resolve(ctx)
	require.NoError(t, err)
	require.Equal(t, "admin", source)
	require.Equal(t, "TEST_ONLY_ENV_KEY", resolved.APIKey)
	require.Error(t, svc.Update(ctx, JevSettingsUpdate{Endpoint: "https://replacement.example/v1"}))
	require.NoError(t, svc.Update(ctx, JevSettingsUpdate{Endpoint: "https://replacement.example/v1", APIKey: "TEST_ONLY_NEW_KEY"}))
	resolved, _, err = svc.Resolve(ctx)
	require.NoError(t, err)
	require.Equal(t, "TEST_ONLY_NEW_KEY", resolved.APIKey)
	view, err = svc.Get(ctx)
	require.NoError(t, err)
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "TEST_ONLY")
	require.NotContains(t, string(raw), "encrypted_key")
	require.NoError(t, svc.Update(ctx, JevSettingsUpdate{Endpoint: view.Endpoint, ClearKey: true}))
	resolved, _, err = svc.Resolve(ctx)
	require.NoError(t, err)
	require.Empty(t, resolved.APIKey) // no env fallback after explicit removal
	repo.readErr = errors.New("TEST_ONLY_SECRET")
	_, _, err = svc.Resolve(ctx)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "TEST_ONLY")
}
func TestJevSettingsRejectsInvalidAndUnstableStorage(t *testing.T) {
	cfg := &config.Config{}
	repo := &chatGPTBillingSettingsRepo{values: map[string]string{}}
	svc := NewJevSettingsService(repo, jevTestEncryptor{}, cfg)
	require.Error(t, svc.Update(context.Background(), JevSettingsUpdate{Endpoint: JevDefaultEndpoint, APIKey: "TEST_ONLY"}))
	require.Empty(t, repo.values)
	cfg.Totp.EncryptionKeyConfigured = true
	for _, endpoint := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com?key=secret", "https://example.com#secret", "https://example.com?"} {
		require.Error(t, svc.Update(context.Background(), JevSettingsUpdate{Endpoint: endpoint, APIKey: "TEST_ONLY"}))
	}
	require.Empty(t, repo.values)
	repo.values[jevSettingsKey] = `{"endpoint":"https://example.com","encrypted_key":"invalid!"}`
	_, _, err := svc.Resolve(context.Background())
	require.Error(t, err)
	require.NoError(t, svc.Update(context.Background(), JevSettingsUpdate{Endpoint: JevDefaultEndpoint, APIKey: "TEST_ONLY_REPLACEMENT"}))
	resolved, _, err := svc.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "TEST_ONLY_REPLACEMENT", resolved.APIKey)
}
