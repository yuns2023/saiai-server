package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// Evidence status recorded in the flagged result diagnostics.
const (
	ModerationEvidenceStored         = "stored"
	ModerationEvidenceNoKey          = "no_key"
	ModerationEvidenceEncryptFailed  = "encrypt_failed"
	ModerationEvidenceUnsupported    = "unsupported"
	ModerationEvidenceSelectionError = "selection_error"
	ModerationEvidenceDisabled       = "disabled"
	// The user was soft-deleted while the job was in flight.
	ModerationEvidenceUserDeleted = "user_deleted"
)

const moderationEvidenceMaxChecks = 4

// InputModerationEvidence is one encrypted check, prepared before the event
// transaction and written in it.
type InputModerationEvidence struct {
	CheckIndex int
	Kinds      []string
	AAD        ModerationEvidenceAAD
	CharCount  int
	KeyID      string
	Ciphertext string
	ExpiresAt  time.Time
}

// moderationEvidencePlaintext is encrypted as a whole so the scores and
// categories shown to administrators are authenticated with the text.
type moderationEvidencePlaintext struct {
	Text       string                       `json:"text"`
	Scores     map[string]float64           `json:"scores"`
	Categories []string                     `json:"categories"`
	Sources    []InputModerationCheckSource `json:"sources"`
	// Selection outcome of the whole event, kept with the text so it survives
	// the result row.
	Truncated         bool     `json:"truncated,omitempty"`
	OmittedCategories []string `json:"omitted_categories,omitempty"`
}

func moderationEvidenceCryptoFromConfig(cfg *config.Config) *moderationEvidenceCrypto {
	if cfg == nil || !cfg.Totp.EncryptionKeyConfigured {
		return nil
	}
	crypto, err := newModerationEvidenceCrypto(cfg.Totp.EncryptionKey, cfg.Gateway.InputModeration.EvidencePreviousKeys)
	if err != nil {
		slog.Error("input_moderation_evidence_key_invalid", "error", err)
		return nil
	}
	return crypto
}

// moderationDecisionRule is the single definition of the final threshold
// decision; evidence selection and fingerprints reuse it per check.
type moderationDecisionRule struct {
	group  *Group
	policy *InputModerationPolicy
}

func (d moderationDecisionRule) audited(category string) bool {
	return d.group.InputModerationPolicy == nil || moderationPolicyMatches(d.policy.AuditCategories, []string{category})
}

func (d moderationDecisionRule) threshold(category string) float64 {
	return float64(moderationPolicyThreshold(d.policy, category, d.group.InputModerationMinConfidence)) / 100
}

func (d moderationDecisionRule) crosses(category string, score float64) bool {
	return d.audited(category) && score >= d.threshold(category)
}

func (d moderationDecisionRule) enforced(category string) bool {
	if d.group.InputModerationPolicy != nil {
		return moderationPolicyMatches(d.policy.EnforcementCategories, []string{category})
	}
	return moderationCategoriesMatch(d.group.InputModerationCategories, []string{category})
}

// moderationCheckCrossings lists, per check, the sorted categories whose own
// score crossed the final threshold.
func moderationCheckCrossings(checks []moderationCheckResult, rule moderationDecisionRule) [][]string {
	crossings := make([][]string, len(checks))
	for i, check := range checks {
		for category, score := range check.Result.Scores {
			if rule.crosses(category, score) {
				crossings[i] = append(crossings[i], category)
			}
		}
		sort.Strings(crossings[i])
	}
	return crossings
}

func containsModerationCategory(categories []string, category string) bool {
	for _, c := range categories {
		if c == category {
			return true
		}
	}
	return false
}

type moderationEvidenceSelection struct {
	Checks            []int
	OmittedCategories []string
	OmittedChecks     int
}

