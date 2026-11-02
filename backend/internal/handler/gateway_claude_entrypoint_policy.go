package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const claudeEntrypointNotAllowedCode = "claude_entrypoint_not_allowed"

// claudeEntrypointPolicyState carries one /v1/messages request through every
// group it may be sent through (initial group, smart switch targets, fallback
// groups, claude_code_only fallbacks). Facts are parsed at most once, from the
// original body, and only when a group with an active policy is checked.
// Not safe for concurrent use.
type claudeEntrypointPolicyState struct {
	userAgent string
	body      []byte
	userID    int64
	apiKeyID  int64

	parsed   bool
	facts    claudebilling.EntrypointFacts
	recorded map[int64]struct{}
}

func newClaudeEntrypointPolicyState(c *gin.Context, body []byte, userID, apiKeyID int64) *claudeEntrypointPolicyState {
	return &claudeEntrypointPolicyState{
		userAgent: c.GetHeader("User-Agent"),
		body:      body,
		userID:    userID,
		apiKeyID:  apiKeyID,
	}
}

func (s *claudeEntrypointPolicyState) getFacts() claudebilling.EntrypointFacts {
	if !s.parsed {
		s.facts = claudebilling.ParseEntrypointFacts(s.userAgent, s.body)
		s.parsed = true
	}
	return s.facts
}

// applyClaudeEntrypointPolicyForKey checks the key's group and, when the
// request will be scheduled on its claude_code_only fallback instead, that
// group as well. It writes the 400 and returns false when the request must
// stop.
func (h *GatewayHandler) applyClaudeEntrypointPolicyForKey(c *gin.Context, state *claudeEntrypointPolicyState, key *service.APIKey, streamStarted bool) bool {
	if state == nil || key == nil {
		return true
	}
	group := key.Group
	if !h.applyClaudeEntrypointPolicy(c, state, group, streamStarted) {
		return false
	}
	if group == nil || !group.ClaudeCodeOnly || group.FallbackGroupID == nil || service.IsClaudeCodeClient(c.Request.Context()) || h.gatewayService == nil {
		return true
	}
	resolved, err := h.gatewayService.ResolveSchedulingGroup(c.Request.Context(), key.GroupID)
	if err != nil || resolved == nil || resolved.ID == group.ID {
		// Resolution errors are reported by account selection itself.
		return true
	}
	return h.applyClaudeEntrypointPolicy(c, state, resolved, streamStarted)
}

// applyClaudeEntrypointPolicy evaluates one group. off: nothing. observe:
// record, never reject. enforce: record, reject non-compliant with 400.
func (h *GatewayHandler) applyClaudeEntrypointPolicy(c *gin.Context, state *claudeEntrypointPolicyState, group *service.Group, streamStarted bool) bool {
	if state == nil || group == nil {
		return true
	}
	policy := group.ClaudeEntrypointPolicyMode()
	if policy == service.ClaudeEntrypointPolicyOff {
		return true
	}
	facts := state.getFacts()
	allowlist := group.EffectiveClaudeEntrypointAllowlist()
	decision := service.DecideClaudeEntrypoint(facts, allowlist)
	result := service.ClaudeEntrypointResult(policy, decision)

	if _, done := state.recorded[group.ID]; !done {
		if state.recorded == nil {
			state.recorded = make(map[int64]struct{}, 2)
		}
		state.recorded[group.ID] = struct{}{}
		h.claudeEntrypointObserver.Record(service.ClaudeEntrypointObservationInput{
			GroupID:   group.ID,
			Facts:     facts,
			Policy:    policy,
			Result:    result,
			Reason:    decision.Reason,
			UserAgent: state.userAgent,
		})
	}

	if !decision.Compliant && h.claudeEntrypointObserver.ShouldLogViolation(group.ID, facts.UAEntrypoint, facts.BodyEntrypoint) {
		requestLogger(c, "handler.gateway.messages").Warn("gateway.claude_entrypoint_policy",
			zap.Int64("group_id", group.ID),
			zap.String("policy", policy),
			zap.String("result", result),
			zap.String("reason", decision.Reason),
			zap.String("ua_entrypoint", claudebilling.SanitizeEntrypointForRecord(facts.UAEntrypoint)),
			zap.String("body_entrypoint", claudebilling.SanitizeEntrypointForRecord(facts.BodyEntrypoint)),
			zap.String("body_status", facts.BodyStatus),
			zap.Int64("user_id", state.userID),
			zap.Int64("api_key_id", state.apiKeyID),
		)
	}

	if decision.Compliant || policy != service.ClaudeEntrypointPolicyEnforce {
		return true
	}
	writeClaudeEntrypointRejection(c, service.ClaudeEntrypointRejectionMessage(facts, allowlist), streamStarted)
	return false
}

// writeClaudeEntrypointRejection writes the Anthropic-shaped 400 with a
// machine-readable code; after the stream started it sends the same error in
// the SSE shape used by handleStreamingAwareError.
func writeClaudeEntrypointRejection(c *gin.Context, message string, streamStarted bool) {
	payload := gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "invalid_request_error",
			"code":    claudeEntrypointNotAllowedCode,
			"message": message,
		},
	}
	if !streamStarted {
		c.JSON(http.StatusBadRequest, payload)
		return
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", encoded); err != nil {
		_ = c.Error(err)
	}
	flusher.Flush()
}
