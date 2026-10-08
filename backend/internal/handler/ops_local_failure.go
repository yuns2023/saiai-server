package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

const opsLocalFailureKey = "ops_local_failure"

// A committed HTTP 101 or an SSE keepalive is not a successful model turn.
// Keep the terminal failure separately for monitoring without changing the
// response status, upstream payload, or WebSocket frames seen by the client.
type opsLocalFailure struct {
	status      int
	errType     string
	code        string
	message     string
	wsCloseCode int
}

func setOpsLocalFailure(c *gin.Context, failure opsLocalFailure) {
	if c == nil || failure.status < 400 {
		return
	}
	if _, exists := c.Get(opsLocalFailureKey); exists {
		return
	}
	failure.message = service.ClientSafeUpstreamErrorMessage(failure.message)
	c.Set(opsLocalFailureKey, failure)
}

func opsFailureResponse(c *gin.Context, wireStatus int, body []byte) (int, []byte, bool) {
	v, exists := c.Get(opsLocalFailureKey)
	failure, valid := v.(opsLocalFailure)
	if !exists || !valid || failure.status < 400 || wireStatus >= 400 {
		return wireStatus, body, false
	}
	errorBody := gin.H{"type": failure.errType, "message": failure.message}
	if failure.code != "" {
		errorBody["code"] = failure.code
	}
	payload := gin.H{"error": errorBody, "transport_status_code": wireStatus}
	if failure.wsCloseCode != 0 {
		payload["websocket_close_code"] = failure.wsCloseCode
	}
	encoded, _ := json.Marshal(payload)
	return failure.status, encoded, true
}

func closeOpenAIClientWSWithOps(c *gin.Context, conn *coderws.Conn, status coderws.StatusCode, reason string) {
	failure := opsLocalFailure{status: http.StatusInternalServerError, errType: "api_error", message: reason, wsCloseCode: int(status)}
	switch status {
	case coderws.StatusPolicyViolation:
		failure.status, failure.errType = http.StatusBadRequest, "invalid_request_error"
	case coderws.StatusTryAgainLater:
		failure.status, failure.errType = http.StatusServiceUnavailable, "api_error"
		switch {
		case strings.Contains(reason, "no available account"):
			failure.code = "no_available_account"
		case strings.Contains(reason, "account is busy"):
			failure.status, failure.errType, failure.code = http.StatusTooManyRequests, "rate_limit_error", "account_concurrency"
		case strings.Contains(reason, "too many concurrent requests"):
			failure.status, failure.errType, failure.code = http.StatusTooManyRequests, "rate_limit_error", "user_concurrency"
		}
	}
	if !opsWSHasCurrentUpstreamFailure(c) {
		setOpsLocalFailure(c, failure)
	}
	closeOpenAIClientWS(conn, status, reason)
}
