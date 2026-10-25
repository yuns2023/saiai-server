package handler

import (
	"context"
	"net/http"

	pkgopenai "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The Codex version gate (settings codex_version_gate_enabled /
// codex_min_version / codex_max_version) admits official Codex clients only
// when their engine version is inside the configured range. It applies to
// Responses HTTP (all subpaths, including /responses/compact) before account
// selection and to the Responses WebSocket upgrade before Accept; WebSocket
// sessions are not re-checked because the version is fixed per connection.
// Non-Codex clients are unaffected. The engine version must be stated
// identically by the `version` header and the User-Agent; anything else is
// unknown and rejected while the gate is on (this includes custom provider
// header overrides of `version`), even with both bounds empty.
const codexVersionUnsupportedCode = "codex_version_unsupported"

type codexVersionGateReader interface {
	GetCodexVersionGate(ctx context.Context) service.CodexVersionGate
}

// codexVersionGateRejection returns the 400 envelope for a request outside
// the gate, or nil when the request is admitted.
func (h *OpenAIGatewayHandler) codexVersionGateRejection(c *gin.Context, transport string) *gatewayErrorEnvelope {
	if h == nil || h.codexVersionGate == nil || c == nil || c.Request == nil {
		return nil
	}
	gate := h.codexVersionGate.GetCodexVersionGate(c.Request.Context())
	if !gate.Enabled {
		return nil
	}
	product, version, isCodex, known := pkgopenai.CodexEngineVersion(c.GetHeader("User-Agent"), c.GetHeader("originator"), c.GetHeader("version"))
	if !isCodex {
		return nil
	}
	minVersion, minOK := pkgopenai.ParseCodexSemver(gate.Min)
	maxVersion, maxOK := pkgopenai.ParseCodexSemver(gate.Max)
	shown, target := "unknown", ""
	switch {
	case !known:
		target = gate.Min
		if target == "" {
			target = gate.Max
		}
		if target == "" {
			target = "latest"
		}
	case minOK && version.Compare(minVersion) < 0:
		shown, target = version.String(), gate.Min
	case maxOK && version.Compare(maxVersion) > 0:
		shown, target = version.String(), gate.Max
	default:
		return nil
	}
	requestLogger(c, "handler.openai_gateway.codex_version_gate",
		zap.String("product", product),
		zap.String("version", shown),
		zap.String("min", gate.Min),
		zap.String("max", gate.Max),
		zap.String("transport", transport),
	).Warn("openai.codex_version_gate_rejected")
	return &gatewayErrorEnvelope{
		Status:  http.StatusBadRequest,
		Type:    "invalid_request_error",
		Code:    codexVersionUnsupportedCode,
		Message: codexVersionGateMessage(shown, gate.Min, gate.Max, target),
	}
}

func codexVersionGateMessage(version, minVersion, maxVersion, target string) string {
	bound := func(v string) string {
		if v == "" {
			return "不限"
		}
		return v
	}
	if version == "unknown" {
		version = "未知（需 User-Agent 与 version 请求头一致）"
	}
	return "当前 Codex 引擎版本 " + version + " 不在允许范围 [" + bound(minVersion) + ", " + bound(maxVersion) + "]。" +
		"请安装 " + target + "：npm i -g @openai/codex@" + target +
		"；若使用后台进程再执行 codex app-server daemon update --from-cli -y，然后重启 Codex。Desktop/VS Code 请等待管理员放行。"
}

// validateCodexVersionGateHTTP writes the 400 and returns false when the
// request is outside the Codex version gate.
func (h *OpenAIGatewayHandler) validateCodexVersionGateHTTP(c *gin.Context) bool {
	envelope := h.codexVersionGateRejection(c, "http")
	if envelope == nil {
		return true
	}
	h.writeError(c, *envelope, false)
	return false
}
