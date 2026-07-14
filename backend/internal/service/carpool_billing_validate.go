package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/billingreject"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

var ErrClaudeOAuthCarpoolBillingMismatch = errors.New("claude oauth carpool billing header integrity check failed")

var claudeCLIUAEntrypointPattern = regexp.MustCompile(`\(external,\s*([^)]+)\)`)

const carpoolBillingMismatchMessage = "Claude OAuth carpool billing header integrity check failed"

type claudeOAuthRealClaudeCodeDiagnostic struct {
	Valid                             bool   `json:"valid"`
	Reason                            string `json:"reason,omitempty"`
	BodyBytes                         int    `json:"body_bytes"`
	HasOAuthBeta                      bool   `json:"has_oauth_beta"`
	HasMetadataUserID                 bool   `json:"has_metadata_user_id"`
	MetadataParseOK                   bool   `json:"metadata_parse_ok"`
	MetadataAccountUUIDEmpty          bool   `json:"metadata_account_uuid_empty"`
	HasDeviceID                       bool   `json:"has_device_id"`
	HasSessionID                      bool   `json:"has_session_id"`
	LoginStateUserEmailContextPresent bool   `json:"login_state_user_email_context_present"`
	HasSystem                         bool   `json:"has_system"`
	UserAgent                         string `json:"user_agent,omitempty"`
	UAVersion                         string `json:"ua_version,omitempty"`
	BodyCCVersion                     string `json:"body_cc_version,omitempty"`
	BodyCCSuffixPresent               bool   `json:"body_cc_suffix_present"`
	VersionMatch                      bool   `json:"version_match"`
	UAEntrypoint                      string `json:"ua_entrypoint,omitempty"`
	BodyEntrypoint                    string `json:"body_entrypoint,omitempty"`
	BodyEntrypointFound               bool   `json:"body_entrypoint_found"`
	EntrypointMatch                   bool   `json:"entrypoint_match"`
	CCHPresent                        bool   `json:"cch_present"`
	CCHValue                          string `json:"cch_value,omitempty"`
	CCHPlaceholder                    bool   `json:"cch_placeholder"`
	CCHValid                          bool   `json:"cch_valid"`
	CCHSeed                           uint64 `json:"-"`
	CCHSeedHex                        string `json:"cch_seed,omitempty"`
	CCHMode                           string `json:"cch_mode,omitempty"`
	CCHNormalizeError                 string `json:"cch_normalize_error,omitempty"`
	MissingCCHTokenModeAllowed        bool   `json:"missing_cch_token_mode_allowed"`
}

type carpoolBillingRejectDiagnostic struct {
	Reason                            string `json:"reason"`
	Detail                            string `json:"detail,omitempty"`
	BodyBytes                         int    `json:"body_bytes"`
	HasBillingHeader                  bool   `json:"has_billing_header"`
	UserAgent                         string `json:"user_agent,omitempty"`
	UAVersion                         string `json:"ua_version,omitempty"`
	BodyCCVersion                     string `json:"body_cc_version,omitempty"`
	BodyEntrypoint                    string `json:"body_entrypoint,omitempty"`
	BodyEntrypointFound               bool   `json:"body_entrypoint_found"`
	CCHPresent                        bool   `json:"cch_present"`
	CCHValue                          string `json:"cch_value,omitempty"`
	MetadataParseOK                   bool   `json:"metadata_parse_ok"`
	MetadataAccountUUIDEmpty          bool   `json:"metadata_account_uuid_empty"`
	HasDeviceID                       bool   `json:"has_device_id"`
	HasSessionID                      bool   `json:"has_session_id"`
	LoginStateUserEmailContextPresent bool   `json:"login_state_user_email_context_present"`
}

func (d *claudeOAuthRealClaudeCodeDiagnostic) fail(reason string) {
	if d.Reason == "" {
		d.Reason = reason
	}
}

