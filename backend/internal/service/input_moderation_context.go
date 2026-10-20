package service

import (
	"context"
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

var errModerationTooLarge = errors.New("moderation input exceeds coverage budget")

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
	if size <= 0 {
		size = 32768
	}
	overlap := min(512, size/8)
	if utf8.RuneCountInString(text) > size+(moderationMaxChunks-1)*(size-overlap) {
		return nil, errModerationTooLarge
	}
	runes := []rune(text)
	var chunks []string
	for start := 0; start < len(runes); {
		end := min(start+size, len(runes))
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
		start = end - overlap
	}
	return chunks, nil
}

func (s *InputModerationService) classifyInput(ctx context.Context, task InputModerationTask, group *Group) (*InputModerationClassification, bool, error) {
	chunks, chunked, err := moderationTaskChunks(task, s.cfg.MaxInputChars)
	if err != nil {
		return nil, false, err
	}
	ctx = WithInputModerationCacheScope(ctx, task.UserID, task.GroupID)
	var combined *InputModerationClassification
	for _, text := range chunks {
		var result *InputModerationClassification
		if group.InputModerationPolicy != nil {
			client, ok := s.client.(InputModerationPolicyClient)
			if !ok {
				return nil, false, fmt.Errorf("moderation client does not support versioned policies")
			}
			result, err = client.ClassifyWithPolicy(ctx, text, group.InputModerationPolicy)
		} else {
			result, err = s.client.Classify(ctx, text)
		}
		if err != nil {
			return nil, false, err
		}
		if result == nil {
			return nil, false, fmt.Errorf("empty moderation result")
		}
		result.Safety = strings.ToLower(strings.TrimSpace(result.Safety))
		if result.Safety != InputModerationSafetySafe && result.Safety != InputModerationSafetyUnsafe && result.Safety != InputModerationSafetyControversial {
			return nil, false, fmt.Errorf("invalid moderation safety")
		}
		for _, score := range result.Scores {
			if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
				return nil, false, fmt.Errorf("invalid moderation score")
			}
		}
		if group.InputModerationPolicy != nil {
			for _, category := range group.InputModerationPolicy.AuditCategories {
				if _, ok := result.Scores[category]; !ok {
					return nil, false, fmt.Errorf("incomplete moderation scores")
				}
			}
		}
		if combined == nil {
			combined = cloneModerationClassification(result)
			continue
		}
		if result.ModelVersion != combined.ModelVersion {
			return nil, false, fmt.Errorf("moderation model changed during chunk processing")
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
	return combined, chunked, nil
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
	parts = appendUniqueModerationText(parts, moderationTextContent(parsed.System))
	for i, raw := range parsed.Messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		text := moderationTextContent(message["content"])
		if role == "system" || role == "developer" || (role == "user" && projectInstructionText(text) && i < len(parsed.Messages)-1) {
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
	parts = appendUniqueModerationText(parts, moderationTextContent(request["instructions"]))
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

// Stable instructions are cached independently; new questions also require
// joint assessment. Chunked results explicitly disclose limited cross-chunk context.
func moderationTaskChunks(task InputModerationTask, size int) ([]string, bool, error) {
	parts := append([]string(nil), task.Instructions...)
	parts = appendUniqueModerationText(parts, strings.TrimSpace(task.Text))
	chunks, err := moderationChunks(strings.Join(parts, "\n\n"), size)
	if err != nil {
		return nil, false, err
	}
	chunked := len(chunks) > 1
	for _, instruction := range task.Instructions {
		stable, err := moderationChunks(instruction, size)
		if err != nil {
			return nil, false, err
		}
		for _, text := range stable {
			chunks = appendUniqueModerationText(chunks, text)
		}
	}
	if len(chunks) > moderationMaxChunks {
		return nil, false, errModerationTooLarge
	}
	return chunks, chunked, nil
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
