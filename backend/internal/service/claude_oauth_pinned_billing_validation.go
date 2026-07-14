package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	"github.com/tidwall/gjson"
)

var ErrClaudeOAuthPinnedBillingValidationFailed = errors.New("claude oauth pinned billing validation failed")

func validatePinnedClaudeOAuthBillingWithResult(body []byte, requestedModel, userAgent string) (string, uint64, claudebilling.CCHInputMode, error) {
	clientVersion, observedSuffix := claudebilling.ExtractCCVersionFromBody(body)
	clientVersion = strings.TrimSpace(clientVersion)
	if clientVersion == "" {
		clientVersion = strings.TrimSpace(ExtractCLIVersion(userAgent))
	}
	if clientVersion == "" {
		clientVersion = "unknown"
	}

	modelID := strings.TrimSpace(requestedModel)
	if modelID == "" {
		modelID = strings.TrimSpace(gjson.GetBytes(body, "model").String())
	}
	if modelID == "" {
		modelID = "unknown"
	}

	fail := func(reason string) (string, error) {
		msg := fmt.Sprintf(
			"Claude OAuth pinned 模式要求使用原生 Claude Code 客户端（客户端版本=%s，请求模型=%s）：%s",
			clientVersion,
			modelID,
			reason,
		)
		return msg, fmt.Errorf("%w: %s", ErrClaudeOAuthPinnedBillingValidationFailed, reason)
	}
	failWithProfile := func(reason string) (string, uint64, claudebilling.CCHInputMode, error) {
		msg, err := fail(reason)
		return msg, 0, "", err
	}

	if observedSuffix == "" || clientVersion == "unknown" {
		return failWithProfile("x-anthropic-billing-header 中缺少或包含无效的 cc_version")
	}

	promptCandidates, err := claudebilling.CandidateUserTexts(body)
	if err != nil || len(promptCandidates) == 0 {
		return failWithProfile("无法提取首条用户消息文本，无法校验 cc_version")
	}
	suffixMatched, _ := claudebilling.MatchCCVersionSuffix(promptCandidates, clientVersion, observedSuffix)

	normalizedBody, cchMatch, err := claudebilling.NormalizeBodyForCCH(body)
	if err != nil {
		return failWithProfile("x-anthropic-billing-header 中缺少或包含无效的 cch")
	}
	if cchMatch.Value == "00000" {
		return failWithProfile("检测到 cch=00000；pinned 模式不接受此类请求，请使用原生 Claude Code 客户端")
	}
	cchCandidates := claudebilling.ComputeCCHCandidates(normalizedBody)
	matchedCandidate, cchMatched := claudebilling.SelectCCHCandidateForMatch(cchCandidates, cchMatch.Value, clientVersion)

	issues := make([]string, 0, 2)
	if !suffixMatched {
		issues = append(issues, fmt.Sprintf(
			"cc_version suffix 不匹配（观测=%s.%s，无候选 prompt 能复现）",
			clientVersion,
			observedSuffix,
		))
	}
	if !cchMatched {
		parts := make([]string, 0, len(cchCandidates))
		for _, cand := range cchCandidates {
			parts = append(parts, fmt.Sprintf("0x%016x/%s=%s", cand.Seed, cand.Mode, cand.Value))
		}
		issues = append(issues, fmt.Sprintf(
			"cch 不匹配（观测=%s，期望任一=%s）",
			cchMatch.Value,
			strings.Join(parts, "/"),
		))
	}
	if len(issues) > 0 {
		return failWithProfile(strings.Join(issues, "; "))
	}

	return "", matchedCandidate.Seed, matchedCandidate.Mode, nil
}