func isClaudeOAuthHaikuMaxTokensOneAuxRequest(c *gin.Context, parsed *ParsedRequest) bool {
	if c == nil || parsed == nil {
		return false
	}
	if parsed.MaxTokens != 1 || parsed.Stream || !strings.Contains(strings.ToLower(parsed.Model), "haiku") {
		return false
	}
	if !containsBetaToken(c.GetHeader("anthropic-beta"), claude.BetaOAuth) {
		return false
	}
	if !claudeCliUserAgentRe.MatchString(c.GetHeader("User-Agent")) {
		return false
	}
	parsedUserID := ParseMetadataUserID(strings.TrimSpace(parsed.MetadataUserID))
	return parsedUserID != nil &&
		strings.TrimSpace(parsedUserID.DeviceID) != "" &&
		strings.TrimSpace(parsedUserID.SessionID) != ""
}

func isClaudeOAuthCountTokensAuxRequest(c *gin.Context, parsed *ParsedRequest) bool {
	if c == nil || c.Request == nil || parsed == nil {
		return false
	}
	if !strings.HasSuffix(c.Request.URL.Path, "/count_tokens") {
		return false
	}
	if strings.TrimSpace(parsed.Model) == "" {
		return false
	}
	beta := c.GetHeader("anthropic-beta")
	if !containsBetaToken(beta, claude.BetaOAuth) || !containsBetaToken(beta, claude.BetaTokenCounting) {
		return false
	}
	if !claudeCliUserAgentRe.MatchString(c.GetHeader("User-Agent")) {
		return false
	}
	if strings.TrimSpace(c.GetHeader("X-Claude-Code-Session-Id")) == "" {
		return false
	}
	xApp := strings.TrimSpace(c.GetHeader("x-app"))
	return xApp == "" || strings.EqualFold(xApp, "cli")
}

func looksLikeClaudeOAuthTraffic(c *gin.Context, parsed *ParsedRequest, body []byte) bool {
	if c != nil && containsBetaToken(c.GetHeader("anthropic-beta"), claude.BetaOAuth) {
		return true
	}
	if parsed != nil && strings.TrimSpace(parsed.MetadataUserID) != "" {
		return true
	}
	return bytes.Contains(body, []byte("x-anthropic-billing-header:"))
}

func containsClaudeLoginStateUserEmailContext(body []byte) bool {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return false
	}
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		if content.Type == gjson.String && isClaudeLoginStateUserEmailContext(content.String()) {
			return true
		}
		if content.IsArray() {
			for _, part := range content.Array() {
				if part.Get("type").String() != "text" {
					continue
				}
				if isClaudeLoginStateUserEmailContext(part.Get("text").String()) {
					return true
				}
			}
		}
	}
	return false
}

func isClaudeLoginStateUserEmailContext(text string) bool {
	return strings.Contains(text, "<system-reminder>") &&
		strings.Contains(text, "# userEmail") &&
		strings.Contains(text, "The user's email address is")
}

func (s *GatewayService) groupHasClaudeOAuthAccounts(ctx context.Context, groupID *int64, platform string) (bool, error) {
	if s == nil || s.accountRepo == nil || groupID == nil || *groupID <= 0 || platform != PlatformAnthropic {
		return false, nil
	}
	accounts, err := s.accountRepo.ListByGroup(ctx, *groupID)
	if err != nil {
		return false, err
	}
	for i := range accounts {
		if accounts[i].IsAnthropicOAuthOrSetupToken() {
			return true, nil
		}
	}
	return false, nil
}

func (s *GatewayService) isClaudeOAuthRequestGateDisabled(ctx context.Context, groupID *int64) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if group, ok := ctx.Value(ctxkey.Group).(*Group); ok && IsGroupContextValid(group) {
		if groupID == nil || *groupID <= 0 || group.ID == *groupID {
			return group.ClaudeOAuthRequestGateDisabled
		}
	}
	if groupID == nil {
		if id, _, ok := claudeOAuthGroupContextFromContext(ctx); ok && id > 0 {
			groupID = &id
		}
	}
	if s == nil || s.groupRepo == nil || groupID == nil || *groupID <= 0 {
		return false
	}
	group, err := s.resolveGroupByID(ctx, *groupID)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "Warning: failed to resolve group %d for Claude OAuth request gate setting: %v", *groupID, err)
		return false
	}
	return group.ClaudeOAuthRequestGateDisabled
}

