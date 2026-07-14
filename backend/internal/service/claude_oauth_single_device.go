package service

import (
	"bufio"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"regexp"
	"strings"
)

var (
	claudeCLIUserAgentVersionPattern = regexp.MustCompile(`(?i)^claude-cli/\d+\.\d+\.\d+`)
	versionedUserAgentTokenPattern   = regexp.MustCompile(`(?i)^([a-z0-9._-]+)/\d+(?:\.\d+)+(?:[a-z0-9._-]*)?$`)
)

type SingleDeviceSlotState struct {
	Slot          int    `json:"slot"`
	SlotKey       string `json:"slot_key"`
	LastSeenAt    int64  `json:"last_seen_at"`
	LastUserAgent string `json:"last_user_agent,omitempty"`
}

type SingleDeviceSlotInfo struct {
	Slot                    int    `json:"slot"`
	SlotKey                 string `json:"slot_key"`
	LastSeenAt              int64  `json:"last_seen_at,omitempty"`
	LastUserAgent           string `json:"last_user_agent,omitempty"`
	UserAgent               string `json:"user_agent,omitempty"`
	Accept                  string `json:"accept,omitempty"`
	ContentType             string `json:"content_type,omitempty"`
	AcceptEncoding          string `json:"accept_encoding,omitempty"`
	StainlessLang           string `json:"stainless_lang,omitempty"`
	StainlessPackageVersion string `json:"stainless_package_version,omitempty"`
	StainlessOS             string `json:"stainless_os,omitempty"`
	StainlessArch           string `json:"stainless_arch,omitempty"`
	StainlessRuntime        string `json:"stainless_runtime,omitempty"`
	StainlessRuntimeVersion string `json:"stainless_runtime_version,omitempty"`
	XApp                    string `json:"x_app,omitempty"`
	AnthropicVersion        string `json:"anthropic_version,omitempty"`
}

func NormalizeClaudeOAuthSingleDeviceSlotKey(userAgent string) string {
	trimmed := strings.TrimSpace(userAgent)
	if trimmed == "" {
		return ""
	}
	trimmed = claudeCLIUserAgentVersionPattern.ReplaceAllString(trimmed, "claude-cli")
	openIdx := strings.Index(trimmed, "(")
	closeIdx := strings.LastIndex(trimmed, ")")
	if openIdx < 0 || closeIdx <= openIdx {
		return strings.ToLower(strings.TrimSpace(trimmed))
	}

	head := strings.ToLower(strings.TrimSpace(trimmed[:openIdx]))
	inside := trimmed[openIdx+1 : closeIdx]
	parts := strings.Split(inside, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		token := strings.ToLower(strings.TrimSpace(part))
		if token == "" {
			continue
		}
		if matches := versionedUserAgentTokenPattern.FindStringSubmatch(token); len(matches) == 2 {
			token = matches[1]
		}
		out = append(out, token)
	}
	if len(out) == 0 {
		return head
	}
	return head + " (" + strings.Join(out, ", ") + ")"
}

func ParseClaudeOAuthFixedHeadersText(raw string) (map[string]string, error) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	out := make(map[string]string)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(strings.TrimSuffix(scanner.Text(), "\r"))
		if line == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			return nil, fmt.Errorf("invalid fixed header line %d", lineNo)
		}
		key := http.CanonicalHeaderKey(strings.TrimSpace(line[:idx]))
		value := strings.TrimSpace(line[idx+1:])
		if key == "" {
			return nil, fmt.Errorf("invalid fixed header line %d", lineNo)
		}
		out[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func ValidateClaudeOAuthSingleDeviceFixedHeaders(headersText string) error {
	_, err := ParseClaudeOAuthFixedHeadersText(headersText)
	return err
}

func ValidateClaudeOAuthSingleDeviceIngressHeaders(headers http.Header, fixedHeaders map[string]string) error {
	_ = headers
	_ = fixedHeaders
	return nil
}

func ApplyClaudeOAuthFixedHeaders(req *http.Request, fixedHeaders map[string]string) {
	if req == nil || len(fixedHeaders) == 0 {
		return
	}
	for key, value := range fixedHeaders {
		req.Header.Set(key, value)
	}
}

func CloneHeadersWithOverrides(headers http.Header, overrides map[string]string) http.Header {
	cloned := headers.Clone()
	if cloned == nil {
		cloned = http.Header{}
	}
	for key, value := range overrides {
		cloned.Set(key, value)
	}
	return cloned
}

func HasClaudeOAuthFixedHeader(fixedHeaders map[string]string, key string) bool {
	if len(fixedHeaders) == 0 {
		return false
	}
	value, ok := fixedHeaders[http.CanonicalHeaderKey(key)]
	return ok && strings.TrimSpace(value) != ""
}

func GenerateTransportIsolationAccountID(accountID int64, isolationKey string) int64 {
	if accountID <= 0 {
		return accountID
	}
	trimmed := strings.TrimSpace(isolationKey)
	if trimmed == "" {
		return accountID
	}
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d::%s", accountID, trimmed)
	sum := h.Sum64() & ((1 << 63) - 1)
	if sum == 0 {
		return accountID
	}
	return int64(sum)
}

func validateClaudeOAuthSingleDeviceConfig(account *Account) error {
	if account == nil || account.GetClaudeOAuthMode() != ClaudeOAuthModeSingleDevice {
		return nil
	}
	if strings.TrimSpace(account.GetExtraString("account_uuid")) == "" {
		return errors.New("single_device mode requires account_uuid")
	}
	if strings.TrimSpace(account.GetExtraString("claude_oauth_fixed_device_id")) == "" {
		return errors.New("single_device mode requires claude_oauth_fixed_device_id")
	}
	if text := strings.TrimSpace(account.getExtraString("claude_oauth_fixed_headers_text")); text != "" {
		if err := ValidateClaudeOAuthSingleDeviceFixedHeaders(text); err != nil {
			return err
		}
	}
	return nil
}
