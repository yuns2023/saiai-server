package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// newOpenAIServiceTierTestHandler stops every request at the first
// post-validation step (user slot acquisition) and counts how many requests
// got that far. A rejected request must never reach it, so it cannot have
// selected an account or contacted an upstream.
func newOpenAIServiceTierTestHandler(t *testing.T) (*OpenAIGatewayHandler, *atomic.Int32) {
	t.Helper()
	var reached atomic.Int32
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			reached.Add(1)
			return false, errors.New("stop after validation")
		},
	}
	return newOpenAIHandlerForPreviousResponseIDValidation(t, cache), &reached
}

func serveOpenAIResponsesForServiceTierTest(h *OpenAIGatewayHandler, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	groupID := int64(2)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 101, GroupID: &groupID, User: &service.User{ID: 1}})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
	h.Responses(c)
	return w
}

func TestOpenAIResponses_RejectsUnbillableServiceTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tiers := []string{`"ultrafast"`, `"scale"`, `"garbage"`, `1`, `true`, `{"tier":"default"}`, `["priority"]`}
	for _, path := range []string{"/openai/v1/responses", "/openai/v1/responses/compact"} {
		for _, tier := range tiers {
			h, reached := newOpenAIServiceTierTestHandler(t)
			w := serveOpenAIResponsesForServiceTierTest(h, path, `{"model":"gpt-5.4","stream":false,"service_tier":`+tier+`,"input":"hi"}`)

			require.Equal(t, http.StatusBadRequest, w.Code, "%s %s", path, tier)
			require.Zero(t, reached.Load(), "%s %s must be rejected before routing", path, tier)
			var parsed struct {
				Error map[string]any `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &parsed))
			require.Equal(t, "invalid_request_error", parsed.Error["type"])
			require.Equal(t, "service_tier", parsed.Error["param"])
			require.Equal(t, "invalid_value", parsed.Error["code"])
			require.Equal(t, service.OpenAIUnsupportedServiceTierMessage, parsed.Error["message"])
		}
	}

	// A duplicate key cannot smuggle an unbillable tier past the first value.
	h, reached := newOpenAIServiceTierTestHandler(t)
	w := serveOpenAIResponsesForServiceTierTest(h, "/openai/v1/responses", `{"model":"gpt-5.4","service_tier":"default","service_tier":"ultrafast"}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, reached.Load())
}

func TestOpenAIResponses_RejectsAmbiguousDuplicateKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := map[string]string{
		`{"model":"gpt-5.4","type":"response.cancel","type":"response.create","input":"hi"}`:  "type",
		`{"model":"gpt-5.4","model":"gpt-6-astra","input":"hi"}`:                              "model",
		`{"model":"gpt-5.4","stream":false,"stream":true,"input":"hi"}`:                       "stream",
		`{"model":"gpt-5.4","previous_response_id":"resp_a","previous_response_id":"resp_b"}`: "previous_response_id",
		`{"model":"gpt-5.4","service_tier":"default","service_tier":"priority"}`:              "service_tier",
	}
	for body, key := range cases {
		h, reached := newOpenAIServiceTierTestHandler(t)
		w := serveOpenAIResponsesForServiceTierTest(h, "/openai/v1/responses", body)
		require.Equal(t, http.StatusBadRequest, w.Code, body)
		require.Zero(t, reached.Load(), body)
		var parsed struct {
			Error map[string]any `json:"error"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &parsed))
		require.Equal(t, "invalid_request_error", parsed.Error["type"], body)
		require.Equal(t, "duplicate_key", parsed.Error["code"], body)
		require.Equal(t, key, parsed.Error["param"], body)
		require.Equal(t, service.OpenAIDuplicateKeyMessage(key), parsed.Error["message"], body)
	}
}

func TestOpenAIResponsesWebSocket_RejectsAmbiguousFirstMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for body, key := range map[string]string{
		`{"type":"response.create","model":"gpt-5.4","type":"response.cancel"}`: "type",
		`{"type":"response.create","model":"gpt-5.4","model":"gpt-6-astra"}`:    "model",
	} {
		h, reached := newOpenAIServiceTierTestHandler(t)
		closeErr := dialOpenAIWSForServiceTierTest(t, h, body)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code, body)
		require.Equal(t, service.OpenAIDuplicateKeyMessage(key), closeErr.Reason, body)
		require.Zero(t, reached.Load(), body)
	}
}

func TestOpenAIResponses_AllowsBillableServiceTiers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bodies := []string{
		`{"model":"gpt-5.4","stream":false,"input":"hi"}`,
		`{"model":"gpt-5.4","stream":false,"service_tier":null,"input":"hi"}`,
	}
	for _, tier := range []string{"auto", "default", "flex", "priority", "fast", " Priority "} {
		bodies = append(bodies, `{"model":"gpt-5.4","stream":false,"service_tier":"`+tier+`","input":"hi"}`)
	}
	for _, body := range bodies {
		h, reached := newOpenAIServiceTierTestHandler(t)
		w := serveOpenAIResponsesForServiceTierTest(h, "/openai/v1/responses", body)
		require.Equal(t, int32(1), reached.Load(), body)
		require.NotContains(t, w.Body.String(), "service_tier", body)
	}
}

func dialOpenAIWSForServiceTierTest(t *testing.T, h *OpenAIGatewayHandler, firstMessage string) coderws.CloseError {
	t.Helper()
	wsServer := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
	t.Cleanup(wsServer.Close)

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(firstMessage)))
	cancelWrite()

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(readCtx)
	cancelRead()
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	return closeErr
}

func TestOpenAIResponsesWebSocket_RejectsUnbillableServiceTierOnFirstMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tier := range []string{`"ultrafast"`, `"scale"`, `"garbage"`, `2`} {
		h, reached := newOpenAIServiceTierTestHandler(t)
		closeErr := dialOpenAIWSForServiceTierTest(t, h, `{"type":"response.create","model":"gpt-5.4","service_tier":`+tier+`}`)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code, tier)
		require.Equal(t, service.OpenAIUnsupportedServiceTierMessage, closeErr.Reason, tier)
		require.Zero(t, reached.Load(), tier)
	}
}

func TestOpenAIResponsesWebSocket_AllowsBillableServiceTierOnFirstMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tier := range []string{"auto", "default", "flex", "priority", "fast"} {
		h, reached := newOpenAIServiceTierTestHandler(t)
		closeErr := dialOpenAIWSForServiceTierTest(t, h, `{"type":"response.create","model":"gpt-5.4","service_tier":"`+tier+`"}`)
		require.Equal(t, coderws.StatusInternalError, closeErr.Code, tier)
		require.Contains(t, closeErr.Reason, "failed to acquire user concurrency slot", tier)
		require.Equal(t, int32(1), reached.Load(), tier)
	}
}

func TestOpenAIWSTurnPayloadError(t *testing.T) {
	allowed := []byte(`{"type":"response.create","model":"gpt-5.4","service_tier":"priority"}`)
	rejected := []byte(`{"type":"response.create","model":"gpt-5.4","service_tier":"ultrafast"}`)

	require.NoError(t, openAIWSTurnPayloadError(allowed, allowed))
	require.NoError(t, openAIWSTurnPayloadError([]byte(`{"type":"response.create","model":"gpt-5.4"}`)))
	ambiguous := []byte(`{"type":"response.create","model":"gpt-5.4","type":"response.cancel"}`)
	var ambiguousErr *service.OpenAIWSClientCloseError
	require.ErrorAs(t, openAIWSTurnPayloadError(allowed, ambiguous), &ambiguousErr)
	require.Equal(t, coderws.StatusPolicyViolation, ambiguousErr.StatusCode())
	require.Equal(t, service.OpenAIDuplicateKeyMessage("type"), ambiguousErr.Reason())
	for _, payloads := range [][][]byte{{rejected, allowed}, {allowed, rejected}} {
		err := openAIWSTurnPayloadError(payloads...)
		var closeErr *service.OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		require.Equal(t, service.OpenAIUnsupportedServiceTierMessage, closeErr.Reason())
	}
}
