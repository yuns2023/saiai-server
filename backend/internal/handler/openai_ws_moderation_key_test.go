package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSModerationKeyUsesCurrentGroup(t *testing.T) {
	connect := &service.Group{ID: 7}
	key := &service.APIKey{ID: 1, Group: connect}
	current := &service.Group{ID: 7, InputModerationEnabled: true, InputModerationPolicy: &service.InputModerationPolicy{InputScope: service.InputModerationScopeFull}}
	got := openAIWSModerationKey(key, current)
	require.Same(t, current, got.Group)
	require.Same(t, connect, key.Group, "connect-time key is not mutated")
	require.True(t, service.InputModerationIncludesInstructions(got.Group))
	require.Same(t, key, openAIWSModerationKey(key, &service.Group{ID: 8}), "a different group is never substituted")
	require.Same(t, key, openAIWSModerationKey(key, nil))
}
