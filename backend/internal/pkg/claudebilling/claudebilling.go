package claudebilling

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/gjson"
)

const (
	// CCHSeed 是 Claude Code ≤ 2.1.108 使用的 xxh64 seed（原算法）。
	// 仍被 rewrite 路径（shared / single_device）复用作为默认 seed。
	CCHSeed uint64 = 0x6E52736AC806831E
	// CCHSeedV2 是 Claude Code ≥ 2.1.110 的 xxh64 seed。算法结构（xxh64 + 低 20 位 hex）
	// 完全不变，只换 seed。2026-04-24 从外部讨论获得，用 2.1.110/114 dump 两独立样本验证。
	CCHSeedV2     uint64 = 0x4D659218E32A3268
	CCVersionSalt        = "59cf53e54c78"
	// CCHSeedV2MinVersion 是已验证的新 seed 分界点：2.1.108 仍是 V1，2.1.110 起为 V2。
	CCHSeedV2MinVersion = "2.1.110"
	// CCHFilteredBodyMinVersion 是 2.1.172+ 静态边界候选；2.1.185 样本已验证。
	CCHFilteredBodyMinVersion = "2.1.172"
	// CCHMissingTokenModeMinVersion 是已实测的 CLAUDE_CODE_OAUTH_TOKEN /
	// setup-token 形态缺省 cch 的公开版本边界：2.1.179 仍带 cch，2.1.181 起不带。
	CCHMissingTokenModeMinVersion = "2.1.181"
)

type CCHInputMode string

const (
	CCHInputModeFullBody       CCHInputMode = "full_body"
	CCHInputModeFilteredBodyV2 CCHInputMode = "filtered_body_v2"
)

type cchValidationProfile struct {
	Seed uint64
	Mode CCHInputMode
}

// cchValidationProfiles 枚举所有已知合法 seed + input mode。
// 严格校验路径接受任一 profile 命中；新增模式时 append 一项即可让校验日志带上候选。
var cchValidationProfiles = []cchValidationProfile{
	{Seed: CCHSeed, Mode: CCHInputModeFullBody},
	{Seed: CCHSeedV2, Mode: CCHInputModeFullBody},
	{Seed: CCHSeedV2, Mode: CCHInputModeFilteredBodyV2},
}

var (
	cchPattern = regexp.MustCompile(`cch=([0-9a-f]{5})`)
	// billingHeaderPattern is intentionally stricter than the loose field regexes:
	// production validation must operate on the current request's billing header,
	// not on old header text embedded in conversation history.
	billingHeaderPattern = regexp.MustCompile(`x-anthropic-billing-header:\s*cc_version=([0-9]+\.[0-9]+\.[0-9]+)\.([0-9a-f]{3});\s*cc_entrypoint=([^;]+);\s*(cch=([0-9a-f]{5}));`)
	// billingHeaderBasePattern intentionally accepts the 2.1.181+ token-mode
	// shape where the active billing header has cc_version + cc_entrypoint but
	// no cch field. Do not use it for CCH normalization.
	billingHeaderBasePattern = regexp.MustCompile(`x-anthropic-billing-header:\s*cc_version=([0-9]+\.[0-9]+\.[0-9]+)\.([0-9a-f]{3});\s*cc_entrypoint=([^;]+);`)
	// billingCCHPattern scopes cch normalization to the actual Claude billing header.
	// Conversation history can contain old "cch=xxxxx" text snippets; those are not
	// part of the client attestation and must not make validation fail.
	billingCCHPattern = regexp.MustCompile(`x-anthropic-billing-header:\s*cc_version=[^;]+;\s*cc_entrypoint=[^;]+;\s*(cch=([0-9a-f]{5}));`)
	// version 固定 M.m.p semver，suffix 必填 3 位 hex。
	// 注意：suffix 在 [0-9a-f]{3} 下可能是纯数字（如 "662"），必须先固定 version 段
	// 不能让它贪婪吃掉 suffix。
	ccVersionPattern  = regexp.MustCompile(`cc_version=([0-9]+\.[0-9]+\.[0-9]+)\.([0-9a-f]{3});`)
	ccEntrypointRegex = regexp.MustCompile(`cc_entrypoint=([^;]+);`)
)

