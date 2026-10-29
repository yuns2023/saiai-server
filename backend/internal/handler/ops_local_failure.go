package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Our own read limit, or a SAIAI local proxy rejecting the frame with 1009
	// before forwarding it. Codex then retries and falls back to HTTPS. This is
	// more specific than a deadline that expired while the close was sent.
	case errors.Is(err, coderws.ErrMessageTooBig), coderws.CloseStatus(err) == coderws.StatusMessageTooBig:
		return openAIWSFirstMessageTooBig
	case timedOut:
		return openAIWSFirstMessageTimedOut
	// The request context is canceled when net/http notices the client's
	// connection is gone, which can win the race against the EOF.
	case coderws.CloseStatus(err) != -1,
		errors.Is(err, context.Canceled),
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
	waitMS := waited.Milliseconds()
	failure.wsFirstFrameWaitMS = &waitMS
	if reason == openAIWSFirstMessageTooBig {
		// coder already tried to send 1009 (on our read limit, or in reply to
		// the peer's). A second Close would wait for the handshake behind the
		// rest of an oversized frame; delivery stays best effort.
		failure.wsCloseCode = int(coderws.StatusMessageTooBig)
		if !opsWSHasCurrentUpstreamFailure(c) {
			setOpsLocalFailure(c, failure)
		}
		if conn != nil {
			_ = conn.CloseNow()
		}
		return
	}
	recordAndCloseOpenAIClientWS(c, conn, coderws.StatusPolicyViolation, failure)
}

// The provider ended the session with a close frame (for example 1012 when
// its server restarts): a provider failure, not a gateway one. A restart or
// try-again-later reason is passed on so the client sees why; anything else
// stays 1011.
func openAIWSUpstreamCloseFailure(upstreamStatus coderws.StatusCode) (opsLocalFailure, bool) {
	if upstreamStatus == -1 {
		return opsLocalFailure{}, false
	}
	clientStatus := coderws.StatusInternalError
	switch upstreamStatus {
	case coderws.StatusServiceRestart, coderws.StatusTryAgainLater:
		clientStatus = upstreamStatus
	}
	return opsLocalFailure{
		status:      http.StatusBadGateway,
		errType:     "upstream_error",
		message:     fmt.Sprintf("upstream websocket closed with %d (%s)", int(upstreamStatus), upstreamStatus),
		wsCloseCode: int(clientStatus),
	}, true
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

// rejectOpenAIWSBilling ends a session that failed the billing check with the
// same status, code and message as the HTTP error. Codex shows the message,
// and stops after two attempts on 429 instead of reconnecting five times and
// falling back to HTTPS, which a bare close frame triggers.
func rejectOpenAIWSBilling(ctx context.Context, c *gin.Context, conn *coderws.Conn, err error) {
	status, code, message := billingErrorDetails(err)
	closeStatus := coderws.StatusPolicyViolation
	if status == http.StatusServiceUnavailable {
		closeStatus = coderws.StatusTryAgainLater
	}
	_ = rejectOpenAIWSSession(ctx, c, conn, &openAIWSSessionRejection{status: status, errType: code, code: code, message: message})
	recordAndCloseOpenAIClientWS(c, conn, closeStatus, opsLocalFailure{status: status, errType: code, code: code, message: message, wsCloseCode: int(closeStatus)})
}

func recordAndCloseOpenAIClientWS(c *gin.Context, conn *coderws.Conn, status coderws.StatusCode, failure opsLocalFailure) {
	if !opsWSHasCurrentUpstreamFailure(c) {
		setOpsLocalFailure(c, failure)
	}
	closeOpenAIClientWS(conn, status, failure.message)
}
