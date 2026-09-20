package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type openAIWSClientFrameConn struct {
	conn *coderws.Conn
}

type openAIWSResumableClientFrame struct {
	messageType coderws.MessageType
	payload     []byte
	err         error
}

// openAIWSResumableClientFrameConn owns the single physical downstream reader
// for the lifetime of the ingress connection. Per-upstream relay cancellation
// only cancels a channel wait; it never cancels coder/websocket.Read and thus
// cannot accidentally close the downstream connection during account failover.
type openAIWSResumableClientFrameConn struct {
	conn    *coderws.Conn
	cancel  context.CancelFunc
	frames  chan openAIWSResumableClientFrame
	writeMu sync.Mutex
}

const openaiWSV2PassthroughModeFields = "ws_mode=passthrough ws_router=v2"

var _ openaiwsv2.FrameConn = (*openAIWSClientFrameConn)(nil)
var _ openaiwsv2.FrameConn = (*openAIWSResumableClientFrameConn)(nil)

func newOpenAIWSResumableClientFrameConn(ctx context.Context, conn *coderws.Conn) *openAIWSResumableClientFrameConn {
	readCtx, cancel := context.WithCancel(ctx)
	result := &openAIWSResumableClientFrameConn{
		conn:   conn,
		cancel: cancel,
		frames: make(chan openAIWSResumableClientFrame, 4),
	}
	go result.readLoop(readCtx)
	return result
}

