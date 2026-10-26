package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	pkgopenai "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The Codex CN-timezone guard (groups.codex_cn_timezone_guard) applies to
// Responses HTTP and WebSocket requests. With codex_timezone_action "reject"
// it rejects JSON string content carrying a China mainland / Hong Kong /
// Macau `<timezone>` environment tag and never modifies forwarded bytes. With
// "rewrite" every Codex `<environment_context>` block (a whole string, or a
// well-formed block embedded in a larger string such as tool output) is sent
// upstream with the timezone set to America/Los_Angeles and the date to the
// item's (or the server's current) LA date (see
// pkgopenai.RewriteCodexEnvironmentTime); a whole-string block that cannot be
// rewritten safely, or a CN zone still present in the bytes to be sent (leak
// safety net), is rejected like the reject mode (fail closed). Rejections log
// where the offending string is (path, offsets, tag names; never content).
// Rejections are HTTP 400 invalid_request_error (Codex does not retry 400;
// it retries 403). Out of scope: native /codex/images and ChatGPT
// conversation routes.
const (
	codexTimezoneGuardCode     = "codex_timezone_reinit_required"
	codexTimezoneGuardErrType  = "invalid_request_error"
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

// codexTimezoneNow is the server clock for the rewrite date (tests stub it).
var codexTimezoneNow = time.Now

// codexTimezoneValidationNow is a placeholder clock for checks that only
// need to know whether a payload can be rewritten; the output is discarded.
var codexTimezoneValidationNow = time.Unix(0, 0)

// Rejection reasons logged by the guard.
const (
	codexTimezoneRejectZone      = "cn_timezone"
	codexTimezoneRejectMalformed = "malformed_environment_context"
	codexTimezoneRejectNoClock   = "rewrite_date_unavailable"
	codexTimezoneRejectEncode    = "rewrite_encode_failed"
	// A CN zone is still present in the bytes that would be sent after the
	// rewrite (e.g. an environment block in a shape the rewrite does not
	// recognise, or a pasted tag): fail closed like reject mode.
	codexTimezoneRejectAfterRewrite = "cn_timezone_after_rewrite"
)

// codexTimezoneRejection is why a payload is rejected. zone is set when a CN
// zone matched; location (when located) says where, without content.
type codexTimezoneRejection struct {
	reason   string
	zone     string
	location pkgopenai.CodexTimezoneLocation
	located  bool
}

// codexTimezoneZoneRejection scans payload like reject mode and returns a
// located rejection with reason when a CN zone is present.
func codexTimezoneZoneRejection(payload []byte, reason string) *codexTimezoneRejection {
	zone, location, found := pkgopenai.LocateChinaTimezone(payload)
	if !found {
		return nil
	}
	return &codexTimezoneRejection{reason: reason, zone: zone, location: location, located: true}
}

// codexTimezoneGuardZone returns why payload must be rejected under group's
// guard (nil: accepted): a China-region zone in reject mode; in rewrite mode
// an environment_context block that cannot be rewritten safely or a CN zone
// left after the rewrite.
func codexTimezoneGuardZone(group *service.Group, payload []byte) *codexTimezoneRejection {
	switch group.CodexTimezoneGuardAction() {
	case service.CodexTimezoneActionReject:
		return codexTimezoneZoneRejection(payload, codexTimezoneRejectZone)
	case service.CodexTimezoneActionRewrite:
		_, _, rejection := rewriteCodexTimezonePayload(group, payload, codexTimezoneValidationNow)
		return rejection
	}
	return nil
}

// codexTimezoneFrozenDate freezes the rewrite clock once per HTTP request
// (R2): every check, smart-switch re-application and retry of the request
// uses the same instant, hence the same date. Not safe for concurrent use.
type codexTimezoneFrozenDate struct {
	now  time.Time
	done bool
}

func (d *codexTimezoneFrozenDate) get() time.Time {
	if !d.done {
		d.now, d.done = codexTimezoneNow(), true
	}
	return d.now
}

// codexTimezoneTurnDates freezes one rewrite clock per WebSocket client turn;
// retries and failover replays of a turn reuse it. Frames that are not a
// turn (turn <= 0) use the current time. Old turns are pruned.
type codexTimezoneTurnDates struct {
	mu    sync.Mutex
	dates map[int]time.Time
}

const codexTimezoneTurnDatesKept = 64

func (d *codexTimezoneTurnDates) get(turn int) time.Time {
	if turn <= 0 {
		return codexTimezoneNow()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if now, ok := d.dates[turn]; ok {
		return now
	}
	now := codexTimezoneNow()
	if d.dates == nil {
		d.dates = make(map[int]time.Time)
	}
	d.dates[turn] = now
	for old := range d.dates {
		if old <= turn-codexTimezoneTurnDatesKept {
			delete(d.dates, old)
		}
	}
	return now
}

func codexTimezoneRewriteRejection(payload []byte, err error) *codexTimezoneRejection {
	if errors.Is(err, pkgopenai.ErrCodexRewriteClock) {
		return &codexTimezoneRejection{reason: codexTimezoneRejectNoClock}
	}
	rejection := &codexTimezoneRejection{reason: codexTimezoneRejectMalformed}
	if offset, ok := pkgopenai.CodexEnvironmentMalformedOffset(err); ok {
		rejection.location, rejection.located = pkgopenai.LocateCodexJSONString(payload, offset)
	}
	return rejection
}

// rewriteCodexTimezonePayload applies the rewrite action to payload with the
// frozen clock now. It returns payload unchanged unless group's guard is on
// with action rewrite. rejection is set when the payload must be rejected
// instead (fail closed).
// The rewritten bytes are then scanned like reject mode (leak safety net):
// a remaining CN zone rejects with reason codexTimezoneRejectAfterRewrite,
// located in the rewritten bytes (same paths as the original).
func rewriteCodexTimezonePayload(group *service.Group, payload []byte, now time.Time) (out []byte, stats pkgopenai.CodexEnvironmentRewriteStats, rejection *codexTimezoneRejection) {
	if group.CodexTimezoneGuardAction() != service.CodexTimezoneActionRewrite {
		return payload, stats, nil
	}
	out, stats, err := pkgopenai.RewriteCodexEnvironmentTime(payload, now)
	if err != nil {
		return payload, stats, codexTimezoneRewriteRejection(payload, err)
	}
	if rejection := codexTimezoneZoneRejection(out, codexTimezoneRejectAfterRewrite); rejection != nil {
		return payload, stats, rejection
	}
	return out, stats, nil
}

// logCodexTimezoneRewrite never logs the request body.
func logCodexTimezoneRewrite(c *gin.Context, group *service.Group, userID int64, transport string, turn int, stats pkgopenai.CodexEnvironmentRewriteStats) {
	fields := []zap.Field{
		zap.Int64("user_id", userID),
		zap.String("transport", transport),
		zap.Int("turn", turn),
		zap.Int("blocks", stats.Blocks),
		zap.Int("timezones_rewritten", stats.Timezones),
		zap.Int("dates_rewritten", stats.Dates),
		zap.Int("embedded_blocks", stats.Embedded),
		zap.Int("embedded_skipped", stats.EmbeddedSkipped),
	}
	if group != nil {
		fields = append(fields, zap.Int64("group_id", group.ID))
	}
	requestLogger(c, "handler.openai_gateway.codex_timezone_guard", fields...).Info("openai.codex_timezone_rewritten")
}

// logCodexTimezoneGuardRejection never logs the request body: the location
// fields are paths, offsets, counts and tag names only.
func logCodexTimezoneGuardRejection(c *gin.Context, group *service.Group, userID int64, rejection *codexTimezoneRejection, transport string, turn int) {
	product, version, _ := codexTimezoneGuardClient(c.GetHeader("User-Agent"))
	fields := []zap.Field{
		zap.Int64("user_id", userID),
		zap.String("reason", rejection.reason),
		zap.String("zone", rejection.zone),
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
	if loc := rejection.location; rejection.located {
		fields = append(fields,
			zap.String("loc_path", loc.Path),
			zap.Bool("loc_in_key", loc.InKey),
			zap.String("loc_item_type", loc.ItemType),
			zap.String("loc_item_role", loc.ItemRole),
			zap.Int("loc_string_bytes", loc.StringBytes),
			zap.Int("loc_tag_offset", loc.TagOffset),
			zap.Bool("loc_starts_with_env_open", loc.StartsWithEnvOpen),
			zap.Int("loc_env_open_count", loc.EnvOpenCount),
			zap.Int("loc_env_close_count", loc.EnvCloseCount),
			zap.Int("loc_env_open_offset", loc.EnvOpenOffset),
			zap.Int("loc_env_close_offset", loc.EnvCloseOffset),
			zap.Strings("loc_nearby_tags", loc.NearbyTags),
		)
	}
	requestLogger(c, "handler.openai_gateway.codex_timezone_guard", fields...).Warn("openai.codex_timezone_guard_rejected")
}

// applyCodexTimezoneGuardHTTP returns the decoded body to send for group:
// original itself, or a rewritten copy in rewrite mode. It is always applied
// to the original decoded body, so a smart switch from a rewrite group to a
// reject or guard-off group re-evaluates the client's bytes. On rejection it
// writes a 400 (an SSE error event when the stream has already started) and
// returns ok=false. A rejection ends the request; it is not an account
// failure and never advances smart switching.
func (h *OpenAIGatewayHandler) applyCodexTimezoneGuardHTTP(c *gin.Context, group *service.Group, userID int64, original []byte, date *codexTimezoneFrozenDate, streamStarted bool) ([]byte, bool) {
	var rejection *codexTimezoneRejection
	body := original
	if group.CodexTimezoneGuardAction() == service.CodexTimezoneActionRewrite {
		var stats pkgopenai.CodexEnvironmentRewriteStats
		body, stats, rejection = rewriteCodexTimezonePayload(group, original, date.get())
		if rejection == nil && stats.Changed() {
			logCodexTimezoneRewrite(c, group, userID, "http", 0, stats)
		}
	} else {
		rejection = codexTimezoneGuardZone(group, original)
	}
	if rejection == nil {
		return body, true
	}
	h.rejectCodexTimezoneHTTP(c, group, userID, rejection, streamStarted)
	return nil, false
}

func (h *OpenAIGatewayHandler) rejectCodexTimezoneHTTP(c *gin.Context, group *service.Group, userID int64, rejection *codexTimezoneRejection, streamStarted bool) {
	logCodexTimezoneGuardRejection(c, group, userID, rejection, "http", 0)
	h.writeError(c, gatewayErrorEnvelope{
		Status:  http.StatusBadRequest,
		Type:    codexTimezoneGuardErrType,
		Code:    codexTimezoneGuardCode,
		Message: codexTimezoneGuardMessage(c.GetHeader("User-Agent")),
	}, streamStarted)
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
		status:  http.StatusBadRequest,
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

// checkCodexTimezoneGuardWS checks one WebSocket payload at a checkpoint
// and rejects the session when the guard matches (reject mode) or the payload
// cannot be rewritten (rewrite mode). It never modifies payload; rewriting
// happens where frames are written upstream (rewriteCodexTimezoneWS).
func checkCodexTimezoneGuardWS(ctx context.Context, c *gin.Context, conn *coderws.Conn, group *service.Group, userID int64, turn int, payload []byte) error {
	rejection := codexTimezoneGuardZone(group, payload)
	if rejection == nil {
		return nil
	}
	return rejectCodexTimezoneWS(ctx, c, conn, group, userID, rejection, turn)
}

func rejectCodexTimezoneWS(ctx context.Context, c *gin.Context, conn *coderws.Conn, group *service.Group, userID int64, rejection *codexTimezoneRejection, turn int) error {
	logCodexTimezoneGuardRejection(c, group, userID, rejection, "ws", turn)
	return rejectOpenAIWSSession(ctx, c, conn, codexTimezoneGuardRejection(codexTimezoneGuardMessage(c.GetHeader("User-Agent"))))
}

// rewriteCodexTimezoneWS is applied to every client frame right before it is
// written upstream (first frame, later turns, pool-path rebuilt turns and
// failover replays), so retained frames may stay original. group is the
// session's current group; dates freezes one date per turn.
func rewriteCodexTimezoneWS(ctx context.Context, c *gin.Context, conn *coderws.Conn, group *service.Group, userID int64, turn int, payload []byte, dates *codexTimezoneTurnDates) ([]byte, error) {
	if group.CodexTimezoneGuardAction() != service.CodexTimezoneActionRewrite {
		return payload, nil
	}
	out, stats, rejection := rewriteCodexTimezonePayload(group, payload, dates.get(turn))
	if rejection != nil {
		return nil, rejectCodexTimezoneWS(ctx, c, conn, group, userID, rejection, turn)
	}
	if stats.Changed() {
		logCodexTimezoneRewrite(c, group, userID, "ws", turn, stats)
	}
	return out, nil
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
