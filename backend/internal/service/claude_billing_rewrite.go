package service

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
)

var claudeBillingCCHPattern = regexp.MustCompile(`cch=([0-9a-f]{5})`)

func resolveClaudeBillingCCHProfile(seed uint64, mode claudebilling.CCHInputMode, version string) (uint64, claudebilling.CCHInputMode) {
	if seed != 0 {
		if mode == "" {
			mode = claudebilling.CCHInputModeForCCVersion(version)
		}
		return seed, mode
	}
	defaultSeed, defaultMode := claudebilling.CCHProfileForCCVersion(version)
	return defaultSeed, defaultMode
}

func buildClaudeBillingHeaderPlaceholder(userAgent, entrypoint string) (string, bool) {
	version := ExtractCLIVersion(userAgent)
	if strings.TrimSpace(version) == "" {
		return "", false
	}
	if strings.TrimSpace(entrypoint) == "" {
		entrypoint = "cli"
	}
	return claudebilling.BuildHeader(version, "000", entrypoint, "00000"), true
}

func rewriteClaudeBillingHeaderForUserAgent(body []byte, userAgent string, cchSeed uint64) []byte {
	return rewriteClaudeBillingHeaderForUserAgentWithMode(body, userAgent, cchSeed, "")
}

func rewriteClaudeBillingHeaderForUserAgentWithMode(body []byte, userAgent string, cchSeed uint64, cchMode claudebilling.CCHInputMode) []byte {
	if len(body) == 0 || !bytes.Contains(body, []byte("x-anthropic-billing-header:")) {
		return body
	}

	prompt, err := claudebilling.ExtractFirstUserText(body)
	if err != nil {
		prompt = ""
	}

	version, _ := claudebilling.ExtractCCVersionFromBody(body)
	if uaVersion := ExtractCLIVersion(userAgent); uaVersion != "" {
		version = uaVersion
	}
	if strings.TrimSpace(version) == "" {
		return body
	}

	suffix := claudebilling.ComputeCCVersionSuffix(prompt, version)
	bodyWithVersion, err := claudebilling.ReplaceCCVersion(body, version, suffix)
	if err != nil {
		return body
	}

	normalizedBody, match, err := claudebilling.NormalizeBodyForCCH(bodyWithVersion)
	if err != nil {
		// 不返回 bodyWithVersion（cc_version 已改但 cch 未更新，会被检测）
		// 回退到原始 body 保持内部一致性
		return body
	}
	seed, mode := resolveClaudeBillingCCHProfile(cchSeed, cchMode, version)
	_, cch := claudebilling.ComputeCCHWithProfile(normalizedBody, seed, mode)
	return claudebilling.ReplaceCCH(normalizedBody, match, cch)
}

func rewriteClaudeBillingHeaderPreservingCCVersionWithMode(body []byte, cchSeed uint64, cchMode claudebilling.CCHInputMode) []byte {
	if len(body) == 0 || !bytes.Contains(body, []byte("x-anthropic-billing-header:")) {
		return body
	}
	match := claudeBillingCCHPattern.FindSubmatch(body)
	if len(match) < 2 {
		return body
	}
	if string(match[1]) == "00000" {
		return body
	}
	version, _ := claudebilling.ExtractCCVersionFromBody(body)
	normalizedBody, cchMatch, err := claudebilling.NormalizeBodyForCCH(body)
	if err != nil {
		return body
	}
	seed, mode := resolveClaudeBillingCCHProfile(cchSeed, cchMode, version)
	_, cch := claudebilling.ComputeCCHWithProfile(normalizedBody, seed, mode)
	return claudebilling.ReplaceCCH(normalizedBody, cchMatch, cch)
}
