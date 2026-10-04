package openai_ws_v2

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRelay_QuotaResponseStartBuffer(testContext *testing.T) {
	created := []byte(`{"type":"response.created","response":{"id":"resp_buffer","status":"in_progress","error":null,"output":[]}}`)
	progress := []byte(`{"type":"response.in_progress","response":{"id":"resp_buffer","output":[]}}`)
	quota := []byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":100,"window_minutes":300}}}`)
	quotaError := []byte(`{"type":"error","error":{"type":"usage_limit_reached"}}`)
	delta := []byte(`{"type":"response.output_text.delta","response_id":"resp_buffer","delta":"visible"}`)
	completion := []byte(`{"type":"response.completed","response":{"id":"resp_buffer","usage":{"input_tokens":1,"output_tokens":1}}}`)
	oversized := []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":"resp_buffer","output":[],"description":"%s"}}`, strings.Repeat("x", 64*1024)))
	tooMany := make([][]byte, 0, 10)
	for frameIndex := 0; frameIndex < 9; frameIndex++ {
		tooMany = append(tooMany, created)
	}
	tooMany = append(tooMany, quotaError)
	scenarios := []struct {
		name       string
		frames     [][]byte
		wantReplay bool
		wantWrites int
	}{
		{name: "created_is_suppressed_on_quota", frames: [][]byte{created, quotaError}, wantReplay: true},
		{name: "progress_is_suppressed_on_quota", frames: [][]byte{created, progress, quotaError}, wantReplay: true},
		{name: "quota_does_not_commit_turn", frames: [][]byte{quota, quotaError}, wantReplay: true, wantWrites: 1},
		{name: "buffered_quota_keeps_order", frames: [][]byte{created, quota, delta, completion}, wantWrites: 4},
		{name: "normal_frames_keep_bytes", frames: [][]byte{created, progress, delta, completion}, wantWrites: 4},
		{name: "text_commits_turn", frames: [][]byte{created, delta, quotaError}, wantWrites: 3},
		{name: "unknown_frame_commits_turn", frames: [][]byte{created, []byte(`{"type":"unknown.provider.event"}`), quotaError}, wantWrites: 3},
		{name: "oversized_start_fails_closed", frames: [][]byte{oversized, quotaError}, wantWrites: 2},
		{name: "frame_count_bound_fails_closed", frames: tooMany, wantWrites: 10},
		{name: "nonempty_start_output_commits", frames: [][]byte{[]byte(`{"type":"response.created","response":{"id":"resp_buffer","output":[{"type":"function_call","name":"tool"}]}}`), quotaError}, wantWrites: 2},
	}
	for _, scenario := range scenarios {
		testContext.Run(scenario.name, func(testContext *testing.T) {
			frames := make([]passthroughTestFrame, 0, len(scenario.frames))
			for _, payload := range scenario.frames {
				frames = append(frames, passthroughTestFrame{msgType: coderws.MessageText, payload: payload})
			}
			client := newPassthroughTestFrameConn(nil, false)
			upstream := newPassthroughTestFrameConn(frames, true)
			replayErr := errors.New("synthetic quota failover")
			result, relayExit := Relay(context.Background(), client, upstream, []byte(`{"type":"response.create","input":[]}`), RelayOptions{
				BufferResponseStart: true,
				BeforeUpstreamFrame: func(frame RelayUpstreamFrame) error {
					if gjson.GetBytes(frame.Payload, "type").String() == "error" && !frame.WroteCurrentTurn {
						return replayErr
					}
					return nil
				},
			})
			if scenario.wantReplay {
				require.NotNil(testContext, relayExit)
				require.ErrorIs(testContext, relayExit.Err, replayErr)
				require.False(testContext, relayExit.WroteCurrentTurn)
			} else {
				require.Nil(testContext, relayExit)
			}
			writes := client.Writes()
			require.Len(testContext, writes, scenario.wantWrites)
			require.Equal(testContext, int64(scenario.wantWrites), result.UpstreamToClientFrames)
			if !scenario.wantReplay {
				for frameIndex, frame := range writes {
					require.Equal(testContext, scenario.frames[frameIndex], frame.payload)
				}
			}
		})
	}
}
