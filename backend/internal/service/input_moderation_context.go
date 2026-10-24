package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const moderationMaxChunks = 16
const moderationMaxPlannedChecks = 64

var errModerationTooLarge = errors.New("moderation input exceeds coverage budget")
var errModerationPlanTooLarge = errors.New("moderation check plan exceeds coverage budget")

// Scope contains authenticated identity only; it is never sent to the detector.
type ModerationCacheScope struct{ UserID, GroupID int64 }
type moderationScopeKey struct{}

func WithInputModerationCacheScope(ctx context.Context, userID, groupID int64) context.Context {
	return context.WithValue(ctx, moderationScopeKey{}, ModerationCacheScope{userID, groupID})
}
func InputModerationCacheScope(ctx context.Context) (ModerationCacheScope, bool) {
	s, ok := ctx.Value(moderationScopeKey{}).(ModerationCacheScope)
	return s, ok && s.UserID > 0 && s.GroupID > 0
}

// Overlap preserves local context at boundaries. All characters are covered;
// this is not a claim of whole-document semantic equivalence to one inference.
func moderationChunks(text string, size int) ([]string, error) {
	ranges, err := moderationChunkRanges(text, size)
	if err != nil {
		return nil, err
	}
	runes := []rune(text)
	chunks := make([]string, 0, len(ranges))
	for _, r := range ranges {
		chunks = append(chunks, string(runes[r[0]:r[1]]))
	}
	return chunks, nil
}

// moderationChunkRanges returns the [start, end) rune ranges of each chunk.
func moderationChunkRanges(text string, size int) ([][2]int, error) {
	if size <= 0 {
		size = 32768
	}
	overlap := min(512, size/8)
	total := utf8.RuneCountInString(text)
	if total > size+(moderationMaxChunks-1)*(size-overlap) {
		return nil, errModerationTooLarge
	}
	var ranges [][2]int
	for start := 0; start < total; {
		end := min(start+size, total)
		ranges = append(ranges, [2]int{start, end})
		if end == total {
			break
		}
		start = end - overlap
	}
	return ranges, nil
}

// Check kinds of a moderation plan. A check is one detector call; identical
// texts from several sources share one check and carry every kind.
const (
	moderationCheckJoint        = "joint"
	moderationCheckText         = "text"
	moderationCheckInstructions = "instructions"
)

