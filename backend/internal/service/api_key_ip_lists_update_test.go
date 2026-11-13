//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyService_UpdateKeepsIPListsWhenOmitted(t *testing.T) {
	stored := &APIKey{
		ID: 4, UserID: 9, Key: "sk-test-ip-lists", Name: "k", Status: StatusActive, MaxConcurrency: 5,
		IPWhitelist: []string{"10.0.0.0/8"}, IPBlacklist: []string{"10.1.2.3"},
	}
	repo := &maxConcurrencyAPIKeyRepoStub{stored: stored}
	svc := &APIKeyService{apiKeyRepo: repo, cfg: &config.Config{}}
	ctx := context.Background()

	// Status-only update (enable/disable) keeps both lists.
	inactive := "inactive" // what the user handler accepts
	_, err := svc.Update(ctx, 4, 9, UpdateAPIKeyRequest{Status: &inactive})
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.0/8"}, repo.updated.IPWhitelist)
	require.Equal(t, []string{"10.1.2.3"}, repo.updated.IPBlacklist)

	// An explicit empty list clears that list only.
	_, err = svc.Update(ctx, 4, 9, UpdateAPIKeyRequest{IPWhitelist: []string{}})
	require.NoError(t, err)
	require.Empty(t, repo.updated.IPWhitelist)
	require.Equal(t, []string{"10.1.2.3"}, repo.updated.IPBlacklist)

	// A provided list replaces the stored one.
	_, err = svc.Update(ctx, 4, 9, UpdateAPIKeyRequest{IPBlacklist: []string{"192.168.0.0/16"}})
	require.NoError(t, err)
	require.Equal(t, []string{"192.168.0.0/16"}, repo.updated.IPBlacklist)
}

// The JSON contract the handler relies on: a missing field or null decodes to
// nil (no change), [] decodes to an empty non-nil slice (clear).
func TestUpdateAPIKeyRequest_IPListJSONSemantics(t *testing.T) {
	var omitted, null, empty UpdateAPIKeyRequest
	require.NoError(t, json.Unmarshal([]byte(`{"status":"active"}`), &omitted))
	require.NoError(t, json.Unmarshal([]byte(`{"ip_whitelist":null}`), &null))
	require.NoError(t, json.Unmarshal([]byte(`{"ip_whitelist":[]}`), &empty))
	require.Nil(t, omitted.IPWhitelist)
	require.Nil(t, null.IPWhitelist)
	require.NotNil(t, empty.IPWhitelist)
	require.Empty(t, empty.IPWhitelist)
}
