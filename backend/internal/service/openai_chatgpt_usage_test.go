package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatGPTConversationStreamObserverParsesChunkedTerminalSignals(t *testing.T) {
	observer := NewChatGPTConversationStreamObserver()
	chunks := []string{
		"event: message\ndata: {\"conversation_id\":\"conv-1\",\"message\":{\"author\":{\"role\":\"assistant\"},",
		"\"metadata\":{\"model_slug\":\"gpt-test\"},\"content\":{\"content_type\":\"text\",\"parts\":[\"discard me\"]}}}\n\n",
		"data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"conv-1\"}\r\n\r\n",
		"data: [DONE]\n\n",
	}
	for _, chunk := range chunks {
		require.NoError(t, observer.Observe([]byte(chunk)))
	}

	summary, err := observer.Finish()
	require.NoError(t, err)
	require.Equal(t, "conv-1", summary.ConversationID)
	require.Equal(t, "gpt-test", summary.ObservedModel)
	require.True(t, summary.CompletionSeen)
	require.True(t, summary.DoneSentinelSeen)
	require.False(t, summary.ProviderErrorSeen)
	require.Nil(t, summary.EventTypes)
	require.Nil(t, summary.TopLevelFields)
	require.Nil(t, summary.MessageMetadataFields)
	require.Nil(t, summary.UsageLikeFieldPaths)
	require.NotContains(t, string(observer.eventData[:cap(observer.eventData)]), "discard me")
	require.NotContains(t, string(observer.lineBuffer[:cap(observer.lineBuffer)]), "discard me")
}

func TestResolveChatGPTTurnBillingIdentityIsRetryStableAndContentSensitive(t *testing.T) {
	first := []byte(`{"conversation_id":"conv-1","client_prepare_state":{"id":"a"},"messages":[{"id":"message-1","author":{"role":"user"},"content":{"parts":["hello"]}}]}`)
	retry := []byte(`{"conversation_id":"conv-1","client_prepare_state":{"id":"b"},"messages":[{"id":"message-1","author":{"role":"user"},"content":{"parts":["hello"]}}]}`)
	changedContent := []byte(`{"conversation_id":"conv-1","messages":[{"id":"message-1","author":{"role":"user"},"content":{"parts":["different"]}}]}`)
	changedConversation := []byte(`{"conversation_id":"conv-2","messages":[{"id":"message-1","author":{"role":"user"},"content":{"parts":["hello"]}}]}`)

	identity := ResolveChatGPTTurnBillingIdentity(first, "fallback-1")
	retryIdentity := ResolveChatGPTTurnBillingIdentity(retry, "fallback-2")
	require.Equal(t, identity, retryIdentity)
	require.Len(t, identity.PayloadHash, 64)
	require.Equal(t, identity.PayloadHash, identity.RequestID)
	require.Len(t, identity.RequestID, 64)
	require.NotContains(t, identity.RequestID, "conv-1")
	require.NotContains(t, identity.RequestID, "message-1")
	require.NotEqual(t, identity, ResolveChatGPTTurnBillingIdentity(changedContent, "fallback"))
	require.NotEqual(t, identity, ResolveChatGPTTurnBillingIdentity(changedConversation, "fallback"))

	fallback := ResolveChatGPTTurnBillingIdentity([]byte(`{"messages":[{"content":"missing id"}]}`), "local:fallback")
	require.Equal(t, "local:fallback", fallback.RequestID)
	require.Len(t, fallback.PayloadHash, 64)
}

func TestChatGPTConversationStreamShapeObserverCapturesNamesOnly(t *testing.T) {
	observer := NewChatGPTConversationStreamShapeObserver()
	stream := "event: delta_encoding\ndata: opaque-encoded-delta\n\n" +
		"event: message\ndata: {\"type\":\"message\",\"conversation_id\":\"conv-1\",\"usage\":{\"input_tokens\":12,\"details\":{\"cached_tokens\":3}},\"message\":{\"author\":{\"role\":\"assistant\"},\"metadata\":{\"model_slug\":\"gpt-test\",\"token_usage\":{\"output_tokens\":4},\"provider_note\":\"must-not-be-captured\"},\"content\":{\"content_type\":\"text\",\"parts\":[{\"secret_token_name\":\"must-not-be-captured\"}]}}}\n\n" +
		"data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"conv-1\"}\n\n" +
		"data: [DONE]\n\n"
	require.NoError(t, observer.Observe([]byte(stream)))

	summary, err := observer.Finish()
	require.NoError(t, err)
	require.Equal(t, []string{"delta_encoding", "message", "message_stream_complete"}, summary.EventTypes)
	require.Equal(t, []string{"conversation_id", "message", "type", "usage"}, summary.TopLevelFields)
	require.Equal(t, []string{"model_slug", "provider_note", "token_usage"}, summary.MessageMetadataFields)
	require.Equal(t, []string{
		"message.metadata.token_usage",
		"message.metadata.token_usage.output_tokens",
		"usage",
		"usage.details",
		"usage.details.cached_tokens",
		"usage.input_tokens",
	}, summary.UsageLikeFieldPaths)
	for _, path := range summary.UsageLikeFieldPaths {
		require.NotContains(t, path, "secret_token_name")
		require.NotContains(t, path, "must-not-be-captured")
	}
}