func (c *openAIWSResumableClientFrameConn) readLoop(ctx context.Context) {
	defer close(c.frames)
	for {
		messageType, payload, err := c.conn.Read(ctx)
		frame := openAIWSResumableClientFrame{
			messageType: messageType,
			payload:     cloneOpenAIWSPayloadBytes(payload),
			err:         err,
		}
		select {
		case c.frames <- frame:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (c *openAIWSResumableClientFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.conn == nil {
		return coderws.MessageText, nil, errOpenAIWSConnClosed
	}
	select {
	case <-ctx.Done():
		return coderws.MessageText, nil, ctx.Err()
	case frame, ok := <-c.frames:
		if !ok {
			return coderws.MessageText, nil, errOpenAIWSConnClosed
		}
		return frame.messageType, frame.payload, frame.err
	}
}

func (c *openAIWSResumableClientFrameConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	if c == nil || c.conn == nil {
		return errOpenAIWSConnClosed
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(ctx, msgType, payload)
}

// Close is intentionally a no-op. The ingress handler owns the downstream
// socket; an individual upstream relay must not close it while failover is in
// progress.
func (c *openAIWSResumableClientFrameConn) Close() error {
	return nil
}

func (c *openAIWSResumableClientFrameConn) Stop() {
	if c != nil && c.cancel != nil {
		c.cancel()
	}
}

func openAIWSOptionalString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func (c *openAIWSClientFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.conn == nil {
		return coderws.MessageText, nil, errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.conn.Read(ctx)
}

func (c *openAIWSClientFrameConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	if c == nil || c.conn == nil {
		return errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.conn.Write(ctx, msgType, payload)
}

func (c *openAIWSClientFrameConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	_ = c.conn.Close(coderws.StatusNormalClosure, "")
	_ = c.conn.CloseNow()
	return nil
}

func (s *OpenAIGatewayService) proxyResponsesWebSocketV2Passthrough(
	ctx context.Context,
	c *gin.Context,
	clientConn *coderws.Conn,
	account *Account,
	token string,
	firstClientMessageType coderws.MessageType,
	firstClientMessage []byte,
	hooks *OpenAIWSIngressHooks,
	wsDecision OpenAIWSProtocolDecision,
	resumableClient *openAIWSResumableClientFrameConn,
) error {
	if s == nil {
		return errors.New("service is nil")
	}
	if clientConn == nil {
		return errors.New("client websocket is nil")
	}
	if account == nil {
		return errors.New("account is nil")
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("token is empty")
	}
	requestModel := strings.TrimSpace(gjson.GetBytes(firstClientMessage, "model").String())
	requestPreviousResponseID := strings.TrimSpace(gjson.GetBytes(firstClientMessage, "previous_response_id").String())
	stateStore := s.getOpenAIWSStateStore()
	userID := getOpenAIUserIDFromContext(c)
	var replayTracker *openAIWSPassthroughReplayTracker
	if account.Type == AccountTypeOAuth && !account.IsOpenAICodexNativeRelay() && hooks != nil && hooks.OnAccountExhausted != nil {
		replayTracker = newOpenAIWSPassthroughReplayTracker(stateStore, userID)
	}
	logOpenAIWSV2Passthrough(
		"relay_start account_id=%d model=%s previous_response_id=%s first_message_type=%s first_message_bytes=%d",
		account.ID,
		truncateOpenAIWSLogValue(requestModel, openAIWSLogValueMaxLen),
		truncateOpenAIWSLogValue(requestPreviousResponseID, openAIWSIDValueMaxLen),
		openaiwsv2RelayMessageTypeName(firstClientMessageType),
		len(firstClientMessage),
	)

	wsURL, err := s.buildOpenAIResponsesWSURL(account)
	if err != nil {
		return fmt.Errorf("build ws url: %w", err)
	}
	if account.IsOpenAICodexNativeRelay() && c != nil && c.Request != nil && c.Request.URL != nil && c.Request.URL.RawQuery != "" {
		parsedWSURL, parseErr := url.Parse(wsURL)
		if parseErr != nil {
			return fmt.Errorf("parse native relay ws url: %w", parseErr)
		}
		if parsedWSURL.RawQuery == "" {
			parsedWSURL.RawQuery = c.Request.URL.RawQuery
		} else {
			parsedWSURL.RawQuery += "&" + c.Request.URL.RawQuery
		}
		wsURL = parsedWSURL.String()
	}
	wsHost := "-"
	wsPath := "-"
	if parsedURL, parseErr := url.Parse(wsURL); parseErr == nil && parsedURL != nil {
		wsHost = normalizeOpenAIWSLogValue(parsedURL.Host)
		wsPath = normalizeOpenAIWSLogValue(parsedURL.Path)
	}
	logOpenAIWSV2Passthrough(
		"relay_dial_start account_id=%d ws_host=%s ws_path=%s proxy_enabled=%v",
		account.ID,
		wsHost,
		wsPath,
		account.ProxyID != nil && account.Proxy != nil,
	)

	var headers http.Header
	if account.IsOpenAICodexNativeRelay() {
		headers = s.buildOpenAINativeRelayWSHeaders(c, token)
	} else {
		isCodexCLI := false
		if c != nil {
			isCodexCLI = openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator"))
		}
		headers, _ = s.buildOpenAIWSHeaders(c, account, token, wsDecision, isCodexCLI, "", "", "")
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	dialer := s.getOpenAIWSPassthroughDialer()
	if dialer == nil {
		return errors.New("openai ws passthrough dialer is nil")
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, s.openAIWSDialTimeout())
	defer cancelDial()
	upstreamConn, statusCode, handshakeHeaders, err := dialer.Dial(dialCtx, wsURL, headers, proxyURL)
	if err != nil {
		logOpenAIWSV2Passthrough(
			"relay_dial_failed account_id=%d status_code=%d err=%s",
			account.ID,
			statusCode,
			truncateOpenAIWSLogValue(err.Error(), openAIWSLogValueMaxLen),
		)
		return s.mapOpenAIWSPassthroughDialError(err, statusCode, handshakeHeaders)
	}
	defer func() {
		_ = upstreamConn.Close()
	}()
	logOpenAIWSV2Passthrough(
		"relay_dial_ok account_id=%d status_code=%d upstream_request_id=%s",
		account.ID,
		statusCode,
		openAIWSHeaderValueForLog(handshakeHeaders, "x-request-id"),
	)

	upstreamFrameConn, ok := upstreamConn.(openaiwsv2.FrameConn)
	if !ok {
		return errors.New("openai ws passthrough upstream connection does not support frame relay")
	}

	completedTurns := atomic.Int32{}
	var relayClient openaiwsv2.FrameConn = &openAIWSClientFrameConn{conn: clientConn}
	if resumableClient != nil {
		relayClient = resumableClient
	}
	relayResult, relayExit := openaiwsv2.RunEntry(openaiwsv2.EntryInput{
		Ctx:                ctx,
		ClientConn:         relayClient,
		UpstreamConn:       upstreamFrameConn,
		FirstClientMessage: firstClientMessage,
		Options: openaiwsv2.RelayOptions{
			WriteTimeout:     s.openAIWSWriteTimeout(),
			IdleTimeout:      s.openAIWSPassthroughIdleTimeout(),
			FirstMessageType: firstClientMessageType,
			OnClientTurn: func(turn int, messageType coderws.MessageType, payload []byte) error {
				if hooks != nil && hooks.BeforeTurn != nil {
					if err := hooks.BeforeTurn(turn); err != nil {
						return err
					}
				}
				if hooks != nil && hooks.OnClientTurn != nil {
					if err := hooks.OnClientTurn(turn, payload); err != nil {
						return err
					}
				}
				replayTracker.RegisterTurn(turn, messageType, payload)
				return nil
			},
			BeforeUpstreamFrame: func(frame openaiwsv2.RelayUpstreamFrame) error {
				if frame.MessageType != coderws.MessageText {
					return nil
				}
				eventType := strings.TrimSpace(gjson.GetBytes(frame.Payload, "type").String())
				if eventType == "error" {
					errCodeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(frame.Payload)
					s.persistOpenAIWSRateLimitSignal(ctx, account, handshakeHeaders, frame.Payload, errCodeRaw, errTypeRaw, errMsgRaw)
					if isOpenAIWSRateLimitError(errCodeRaw, errTypeRaw, errMsgRaw) &&
						!frame.WroteCurrentTurn &&
						hooks != nil && hooks.OnAccountExhausted != nil &&
						account.Type == AccountTypeOAuth && !account.IsOpenAICodexNativeRelay() {
						errMessage := strings.TrimSpace(errMsgRaw)
						if errMessage == "" {
							errMessage = "upstream account quota exhausted"
						}
						if failoverErr, ok := replayTracker.BuildFailoverError(account.ID, errors.New(errMessage)); ok {
							logOpenAIWSV2Passthrough(
								"relay_account_failover account_id=%d turn=%d action=suppress_quota_error_and_replay",
								account.ID,
								failoverErr.Turn(),
							)
							return failoverErr
						}
					}
				}
				replayTracker.ObserveUpstreamFrame(frame.Payload)
				return nil
			},
			TurnMetadata: func(turn int, payload []byte) openaiwsv2.RelayTurnMetadata {
				metadata := openaiwsv2.RelayTurnMetadata{
					Turn:               turn,
					RequestModel:       strings.TrimSpace(gjson.GetBytes(payload, "model").String()),
					RequestPayloadHash: HashUsageRequestPayload(payload),
				}
				if effort := extractOpenAIReasoningEffortFromBody(payload, metadata.RequestModel); effort != nil {
					metadata.ReasoningEffort = *effort
				}
				if serviceTier := extractOpenAIServiceTierFromBody(payload); serviceTier != nil {
					metadata.RequestServiceTier = *serviceTier
				}
				return metadata
			},
			OnUsageParseFailure: func(eventType string, usageRaw string) {
				logOpenAIWSV2Passthrough(
					"usage_parse_failed event_type=%s usage_raw=%s",
					truncateOpenAIWSLogValue(eventType, openAIWSLogValueMaxLen),
					truncateOpenAIWSLogValue(usageRaw, openAIWSLogValueMaxLen),
				)
			},
			OnTurnComplete: func(turn openaiwsv2.RelayTurnResult) {
				completionNo := int(completedTurns.Add(1))
				turnNo := turn.Turn
				if turnNo <= 0 {
					turnNo = completionNo
				}
				requestServiceTier := normalizeOpenAIServiceTier(turn.RequestServiceTier)
				turnResult := &OpenAIForwardResult{
					RequestID: turn.RequestID,
					Usage: OpenAIUsage{
						InputTokens:              turn.Usage.InputTokens,
						OutputTokens:             turn.Usage.OutputTokens,
						CacheCreationInputTokens: turn.Usage.CacheCreationInputTokens,
						CacheReadInputTokens:     turn.Usage.CacheReadInputTokens,
						ReportedServiceTier:      normalizeOpenAIReportedServiceTier(turn.Usage.ServiceTier),
					},
					Model:              turn.RequestModel,
					ReasoningEffort:    openAIWSOptionalString(turn.ReasoningEffort),
					RequestPayloadHash: turn.RequestPayloadHash,
					ServiceTier: resolveOpenAIServiceTier(
						normalizeOpenAIReportedServiceTier(turn.Usage.ServiceTier),
						requestServiceTier,
					),
					Stream:          true,
					OpenAIWSMode:    true,
					ResponseHeaders: cloneHeader(handshakeHeaders),
					Duration:        turn.Duration,
					FirstTokenMs:    turn.FirstTokenMs,
				}
				logOpenAIWSV2Passthrough(
					"relay_turn_completed account_id=%d turn=%d request_id=%s terminal_event=%s duration_ms=%d first_token_ms=%d input_tokens=%d output_tokens=%d cache_read_tokens=%d",
					account.ID,
					turnNo,
					truncateOpenAIWSLogValue(turnResult.RequestID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(turn.TerminalEventType, openAIWSLogValueMaxLen),
					turnResult.Duration.Milliseconds(),
					openAIWSFirstTokenMsForLog(turnResult.FirstTokenMs),
					turnResult.Usage.InputTokens,
					turnResult.Usage.OutputTokens,
					turnResult.Usage.CacheReadInputTokens,
				)
				if stateStore != nil && turnResult.RequestID != "" &&
					(turn.TerminalEventType == "response.completed" || turn.TerminalEventType == "response.done") {
					logOpenAIWSBindResponseAccountWarn(
						getOpenAIGroupIDFromContext(c),
						account.ID,
						turnResult.RequestID,
						stateStore.BindResponseAccountForUser(ctx, userID, turnResult.RequestID, account.ID, s.openAIWSResponseStickyTTL()),
					)
				}
				if hooks != nil && hooks.AfterTurn != nil {
					hooks.AfterTurn(turnNo, turnResult, nil)
				}
			},
			OnTrace: func(event openaiwsv2.RelayTraceEvent) {
				logOpenAIWSV2Passthrough(
					"relay_trace account_id=%d stage=%s direction=%s msg_type=%s bytes=%d graceful=%v wrote_downstream=%v err=%s",
					account.ID,
					truncateOpenAIWSLogValue(event.Stage, openAIWSLogValueMaxLen),
					truncateOpenAIWSLogValue(event.Direction, openAIWSLogValueMaxLen),
					truncateOpenAIWSLogValue(event.MessageType, openAIWSLogValueMaxLen),
					event.PayloadBytes,
					event.Graceful,
					event.WroteDownstream,
					truncateOpenAIWSLogValue(event.Error, openAIWSLogValueMaxLen),
				)
			},
		},
	})

	result := &OpenAIForwardResult{
		RequestID: relayResult.RequestID,
		Usage: OpenAIUsage{
			InputTokens:              relayResult.Usage.InputTokens,
			OutputTokens:             relayResult.Usage.OutputTokens,
			CacheCreationInputTokens: relayResult.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     relayResult.Usage.CacheReadInputTokens,
			ReportedServiceTier:      normalizeOpenAIReportedServiceTier(relayResult.Usage.ServiceTier),
		},
		Model:              relayResult.RequestModel,
		ReasoningEffort:    openAIWSOptionalString(relayResult.ReasoningEffort),
		RequestPayloadHash: relayResult.RequestPayloadHash,
		ServiceTier: resolveOpenAIServiceTier(
			normalizeOpenAIReportedServiceTier(relayResult.Usage.ServiceTier),
			normalizeOpenAIServiceTier(relayResult.RequestServiceTier),
		),
		Stream:          true,
		OpenAIWSMode:    true,
		ResponseHeaders: cloneHeader(handshakeHeaders),
		Duration:        relayResult.Duration,
		FirstTokenMs:    relayResult.FirstTokenMs,
	}

	turnCount := int(completedTurns.Load())
	if relayExit == nil {
		logOpenAIWSV2Passthrough(
			"relay_completed account_id=%d request_id=%s terminal_event=%s duration_ms=%d c2u_frames=%d u2c_frames=%d dropped_frames=%d turns=%d",
			account.ID,
			truncateOpenAIWSLogValue(result.RequestID, openAIWSIDValueMaxLen),
			truncateOpenAIWSLogValue(relayResult.TerminalEventType, openAIWSLogValueMaxLen),
			result.Duration.Milliseconds(),
			relayResult.ClientToUpstreamFrames,
			relayResult.UpstreamToClientFrames,
			relayResult.DroppedDownstreamFrames,
			turnCount,
		)
		// 正常路径按 terminal 事件逐 turn 已回调；仅在零 turn 场景兜底回调一次。
		if turnCount == 0 && hooks != nil && hooks.AfterTurn != nil {
			hooks.AfterTurn(1, result, nil)
		}
		return nil
	}
	logOpenAIWSV2Passthrough(
		"relay_failed account_id=%d stage=%s wrote_downstream=%v err=%s duration_ms=%d c2u_frames=%d u2c_frames=%d dropped_frames=%d turns=%d",
		account.ID,
		truncateOpenAIWSLogValue(relayExit.Stage, openAIWSLogValueMaxLen),
		relayExit.WroteDownstream,
		truncateOpenAIWSLogValue(relayErrorText(relayExit.Err), openAIWSLogValueMaxLen),
		result.Duration.Milliseconds(),
		relayResult.ClientToUpstreamFrames,
		relayResult.UpstreamToClientFrames,
		relayResult.DroppedDownstreamFrames,
		turnCount,
	)

	relayErr := relayExit.Err
	if relayExit.Stage == "idle_timeout" {
		relayErr = NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"client websocket idle timeout",
			relayErr,
		)
	}
	turnErr := wrapOpenAIWSIngressTurnError(
		relayExit.Stage,
		relayErr,
		relayExit.WroteDownstream,
	)
	if hooks != nil && hooks.AfterTurn != nil {
		hooks.AfterTurn(turnCount+1, nil, turnErr)
	}
	return turnErr
}

// buildOpenAINativeRelayWSHeaders preserves application-level Codex handshake
// metadata while leaving WebSocket transport headers to coder/websocket. Only
// per-hop authentication and the internal relay chain are replaced.
func (s *OpenAIGatewayService) buildOpenAINativeRelayWSHeaders(c *gin.Context, token string) http.Header {
	headers := make(http.Header)
	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			if !shouldCopyOpenAINativeRelayWSHeader(key) {
				continue
			}
			for _, value := range values {
				headers.Add(key, value)
			}
		}
		if accountID := strings.TrimSpace(c.GetHeader("chatgpt-account-id")); accountID != "" {
			headers.Set("chatgpt-account-id", accountID)
		}
		headers.Set(
			openAINativeRelayChainHeader,
			appendOpenAINativeRelayChain(c.GetHeader(openAINativeRelayChainHeader), s.openAINativeRelayID()),
		)
	}
	headers.Set("authorization", "Bearer "+token)
	return headers
}

func shouldCopyOpenAINativeRelayWSHeader(key string) bool {
	if !shouldCopyOpenAIRequestHeader(key) {
		return false
	}
	return !strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "sec-websocket-")
}

func (s *OpenAIGatewayService) mapOpenAIWSPassthroughDialError(
	err error,
	statusCode int,
	handshakeHeaders http.Header,
) error {
	if err == nil {
		return nil
	}
	wrappedErr := err
	var dialErr *openAIWSDialError
	if !errors.As(err, &dialErr) {
		wrappedErr = &openAIWSDialError{
			StatusCode:      statusCode,
			ResponseHeaders: cloneHeader(handshakeHeaders),
			Err:             err,
		}
	}

	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewOpenAIWSClientCloseError(
			coderws.StatusTryAgainLater,
			"upstream websocket connect timeout",
			wrappedErr,
		)
	}
	if statusCode == http.StatusTooManyRequests {
		return NewOpenAIWSClientCloseError(
			coderws.StatusTryAgainLater,
			"upstream websocket is busy, please retry later",
			wrappedErr,
		)
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"upstream websocket authentication failed",
			wrappedErr,
		)
	}
	if statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError {
		return NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"upstream websocket handshake rejected",
			wrappedErr,
		)
	}
	return fmt.Errorf("openai ws passthrough dial: %w", wrappedErr)
}

func openaiwsv2RelayMessageTypeName(msgType coderws.MessageType) string {
	switch msgType {
	case coderws.MessageText:
		return "text"
	case coderws.MessageBinary:
		return "binary"
	default:
		return fmt.Sprintf("unknown(%d)", msgType)
	}
}

func relayErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func openAIWSFirstTokenMsForLog(firstTokenMs *int) int {
	if firstTokenMs == nil {
		return -1
	}
	return *firstTokenMs
}

func logOpenAIWSV2Passthrough(format string, args ...any) {
	logger.LegacyPrintf(
		"service.openai_ws_v2",
		"[OpenAI WS v2 passthrough] %s "+format,
		append([]any{openaiWSV2PassthroughModeFields}, args...)...,
	)
}