// ValidateClaudeOAuthRequestShapeForGroup rejects malformed Claude OAuth-shaped
// requests before scheduling. This prevents a bad Claude Code side request from
// burning OAuth/setup-token attempts and then falling through to API-key channels
// in mixed groups.
func (s *GatewayService) ValidateClaudeOAuthRequestShapeForGroup(ctx context.Context, c *gin.Context, groupID *int64, platform string, parsed *ParsedRequest, body []byte, forCountTokens bool) error {
	if parsed == nil || platform != PlatformAnthropic || !looksLikeClaudeOAuthTraffic(c, parsed, body) {
		return nil
	}
	if s.isClaudeOAuthRequestGateDisabled(ctx, groupID) {
		return nil
	}
	hasOAuth, err := s.groupHasClaudeOAuthAccounts(ctx, groupID, platform)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "Warning: failed to check Claude OAuth accounts for group %d: %v", derefGroupID(groupID), err)
		return nil
	}
	if !hasOAuth {
		return nil
	}

	diag := diagnoseClaudeOAuthRealClaudeCodeRequest(c, parsed, body)
	if diag.Valid {
		return nil
	}
	if forCountTokens && isClaudeOAuthCountTokensAuxRequest(c, parsed) {
		return nil
	}
	if !forCountTokens && isClaudeOAuthHaikuMaxTokensOneAuxRequest(c, parsed) {
		return nil
	}

	logClaudeOAuthRealClaudeCodeReject(c, nil, diag)
	SetOpsFullRequestHeadersCapture(c)
	SetOpsRequestBodyCaptureOptions(c, OpsRequestBodyCaptureOptions{MaxBytes: 64 * 1024})
	return s.claudeOAuthClientRequestError(c, forCountTokens, claudeOAuthRealClaudeCodeRequiredMessage, ErrClaudeCodeOnly)
}

