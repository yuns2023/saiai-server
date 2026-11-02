package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Claude entrypoint policy (groups.claude_entrypoint_policy, migration 117).
// See docs/CLAUDE_ENTRYPOINT_POLICY.md. The entrypoint is declared by the
// client (User-Agent + billing header); the policy is a filter, not proof of
// client identity.
const (
	ClaudeEntrypointPolicyOff     = "off"
	ClaudeEntrypointPolicyObserve = "observe"
	ClaudeEntrypointPolicyEnforce = "enforce"
)

// Decision reasons, in evaluation order.
const (
	ClaudeEntrypointReasonOK             = "ok"
	ClaudeEntrypointReasonUANotAllowed   = "ua_not_allowed"
	ClaudeEntrypointReasonBodyMissing    = "body_missing"
	ClaudeEntrypointReasonBodyMalformed  = "body_malformed"
	ClaudeEntrypointReasonBodyAmbiguous  = "body_ambiguous"
	ClaudeEntrypointReasonBodyNotAllowed = "body_not_allowed"
	ClaudeEntrypointReasonMismatch       = "mismatch"
)

// Recorded results: observe never rejects, enforce rejects non-compliant.
const (
	ClaudeEntrypointResultAllowed           = "allowed"
	ClaudeEntrypointResultViolationObserved = "violation_observed"
	ClaudeEntrypointResultRejected          = "rejected"
)

const maxClaudeEntrypointAllowlistItems = 32

var claudeEntrypointAllowlistItemPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// DefaultClaudeEntrypointAllowlist returns a fresh copy of the default
// allowlist (official CLI, VS Code extension, Agent SDK / claude -p).
func DefaultClaudeEntrypointAllowlist() []string {
	return []string{"cli", "claude-vscode", "sdk-cli"}
}

// NormalizeClaudeEntrypointPolicy maps stored values; unknown → off. Use
// ValidateClaudeEntrypointPolicy for admin input.
func NormalizeClaudeEntrypointPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case ClaudeEntrypointPolicyObserve:
		return ClaudeEntrypointPolicyObserve
	case ClaudeEntrypointPolicyEnforce:
		return ClaudeEntrypointPolicyEnforce
	default:
		return ClaudeEntrypointPolicyOff
	}
}

// ValidateClaudeEntrypointPolicy accepts off|observe|enforce (any case,
// surrounding space); "" means off. Anything else is rejected.
func ValidateClaudeEntrypointPolicy(policy string) (string, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(policy)); normalized {
	case "":
		return ClaudeEntrypointPolicyOff, nil
	case ClaudeEntrypointPolicyOff, ClaudeEntrypointPolicyObserve, ClaudeEntrypointPolicyEnforce:
		return normalized, nil
	default:
		return "", infraerrors.BadRequest("INVALID_CLAUDE_ENTRYPOINT_POLICY",
			fmt.Sprintf("claude_entrypoint_policy must be off, observe or enforce (got %q)", policy))
	}
}

// NormalizeClaudeEntrypointAllowlist trims and lower-cases every item,
// removes duplicates (first occurrence wins) and validates the result. An
// empty list is valid and means no entrypoint is allowed.
func NormalizeClaudeEntrypointAllowlist(items []string) ([]string, error) {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		value := claudebilling.NormalizeEntrypoint(item)
		if !claudeEntrypointAllowlistItemPattern.MatchString(value) {
			return nil, infraerrors.BadRequest("INVALID_CLAUDE_ENTRYPOINT_ALLOWLIST",
				fmt.Sprintf("claude_entrypoint_allowlist item %q must match [a-z0-9][a-z0-9._-]{0,63}", item))
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) > maxClaudeEntrypointAllowlistItems {
		return nil, infraerrors.BadRequest("INVALID_CLAUDE_ENTRYPOINT_ALLOWLIST",
			fmt.Sprintf("claude_entrypoint_allowlist cannot contain more than %d items", maxClaudeEntrypointAllowlistItems))
	}
	return out, nil
}

