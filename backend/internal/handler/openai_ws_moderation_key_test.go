package handler

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSModerationKeyUsesCurrentScopeAndConnectSwitch(t *testing.T) {
	full := &service.InputModerationPolicy{InputScope: service.InputModerationScopeFull}
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "req")
	for _, tc := range []struct{ connectOn, currentOn bool }{{true, false}, {false, true}, {true, true}} {
		connect := &service.Group{ID: 7, InputModerationEnabled: tc.connectOn}
		key := &service.APIKey{ID: 1, Group: connect}
		current := &service.Group{ID: 7, InputModerationEnabled: tc.currentOn, InputModerationPolicy: full}
		got := openAIWSModerationKey(key, current)
		require.True(t, service.InputModerationIncludesInstructions(got.Group), "scope follows the current group")
		require.Equal(t, tc.connectOn, got.Group.InputModerationEnabled, "switch stays connect-time")
		// Submission and the usage moderation_ref (AfterTurn, connect-time key) agree.
		submitted := (&service.InputModerationService{}).EnabledForGroup(got.Group)
		ref := service.CaptureInputModerationUsageRef(ctx, key, service.InputModerationSourceOpenAIResponsesWS, 2)
		require.Equal(t, submitted, ref != nil)
		require.Nil(t, connect.InputModerationPolicy, "connect-time group is not mutated")
	}
	key := &service.APIKey{ID: 1, Group: &service.Group{ID: 7}}
	require.Same(t, key, openAIWSModerationKey(key, &service.Group{ID: 8, InputModerationPolicy: full}), "a different group is never substituted")
	require.Same(t, key, openAIWSModerationKey(key, nil))
}
