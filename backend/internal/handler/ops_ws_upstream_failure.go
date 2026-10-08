package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/gin-gonic/gin"
)

const opsWSRecorderKey = "ops_ws_failure_recorder"

// Entries are queued per failed turn while the socket is still open. Only the
// latest turn's outcome suppresses a redundant connection-close 500; a later
// turn starts with a clean outcome and retains its own model/account identity.
type opsWSFailureRecorder struct {
	mu     sync.Mutex
	ops    *service.OpsService
	turn   int
	failed bool
}

func opsWSRecorder(c *gin.Context) *opsWSFailureRecorder {
	if c == nil {
		return nil
	}
	value, _ := c.Get(opsWSRecorderKey)
	recorder, _ := value.(*opsWSFailureRecorder)
	return recorder
}

func opsWSStartTurn(c *gin.Context, turn int) {
	if recorder := opsWSRecorder(c); recorder != nil {
		recorder.mu.Lock()
		if turn >= recorder.turn {
			recorder.turn, recorder.failed = turn, false
		}
		recorder.mu.Unlock()
	}
}

func opsWSSuccessfulTurn(c *gin.Context, turn int) {
	if recorder := opsWSRecorder(c); recorder != nil {
		recorder.mu.Lock()
		if turn == recorder.turn {
			recorder.failed = false
		}
		recorder.mu.Unlock()
	}
}

func opsWSHasCurrentUpstreamFailure(c *gin.Context) bool {
	recorder := opsWSRecorder(c)
	if recorder == nil {
		return false
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.failed
}

func recordOpenAIWSUpstreamFailure(c *gin.Context, failure *service.OpenAIWSUpstreamFailure) {
	recorder := opsWSRecorder(c)
	if recorder == nil || failure == nil {
		return
	}
	recorder.mu.Lock()
	if failure.Turn == recorder.turn {
		recorder.failed = true
	}
	recorder.mu.Unlock()
	if recorder.ops == nil || !recorder.ops.IsMonitoringEnabled(c.Request.Context()) {
		return
	}
	status := failure.Status
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	message := truncateString(service.ClientSafeUpstreamErrorMessage(logredact.RedactText(failure.Message)), 2048)
	errType := normalizeOpsErrorType(opsWSErrorIdentifier(failure.ErrorType), opsWSErrorIdentifier(failure.Code))
	payload := gin.H{
		"error":                 gin.H{"type": errType, "code": opsWSErrorIdentifier(failure.Code), "message": message},
		"transport_status_code": http.StatusSwitchingProtocols,
		"websocket_turn":        failure.Turn, "websocket_event_type": failure.EventType,
	}
	if failure.Status >= 400 && failure.Status <= 599 {
		payload["upstream_status_code"] = failure.Status
	}
	if failure.ResponseID != "" {
		payload["response_id"] = truncateString(logredact.RedactText(failure.ResponseID), 128)
	}
	body, _ := json.Marshal(payload)
	requestID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
	clientRequestID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
	entry := &service.OpsInsertErrorLogInput{
		RequestID: requestID, ClientRequestID: clientRequestID, Platform: service.PlatformOpenAI,
		Model: failure.Model, RequestPath: c.Request.URL.Path, Stream: true, UserAgent: c.GetHeader("User-Agent"),
		ErrorPhase: "upstream", ErrorType: errType, Severity: classifyOpsSeverity(errType, status),
		StatusCode: status, ErrorMessage: message, ErrorBody: string(body), ErrorOwner: "provider", ErrorSource: "upstream_ws",
		IsRetryable: classifyOpsIsRetryable(errType, status), CreatedAt: time.Now(), UpstreamErrorMessage: &message,
		UpstreamErrorDetail: func() *string { detail := string(body); return &detail }(),
		UpstreamErrors: []*service.OpsUpstreamErrorEvent{{
			Platform: service.PlatformOpenAI, AccountID: failure.AccountID, UpstreamStatusCode: failure.Status,
			Kind: "websocket_error", Message: message, Detail: string(body),
		}},
	}
	if failure.AccountID > 0 {
		accountID := failure.AccountID
		entry.AccountID = &accountID
	}
	if failure.Status >= 400 && failure.Status <= 599 {
		upstreamStatus := failure.Status
		entry.UpstreamStatusCode = &upstreamStatus
	}
	if apiKey, _ := middleware.GetAPIKeyFromContext(c); apiKey != nil {
		entry.APIKeyID, entry.GroupID = &apiKey.ID, apiKey.GroupID
		if apiKey.User != nil {
			entry.UserID = &apiKey.User.ID
		}
	}
	if value := strings.TrimSpace(ip.GetClientIP(c)); value != "" {
		entry.ClientIP = &value
	}
	if recorder.ops.FilterOpsLogEvents(c.Request.Context(), []service.OpsLogFilterEvent{{
		Source: "upstream", Platform: entry.Platform, GroupID: entry.GroupID, StatusCode: status, Message: message,
	}}) {
		return
	}
	entry.RequestHeadersJSON = extractOpsRequestHeadersForLog(c, recorder.ops)
	enqueueOpsErrorLog(recorder.ops, entry)
}

func opsWSErrorIdentifier(value string) string {
	value = strings.TrimSpace(service.ClientSafeUpstreamErrorMessage(logredact.RedactText(value)))
	if len(value) > 128 {
		return ""
	}
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '_', char == '-', char == '.':
		default:
			return ""
		}
	}
	return value
}