func TestChatGPTConversationStreamObserverDoesNotTreatDoneAsCompletion(t *testing.T) {
	observer := NewChatGPTConversationStreamObserver()
	require.NoError(t, observer.Observe([]byte("data: {\"conversation_id\":\"conv-partial\"}\n\ndata: [DONE]\n\n")))

	summary, err := observer.Finish()
	require.NoError(t, err)
	require.Equal(t, "conv-partial", summary.ConversationID)
	require.False(t, summary.CompletionSeen)
	require.True(t, summary.DoneSentinelSeen)
}

func TestChatGPTConversationStreamObserverFlagsProviderError(t *testing.T) {
	observer := NewChatGPTConversationStreamObserver()
	require.NoError(t, observer.Observe([]byte("data: {\"error\":{\"code\":\"fixture\"}}\n\n")))

	summary, err := observer.Finish()
	require.NoError(t, err)
	require.True(t, summary.ProviderErrorSeen)
}

func TestParseChatGPTThreadUsageSnapshot(t *testing.T) {
	body := []byte(`{
		"threads": [{
			"thread_id": "conv-usage",
			"estimated_usage_credits_micros": 120000,
			"estimated_usage_usd_micros": 34000,
			"groups": [
				{
					"model": "gpt-test",
					"reasoning_effort": "high",
					"speed": "standard",
					"net_new_input_tokens": 101,
					"cached_input_tokens": 202,
					"output_tokens": 303,
					"estimated_usage_credits_micros": 120000,
					"future_field": true
				}
			]
		}],
		"future_top_level_field": true
	}`)

	snapshot, err := ParseChatGPTThreadUsageSnapshot(body, "conv-usage")
	require.NoError(t, err)
	require.Equal(t, "conv-usage", snapshot.ThreadID)
	require.Equal(t, int64(120000), *snapshot.EstimatedCreditMicros)
	require.Equal(t, int64(34000), *snapshot.EstimatedUSDMicros)
	require.Len(t, snapshot.Groups, 1)
	require.Equal(t, ChatGPTThreadUsageGroup{
		Model:                 "gpt-test",
		ReasoningEffort:       "high",
		Speed:                 "standard",
		NetNewInputTokens:     101,
		CachedInputTokens:     202,
		OutputTokens:          303,
		EstimatedCreditMicros: int64Pointer(120000),
	}, snapshot.Groups[0])
}

func TestParseChatGPTThreadUsageSnapshotPending(t *testing.T) {
	_, err := ParseChatGPTThreadUsageSnapshot([]byte(`{"threads":[]}`), "conv-pending")
	require.ErrorIs(t, err, ErrChatGPTThreadUsagePending)
}

func TestParseChatGPTThreadUsageSnapshotRejectsUnsafeTokenValues(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "missing", field: "\"net_new_input_tokens\":0,", value: ""},
		{name: "negative", field: "\"output_tokens\":0", value: "\"output_tokens\":-1"},
		{name: "fractional", field: "\"cached_input_tokens\":0", value: "\"cached_input_tokens\":0.5"},
		{name: "string", field: "\"net_new_input_tokens\":0", value: "\"net_new_input_tokens\":\"0\""},
		{name: "overflow", field: "\"output_tokens\":0", value: "\"output_tokens\":9223372036854775808"},
	}
	fixture := `{"threads":[{"thread_id":"conv","groups":[{"model":"gpt-test","net_new_input_tokens":0,"cached_input_tokens":0,"output_tokens":0}]}]}`
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fixture
			if tt.value == "" {
				body = replaceOnce(body, tt.field, "")
			} else {
				body = replaceOnce(body, tt.field, tt.value)
			}
			_, err := ParseChatGPTThreadUsageSnapshot([]byte(body), "conv")
			require.Error(t, err)
		})
	}
}

