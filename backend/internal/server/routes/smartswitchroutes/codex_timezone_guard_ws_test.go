//go:build unit

package smartswitchroutes_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// wsUpstream answers every response.create with a completed response and
// counts the frames it received.
type wsUpstream struct {
	frames chan string
}

func (u *wsUpstream) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := coderws.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	for i := 1; ; i++ {
		_, payload, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		u.frames <- string(payload)
		resp := `{"type":"response.completed","response":{"id":"resp_ws_` + string(rune('0'+i)) + `","object":"response","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`
		if err := conn.Write(r.Context(), coderws.MessageText, []byte(resp)); err != nil {
			return
		}
	}
}

func newGuardWSLab(t *testing.T) (*smartRoutesLab, *wsUpstream, *httptest.Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	up := &wsUpstream{frames: make(chan string, 8)}
	upstream := httptest.NewServer(http.HandlerFunc(up.serve))
	t.Cleanup(upstream.Close)
	lab := newSmartRoutesLab(upstream.URL)
	lab.mu.Lock()
	lab.key.SmartSwitch = nil
	for group, accounts := range lab.accounts {
		for i := range accounts {
			accounts[i].Extra = map[string]any{"responses_websockets_v2_enabled": true}
		}
		lab.accounts[group] = accounts
	}
	lab.mu.Unlock()
	gateway := newSmartRoutesGateway(t, lab, func(cfg *config.Config) {
		cfg.Gateway.OpenAIWS.Enabled = true
		cfg.Gateway.OpenAIWS.APIKeyEnabled = true
		cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
		cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
		cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
		cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
		cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
		cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
		cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	})
	return lab, up, gateway
}

func dialGuardWS(t *testing.T, gateway *httptest.Server) (*coderws.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+smartTestKey)
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn, ctx
}

func wsTurn(t *testing.T, ctx context.Context, conn *coderws.Conn, frame string) []byte {
	t.Helper()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(frame)))
	_, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	return payload
}

const guardWSCleanFrame = `{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`

func TestCodexTimezoneGuardWSLaterTurnUsesCurrentSwitch(t *testing.T) {
	lab, up, gateway := newGuardWSLab(t)
	conn, ctx := dialGuardWS(t, gateway)

	first := wsTurn(t, ctx, conn, guardWSCleanFrame)
	require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String(), string(first))
	<-up.frames

	// The administrator enables the guard while the session is open.
	lab.mu.Lock()
	lab.groups[smartSubscriptionGroup].CodexCNTimezoneGuard = true
	lab.mu.Unlock()
	tagged := `{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<timezone>Asia/Shanghai</timezone>"}]}]}`
	frame := wsTurn(t, ctx, conn, tagged)
	require.Equal(t, "error", gjson.GetBytes(frame, "type").String(), string(frame))
	require.Equal(t, int64(403), gjson.GetBytes(frame, "status").Int())
	require.Equal(t, "codex_timezone_reinit_required", gjson.GetBytes(frame, "error.code").String())
	_, _, err := conn.Read(ctx)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
	require.Len(t, up.frames, 0, "the tagged turn is never forwarded")
}

func TestOpenAIWSKeyRebindEndsSession(t *testing.T) {
	lab, up, gateway := newGuardWSLab(t)
	conn, ctx := dialGuardWS(t, gateway)

	first := wsTurn(t, ctx, conn, guardWSCleanFrame)
	require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String(), string(first))
	<-up.frames

	// Rebind the key from group A (guard off) to group B mid-session.
	lab.mu.Lock()
	rebound := smartPaygGroup
	lab.key.GroupID = &rebound
	lab.mu.Unlock()
	frame := wsTurn(t, ctx, conn, guardWSCleanFrame)
	require.Equal(t, "error", gjson.GetBytes(frame, "type").String(), string(frame))
	require.Equal(t, "api_key_group_changed", gjson.GetBytes(frame, "error.code").String())
	_, _, err := conn.Read(ctx)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
	require.Len(t, up.frames, 0, "no turn is forwarded after the rebind")
}
