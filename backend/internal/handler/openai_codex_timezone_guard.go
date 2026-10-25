package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	pkgopenai "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The Codex CN-timezone guard (groups.codex_cn_timezone_guard) rejects
// Responses HTTP and WebSocket requests whose JSON string content carries a
// China mainland / Hong Kong / Macau `<timezone>` environment tag. It only
// reads the decoded request body; forwarded bytes are never modified.
// Out of scope: native /codex/images and ChatGPT conversation routes.
const (
	codexTimezoneGuardCode     = "codex_timezone_reinit_required"
	codexTimezoneGuardErrType  = "permission_error"
	codexTimezoneGuardWSReason = "codex timezone reinit required"

	codexTimezoneGuardMessageGeneric = "请求内容或历史中包含中国地区时区信息。请重新运行 saiai cli 初始化（会关闭 Codex 的时区上报），重启 Codex 并新开会话后再使用。"
	codexTimezoneGuardMessageOld     = "当前 Codex 版本过旧，会上报本机时区。请将 Codex 升级到 0.163 或以上，重新运行 saiai cli 初始化，重启 Codex 并新开会话后再使用。"
)

// codexTimezoneGuardMinVersion is the first Codex release that supports
// include_environment_context_time = false.
var codexTimezoneGuardMinVersion = [3]int{0, 163, 0}

// codexTimezoneGuardProducts are the Codex backend User-Agent products whose
// version is the codex-rs version that renders environment_context.
var codexTimezoneGuardProducts = map[string]struct{}{
	"codex-tui":     {},
	"codex_exec":    {},
	"codex_vscode":  {},
	"codex_cli_rs":  {},
	"codex desktop": {},
}

// codexTimezoneGuardClient extracts the recognised Codex product and version
// from a User-Agent; ok is false for anything else.
func codexTimezoneGuardClient(userAgent string) (product string, version [3]int, ok bool) {
	userAgent = strings.TrimSpace(userAgent)
	name, _, found := strings.Cut(userAgent, "/")
	if !found {
		return "", version, false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if _, known := codexTimezoneGuardProducts[name]; !known {
		return "", version, false
	}
	client, parsed := tlsfingerprint.ParseCodexUserAgent(userAgent)
	if !parsed {
		return "", version, false
	}
	return name, client.Version, true
}

// codexTimezoneGuardMessage selects the old-version text only for a
// recognised Codex backend version below 0.163; everything else gets the
// generic text.
func codexTimezoneGuardMessage(userAgent string) string {
	if _, version, ok := codexTimezoneGuardClient(userAgent); ok {
		for i := range version {
			if version[i] != codexTimezoneGuardMinVersion[i] {
				if version[i] < codexTimezoneGuardMinVersion[i] {
					return codexTimezoneGuardMessageOld
				}
				break
			}
		}
	}
	return codexTimezoneGuardMessageGeneric
}

// codexTimezoneGuardZone returns the offending zone when group enables the
// guard and payload carries a China-region timezone tag.
func codexTimezoneGuardZone(group *service.Group, payload []byte) (string, bool) {
	if group == nil || !group.CodexCNTimezoneGuard {
		return "", false
	}
	return pkgopenai.FindChinaTimezone(payload)
}

// logCodexTimezoneGuardRejection never logs the request body.
func logCodexTimezoneGuardRejection(c *gin.Context, group *service.Group, userID int64, zone, transport string, turn int) {
	product, version, _ := codexTimezoneGuardClient(c.GetHeader("User-Agent"))
	fields := []zap.Field{
		zap.Int64("user_id", userID),
		zap.String("zone", zone),
		zap.String("ua_product", product),
		zap.String("transport", transport),
		zap.Int("turn", turn),
	}
	if group != nil {
		fields = append(fields, zap.Int64("group_id", group.ID))
	}
	if product != "" {
		fields = append(fields, zap.Ints("ua_version", version[:]))
	}
	requestLogger(c, "handler.openai_gateway.codex_timezone_guard", fields...).Warn("openai.codex_timezone_guard_rejected")
}

// validateCodexTimezoneGuardHTTP writes a 403 and returns false when the
// guard rejects body. A rejection ends the request; it is not an account
// failure and never advances smart switching.
func (h *OpenAIGatewayHandler) validateCodexTimezoneGuardHTTP(c *gin.Context, group *service.Group, userID int64, body []byte) bool {
	zone, found := codexTimezoneGuardZone(group, body)
	if !found {
		return true
	}
	logCodexTimezoneGuardRejection(c, group, userID, zone, "http", 0)
	h.writeError(c, gatewayErrorEnvelope{
		Status:  http.StatusForbidden,
		Type:    codexTimezoneGuardErrType,
		Code:    codexTimezoneGuardCode,
		Message: codexTimezoneGuardMessage(c.GetHeader("User-Agent")),
	}, false)
	return false
}

// openAIWSSessionRejection marks a WebSocket session ended by a gateway
// content/identity check (not an account failure). The Codex-visible error
// frame has already been attempted; the handler records ops and closes 1008.
type openAIWSSessionRejection struct {
	status  int
	errType string
	code    string
	message string
	reason  string
}

func (e *openAIWSSessionRejection) Error() string {
	return "openai websocket session rejected: " + e.code
}

func (e *openAIWSSessionRejection) frame() []byte {
	frame, _ := json.Marshal(map[string]any{
		"type":   "error",
		"status": e.status,
		"error": map[string]any{
			"type":    e.errType,
			"code":    e.code,
			"message": e.message,
		},
	})
	return frame
}

// rejectOpenAIWSSession writes the error frame immediately (coder/websocket
// serializes whole-message writes, so it cannot interleave with a relayed
// frame) and returns an error that ends the session with close code 1008.
// Delivery is best effort; a failed write is logged without the payload.
// The relay keeps draining usage of turns that were already forwarded.
func rejectOpenAIWSSession(ctx context.Context, c *gin.Context, conn *coderws.Conn, rejection *openAIWSSessionRejection) error {
	if conn != nil {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := conn.Write(writeCtx, coderws.MessageText, rejection.frame())
		cancel()
		if err != nil {
			requestLogger(c, "handler.openai_gateway.ws_session_rejection",
				zap.String("code", rejection.code),
				zap.Error(err),
			).Warn("openai.websocket_rejection_frame_write_failed")
		}
	}
	return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, rejection.reason, rejection)
}

