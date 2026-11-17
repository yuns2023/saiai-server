package handler

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// AITPInstallCodeHandler serves one-time AITP install codes (see
// service/aitp_install_code_service.go). Responses carry the code or the plaintext key:
// they are never written to logs, and the HTTP trace omits their bodies.
type AITPInstallCodeHandler struct {
	codes *service.AITPInstallCodeService
}

func NewAITPInstallCodeHandler(codes *service.AITPInstallCodeService) *AITPInstallCodeHandler {
	return &AITPInstallCodeHandler{codes: codes}
}

type createAITPInstallCodeRequest struct {
	Shell string `json:"shell"`
}

// aitpInstallCodeRedeemHint answers a GET/HEAD (link previews, browsers): it never consumes.
const aitpInstallCodeRedeemHint = "这是一次性安装命令的地址，不能在浏览器里打开。请在终端里运行网站 Key 页面上显示的完整命令。\n" +
	"This is a one-time install command URL and does not open in a browser. Run the command shown on the Key page in a terminal.\n"

func setAITPInstallCodeNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
}

// Create POST /api/v1/keys/:id/install-code {"shell": "unix"|"powershell"|"cmd"}
func (h *AITPInstallCodeHandler) Create(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || keyID <= 0 {
		response.BadRequest(c, "Invalid key ID")
		return
	}
	var req createAITPInstallCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request")
		return
	}

	setAITPInstallCodeNoStore(c)
	issued, err := h.codes.Create(c.Request.Context(), subject.UserID, keyID, req.Shell)
	shellForLog := "invalid"
	if _, ok := service.ParseAITPInstallShell(req.Shell); ok {
		shellForLog = req.Shell
	}
	audit := logger.FromContext(c.Request.Context()).With(
		zap.String("component", "audit.aitp_install_code"),
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", keyID),
		zap.String("shell", shellForLog),
	)
	if err != nil {
		audit.Info("aitp install code not issued", zap.String("result", "refused"), zap.Error(err))
		response.ErrorFrom(c, err)
		return
	}
	audit.Info("aitp install code issued", zap.String("result", "ok"))
	response.Success(c, issued)
}

// Redeem POST /i/:code (no auth). Always 200 text/plain: the body is piped into a shell,
// so failures are scripts that print a message and fail.
func (h *AITPInstallCodeHandler) Redeem(c *gin.Context) {
	clientIP := ip.GetTrustedClientIP(c)
	result := h.codes.Redeem(c.Request.Context(), c.Param("code"), clientIP, c.GetHeader("User-Agent"))
	logger.FromContext(c.Request.Context()).Info("aitp install code redeem",
		zap.String("component", "audit.aitp_install_code"),
		zap.String("result", string(result.Outcome)),
		zap.String("detail", result.Detail),
		zap.String("shell", string(result.Shell)),
		zap.Int64("user_id", result.UserID),
		zap.Int64("api_key_id", result.APIKeyID),
		zap.String("client_ip", clientIP),
	)
	setAITPInstallCodeNoStore(c)
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(result.Script))
}

// RedeemMethodNotAllowed GET/HEAD /i/:code: never consumes and never returns a key.
func (h *AITPInstallCodeHandler) RedeemMethodNotAllowed(c *gin.Context) {
	c.Header("Allow", http.MethodPost)
	setAITPInstallCodeNoStore(c)
	c.Data(http.StatusMethodNotAllowed, "text/plain; charset=utf-8", []byte(aitpInstallCodeRedeemHint))
}
