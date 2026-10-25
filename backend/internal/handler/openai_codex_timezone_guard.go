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

// codexTimezoneGuardWSError marks a WebSocket session ended by the guard. The
// error frame has already been sent; the handler only closes with 1008.
type codexTimezoneGuardWSError struct {
	message string
}

func (e *codexTimezoneGuardWSError) Error() string {
	return "codex timezone guard rejected the turn"
}

func codexTimezoneGuardWSErrorFrame(message string) []byte {
	frame, _ := json.Marshal(map[string]any{
		"type":   "error",
		"status": http.StatusForbidden,
		"error": map[string]any{
			"type":    codexTimezoneGuardErrType,
			"code":    codexTimezoneGuardCode,
			"message": message,
		},
	})
	return frame
}

// checkCodexTimezoneGuardWS scans one WebSocket payload. On rejection it
// writes the Codex-visible error frame immediately (coder/websocket
// serializes whole-message writes, so it cannot interleave with a relayed
// frame) and returns an error that ends the session with close code 1008.
// The relay keeps draining usage of turns that were already forwarded.
func checkCodexTimezoneGuardWS(ctx context.Context, c *gin.Context, conn *coderws.Conn, group *service.Group, userID int64, turn int, payload []byte) error {
	zone, found := codexTimezoneGuardZone(group, payload)
	if !found {
		return nil
	}
	logCodexTimezoneGuardRejection(c, group, userID, zone, "ws", turn)
	message := codexTimezoneGuardMessage(c.GetHeader("User-Agent"))
	if conn != nil {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = conn.Write(writeCtx, coderws.MessageText, codexTimezoneGuardWSErrorFrame(message))
		cancel()
	}
	return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, codexTimezoneGuardWSReason, &codexTimezoneGuardWSError{message: message})
}

// closeCodexTimezoneGuardWS records the 403 + code for ops and closes 1008.
func closeCodexTimezoneGuardWS(c *gin.Context, conn *coderws.Conn, guardErr *codexTimezoneGuardWSError) {
	if !opsWSHasCurrentUpstreamFailure(c) {
		setOpsLocalFailure(c, opsLocalFailure{
			status:      http.StatusForbidden,
			errType:     codexTimezoneGuardErrType,
			code:        codexTimezoneGuardCode,
			message:     guardErr.message,
			wsCloseCode: int(coderws.StatusPolicyViolation),
		})
	}
	closeOpenAIClientWS(conn, coderws.StatusPolicyViolation, codexTimezoneGuardWSReason)
}

// closeCodexTimezoneGuardWSFromError closes the session when err carries a
// guard rejection and reports whether it did.
func closeCodexTimezoneGuardWSFromError(c *gin.Context, conn *coderws.Conn, err error) bool {
	var guardErr *codexTimezoneGuardWSError
	if !errors.As(err, &guardErr) {
		return false
	}
	closeCodexTimezoneGuardWS(c, conn, guardErr)
	return true
}