func TestParseChatGPTThreadUsageSnapshotRequiresThreadsArray(t *testing.T) {
	for _, body := range []string{`{}`, `{"threads":null}`, `{"threads":{}}`} {
		_, err := ParseChatGPTThreadUsageSnapshot([]byte(body), "conv")
		require.Error(t, err)
		require.False(t, errors.Is(err, ErrChatGPTThreadUsagePending))
	}
}

func TestParseChatGPTThreadUsageSnapshotRejectsDuplicateThread(t *testing.T) {
	body := []byte(`{"threads":[
		{"thread_id":"conv","groups":[{"model":"gpt-test","net_new_input_tokens":0,"cached_input_tokens":0,"output_tokens":0}]},
		{"thread_id":"conv","groups":[{"model":"gpt-test","net_new_input_tokens":0,"cached_input_tokens":0,"output_tokens":0}]}
	]}`)
	_, err := ParseChatGPTThreadUsageSnapshot(body, "conv")
	require.ErrorContains(t, err, "duplicate thread")
	require.False(t, errors.Is(err, ErrChatGPTThreadUsagePending))
}

func TestDiffChatGPTThreadUsageSnapshots(t *testing.T) {
	previous := &ChatGPTThreadUsageSnapshot{
		ThreadID:              "conv",
		EstimatedCreditMicros: int64Pointer(100),
		EstimatedUSDMicros:    int64Pointer(40),
		Groups: []ChatGPTThreadUsageGroup{{
			Model: "gpt-a", ReasoningEffort: "high", Speed: "standard",
			NetNewInputTokens: 10, CachedInputTokens: 20, OutputTokens: 30,
			EstimatedCreditMicros: int64Pointer(100),
		}},
	}
	current := &ChatGPTThreadUsageSnapshot{
		ThreadID:              "conv",
		EstimatedCreditMicros: int64Pointer(175),
		EstimatedUSDMicros:    int64Pointer(65),
		Groups: []ChatGPTThreadUsageGroup{
			{
				Model: "gpt-a", ReasoningEffort: "high", Speed: "standard",
				NetNewInputTokens: 15, CachedInputTokens: 22, OutputTokens: 37,
				EstimatedCreditMicros: int64Pointer(150),
			},
			{
				Model: "gpt-b", Speed: "fast",
				NetNewInputTokens: 3, CachedInputTokens: 4, OutputTokens: 5,
				EstimatedCreditMicros: int64Pointer(25),
			},
		},
	}

	delta, err := DiffChatGPTThreadUsageSnapshots(previous, current)
	require.NoError(t, err)
	require.Equal(t, int64(75), *delta.EstimatedCreditMicros)
	require.Equal(t, int64(25), *delta.EstimatedUSDMicros)
	require.Equal(t, []ChatGPTThreadUsageGroup{
		{
			Model: "gpt-a", ReasoningEffort: "high", Speed: "standard",
			NetNewInputTokens: 5, CachedInputTokens: 2, OutputTokens: 7,
			EstimatedCreditMicros: int64Pointer(50),
		},
		{
			Model: "gpt-b", Speed: "fast",
			NetNewInputTokens: 3, CachedInputTokens: 4, OutputTokens: 5,
			EstimatedCreditMicros: int64Pointer(25),
		},
	}, delta.Groups)
}

func TestDiffChatGPTThreadUsageSnapshotsRejectsRegression(t *testing.T) {
	previous := &ChatGPTThreadUsageSnapshot{
		ThreadID: "conv",
		Groups: []ChatGPTThreadUsageGroup{{
			Model: "gpt-a", NetNewInputTokens: 10,
		}},
	}
	current := &ChatGPTThreadUsageSnapshot{
		ThreadID: "conv",
		Groups: []ChatGPTThreadUsageGroup{{
			Model: "gpt-a", NetNewInputTokens: 9,
		}},
	}

	_, err := DiffChatGPTThreadUsageSnapshots(previous, current)
	require.ErrorContains(t, err, "regressed")
}

func TestDiffChatGPTThreadUsageSnapshotsRejectsDroppedDimension(t *testing.T) {
	previous := &ChatGPTThreadUsageSnapshot{
		ThreadID: "conv",
		Groups: []ChatGPTThreadUsageGroup{{
			Model: "gpt-a", OutputTokens: 1,
		}},
	}
	current := &ChatGPTThreadUsageSnapshot{ThreadID: "conv"}

	_, err := DiffChatGPTThreadUsageSnapshots(previous, current)
	require.ErrorContains(t, err, "dropped")
}

func int64Pointer(value int64) *int64 {
	return &value
}

func replaceOnce(source, old, replacement string) string {
	for i := 0; i+len(old) <= len(source); i++ {
		if source[i:i+len(old)] == old {
			return source[:i] + replacement + source[i+len(old):]
		}
	}
	return source
}