type billingHeaderMatch struct {
	FullStart       int
	FullEnd         int
	CCVersionStart  int
	CCVersionEnd    int
	VersionStart    int
	VersionEnd      int
	SuffixStart     int
	SuffixEnd       int
	EntrypointStart int
	EntrypointEnd   int
	CCHFullStart    int
	CCHFullEnd      int
	CCHValueStart   int
	CCHValueEnd     int
	Version         string
	Suffix          string
	Entrypoint      string
	CCH             string
}

type CCHMatch struct {
	FullStart  int
	FullEnd    int
	ValueStart int
	ValueEnd   int
	Value      string
}

func ExtractFirstUserText(body []byte) (string, error) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return "", fmt.Errorf("messages array not found")
	}
	var skippedMeta string
	for _, msg := range messages.Array() {
		if msg.Get("role").String() != "user" {
			continue
		}
		content := msg.Get("content")
		if !content.Exists() {
			return "", fmt.Errorf("user message content not found")
		}
		if content.Type == gjson.String {
			text := content.String()
			if isMetaText(text) {
				if skippedMeta == "" {
					skippedMeta = text
				}
				continue
			}
			return strings.TrimSpace(text), nil
		}
		if content.IsArray() {
			for _, part := range content.Array() {
				if part.Get("type").String() != "text" {
					continue
				}
				text := part.Get("text").String()
				if isMetaText(text) {
					if skippedMeta == "" {
						skippedMeta = text
					}
					continue
				}
				return strings.TrimSpace(text), nil
			}
			continue
		}
	}
	if skippedMeta != "" {
		return strings.TrimSpace(skippedMeta), nil
	}
	return "", fmt.Errorf("no user message found")
}

// isMetaText 判断首条 user text 是否是 Claude Code 自动注入的 meta 块（不是用户真实输入）。
// Claude Code 在计算 cc_version suffix 时会跳过这类 meta block 用下一条 user text。
// 仅用于 ExtractFirstUserText 的"显示/日志"用途——给 CLI 或错误消息选一个更可读的
// prompt 预览。
// 严格校验路径（carpool / pinned）不要依赖此函数；它的前缀清单天然滞后，每次
// Claude Code 新增一种注入 wrapper 都得追。校验改用 CandidateUserTexts 穷举首条
// user message 里所有 text block，把"哪一条是客户端真 fingerprint 输入"交给 suffix
// 命中检测自己回答。
// 已观察到的 meta 前缀（保留用于显示选择）：
//   - <system-reminder>（interactive TUI MCP instructions / skills / context）
//   - <available-deferred-tools>（native 2.1.81+）
//   - <local-command-caveat> / <command-name> / <local-command-stdout> / <local-command-stderr>
//     （`/` 本地命令执行包装，2.1.119 观察到）
func isMetaText(text string) bool {
	t := strings.TrimLeft(text, " \t\n\r")
	return strings.HasPrefix(t, "<available-deferred-tools>") ||
		strings.HasPrefix(t, "<system-reminder>") ||
		strings.HasPrefix(t, "<local-command-caveat>") ||
		strings.HasPrefix(t, "<command-name>") ||
		strings.HasPrefix(t, "<local-command-stdout>") ||
		strings.HasPrefix(t, "<local-command-stderr>")
}