func diagnoseClaudeOAuthRealClaudeCodeRequest(c *gin.Context, parsed *ParsedRequest, body []byte) claudeOAuthRealClaudeCodeDiagnostic {
	diag := claudeOAuthRealClaudeCodeDiagnostic{BodyBytes: len(body)}
	if c == nil || parsed == nil || len(body) == 0 {
		diag.fail("missing_context_or_body")
		return diag
	}
	diag.HasOAuthBeta = containsBetaToken(c.GetHeader("anthropic-beta"), claude.BetaOAuth)
	if !diag.HasOAuthBeta {
		diag.fail("missing_oauth_beta")
	}

	metadataUserID := strings.TrimSpace(parsed.MetadataUserID)
	diag.HasMetadataUserID = metadataUserID != ""
	parsedUserID := ParseMetadataUserID(metadataUserID)
	diag.MetadataParseOK = parsedUserID != nil
	if parsedUserID != nil {
		diag.MetadataAccountUUIDEmpty = strings.TrimSpace(parsedUserID.AccountUUID) == ""
		diag.HasDeviceID = strings.TrimSpace(parsedUserID.DeviceID) != ""
		diag.HasSessionID = strings.TrimSpace(parsedUserID.SessionID) != ""
	}
	if !diag.HasMetadataUserID || !diag.MetadataParseOK || !diag.HasDeviceID || !diag.HasSessionID {
		diag.fail("invalid_metadata_user_id")
	}
	diag.LoginStateUserEmailContextPresent = containsClaudeLoginStateUserEmailContext(body)
	if diag.LoginStateUserEmailContextPresent {
		diag.fail("login_state_user_email_context_present")
	}

	// Strict OAuth ingress validation must only accept the active system billing
	// header. Without an explicit system field, claudebilling falls back to a
	// looser whole-body search for legacy rewrite tools, which is not appropriate
	// for deciding whether a client request is a real Claude Code request.
	diag.HasSystem = gjson.GetBytes(body, "system").Exists()
	if !diag.HasSystem {
		diag.fail("missing_system")
	}

	ua := c.GetHeader("User-Agent")
	diag.UserAgent = truncateString(ua, 256)
	uaVersion := ExtractCLIVersion(ua)
	diag.UAVersion = strings.TrimSpace(uaVersion)
	if diag.UAVersion == "" {
		diag.fail("missing_ua_version")
	}
	bodyVersion, bodySuffix := claudebilling.ExtractCCVersionFromBody(body)
	diag.BodyCCVersion = strings.TrimSpace(bodyVersion)
	diag.BodyCCSuffixPresent = strings.TrimSpace(bodySuffix) != ""
	diag.VersionMatch = diag.UAVersion != "" && diag.BodyCCVersion != "" && diag.UAVersion == diag.BodyCCVersion
	if diag.BodyCCVersion == "" || !diag.VersionMatch {
		diag.fail("cc_version_mismatch")
	}

	uaEntrypoint := extractClaudeCLIEntrypoint(ua)
	diag.UAEntrypoint = strings.TrimSpace(uaEntrypoint)
	bodyEntrypoint, ok := claudebilling.ExtractCCEntrypointFromBody(body)
	bodyEntrypoint = strings.TrimSpace(bodyEntrypoint)
	diag.BodyEntrypoint = bodyEntrypoint
	diag.BodyEntrypointFound = ok
	diag.EntrypointMatch = diag.UAEntrypoint != "" && ok && bodyEntrypoint != "" && diag.UAEntrypoint == bodyEntrypoint
	if !diag.EntrypointMatch {
		diag.fail("cc_entrypoint_mismatch")
	}

	normalizedBody, match, err := claudebilling.NormalizeBodyForCCH(body)
	if err != nil {
		diag.CCHNormalizeError = truncateString(err.Error(), 256)
		if isMissingCCHError(err) && isClaudeOAuthMissingCCHTokenModeAllowedForDiagnostic(diag) {
			diag.MissingCCHTokenModeAllowed = true
		} else if isMissingCCHError(err) {
			diag.fail("missing_cch_unsupported_mode")
		} else {
			diag.fail("cch_missing_or_malformed")
		}
	} else {
		diag.CCHPresent = strings.TrimSpace(match.Value) != ""
		diag.CCHValue = strings.TrimSpace(match.Value)
		diag.CCHPlaceholder = diag.CCHValue == "00000"
		if !diag.CCHPresent || diag.CCHPlaceholder {
			diag.fail("cch_missing_or_placeholder")
		} else {
			cchCandidates := claudebilling.ComputeCCHCandidates(normalizedBody)
			cand, ok := claudebilling.SelectCCHCandidateForMatch(cchCandidates, match.Value, bodyVersion)
			diag.CCHValid = ok
			if ok {
				diag.CCHSeed = cand.Seed
				diag.CCHSeedHex = fmt.Sprintf("0x%016x", cand.Seed)
				diag.CCHMode = string(cand.Mode)
			}
			if !diag.CCHValid {
				diag.fail("cch_mismatch")
			}
		}
	}
	billingHeaderValid := (diag.CCHPresent && !diag.CCHPlaceholder && diag.CCHValid) ||
		diag.MissingCCHTokenModeAllowed
	diag.Valid = diag.HasOAuthBeta &&
		diag.HasMetadataUserID &&
		diag.MetadataParseOK &&
		diag.HasDeviceID &&
		diag.HasSessionID &&
		!diag.LoginStateUserEmailContextPresent &&
		diag.HasSystem &&
		diag.UAVersion != "" &&
		diag.VersionMatch &&
		diag.EntrypointMatch &&
		billingHeaderValid
	if diag.LoginStateUserEmailContextPresent {
		diag.Reason = "login_state_user_email_context_present"
	}
	return diag
}