// selectModerationEvidence picks at most limit crossing checks: first the best
// check for each enforcement category, then for the other flagged categories,
// then further crossing checks by margin. ok is false when a flagged category
// has no crossing check; no other check is substituted.
func selectModerationEvidence(checks []moderationCheckResult, crossings [][]string, flagged []string, rule moderationDecisionRule, limit int) (moderationEvidenceSelection, bool) {
	margin := func(i int, category string) float64 {
		return checks[i].Result.Scores[category] - rule.threshold(category)
	}
	best := map[string]int{}
	for _, category := range flagged {
		best[category] = -1
		for i := range checks {
			if containsModerationCategory(crossings[i], category) && (best[category] < 0 || margin(i, category) > margin(best[category], category)) {
				best[category] = i
			}
		}
		if best[category] < 0 {
			return moderationEvidenceSelection{}, false
		}
	}
	chosen := map[int]bool{}
	covered := map[string]bool{}
	var selection moderationEvidenceSelection
	choose := func(i int) {
		chosen[i] = true
		selection.Checks = append(selection.Checks, i)
		for _, category := range crossings[i] {
			covered[category] = true
		}
	}
	for _, enforcement := range []bool{true, false} {
		var categories []string
		for _, category := range flagged {
			if rule.enforced(category) == enforcement {
				categories = append(categories, category)
			}
		}
		sort.SliceStable(categories, func(a, b int) bool {
			return margin(best[categories[a]], categories[a]) > margin(best[categories[b]], categories[b])
		})
		for _, category := range categories {
			if !covered[category] && len(selection.Checks) < limit {
				choose(best[category])
			}
		}
	}
	maxMargin := func(i int) float64 {
		value := -1.0
		for _, category := range crossings[i] {
			if m := margin(i, category); m > value {
				value = m
			}
		}
		return value
	}
	var rest []int
	for i := range checks {
		if len(crossings[i]) > 0 && !chosen[i] {
			rest = append(rest, i)
		}
	}
	sort.SliceStable(rest, func(a, b int) bool { return maxMargin(rest[a]) > maxMargin(rest[b]) })
	for _, i := range rest {
		if len(selection.Checks) < limit {
			choose(i)
		} else {
			selection.OmittedChecks++
		}
	}
	for _, category := range flagged {
		if !covered[category] {
			selection.OmittedCategories = append(selection.OmittedCategories, category)
		}
	}
	sort.Strings(selection.OmittedCategories)
	sort.Ints(selection.Checks)
	return selection, true
}

// captureModerationEvidence records the evidence status in the job diagnostics
// and returns the encrypted checks to persist with the event. Capture is best
// effort: every failure leaves the event unchanged.
func (s *InputModerationService) captureModerationEvidence(task InputModerationTask, checks []moderationCheckResult, crossings [][]string, flagged []string, rule moderationDecisionRule, scored bool) []InputModerationEvidence {
	diagnostics := task.Diagnostics
	setStatus := func(status string) []InputModerationEvidence {
		diagnostics.EvidenceStatus = status
		return nil
	}
	switch {
	case !scored:
		return setStatus(ModerationEvidenceUnsupported)
	case s.cfg.EvidenceRetentionDays <= 0:
		return setStatus(ModerationEvidenceDisabled)
	case s.evidenceSealer == nil:
		return setStatus(ModerationEvidenceNoKey)
	}
	selection, ok := selectModerationEvidence(checks, crossings, flagged, rule, moderationEvidenceMaxChecks)
	if !ok {
		slog.Error("input_moderation_evidence_selection_failed", "job_id", task.JobID, "user_id", task.UserID, "group_id", task.GroupID)
		return setStatus(ModerationEvidenceSelectionError)
	}
	truncated := len(selection.OmittedCategories) > 0 || selection.OmittedChecks > 0
	expiresAt := time.Now().UTC().Add(time.Duration(s.cfg.EvidenceRetentionDays) * 24 * time.Hour)
	evidence := make([]InputModerationEvidence, 0, len(selection.Checks))
	for _, i := range selection.Checks {
		check := checks[i]
		scores := map[string]float64{}
		for category, score := range check.Result.Scores {
			if rule.audited(category) {
				scores[category] = score
			}
		}
		plaintext, err := json.Marshal(moderationEvidencePlaintext{Text: check.Check.Text, Scores: scores, Categories: crossings[i], Sources: check.Check.Sources,
			Truncated: truncated, OmittedCategories: selection.OmittedCategories})
		if err != nil {
			slog.Error("input_moderation_evidence_encode_failed", "job_id", task.JobID)
			return setStatus(ModerationEvidenceEncryptFailed)
		}
		aad := ModerationEvidenceAAD{JobID: task.JobID, UserID: task.UserID, GroupID: task.GroupID, CheckIndex: check.Check.Index, Kinds: check.Check.Kinds()}
		ciphertext, err := s.evidenceSealer.Seal(aad, plaintext)
		if err != nil {
			slog.Error("input_moderation_evidence_encrypt_failed", "job_id", task.JobID, "error", err)
			return setStatus(ModerationEvidenceEncryptFailed)
		}
		evidence = append(evidence, InputModerationEvidence{
			CheckIndex: check.Check.Index, Kinds: aad.Kinds, AAD: aad, CharCount: utf8.RuneCountInString(check.Check.Text),
			KeyID: s.evidenceSealer.KeyID(), Ciphertext: ciphertext, ExpiresAt: expiresAt,
		})
	}
	diagnostics.EvidenceStatus = ModerationEvidenceStored
	diagnostics.EvidenceCount = len(evidence)
	diagnostics.EvidenceTruncated = truncated
	diagnostics.EvidenceOmittedCategories = selection.OmittedCategories
	diagnostics.EvidenceOmittedChecks = selection.OmittedChecks
	return evidence
}

type moderationContentMACInput struct {
	UserID  int64  `json:"user_id"`
	GroupID int64  `json:"group_id"`
	Text    string `json:"text"`
}

type moderationFingerprintInput struct {
	RulesVersion string   `json:"rules_version"`
	Categories   []string `json:"categories"`
	ContentMACs  []string `json:"content_macs"`
}