func codexTimezoneGuardRejection(message string) *openAIWSSessionRejection {
	return &openAIWSSessionRejection{
		status:  http.StatusForbidden,
		errType: codexTimezoneGuardErrType,
		code:    codexTimezoneGuardCode,
		message: message,
		reason:  codexTimezoneGuardWSReason,
	}
}

// openAIWSGroupChangedRejection ends a session whose key was rebound to
// another group after the connection opened: the session's accounts and
// settings belong to the old group, so the client must reconnect.
func openAIWSGroupChangedRejection() *openAIWSSessionRejection {
	return &openAIWSSessionRejection{
		status:  http.StatusConflict,
		errType: "invalid_request_error",
		code:    "api_key_group_changed",
		message: "API Key 分组已变更，请重新连接。",
		reason:  "api key group changed",
	}
}

// checkCodexTimezoneGuardWS scans one WebSocket payload and rejects the
// session when the guard matches.
func checkCodexTimezoneGuardWS(ctx context.Context, c *gin.Context, conn *coderws.Conn, group *service.Group, userID int64, turn int, payload []byte) error {
	zone, found := codexTimezoneGuardZone(group, payload)
	if !found {
		return nil
	}
	logCodexTimezoneGuardRejection(c, group, userID, zone, "ws", turn)
	return rejectOpenAIWSSession(ctx, c, conn, codexTimezoneGuardRejection(codexTimezoneGuardMessage(c.GetHeader("User-Agent"))))
}

// closeOpenAIWSRejectedSession records the status + code for ops and closes
// 1008 when err carries a session rejection; it reports whether it did.
func closeOpenAIWSRejectedSession(c *gin.Context, conn *coderws.Conn, err error) bool {
	var rejection *openAIWSSessionRejection
	if !errors.As(err, &rejection) {
		return false
	}
	if c != nil && !opsWSHasCurrentUpstreamFailure(c) {
		setOpsLocalFailure(c, opsLocalFailure{
			status:      rejection.status,
			errType:     rejection.errType,
			code:        rejection.code,
			message:     rejection.message,
			wsCloseCode: int(coderws.StatusPolicyViolation),
		})
	}
	closeOpenAIClientWS(conn, coderws.StatusPolicyViolation, rejection.reason)
	return true
}