func isClaudeOAuthMissingCCHTokenModeAllowedForDiagnostic(diag claudeOAuthRealClaudeCodeDiagnostic) bool {
	return diag.HasOAuthBeta &&
		diag.MetadataParseOK &&
		diag.MetadataAccountUUIDEmpty &&
		diag.HasDeviceID &&
		diag.HasSessionID &&
		diag.HasSystem &&
		diag.UAVersion != "" &&
		diag.VersionMatch &&
		diag.EntrypointMatch &&
		claudebilling.AllowsMissingCCHInTokenMode(diag.BodyCCVersion)
}

func logClaudeOAuthRealClaudeCodeReject(c *gin.Context, account *Account, diag claudeOAuthRealClaudeCodeDiagnostic) {
	reqID := requestIDFromGinContext(c)
	accountID := int64(0)
	platform := ""
	accountName := ""
	if account != nil {
		accountID = account.ID
		platform = account.Platform
		accountName = account.Name
	}
	payload, _ := json.Marshal(diag)
	logger.LegacyPrintf(
		"service.gateway",
		"Warning: claude oauth real claude code reject request_id=%s account=%d reason=%s diag=%s",
		reqID, accountID, diag.Reason, string(payload),
	)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:    platform,
		AccountID:   accountID,
		AccountName: accountName,
		Kind:        "client_reject",
		Message:     "Claude OAuth real Claude Code validation failed",
		Detail:      string(payload),
	})
}

// validateCarpoolBillingIntegrity 对 carpool 模式下带 x-anthropic-billing-header 的请求做前置校验。
// cch 是完整 body attestation，仍然严格校验；cc_version suffix 只作为可观测信号记录，
// 因为 gateway 看到的 wire body 可能已经缺少客户端计算 fingerprint 时使用的原始输入。
func (s *GatewayService) validateCarpoolBillingIntegrity(
	c *gin.Context,
	body []byte,
	clientHeaders http.Header,
	account *Account,
	forCountTokens bool,
) error {
	_, _, err := s.validateCarpoolBillingIntegrityWithResult(c, body, clientHeaders, account, forCountTokens)
	return err
}