// CandidateUserTexts 返回所有 role=user 消息里所有 text block 的 text（已 TrimSpace），
// 按 wire body 原顺序。用于严格校验 cc_version suffix 时做"任一候选命中"枚举。
//
// 为什么要枚举：客户端 fingerprint 是在 messagesForAPI 上算的（leak 源码
// src/services/api/claude.ts:1325），早于 `<system-reminder>` / `<local-command-*>`
// 等 meta 块被注入到出 wire body。我们在 gateway 看到的 body 已经注入过 meta，
// 无法可靠地从 wire 结构上识别哪个 text 是客户端 fingerprint 的输入。
// 穷举所有 text 候选并比 suffix，是对 meta 白名单维护的解耦。
// 2.1.128+ 的续聊 / 工具调用场景里，第一条 user 可能只是 tool meta，真实
// fingerprint 输入会出现在后续 user message 中，所以这里不能只看第一条 user。
func CandidateUserTexts(body []byte) ([]string, error) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return nil, fmt.Errorf("messages array not found")
	}
	out := make([]string, 0)
	for _, msg := range messages.Array() {
		if msg.Get("role").String() != "user" {
			continue
		}
		content := msg.Get("content")
		if !content.Exists() {
			continue
		}
		if content.Type == gjson.String {
			out = append(out, strings.TrimSpace(content.String()))
			continue
		}
		if content.IsArray() {
			for _, part := range content.Array() {
				if part.Get("type").String() != "text" {
					continue
				}
				out = append(out, strings.TrimSpace(part.Get("text").String()))
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no user text candidates found")
	}
	return out, nil
}

// MatchCCVersionSuffix 判断任一 text 候选能否复现观察到的 3-hex suffix。
// 返回 (matched, matchedPrompt) —— 命中时 matchedPrompt 可用于日志记录。
func MatchCCVersionSuffix(candidates []string, version, observedSuffix string) (bool, string) {
	for _, p := range candidates {
		if ComputeCCVersionSuffix(p, version) == observedSuffix {
			return true, p
		}
	}
	return false, ""
}

func ExtractCCVersionFromBody(body []byte) (version string, suffix string) {
	if matches, scoped := billingHeaderBaseMatchesInSystem(body); scoped {
		if len(matches) > 0 {
			return matches[0].Version, matches[0].Suffix
		}
		return "", ""
	}
	match := ccVersionPattern.FindSubmatch(body)
	if len(match) >= 2 {
		version = string(match[1])
	}
	if len(match) >= 3 {
		suffix = string(match[2])
	}
	return version, suffix
}

func ExtractCCEntrypointFromBody(body []byte) (string, bool) {
	if matches, scoped := billingHeaderBaseMatchesInSystem(body); scoped {
		if len(matches) > 0 {
			return matches[0].Entrypoint, true
		}
		return "", false
	}
	match := ccEntrypointRegex.FindSubmatch(body)
	if len(match) < 2 {
		return "", false
	}
	return string(match[1]), true
}

// PickCCVersionChars 取首条 user text 的第 4 / 7 / 20 个**字符**（非字节），
// 与客户端 JS 的 string indexing 对齐（BMP 范围下 UTF-16 code unit = codepoint）。
// 历史 bug：之前用 prompt[idx] 是 Go byte indexing，对 ASCII 等价但对 Chinese
// 等多字节 UTF-8 字符永远算错，导致非 ASCII prompt 全部 cc_version_mismatch。
// The accepted fingerprint inputs are documented by the compatibility tests in
// this package. Keep input selection separate from the hash implementation.
func PickCCVersionChars(prompt string) string {
	runes := []rune(prompt)
	picked := make([]rune, 3)
	for i, idx := range []int{4, 7, 20} {
		if idx < len(runes) {
			picked[i] = runes[idx]
		} else {
			picked[i] = '0'
		}
	}
	return string(picked)
}

func ComputeCCVersionSuffix(prompt, version string) string {
	sum := sha256.Sum256([]byte(CCVersionSalt + PickCCVersionChars(prompt) + version))
	return fmt.Sprintf("%x", sum[:])[:3]
}

func ReplaceCCVersion(body []byte, version, suffix string) ([]byte, error) {
	if matches, scoped := billingHeaderMatchesInSystem(body); scoped {
		if len(matches) == 0 {
			return nil, errors.New("no cc_version field found")
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("expected exactly 1 cc_version field, found %d", len(matches))
		}
		m := matches[0]
		out := make([]byte, 0, len(body)+8)
		out = append(out, body[:m.CCVersionStart]...)
		out = append(out, []byte(fmt.Sprintf("cc_version=%s.%s;", version, suffix))...)
		out = append(out, body[m.CCVersionEnd:]...)
		return out, nil
	}
	indices := ccVersionPattern.FindAllSubmatchIndex(body, -1)
	if len(indices) == 0 {
		return nil, errors.New("no cc_version field found")
	}
	if len(indices) != 1 {
		return nil, fmt.Errorf("expected exactly 1 cc_version field, found %d", len(indices))
	}
	idx := indices[0]
	out := make([]byte, 0, len(body)+8)
	out = append(out, body[:idx[0]]...)
	out = append(out, []byte(fmt.Sprintf("cc_version=%s.%s;", version, suffix))...)
	out = append(out, body[idx[1]:]...)
	return out, nil
}

func NormalizeBodyForCCH(body []byte) ([]byte, CCHMatch, error) {
	if matches, scoped := billingHeaderMatchesInSystem(body); scoped {
		if len(matches) == 0 {
			return nil, CCHMatch{}, errors.New("no cch=xxxxx field found")
		}
		if len(matches) != 1 {
			return nil, CCHMatch{}, fmt.Errorf("expected exactly 1 billing cch field, found %d", len(matches))
		}
		m := matches[0]
		match := CCHMatch{
			FullStart:  m.CCHFullStart,
			FullEnd:    m.CCHFullEnd,
			ValueStart: m.CCHValueStart,
			ValueEnd:   m.CCHValueEnd,
			Value:      m.CCH,
		}
		normalized := bytes.Clone(body)
		copy(normalized[match.ValueStart:match.ValueEnd], []byte("00000"))
		return normalized, match, nil
	}

	if billingIndices := billingCCHPattern.FindAllSubmatchIndex(body, -1); len(billingIndices) > 0 {
		if len(billingIndices) != 1 {
			return nil, CCHMatch{}, fmt.Errorf("expected exactly 1 billing cch field, found %d", len(billingIndices))
		}
		idx := billingIndices[0]
		match := CCHMatch{
			FullStart:  idx[2],
			FullEnd:    idx[3],
			ValueStart: idx[4],
			ValueEnd:   idx[5],
			Value:      string(body[idx[4]:idx[5]]),
		}
		normalized := bytes.Clone(body)
		copy(normalized[match.ValueStart:match.ValueEnd], []byte("00000"))
		return normalized, match, nil
	}

	indices := cchPattern.FindAllSubmatchIndex(body, -1)
	if len(indices) == 0 {
		return nil, CCHMatch{}, errors.New("no cch=xxxxx field found")
	}
	if len(indices) != 1 {
		return nil, CCHMatch{}, fmt.Errorf("expected exactly 1 cch=xxxxx field, found %d", len(indices))
	}

	idx := indices[0]
	match := CCHMatch{
		FullStart:  idx[0],
		FullEnd:    idx[1],
		ValueStart: idx[2],
		ValueEnd:   idx[3],
		Value:      string(body[idx[2]:idx[3]]),
	}

	normalized := bytes.Clone(body)
	copy(normalized[match.ValueStart:match.ValueEnd], []byte("00000"))
	return normalized, match, nil
}

// ComputeCCH 用默认 seed（CCHSeed，等价于 Claude Code ≤ 2.1.108）计算 cch。
// 新的 rewrite 路径应按客户端版本或校验命中的 seed 调用 ComputeCCHWithSeed；
// 此函数只保留给明确需要旧默认 seed 的兼容调用。
func ComputeCCH(normalizedBody []byte) (uint64, string) {
	return ComputeCCHWithSeed(normalizedBody, CCHSeed)
}

// ComputeCCHWithSeed 用给定 seed 计算 xxh64 并返回低 20 位的 5-hex cch。
func ComputeCCHWithSeed(normalizedBody []byte, seed uint64) (uint64, string) {
	d := xxhash.NewWithSeed(seed)
	_, _ = d.Write(normalizedBody)
	sum := d.Sum64()
	return sum, fmt.Sprintf("%05x", sum&0xFFFFF)
}

func ComputeCCHWithProfile(normalizedBody []byte, seed uint64, mode CCHInputMode) (uint64, string) {
	return ComputeCCHWithSeed(CCHInputForMode(normalizedBody, mode), seed)
}

// SeedForCCVersion 返回指定 Claude Code 版本应使用的 CCH seed。
func SeedForCCVersion(version string) uint64 {
	if strings.TrimSpace(version) != "" && compareCCSemver(version, CCHSeedV2MinVersion) >= 0 {
		return CCHSeedV2
	}
	return CCHSeed
}

func CCHInputModeForCCVersion(version string) CCHInputMode {
	if strings.TrimSpace(version) != "" && compareCCSemver(version, CCHFilteredBodyMinVersion) >= 0 {
		return CCHInputModeFilteredBodyV2
	}
	return CCHInputModeFullBody
}

func CCHProfileForCCVersion(version string) (uint64, CCHInputMode) {
	return SeedForCCVersion(version), CCHInputModeForCCVersion(version)
}

func AllowsMissingCCHInTokenMode(version string) bool {
	return strings.TrimSpace(version) != "" && compareCCSemver(version, CCHMissingTokenModeMinVersion) >= 0
}

// CCHCandidate 描述单个 seed 下计算出的 cch 候选，供严格校验路径比对。
type CCHCandidate struct {
	Seed  uint64
	Mode  CCHInputMode
	Sum64 uint64
	Value string
}

// ComputeCCHCandidates 对 cchValidationProfiles 中所有已知合法 profile 计算候选 cch。
// 严格校验路径比较观察值与任一候选 Value 是否相等；全不中才拒。
func ComputeCCHCandidates(normalizedBody []byte) []CCHCandidate {
	out := make([]CCHCandidate, 0, len(cchValidationProfiles))
	for _, profile := range cchValidationProfiles {
		sum, value := ComputeCCHWithProfile(normalizedBody, profile.Seed, profile.Mode)
		out = append(out, CCHCandidate{Seed: profile.Seed, Mode: profile.Mode, Sum64: sum, Value: value})
	}
	return out
}

// SelectCCHCandidateForMatch chooses the seed + input mode for an observed cch match.
// If multiple known profiles collide on the same 5-hex cch, cc_version acts as the tiebreaker.
func SelectCCHCandidateForMatch(candidates []CCHCandidate, observedValue, version string) (CCHCandidate, bool) {
	observedValue = strings.TrimSpace(observedValue)
	matches := make([]CCHCandidate, 0, 1)
	for _, cand := range candidates {
		if cand.Value == observedValue {
			matches = append(matches, cand)
		}
	}
	if len(matches) == 0 {
		return CCHCandidate{}, false
	}
	if len(matches) == 1 {
		return matches[0], true
	}

	preferredSeed, preferredMode := CCHProfileForCCVersion(version)
	for _, cand := range matches {
		if cand.Seed == preferredSeed && normalizeCCHInputMode(cand.Mode) == preferredMode {
			return cand, true
		}
	}
	for _, cand := range matches {
		if cand.Seed == preferredSeed {
			return cand, true
		}
	}
	return matches[0], true
}

// SelectCCHSeedForMatch is kept for older call sites/tests that only need the seed.
func SelectCCHSeedForMatch(candidates []CCHCandidate, observedValue, version string) (uint64, bool) {
	cand, ok := SelectCCHCandidateForMatch(candidates, observedValue, version)
	return cand.Seed, ok
}

func CCHInputForMode(normalizedBody []byte, mode CCHInputMode) []byte {
	switch normalizeCCHInputMode(mode) {
	case CCHInputModeFilteredBodyV2:
		return filterCCHBodyV2(normalizedBody)
	default:
		return normalizedBody
	}
}

func normalizeCCHInputMode(mode CCHInputMode) CCHInputMode {
	switch mode {
	case CCHInputModeFilteredBodyV2:
		return CCHInputModeFilteredBodyV2
	default:
		return CCHInputModeFullBody
	}
}

func filterCCHBodyV2(body []byte) []byte {
	const modelPattern = `"model":"`
	cursor := 0
	chunks := make([][]byte, 0, 8)
	for cursor < len(body) {
		skipRange, hasSkip := findNextCCHFilteredSkipRange(body, cursor)
		modelKey := bytes.Index(body[cursor:], []byte(modelPattern))
		if modelKey >= 0 {
			modelKey += cursor
		}

		if hasSkip && (modelKey < 0 || skipRange.start < modelKey) {
			chunks = append(chunks, body[cursor:skipRange.start])
			cursor = skipRange.end
			continue
		}

		if modelKey >= 0 {
			valueStart := modelKey + len(modelPattern)
			valueEnd, ok := findJSONStringEnd(body, valueStart)
			if !ok {
				chunks = append(chunks, body[cursor:])
				break
			}
			// Keep `"model":"` and the closing quote, but omit the string value.
			chunks = append(chunks, body[cursor:valueStart])
			cursor = valueEnd
			continue
		}

		chunks = append(chunks, body[cursor:])
		break
	}
	return bytes.Join(chunks, nil)
}

func findNextCCHFilteredSkipRange(body []byte, start int) (rawSpan, bool) {
	ranges := make([]rawSpan, 0, 3)
	if span, ok := findFallbacksRange(body, start); ok {
		ranges = append(ranges, span)
	}
	if span, ok := findFallbackCreditTokenRange(body, start); ok {
		ranges = append(ranges, span)
	}
	if span, ok := findMaxTokensRange(body, start); ok {
		ranges = append(ranges, span)
	}
	if len(ranges) == 0 {
		return rawSpan{}, false
	}
	best := ranges[0]
	for _, span := range ranges[1:] {
		if span.start < best.start {
			best = span
		}
	}
	return best, true
}

func findFallbacksRange(body []byte, start int) (rawSpan, bool) {
	pattern := []byte(`"fallbacks":[`)
	keyStart := bytes.Index(body[start:], pattern)
	if keyStart < 0 {
		return rawSpan{}, false
	}
	keyStart += start
	arrayStart := keyStart + len(pattern) - 1
	arrayEnd, ok := findJSONArrayEnd(body, arrayStart)
	if !ok {
		return rawSpan{}, false
	}
	return adjustCCHFilteredFieldRange(body, keyStart, arrayEnd+1, start), true
}

func findFallbackCreditTokenRange(body []byte, start int) (rawSpan, bool) {
	pattern := []byte(`"fallback_credit_token":"`)
	keyStart := bytes.Index(body[start:], pattern)
	if keyStart < 0 {
		return rawSpan{}, false
	}
	keyStart += start
	valueStart := keyStart + len(pattern)
	valueEnd, ok := findJSONStringEnd(body, valueStart)
	if !ok {
		return rawSpan{}, false
	}
	return adjustCCHFilteredFieldRange(body, keyStart, valueEnd+1, start), true
}

func findMaxTokensRange(body []byte, start int) (rawSpan, bool) {
	pattern := []byte(`"max_tokens":`)
	searchFrom := start
	for searchFrom < len(body) {
		keyStart := bytes.Index(body[searchFrom:], pattern)
		if keyStart < 0 {
			return rawSpan{}, false
		}
		keyStart += searchFrom
		pos := keyStart + len(pattern)
		for pos < len(body) && body[pos] >= '0' && body[pos] <= '9' {
			pos++
		}
		if pos > keyStart+len(pattern) {
			return adjustCCHFilteredFieldRange(body, keyStart, pos, start), true
		}
		searchFrom = keyStart + 1
	}
	return rawSpan{}, false
}

func adjustCCHFilteredFieldRange(body []byte, start, end, lowerBound int) rawSpan {
	if end < len(body) && body[end] == ',' {
		return rawSpan{start: start, end: end + 1}
	}
	if start > lowerBound && body[start-1] == ',' {
		return rawSpan{start: start - 1, end: end}
	}
	return rawSpan{start: start, end: end}
}

func findJSONStringEnd(body []byte, valueStart int) (int, bool) {
	for pos := valueStart; pos < len(body); {
		switch body[pos] {
		case '"':
			return pos, true
		case '\\':
			pos += 2
		default:
			pos++
		}
	}
	return 0, false
}

func findJSONArrayEnd(body []byte, arrayStart int) (int, bool) {
	depth := 1
	inString := false
	for pos := arrayStart + 1; pos < len(body); {
		if inString {
			switch body[pos] {
			case '"':
				inString = false
				pos++
			case '\\':
				pos += 2
			default:
				pos++
			}
			continue
		}

		switch body[pos] {
		case '"':
			inString = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return pos, true
			}
		}
		pos++
	}
	return 0, false
}