// moderationIncidentFingerprint identifies an incident that is fully explained
// by stable instruction bundles, so repeating the bundle on later turns does
// not count another strike. It is empty (the incident always counts) unless
// every flagged enforcement category is crossed by an instruction check and
// the new text alone crosses no enforcement category. textAlone runs the
// extra text-alone check when the plan has none; ok=false means unknown.
func moderationIncidentFingerprint(sealer moderationEvidenceSealer, task InputModerationTask, rulesVersion string, checks []moderationCheckResult, crossings [][]string, flagged []string, rule moderationDecisionRule, textAlone func() (crosses bool, ok bool)) string {
	if sealer == nil {
		return ""
	}
	var enforced []string
	for _, category := range flagged {
		if rule.enforced(category) {
			enforced = append(enforced, category)
		}
	}
	sort.Strings(enforced)
	if len(enforced) == 0 {
		return ""
	}
	macSet := map[string]bool{}
	for _, category := range enforced {
		explained := false
		for i, check := range checks {
			if check.Check.HasKind(moderationCheckInstructions) && containsModerationCategory(crossings[i], category) {
				explained = true
				macSet[sealer.MAC(moderationContentMACInput{UserID: task.UserID, GroupID: task.GroupID, Text: check.Check.Text})] = true
			}
		}
		if !explained {
			return ""
		}
	}
	if strings.TrimSpace(task.Text) != "" {
		hasTextCheck := false
		for i, check := range checks {
			if !check.Check.HasKind(moderationCheckText) {
				continue
			}
			hasTextCheck = true
			for _, category := range crossings[i] {
				if rule.enforced(category) {
					return ""
				}
			}
		}
		if !hasTextCheck {
			crosses, ok := textAlone()
			if !ok || crosses {
				return ""
			}
		}
	}
	macs := make([]string, 0, len(macSet))
	for mac := range macSet {
		macs = append(macs, mac)
	}
	sort.Strings(macs)
	return sealer.MAC(moderationFingerprintInput{RulesVersion: rulesVersion, Categories: enforced, ContentMACs: macs})
}

// moderationTextAloneNeeded reports whether a fingerprint would depend on an
// extra text-alone check: every flagged enforcement category is explained by
// an instruction check, there is new text, and the plan has no text check.
func moderationTextAloneNeeded(task InputModerationTask, checks []moderationCheckResult, crossings [][]string, flagged []string, rule moderationDecisionRule) bool {
	if strings.TrimSpace(task.Text) == "" {
		return false
	}
	enforced := 0
	for _, category := range flagged {
		if !rule.enforced(category) {
			continue
		}
		enforced++
		explained := false
		for i, check := range checks {
			if check.Check.HasKind(moderationCheckInstructions) && containsModerationCategory(crossings[i], category) {
				explained = true
			}
		}
		if !explained {
			return false
		}
	}
	for _, check := range checks {
		if check.Check.HasKind(moderationCheckText) {
			return false
		}
	}
	return enforced > 0
}

// moderationTextAloneScores runs the optional text-alone check under the
// submitted policy, before the final group/user/policy re-read, so a change
// made while it runs is still caught by that re-read. It returns nil when the
// check is not needed or could not complete; the incident then counts.
func (s *InputModerationService) moderationTextAloneScores(task InputModerationTask, group *Group, policy *InputModerationPolicy, combined *InputModerationClassification, checks []moderationCheckResult) map[string]float64 {
	if s.evidenceSealer == nil || !group.InputModerationAutoDisableUser || combined == nil || len(combined.Scores) == 0 {
		return nil
	}
	rule := moderationDecisionRule{group: group, policy: policy}
	var flagged []string
	for category, score := range combined.Scores {
		if rule.crosses(category, score) {
			flagged = append(flagged, category)
		}
	}
	if !moderationTextAloneNeeded(task, checks, moderationCheckCrossings(checks, rule), flagged, rule) {
		return nil
	}
	timeout := time.Duration(s.cfg.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ctx = WithInputModerationDiagnostics(ctx, task.Diagnostics)
	ctx = WithInputModerationCacheScope(ctx, task.UserID, task.GroupID)
	task.Diagnostics.PlannedChecks++
	result, err := s.classifyModerationText(ctx, strings.TrimSpace(task.Text), group)
	if err != nil {
		slog.Warn("input_moderation_text_alone_check_failed", "job_id", task.JobID, "user_id", task.UserID, "group_id", task.GroupID, "error", err)
		return nil
	}
	task.Diagnostics.CompletedChecks++
	return result.Scores
}

// moderationTextAloneCrosses evaluates precomputed text-alone scores with the
// final rule; ok is false when no scores are available.
func moderationTextAloneCrosses(scores map[string]float64, rule moderationDecisionRule) (crosses bool, ok bool) {
	if scores == nil {
		return false, false
	}
	for category, score := range scores {
		if rule.enforced(category) && rule.crosses(category, score) {
			return true, true
		}
	}
	return false, true
}
