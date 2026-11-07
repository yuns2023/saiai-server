package handler

import (
	"errors"
	"net/http"

	pkgopenai "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// rewriteChatGPTTimezone applies the group's China timezone guard to a native
// Chat body. ChatGPT web clients cannot turn the browser zone off, so both
// guard actions rewrite here instead of rejecting. ok=false means a response
// was written. The body is never logged.
func (h *OpenAIGatewayHandler) rewriteChatGPTTimezone(c *gin.Context, group *service.Group, body []byte) ([]byte, bool) {
	if group.CodexTimezoneGuardAction() == "" {
		return body, true
	}
	out, changed, err := pkgopenai.RewriteChatGPTTimezone(body, codexTimezoneNow())
	switch {
	case errors.Is(err, pkgopenai.ErrChatGPTTimezoneAmbiguous):
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "ChatGPT body repeats a timezone field")
		return nil, false
	case err != nil:
		requestLogger(c, "handler.openai_gateway.chatgpt_timezone",
			zap.Int64("group_id", group.ID), zap.Bool("clock_unavailable", errors.Is(err, pkgopenai.ErrCodexRewriteClock)),
			zap.Error(err)).Warn("openai.chatgpt_timezone_rewrite_failed")
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Timezone rewrite is unavailable; retry later")
		return nil, false
	}
	if changed {
		requestLogger(c, "handler.openai_gateway.chatgpt_timezone",
			zap.Int64("group_id", group.ID), zap.String("path", c.Request.URL.Path)).Info("openai.chatgpt_timezone_rewritten")
	}
	return out, true
}