func compareCCSemver(a, b string) int {
	aParts := parseCCSemver(a)
	bParts := parseCCSemver(b)
	for i := 0; i < 3; i++ {
		if aParts[i] < bParts[i] {
			return -1
		}
		if aParts[i] > bParts[i] {
			return 1
		}
	}
	return 0
}

func parseCCSemver(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	result := [3]int{}
	for i := 0; i < len(parts) && i < len(result); i++ {
		if parsed, err := strconv.Atoi(parts[i]); err == nil {
			result[i] = parsed
		}
	}
	return result
}

func ReplaceCCH(body []byte, match CCHMatch, cch string) []byte {
	out := bytes.Clone(body)
	copy(out[match.ValueStart:match.ValueEnd], []byte(cch))
	return out
}

func BuildHeader(version, suffix, entrypoint, cch string) string {
	return fmt.Sprintf(
		"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=%s;",
		version, suffix, entrypoint, cch,
	)
}

type rawSpan struct {
	start int
	end   int
}

func billingHeaderMatchesInSystem(body []byte) ([]billingHeaderMatch, bool) {
	spans, scoped := systemTextRawSpans(body)
	if !scoped {
		return nil, false
	}
	out := make([]billingHeaderMatch, 0, 1)
	for _, span := range spans {
		if span.start < 0 || span.end > len(body) || span.start >= span.end {
			continue
		}
		for _, idx := range billingHeaderPattern.FindAllSubmatchIndex(body[span.start:span.end], -1) {
			if len(idx) < 12 {
				continue
			}
			abs := func(i int) int {
				if i < 0 {
					return -1
				}
				return span.start + i
			}
			m := billingHeaderMatch{
				FullStart:       abs(idx[0]),
				FullEnd:         abs(idx[1]),
				VersionStart:    abs(idx[2]),
				VersionEnd:      abs(idx[3]),
				SuffixStart:     abs(idx[4]),
				SuffixEnd:       abs(idx[5]),
				EntrypointStart: abs(idx[6]),
				EntrypointEnd:   abs(idx[7]),
				CCHFullStart:    abs(idx[8]),
				CCHFullEnd:      abs(idx[9]),
				CCHValueStart:   abs(idx[10]),
				CCHValueEnd:     abs(idx[11]),
				Version:         string(body[abs(idx[2]):abs(idx[3])]),
				Suffix:          string(body[abs(idx[4]):abs(idx[5])]),
				Entrypoint:      string(body[abs(idx[6]):abs(idx[7])]),
				CCH:             string(body[abs(idx[10]):abs(idx[11])]),
			}
			m.CCVersionStart = m.FullStart + strings.Index(string(body[m.FullStart:m.FullEnd]), "cc_version=")
			if m.CCVersionStart >= m.FullStart {
				m.CCVersionEnd = m.SuffixEnd + 1
			}
			out = append(out, m)
		}
	}
	return out, true
}