func (s *GatewayService) validateCarpoolBillingIntegrityWithResult(
	c *gin.Context,
	body []byte,
	clientHeaders http.Header,
	account *Account,
	forCountTokens bool,
) (uint64, claudebilling.CCHInputMode, error) {
	if len(body) == 0 || !bytes.Contains(body, []byte("x-anthropic-billing-header:")) {
		return 0, "", nil
	}

	reject := func(subReason, detail string) (uint64, claudebilling.CCHInputMode, error) {
		return 0, "", s.rejectCarpoolBilling(c, body, clientHeaders, forCountTokens, account, subReason, detail)
	}
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}

	ua := clientHeaders.Get("User-Agent")
	uaVersion := ExtractCLIVersion(ua)
	if strings.TrimSpace(uaVersion) == "" {
		return reject("billing_ua_missing", "User-Agent is not a claude-cli")
	}

	bodyVersion, bodySuffix := claudebilling.ExtractCCVersionFromBody(body)
	if strings.TrimSpace(bodyVersion) == "" || strings.TrimSpace(bodySuffix) == "" {
		detail := fmt.Sprintf("cc_version malformed observed_version=%q observed_suffix=%q ua=%q",
			bodyVersion, bodySuffix, ua)
		return reject("billing_cc_version_malformed", detail)
	}

	if uaVersion != bodyVersion {
		detail := fmt.Sprintf("ua_version=%s body_version=%s", uaVersion, bodyVersion)
		return reject("billing_ua_version_mismatch", detail)
	}

	uaEntrypoint := extractClaudeCLIEntrypoint(ua)
	bodyEntrypoint, _ := claudebilling.ExtractCCEntrypointFromBody(body)
	bodyEntrypoint = strings.TrimSpace(bodyEntrypoint)
	if uaEntrypoint == "" || bodyEntrypoint == "" || uaEntrypoint != bodyEntrypoint {
		detail := fmt.Sprintf("ua_entrypoint=%q body_entrypoint=%q", uaEntrypoint, bodyEntrypoint)
		return reject("billing_cc_entrypoint_mismatch", detail)
	}

	promptCandidates, _ := claudebilling.CandidateUserTexts(body)
	if matched, _ := claudebilling.MatchCCVersionSuffix(promptCandidates, bodyVersion, bodySuffix); !matched {
		preview := ""
		if len(promptCandidates) > 0 {
			preview = promptCandidates[0]
			if len(preview) > 60 {
				preview = preview[:60]
			}
		}
		logger.LegacyPrintf(
			"service.gateway",
			"Warning: carpool billing cc_version suffix not reproducible; allowing request_id=%s account=%d observed=%s version=%s candidates=%d prompt0_prefix=%q",
			requestIDFromGinContext(c), accountID, bodySuffix, bodyVersion, len(promptCandidates), preview,
		)
	}

	normalizedBody, match, err := claudebilling.NormalizeBodyForCCH(body)
	if err != nil {
		if isMissingCCHError(err) && isClaudeOAuthMissingCCHTokenModeAllowed(body, clientHeaders) {
			if account != nil && account.Platform == PlatformAnthropic && account.Type == AccountTypeSetupToken {
				return 0, "", nil
			}
			if account != nil && account.IsAnthropicOAuthOrSetupToken() {
				return 0, "", s.claudeOAuthAccountFailoverError(c, account, http.StatusBadRequest, "invalid_request_error", claudeOAuthRealClaudeCodeRequiredMessage, ErrClaudeCodeOnly)
			}
		}
		return reject("billing_cch_malformed", "cch field missing or malformed")
	}

	// npm @anthropic-ai/claude-code 在 JS 层只写占位 "00000"，native 客户端才会重算；
	// Preserve the legacy placeholder shape for compatible official npm clients.
	if match.Value == "00000" {
		return 0, "", nil
	}

	cchCandidates := claudebilling.ComputeCCHCandidates(normalizedBody)
	if cand, ok := claudebilling.SelectCCHCandidateForMatch(cchCandidates, match.Value, bodyVersion); ok {
		return cand.Seed, cand.Mode, nil
	}

	parts := make([]string, 0, len(cchCandidates))
	for _, cand := range cchCandidates {
		parts = append(parts, fmt.Sprintf("0x%016x/%s=%s", cand.Seed, cand.Mode, cand.Value))
	}
	detail := fmt.Sprintf(
		"observed=%s candidates=[%s] version=%s body_len=%d",
		match.Value, strings.Join(parts, ","), bodyVersion, len(body),
	)
	return reject("billing_cch_mismatch", detail)
}

func isMissingCCHError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no cch=xxxxx field found")
}

func isClaudeOAuthMissingCCHTokenModeAllowed(body []byte, clientHeaders http.Header) bool {
	if len(body) == 0 {
		return false
	}
	if !containsBetaToken(clientHeaders.Get("anthropic-beta"), claude.BetaOAuth) {
		return false
	}
	parsedUserID := ParseMetadataUserID(strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String()))
	if parsedUserID == nil ||
		strings.TrimSpace(parsedUserID.AccountUUID) != "" ||
		strings.TrimSpace(parsedUserID.DeviceID) == "" ||
		strings.TrimSpace(parsedUserID.SessionID) == "" {
		return false
	}

	uaVersion := ExtractCLIVersion(clientHeaders.Get("User-Agent"))
	bodyVersion, _ := claudebilling.ExtractCCVersionFromBody(body)
	if strings.TrimSpace(uaVersion) == "" ||
		strings.TrimSpace(bodyVersion) == "" ||
		uaVersion != bodyVersion ||
		!claudebilling.AllowsMissingCCHInTokenMode(bodyVersion) {
		return false
	}

	uaEntrypoint := extractClaudeCLIEntrypoint(clientHeaders.Get("User-Agent"))
	bodyEntrypoint, ok := claudebilling.ExtractCCEntrypointFromBody(body)
	bodyEntrypoint = strings.TrimSpace(bodyEntrypoint)
	return uaEntrypoint != "" && ok && bodyEntrypoint != "" && uaEntrypoint == bodyEntrypoint
}

