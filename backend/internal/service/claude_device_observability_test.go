package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeDeviceObservabilityAuthSnapshot(t *testing.T) {
	svc := &APIKeyService{}
	key := &APIKey{ID: 2, UserID: 1, Name: "work laptop", Key: "test-secret-do-not-record", User: &User{ID: 1, Username: "alice", Email: "alice@example.com"}}
	snapshot := svc.snapshotFromAPIKey(key)
	payload, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(payload), key.Key)
	var restored APIKeyAuthSnapshot
	require.NoError(t, json.Unmarshal(payload, &restored))
	cachedKey := svc.snapshotToAPIKey(key.Key, &restored)
	owner := claudeDeviceOwnerFromAPIKey(cachedKey)
	require.Equal(t, &ClaudeDeviceOwner{UserID: 1, Username: "alice", Email: "alice@example.com", APIKeyID: 2, APIKeyName: "work laptop"}, owner)
	payload, err = json.Marshal(owner)
	require.NoError(t, err)
	require.NotContains(t, string(payload), key.Key)
	require.Nil(t, claudeDeviceOwnerFromAPIKey(nil))
}

func TestClaudeDeviceObservabilityIngressHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("User-Agent", "claude-cli/2.1.296 (external, cli)")
	headers.Set("X-Stainless-Os", "Windows")
	headers.Set("X-Stainless-Arch", "x64")
	headers.Set("X-Stainless-Runtime", "node")
	headers.Set("X-Stainless-Runtime-Version", "v26.3.0")
	headers.Set("X-Stainless-Package-Version", "0.128.0")
	headers.Set("Authorization", "Bearer test-secret-do-not-record")
	hints := ExtractClientHints(headers)
	require.Equal(t, ClientHints{UserAgent: "claude-cli/2.1.296 (external, cli)", OS: "Windows", Arch: "x64", Runtime: "node", RuntimeVersion: "v26.3.0", SDKVersion: "0.128.0"}, hints)
	groupID := int64(3)
	repo := &claudeDeviceRepoStub{result: &ClaudeDeviceRegistrationResult{Allowed: true}}
	err := NewClaudeDeviceService(repo).CheckAndRegister(context.Background(), &APIKey{ID: 2, UserID: 1, GroupID: &groupID, Group: &Group{ClaudeDeviceLimitMode: "enforce"}}, &ParsedRequest{MetadataUserID: FormatMetadataUserID(strings.Repeat("a", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.296")}, hints)
	require.NoError(t, err)
	require.Equal(t, hints, repo.hints)
}