// validateClaudeEntrypointPolicyPlatform rejects observe/enforce on groups
// that do not serve Anthropic /v1/messages traffic.
func validateClaudeEntrypointPolicyPlatform(policy, platform string) error {
	if policy != ClaudeEntrypointPolicyOff && platform != PlatformAnthropic {
		return infraerrors.BadRequest("INVALID_CLAUDE_ENTRYPOINT_POLICY",
			fmt.Sprintf("claude_entrypoint_policy %q is only supported for anthropic groups (group platform: %s); set it to off first", policy, platform))
	}
	return nil
}

// ClaudeEntrypointPolicyMode returns the normalized policy ("off" for nil).
func (g *Group) ClaudeEntrypointPolicyMode() string {
	if g == nil {
		return ClaudeEntrypointPolicyOff
	}
	return NormalizeClaudeEntrypointPolicy(g.ClaudeEntrypointPolicy)
}

// EffectiveClaudeEntrypointAllowlist returns the stored allowlist. A nil
// slice means the field was never loaded and falls back to the DB default; a
// non-nil empty slice allows nothing.
func (g *Group) EffectiveClaudeEntrypointAllowlist() []string {
	if g == nil || g.ClaudeEntrypointAllowlist == nil {
		return DefaultClaudeEntrypointAllowlist()
	}
	return g.ClaudeEntrypointAllowlist
}

// ClaudeEntrypointDecision is the outcome of checking request facts against
// one group's allowlist.
type ClaudeEntrypointDecision struct {
	Compliant bool
	Reason    string
}

// DecideClaudeEntrypoint is compliant iff the UA entrypoint is allowed, the
// body billing header is valid, its entrypoint is allowed and both agree.
// The first failing check (in the order of the reason constants) wins.
func DecideClaudeEntrypoint(facts claudebilling.EntrypointFacts, allowlist []string) ClaudeEntrypointDecision {
	allowed := func(entrypoint string) bool {
		if entrypoint == "" {
			return false
		}
		for _, item := range allowlist {
			if item == entrypoint {
				return true
			}
		}
		return false
	}
	reason := ClaudeEntrypointReasonOK
	switch {
	case !allowed(facts.UAEntrypoint):
		reason = ClaudeEntrypointReasonUANotAllowed
	case facts.BodyStatus == claudebilling.EntrypointBodyMissing:
		reason = ClaudeEntrypointReasonBodyMissing
	case facts.BodyStatus == claudebilling.EntrypointBodyMalformed:
		reason = ClaudeEntrypointReasonBodyMalformed
	case facts.BodyStatus != claudebilling.EntrypointBodyValid:
		reason = ClaudeEntrypointReasonBodyAmbiguous
	case !allowed(facts.BodyEntrypoint):
		reason = ClaudeEntrypointReasonBodyNotAllowed
	case facts.UAEntrypoint != facts.BodyEntrypoint:
		reason = ClaudeEntrypointReasonMismatch
	}
	return ClaudeEntrypointDecision{Compliant: reason == ClaudeEntrypointReasonOK, Reason: reason}
}

// ClaudeEntrypointResult maps a policy + decision to the recorded result.
func ClaudeEntrypointResult(policy string, decision ClaudeEntrypointDecision) string {
	switch {
	case decision.Compliant:
		return ClaudeEntrypointResultAllowed
	case policy == ClaudeEntrypointPolicyEnforce:
		return ClaudeEntrypointResultRejected
	default:
		return ClaudeEntrypointResultViolationObserved
	}
}

// ClaudeEntrypointRejectionMessage is the client-facing 400 message. It only
// states what the request declared; it does not claim client verification.
func ClaudeEntrypointRejectionMessage(facts claudebilling.EntrypointFacts, allowlist []string) string {
	body := claudebilling.EntrypointNone
	if facts.BodyStatus == claudebilling.EntrypointBodyValid {
		body = claudebilling.SanitizeEntrypointForRecord(facts.BodyEntrypoint)
	} else if facts.BodyStatus != claudebilling.EntrypointBodyMissing {
		body = "<" + facts.BodyStatus + ">"
	}
	allowed := "none"
	if len(allowlist) > 0 {
		allowed = strings.Join(allowlist, ", ")
	}
	return fmt.Sprintf("This group does not allow this Claude client entrypoint (UA: %s, body: %s). Allowed: %s.",
		claudebilling.SanitizeEntrypointForRecord(facts.UAEntrypoint), body, allowed)
}
