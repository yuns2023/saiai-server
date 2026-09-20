package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSPassthroughReplayTrackerBuildsCrossAccountReplay(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	tracker := newOpenAIWSPassthroughReplayTracker(store, 77)

	tracker.RegisterTurn(
		1,
		coderws.MessageText,
		[]byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`),
	)
	tracker.ObserveUpstreamFrame([]byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp_turn_1",
			"status":"completed",
			"output":[
				{"type":"reasoning","encrypted_content":"account-a-only","summary":[{"type":"summary_text","text":"safe summary"}]},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}
			]
		}
	}`))

	stored, ok := store.GetResponseReplayForUser(77, "resp_turn_1")
	require.True(t, ok)
	require.Len(t, stored, 3)
	require.False(t, gjson.GetBytes(stored[1], "encrypted_content").Exists())
	require.Equal(t, "safe summary", gjson.GetBytes(stored[1], "summary.0.text").String())

	tracker.RegisterTurn(
		2,
		coderws.MessageText,
		[]byte(`{"type":"response.create","model":"gpt-5.3-codex","previous_response_id":"resp_turn_1","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`),
	)
	failoverErr, ok := tracker.BuildFailoverError(11, nil)
	require.True(t, ok)
	require.Equal(t, int64(11), failoverErr.AccountID())
	require.Equal(t, 2, failoverErr.Turn())

	replayed := failoverErr.ReplayPayload()
	require.False(t, gjson.GetBytes(replayed, "previous_response_id").Exists())
	require.Len(t, gjson.GetBytes(replayed, "input").Array(), 4)
	require.Equal(t, "message", gjson.GetBytes(replayed, "input.0.type").String())
	require.Equal(t, "reasoning", gjson.GetBytes(replayed, "input.1.type").String())
	require.False(t, gjson.GetBytes(replayed, "input.1.encrypted_content").Exists())
	require.Equal(t, "message", gjson.GetBytes(replayed, "input.2.type").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(replayed, "input.3.type").String())
}

func TestOpenAIWSPassthroughReplayTrackerFailsClosedWithoutPriorReplay(t *testing.T) {
	tracker := newOpenAIWSPassthroughReplayTracker(NewOpenAIWSStateStore(nil), 77)
	tracker.RegisterTurn(
		1,
		coderws.MessageText,
		[]byte(`{"type":"response.create","model":"gpt-5.3-codex","previous_response_id":"resp_unknown","input":[{"type":"input_text","text":"continue"}]}`),
	)
	_, ok := tracker.BuildFailoverError(11, nil)
	require.False(t, ok)
}

func TestOpenAIWSPassthroughReplayTrackerFailsClosedForAmbiguousPipelinedTurns(t *testing.T) {
	tracker := newOpenAIWSPassthroughReplayTracker(NewOpenAIWSStateStore(nil), 77)
	tracker.RegisterTurn(
		1,
		coderws.MessageText,
		[]byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[{"type":"input_text","text":"first"}]}`),
	)
	tracker.RegisterTurn(
		2,
		coderws.MessageText,
		[]byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[{"type":"input_text","text":"second"}]}`),
	)

	_, ok := tracker.BuildFailoverError(11, nil)
	require.False(t, ok)
}

func TestBuildOpenAIWSFailoverPayloadRejectsProviderItemReference(t *testing.T) {
	payload := []byte(`{"type":"response.create","model":"gpt-5.3-codex","previous_response_id":"resp_owner"}`)
	fullInput := mustOpenAIWSRawMessages(t, `[{"type":"item_reference","id":"item_account_a"}]`)

	updated, ok, err := buildOpenAIWSFailoverPayload(payload, fullInput, true)
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, updated)
}

func TestPrepareOpenAIWSContinuationFailoverPayloadRequiresRateLimitedOwner(t *testing.T) {
	ctx := context.Background()
	resetAt := time.Now().Add(time.Hour)
	owner := Account{
		ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, RateLimitResetAt: &resetAt,
	}
	store := NewOpenAIWSStateStore(nil)
	require.True(t, store.BindResponseReplayForUser(
		77,
		"resp_owner",
		mustOpenAIWSRawMessages(t, `[{"type":"input_text","text":"earlier"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]`),
		time.Hour,
	))
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{owner}},
		openaiWSStateStore: store,
	}

	payload, ok, err := svc.PrepareOpenAIWSContinuationFailoverPayload(
		ctx,
		77,
		owner.ID,
		"resp_owner",
		[]byte(`{"type":"response.create","model":"gpt-5.3-codex","previous_response_id":"resp_owner","input":[{"type":"input_text","text":"next"}]}`),
	)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, gjson.GetBytes(payload, "previous_response_id").Exists())
	require.Len(t, gjson.GetBytes(payload, "input").Array(), 3)

	owner.RateLimitResetAt = nil
	svc.accountRepo = stubOpenAIAccountRepo{accounts: []Account{owner}}
	_, ok, err = svc.PrepareOpenAIWSContinuationFailoverPayload(
		ctx,
		77,
		owner.ID,
		"resp_owner",
		[]byte(`{"type":"response.create","model":"gpt-5.3-codex","previous_response_id":"resp_owner","input":[]}`),
	)
	require.NoError(t, err)
	require.False(t, ok)
}

func mustOpenAIWSRawMessages(t *testing.T, raw string) []json.RawMessage {
	t.Helper()
	var messages []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &messages))
	return messages
}