func billingHeaderBaseMatchesInSystem(body []byte) ([]billingHeaderMatch, bool) {
	spans, scoped := systemTextRawSpans(body)
	if !scoped {
		return nil, false
	}
	out := make([]billingHeaderMatch, 0, 1)
	for _, span := range spans {
		if span.start < 0 || span.end > len(body) || span.start >= span.end {
			continue
		}
		for _, idx := range billingHeaderBasePattern.FindAllSubmatchIndex(body[span.start:span.end], -1) {
			if len(idx) < 8 {
				continue
			}
			abs := func(i int) int {
				if i < 0 {
					return -1
				}
				return span.start + i
			}
			m := billingHeaderMatch{
				FullStart:       abs(idx[0]),
				FullEnd:         abs(idx[1]),
				VersionStart:    abs(idx[2]),
				VersionEnd:      abs(idx[3]),
				SuffixStart:     abs(idx[4]),
				SuffixEnd:       abs(idx[5]),
				EntrypointStart: abs(idx[6]),
				EntrypointEnd:   abs(idx[7]),
				CCHFullStart:    -1,
				CCHFullEnd:      -1,
				CCHValueStart:   -1,
				CCHValueEnd:     -1,
				Version:         string(body[abs(idx[2]):abs(idx[3])]),
				Suffix:          string(body[abs(idx[4]):abs(idx[5])]),
				Entrypoint:      string(body[abs(idx[6]):abs(idx[7])]),
			}
			m.CCVersionStart = m.FullStart + strings.Index(string(body[m.FullStart:m.FullEnd]), "cc_version=")
			if m.CCVersionStart >= m.FullStart {
				m.CCVersionEnd = m.SuffixEnd + 1
			}
			out = append(out, m)
		}
	}
	return out, true
}

func systemTextRawSpans(body []byte) ([]rawSpan, bool) {
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		return nil, false
	}
	out := make([]rawSpan, 0)
	add := func(r gjson.Result) {
		if r.Raw == "" {
			return
		}
		start := r.Index
		end := start + len(r.Raw)
		if start >= 0 && end <= len(body) && string(body[start:end]) == r.Raw {
			out = append(out, rawSpan{start: start, end: end})
			return
		}
		if pos := bytes.Index(body, []byte(r.Raw)); pos >= 0 {
			out = append(out, rawSpan{start: pos, end: pos + len(r.Raw)})
		}
	}
	if system.Type == gjson.String {
		add(system)
		return out, true
	}
	if system.IsArray() {
		for _, item := range system.Array() {
			if item.Type == gjson.String {
				add(item)
				continue
			}
			text := item.Get("text")
			if text.Type == gjson.String {
				add(text)
			}
		}
	}
	return out, true
}