func requestIDFromGinContext(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	ctx := c.Request.Context()
	for _, key := range []ctxkey.Key{ctxkey.RequestID, ctxkey.ClientRequestID} {
		if v, ok := ctx.Value(key).(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func (s *GatewayService) rejectCarpoolBilling(
	c *gin.Context,
	body []byte,
	clientHeaders http.Header,
	forCountTokens bool,
	account *Account,
	subReason, detail string,
) error {
	reqID := requestIDFromGinContext(c)
	accountID := int64(0)
	platform := ""
	accountName := ""
	if account != nil {
		accountID = account.ID
		platform = account.Platform
		accountName = account.Name
	}
	opsDetail := buildCarpoolBillingRejectOpsDetail(body, clientHeaders, subReason, detail)
	logger.LegacyPrintf(
		"service.gateway",
		"Warning: carpool billing integrity reject request_id=%s account=%d reason=%s detail=%s",
		reqID, accountID, subReason, detail,
	)
	setOpsUpstreamError(c, http.StatusBadRequest, carpoolBillingMismatchMessage, opsDetail)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           platform,
		AccountID:          accountID,
		AccountName:        accountName,
		UpstreamStatusCode: http.StatusBadRequest,
		Kind:               "client_reject",
		Message:            carpoolBillingMismatchMessage,
		Detail:             opsDetail,
	})
	billingreject.Default().Capture(reqID, accountID, subReason, detail, clientHeaders, body)
	s.writeClaudeOAuthRequestError(c, forCountTokens, carpoolBillingMismatchMessage)
	return ErrClaudeOAuthCarpoolBillingMismatch
}

func buildCarpoolBillingRejectOpsDetail(body []byte, clientHeaders http.Header, reason, detail string) string {
	diag := carpoolBillingRejectDiagnostic{
		Reason:                            strings.TrimSpace(reason),
		Detail:                            strings.TrimSpace(detail),
		BodyBytes:                         len(body),
		HasBillingHeader:                  bytes.Contains(body, []byte("x-anthropic-billing-header:")),
		UserAgent:                         truncateString(clientHeaders.Get("User-Agent"), 256),
		UAVersion:                         strings.TrimSpace(ExtractCLIVersion(clientHeaders.Get("User-Agent"))),
		LoginStateUserEmailContextPresent: containsClaudeLoginStateUserEmailContext(body),
	}
	bodyVersion, _ := claudebilling.ExtractCCVersionFromBody(body)
	diag.BodyCCVersion = strings.TrimSpace(bodyVersion)
	bodyEntrypoint, ok := claudebilling.ExtractCCEntrypointFromBody(body)
	diag.BodyEntrypoint = strings.TrimSpace(bodyEntrypoint)
	diag.BodyEntrypointFound = ok

	if _, match, err := claudebilling.NormalizeBodyForCCH(body); err == nil {
		diag.CCHPresent = strings.TrimSpace(match.Value) != ""
		diag.CCHValue = strings.TrimSpace(match.Value)
	}

	parsedUserID := ParseMetadataUserID(strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String()))
	diag.MetadataParseOK = parsedUserID != nil
	if parsedUserID != nil {
		diag.MetadataAccountUUIDEmpty = strings.TrimSpace(parsedUserID.AccountUUID) == ""
		diag.HasDeviceID = strings.TrimSpace(parsedUserID.DeviceID) != ""
		diag.HasSessionID = strings.TrimSpace(parsedUserID.SessionID) != ""
	}
	payload, err := json.Marshal(diag)
	if err != nil {
		return fmt.Sprintf(`{"reason":%q,"detail":%q}`, strings.TrimSpace(reason), strings.TrimSpace(detail))
	}
	return string(payload)
}

func extractClaudeCLIEntrypoint(ua string) string {
	match := claudeCLIUAEntrypointPattern.FindStringSubmatch(ua)
	if len(match) < 2 {
		return ""
	}
	entrypoint := strings.TrimSpace(match[1])
	if idx := strings.Index(entrypoint, ","); idx >= 0 {
		entrypoint = strings.TrimSpace(entrypoint[:idx])
	}
	return entrypoint
}
