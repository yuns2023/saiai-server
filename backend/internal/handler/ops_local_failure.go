package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

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
	// Time from the upgrade until the first frame read failed.
	wsFirstFrameWaitMS *int64
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
	if failure.wsFirstFrameWaitMS != nil {
		payload["websocket_first_frame_wait_ms"] = *failure.wsFirstFrameWaitMS
	}
	encoded, _ := json.Marshal(payload)
	return failure.status, encoded, true
}

func closeOpenAIClientWSWithOps(c *gin.Context, conn *coderws.Conn, status coderws.StatusCode, reason string) {
	recordAndCloseOpenAIClientWS(c, conn, status, openAIClientWSOpsFailure(status, reason))
}

const (
	openAIWSFirstMessageClientClosed = "client closed websocket before sending response.create"
	openAIWSFirstMessageTimedOut     = "timed out waiting for first response.create message"
	openAIWSFirstMessageMissing      = "missing first response.create message"
	openAIWSFirstMessageTooBig       = "response.create message exceeds the 16 MiB websocket limit"
)

// The first frame never arrived. A client that drops the connection right
// after the upgrade and a silent client that hits the read timeout are
// different incidents, so name which one ended the session. timedOut must
// come from the first-message read context itself: the library returns the
// same DeadlineExceeded for its own control-frame timeout.
func openAIWSFirstReadFailureReason(err error, timedOut bool) string {
	switch {
	case timedOut:
		return openAIWSFirstMessageTimedOut
	// Our own read limit, or a SAIAI local proxy rejecting the frame with 1009
	// before forwarding it. Codex then retries and falls back to HTTPS.
	case errors.Is(err, coderws.ErrMessageTooBig), coderws.CloseStatus(err) == coderws.StatusMessageTooBig:
		return openAIWSFirstMessageTooBig
	case coderws.CloseStatus(err) != -1,
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, net.ErrClosed),
		errors.Is(err, syscall.ECONNRESET):
		return openAIWSFirstMessageClientClosed
	}
	return openAIWSFirstMessageMissing
}

func closeOpenAIClientWSFirstReadFailure(c *gin.Context, conn *coderws.Conn, err error, timedOut bool, waited time.Duration) {
	reason := openAIWSFirstReadFailureReason(err, timedOut)
	failure := openAIClientWSOpsFailure(coderws.StatusPolicyViolation, reason)
	closeStatus := coderws.StatusPolicyViolation
	if reason == openAIWSFirstMessageTooBig {
		closeStatus = coderws.StatusMessageTooBig
		failure.wsCloseCode = int(closeStatus)
	}
	waitMS := waited.Milliseconds()
	failure.wsFirstFrameWaitMS = &waitMS
	recordAndCloseOpenAIClientWS(c, conn, closeStatus, failure)
}

func openAIClientWSOpsFailure(status coderws.StatusCode, reason string) opsLocalFailure {
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
	return failure
}

func recordAndCloseOpenAIClientWS(c *gin.Context, conn *coderws.Conn, status coderws.StatusCode, failure opsLocalFailure) {
	if !opsWSHasCurrentUpstreamFailure(c) {
		setOpsLocalFailure(c, failure)
	}
	closeOpenAIClientWS(conn, status, failure.message)
}