// InputModerationCheckSource locates a check inside the text it was cut from: the
// joined instructions+text, the new text alone, or instruction bundle Bundle
// (1-based). Start/End are rune offsets.
type InputModerationCheckSource struct {
	Kind   string `json:"kind"`
	Bundle int    `json:"bundle,omitempty"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

type moderationCheck struct {
	Index   int
	Text    string
	Sources []InputModerationCheckSource
}

// Kinds lists the unique source kinds in a fixed order.
func (c moderationCheck) Kinds() []string {
	var kinds []string
	for _, kind := range []string{moderationCheckJoint, moderationCheckText, moderationCheckInstructions} {
		if c.HasKind(kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func (c moderationCheck) HasKind(kind string) bool {
	for _, source := range c.Sources {
		if source.Kind == kind {
			return true
		}
	}
	return false
}

type moderationCheckResult struct {
	Check  moderationCheck
	Result *InputModerationClassification
}

func appendModerationCheck(checks []moderationCheck, text string, source InputModerationCheckSource) []moderationCheck {
	if text == "" {
		return checks
	}
	for i := range checks {
		if checks[i].Text == text {
			checks[i].Sources = append(checks[i].Sources, source)
			return checks
		}
	}
	return append(checks, moderationCheck{Index: len(checks), Text: text, Sources: []InputModerationCheckSource{source}})
}

func appendModerationCheckChunks(checks []moderationCheck, text string, size int, kind string) ([]moderationCheck, error) {
	ranges, err := moderationChunkRanges(text, size)
	if err != nil {
		return nil, err
	}
	// Chunks of one text are not merged with each other, as before the plan
	// carried kinds, so check counts and detector calls are unchanged.
	runes := []rune(text)
	for _, r := range ranges {
		checks = append(checks, moderationCheck{Index: len(checks), Text: string(runes[r[0]:r[1]]),
			Sources: []InputModerationCheckSource{{Kind: kind, Start: r[0], End: r[1]}}})
	}
	return checks, nil
}

func (s *InputModerationService) classifyInput(ctx context.Context, task InputModerationTask, group *Group) (*InputModerationClassification, []moderationCheckResult, string, error) {
	if task.Diagnostics == nil {
		task.Diagnostics = newModerationDiagnostics(task, s.cfg.MaxInputChars)
	}
	checks, coverage, err := moderationTaskChunks(task, s.cfg.MaxInputChars)
	if err != nil {
		task.Diagnostics.Limit = "input_chars"
		if errors.Is(err, errModerationPlanTooLarge) {
			task.Diagnostics.Limit = "planned_checks"
		}
		return nil, nil, "", err
	}
	task.Diagnostics.PlannedChecks = len(checks)
	ctx = WithInputModerationDiagnostics(ctx, task.Diagnostics)
	ctx = WithInputModerationCacheScope(ctx, task.UserID, task.GroupID)
	var combined *InputModerationClassification
	results := make([]moderationCheckResult, 0, len(checks))
	for _, check := range checks {
		result, err := s.classifyModerationText(ctx, check.Text, group)
		if err != nil {
			return nil, nil, "", err
		}
		task.Diagnostics.CompletedChecks++
		results = append(results, moderationCheckResult{Check: check, Result: cloneModerationClassification(result)})
		if combined == nil {
			combined = cloneModerationClassification(result)
			continue
		}
		if result.ModelVersion != combined.ModelVersion {
			return nil, nil, "", fmt.Errorf("moderation model changed during chunk processing")
		}
		for category, score := range result.Scores {
			if previous, exists := combined.Scores[category]; !exists || score > previous {
				combined.Scores[category] = score
			}
		}
		combined.Categories = normalizeModerationCategories(append(combined.Categories, result.Categories...))
		if result.Safety == InputModerationSafetyUnsafe || (result.Safety == InputModerationSafetyControversial && combined.Safety == InputModerationSafetySafe) {
			combined.Safety = result.Safety
		}
	}
	return combined, results, coverage, nil
}

// classifyModerationText runs one validated check. ctx must carry the job's
// diagnostics and cache scope so the detector budget applies.
func (s *InputModerationService) classifyModerationText(ctx context.Context, text string, group *Group) (*InputModerationClassification, error) {
	var result *InputModerationClassification
	var err error
	if _, budgeted := s.client.(InputModerationBudgetClient); !budgeted {
		if err := ReserveInputModerationCall(ctx); err != nil {
			return nil, err
		}
	}
	if group.InputModerationPolicy != nil {
		client, ok := s.client.(InputModerationPolicyClient)
		if !ok {
			return nil, fmt.Errorf("moderation client does not support versioned policies")
		}
		result, err = client.ClassifyWithPolicy(ctx, text, group.InputModerationPolicy)
	} else {
		result, err = s.client.Classify(ctx, text)
	}
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("empty moderation result")
	}
	result.Safety = strings.ToLower(strings.TrimSpace(result.Safety))
	if result.Safety != InputModerationSafetySafe && result.Safety != InputModerationSafetyUnsafe && result.Safety != InputModerationSafetyControversial {
		return nil, fmt.Errorf("invalid moderation safety")
	}
	for _, score := range result.Scores {
		if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
			return nil, fmt.Errorf("invalid moderation score")
		}
	}
	if group.InputModerationPolicy != nil {
		for _, category := range group.InputModerationPolicy.AuditCategories {
			if _, ok := result.Scores[category]; !ok {
				return nil, fmt.Errorf("incomplete moderation scores")
			}
		}
	}
	return result, nil
}

func cloneModerationClassification(r *InputModerationClassification) *InputModerationClassification {
	copy := *r
	copy.Scores = make(map[string]float64, len(r.Scores))
	for k, v := range r.Scores {
		copy.Scores[k] = v
	}
	copy.Categories = append([]string(nil), r.Categories...)
	return &copy
}

// Project instructions are identified by their textual envelope, not trusted
// because of a filename. No filesystem reads or path-based exemptions occur.
func projectInstructionText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "claude.md") || strings.Contains(lower, "agents.md") || strings.Contains(lower, "<user_instructions>")
}
func moderationTextContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		var parts []string
		for _, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := block["type"].(string)
			if kind == "text" || kind == "input_text" || kind == "" {
				if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
func appendUniqueModerationText(parts []string, text string) []string {
	if text == "" {
		return parts
	}
	for _, part := range parts {
		if part == text {
			return parts
		}
	}
	return append(parts, text)
}

func ExtractAnthropicModerationInput(parsed *ParsedRequest) (string, []string) {
	if parsed == nil {
		return "", nil
	}
	var parts []string
	parts = appendModerationInstructionBlocks(parts, parsed.System)
	for i, raw := range parsed.Messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		text := moderationTextContent(message["content"])
		if role == "system" || role == "developer" || (role == "user" && projectInstructionText(text) && (i < len(parsed.Messages)-1 || !IsRealUserMessage(parsed))) {
			parts = appendUniqueModerationText(parts, text)
		}
	}
	parts = appendProjectToolResults(parts, parsed.Messages)
	return separateProjectInstructions(ExtractLatestRealUserText(parsed), parts)
}

func ExtractOpenAIModerationInput(body []byte) (string, []string) {
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return "", nil
	}
	var parts []string
	parts = appendModerationInstructionBlocks(parts, request["instructions"])
	if input, ok := request["input"].([]any); ok {
		for i, raw := range input {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			role, _ := item["role"].(string)
			text := moderationTextContent(item["content"])
			if role == "system" || role == "developer" || (role == "user" && projectInstructionText(text) && i < len(input)-1) {
				parts = appendUniqueModerationText(parts, text)
			}
		}
	}
	return separateProjectInstructions(ExtractLatestOpenAIUserText(body), parts)
}

// Coverage of a planned moderation job, recorded as the result reason.
const (
	moderationCoverageFull         = ""
	moderationCoverageChunked      = "chunked_input"
	moderationCoverageJointSkipped = "joint_context_skipped"
)

// Instructions are packed into bundles of at most one chunk so a request with
// many small blocks (Claude Code sends 100+ per turn) needs only a few checks.
// A bundle closes after a block whose content hash selects a boundary, with a
// probability proportional to the block's size (about one boundary per quarter
// chunk). Boundaries depend on each block's own content, so an edit usually
// invalidates only nearby bundles: packing realigns at the next unchanged hash
// boundary. A run without hash boundaries is closed by the size limit, and an
// edit inside such a run can shift every bundle up to the next hash boundary.
func moderationInstructionBundles(instructions []string, size int) []string {
	if size <= 0 {
		size = 32768
	}
	target := uint64(max(1, size/4))
	var bundles []string
	var current []string
	currentRunes := 0
	flush := func() {
		if len(current) > 0 {
			bundles = appendUniqueModerationText(bundles, strings.Join(current, "\n\n"))
			current, currentRunes = nil, 0
		}
	}
	for _, instruction := range instructions {
		instruction = strings.TrimSpace(instruction)
		if instruction == "" {
			continue
		}
		runes := utf8.RuneCountInString(instruction)
		if runes >= size {
			// Oversized blocks stay standalone and are chunked like any text.
			flush()
			bundles = appendUniqueModerationText(bundles, instruction)
			continue
		}
		if len(current) > 0 && currentRunes+2+runes > size {
			flush()
		}
		if len(current) > 0 {
			currentRunes += 2
		}
		current = append(current, instruction)
		currentRunes += runes
		sum := sha256.Sum256([]byte(instruction))
		if binary.BigEndian.Uint64(sum[:8])%target < uint64(runes) {
			flush()
		}
	}
	flush()
	return bundles
}

// The plan checks the new text jointly with the instructions, then each
// instruction bundle on its own (stable across turns, hence usually cached).
// Without new text the bundles alone cover the instructions. When the joint
// text is too large to cover, the new text is checked alone instead of failing
// the job; the result discloses that joint context was skipped. The
// planned-check cap applies to the unique checks of this plan.
func moderationTaskChunks(task InputModerationTask, size int) ([]moderationCheck, string, error) {
	text := strings.TrimSpace(task.Text)
	parts := append([]string(nil), task.Instructions...)
	parts = appendUniqueModerationText(parts, text)
	coverage := moderationCoverageFull
	var checks []moderationCheck
	var err error
	if text != "" || len(task.Instructions) == 0 {
		kind := moderationCheckJoint
		if len(task.Instructions) == 0 {
			kind = moderationCheckText
		}
		checks, err = appendModerationCheckChunks(nil, strings.Join(parts, "\n\n"), size, kind)
	}
	if errors.Is(err, errModerationTooLarge) && len(task.Instructions) > 0 {
		coverage = moderationCoverageJointSkipped
		checks, err = nil, nil
		if text != "" {
			checks, err = appendModerationCheckChunks(nil, text, size, moderationCheckText)
		}
	}
	if err != nil {
		return nil, "", err
	}
	if coverage == moderationCoverageFull && len(checks) > 1 {
		coverage = moderationCoverageChunked
	}
	if task.Diagnostics != nil {
		task.Diagnostics.JointSkipped = coverage == moderationCoverageJointSkipped
	}
	bundles := moderationInstructionBundles(task.Instructions, size)
	if task.Diagnostics != nil {
		task.Diagnostics.InstructionBundles = len(bundles)
	}
	for number, bundle := range bundles {
		ranges, err := moderationChunkRanges(bundle, size)
		if err != nil {
			return nil, "", err
		}
		runes := []rune(bundle)
		for _, r := range ranges {
			checks = appendModerationCheck(checks, string(runes[r[0]:r[1]]), InputModerationCheckSource{Kind: moderationCheckInstructions, Bundle: number + 1, Start: r[0], End: r[1]})
			if task.Diagnostics != nil {
				task.Diagnostics.PlannedChecks = len(checks)
			}
			if len(checks) > moderationMaxPlannedChecks {
				return nil, "", errModerationPlanTooLarge
			}
		}
	}
	if task.Diagnostics != nil {
		task.Diagnostics.PlannedChecks = len(checks)
	}
	return checks, coverage, nil
}

// Only structured file-read calls identify file output reliably. Arbitrary shell
// commands and unrelated tool outputs remain outside this extraction contract.
func appendProjectToolResults(parts []string, messages []any) []string {
	reads := map[string]bool{}
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		blocks, _ := message["content"].([]any)
		for _, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "tool_use" && (block["name"] == "Read" || block["name"] == "read_file") {
				input, _ := block["input"].(map[string]any)
				filename, _ := input["file_path"].(string)
				if filename == "" {
					filename, _ = input["path"].(string)
				}
				base := strings.ToLower(path.Base(strings.ReplaceAll(filename, "\\", "/")))
				id, _ := block["id"].(string)
				if id != "" && (base == "claude.md" || base == "agents.md") {
					reads[id] = true
				}
			}
			if block["type"] == "tool_result" {
				id, _ := block["tool_use_id"].(string)
				if reads[id] {
					parts = appendUniqueModerationText(parts, moderationTextContent(block["content"]))
				}
			}
		}
	}
	return parts
}

var projectInstructionEnvelope = regexp.MustCompile(`(?is)<system-reminder>.*?</system-reminder>|<user_instructions>.*?</user_instructions>`)

// Retain the entire matched envelope; malformed/unrecognized wrappers remain in
// user text, so extraction never removes text from the assessment.
func separateProjectInstructions(text string, parts []string) (string, []string) {
	remaining := projectInstructionEnvelope.ReplaceAllStringFunc(text, func(block string) string {
		if !projectInstructionText(block) {
			return block
		}
		parts = appendUniqueModerationText(parts, block)
		return "\n"
	})
	return strings.TrimSpace(remaining), parts
}

// Keep stable blocks independent of changing neighboring protocol text so a
// change to one block does not invalidate every project's standalone scores.
func appendModerationInstructionBlocks(parts []string, content any) []string {
	if blocks, ok := content.([]any); ok {
		for _, block := range blocks {
			parts = appendUniqueModerationText(parts, moderationTextContent([]any{block}))
		}
		return parts
	}
	return appendUniqueModerationText(parts, moderationTextContent(content))
}
