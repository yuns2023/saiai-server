package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/gin-gonic/gin"
)

const (
	SettingKeyOpsLogFilters    = "ops_log_filters"
	OpsLocalAuthUserInactive   = "user_inactive"
	OpsLocalAuthInvalidAPIKey  = "invalid_api_key"
	OpsLocalAuthAPIKeyRequired = "api_key_required"
	opsLocalAuthReasonKey      = "ops_local_auth_reject_reason"
	opsLocalAuthTypePrefix     = "gateway_auth_"
	opsLogFilterCacheTTL       = 2 * time.Second
)

type OpsLogFilterRule struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	Source      string   `json:"source"`
	Reason      string   `json:"reason,omitempty"`
	Platform    string   `json:"platform,omitempty"`
	GroupID     *int64   `json:"group_id,omitempty"`
	StatusCodes []int    `json:"status_codes,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	MatchMode   string   `json:"match_mode"`
}

type OpsLogFilterConfig struct {
	Rules      []OpsLogFilterRule `json:"rules"`
	Revision   string             `json:"revision"`
	Counts     map[string]uint64  `json:"counts"`
	CountScope string             `json:"count_scope"`
}

type OpsLogFilterUpdate struct {
	Rules    []OpsLogFilterRule `json:"rules"`
	Revision string             `json:"revision"`
}

type OpsLogFilterEvent struct {
	Source     string
	Reason     string
	Platform   string
	GroupID    *int64
	StatusCode int
	Message    string
}

type OpsLogFilterProposal struct {
	Rule                       *OpsLogFilterRule `json:"rule"`
	Verified                   bool              `json:"verified"`
	RequiresGlobalConfirmation bool              `json:"requires_global_confirmation"`
}

type opsLogFilterState struct {
	mu        sync.Mutex
	rules     []OpsLogFilterRule
	expiresAt time.Time
	counts    map[string]uint64
}

var opsLogFilterIDPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{1,80}$`)

func isOpsLocalAuthReason(reason string) bool {
	switch reason {
	case OpsLocalAuthUserInactive, OpsLocalAuthInvalidAPIKey, OpsLocalAuthAPIKeyRequired:
		return true
	default:
		return false
	}
}

func MarkOpsLocalAuthRejected(ctx *gin.Context, reason string) {
	if ctx != nil && isOpsLocalAuthReason(reason) {
		ctx.Set(opsLocalAuthReasonKey, reason)
	}
}

func GetOpsLocalAuthRejectReason(ctx *gin.Context) string {
	if ctx == nil {
		return ""
	}
	reason := ctx.GetString(opsLocalAuthReasonKey)
	if !isOpsLocalAuthReason(reason) {
		return ""
	}
	return reason
}

func OpsLocalAuthErrorType(reason string) string {
	if !isOpsLocalAuthReason(reason) {
		return "authentication_error"
	}
	return opsLocalAuthTypePrefix + reason
}

func validateOpsLogFilterRules(rules []OpsLogFilterRule) error {
	if len(rules) > 50 {
		return errors.New("at most 50 log filter rules are allowed")
	}
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if !opsLogFilterIDPattern.MatchString(rule.ID) || seen[rule.ID] {
			return errors.New("rule IDs must be unique, using 1-80 letters, digits or hyphens")
		}
		seen[rule.ID] = true
		if strings.TrimSpace(rule.Name) == "" || utf8.RuneCountInString(rule.Name) > 120 {
			return errors.New("rule names must contain 1-120 characters")
		}
		if rule.MatchMode != "all" && rule.MatchMode != "any" {
			return errors.New("match_mode must be all or any")
		}
		if rule.GroupID != nil && *rule.GroupID <= 0 {
			return errors.New("group_id must be positive")
		}
		if rule.Platform != "" && rule.Platform != PlatformAnthropic && rule.Platform != PlatformOpenAI && rule.Platform != PlatformGemini && rule.Platform != PlatformAntigravity {
			return errors.New("unsupported filter platform")
		}
		switch rule.Source {
		case "local_auth":
			if !isOpsLocalAuthReason(rule.Reason) {
				return errors.New("local_auth rules require a supported rejection reason")
			}
			if rule.Reason != OpsLocalAuthUserInactive && rule.GroupID != nil {
				return errors.New("missing or invalid keys cannot be scoped to a group before authentication")
			}
		case "upstream":
			if rule.Reason != "" || len(rule.Keywords) == 0 || len(rule.StatusCodes) == 0 || rule.MatchMode != "all" {
				return errors.New("upstream rules require both status codes and keywords with match_mode=all")
			}
		default:
			return errors.New("source must be local_auth or upstream")
		}
		if len(rule.StatusCodes) > 20 || len(rule.Keywords) > 10 {
			return errors.New("rules allow at most 20 status codes and 10 keywords")
		}
		for _, status := range rule.StatusCodes {
			if status < 400 || status > 599 {
				return errors.New("filter status codes must be between 400 and 599")
			}
		}
		for _, keyword := range rule.Keywords {
			if strings.TrimSpace(keyword) == "" || utf8.RuneCountInString(keyword) > 256 {
				return errors.New("keywords must contain 1-256 characters")
			}
		}
	}
	return nil
}

func (s *OpsService) loadOpsLogFilterRules(ctx context.Context) ([]OpsLogFilterRule, string, error) {
	rules := []OpsLogFilterRule{}
	if s != nil && s.settingRepo != nil {
		raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpsLogFilters)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			return nil, "", err
		}
		if err == nil {
			if err := json.Unmarshal([]byte(raw), &rules); err != nil {
				return nil, "", errors.New("invalid stored log filter configuration")
			}
		}
	}
	if rules == nil {
		rules = []OpsLogFilterRule{}
	}
	if err := validateOpsLogFilterRules(rules); err != nil {
		return nil, "", err
	}
	raw, _ := json.Marshal(rules)
	digest := sha256.Sum256(raw)
	return rules, hex.EncodeToString(digest[:]), nil
}

func (s *OpsService) logFilterConfigLocked(rules []OpsLogFilterRule, revision string) *OpsLogFilterConfig {
	counts := make(map[string]uint64, len(rules))
	for _, rule := range rules {
		counts[rule.ID] = s.logFilterState.counts[rule.ID]
	}
	return &OpsLogFilterConfig{Rules: rules, Revision: revision, Counts: counts, CountScope: "process"}
}

func (s *OpsService) GetOpsLogFilters(ctx context.Context) (*OpsLogFilterConfig, error) {
	s.logFilterState.mu.Lock()
	defer s.logFilterState.mu.Unlock()
	rules, revision, err := s.loadOpsLogFilterRules(ctx)
	if err != nil {
		return nil, err
	}
	return s.logFilterConfigLocked(rules, revision), nil
}

func (s *OpsService) UpdateOpsLogFilters(ctx context.Context, update *OpsLogFilterUpdate) (*OpsLogFilterConfig, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("setting repository not initialized")
	}
	if update == nil || update.Revision == "" {
		return nil, infraerrors.BadRequest("OPS_LOG_FILTER_REVISION_REQUIRED", "Reload log filters before saving")
	}
	if err := validateOpsLogFilterRules(update.Rules); err != nil {
		return nil, infraerrors.BadRequest("OPS_LOG_FILTER_INVALID", err.Error())
	}
	s.logFilterState.mu.Lock()
	defer s.logFilterState.mu.Unlock()
	_, revision, err := s.loadOpsLogFilterRules(ctx)
	if err != nil {
		return nil, err
	}
	if update.Revision != revision {
		return nil, infraerrors.Conflict("OPS_LOG_FILTER_CONFLICT", "Log filters changed; reload before saving")
	}
	rules := update.Rules
	if rules == nil {
		rules = []OpsLogFilterRule{}
	}
	raw, _ := json.Marshal(rules)
	if err := s.settingRepo.Set(ctx, SettingKeyOpsLogFilters, string(raw)); err != nil {
		return nil, err
	}
	stored := []OpsLogFilterRule{}
	_ = json.Unmarshal(raw, &stored)
	s.logFilterState.rules = stored
	s.logFilterState.expiresAt = time.Now().Add(opsLogFilterCacheTTL)
	counts := make(map[string]uint64, len(stored))
	for _, rule := range stored {
		counts[rule.ID] = s.logFilterState.counts[rule.ID]
	}
	s.logFilterState.counts = counts
	digest := sha256.Sum256(raw)
	return s.logFilterConfigLocked(rules, hex.EncodeToString(digest[:])), nil
}

func matchOpsLogFilter(rule OpsLogFilterRule, event OpsLogFilterEvent) bool {
	if !rule.Enabled || rule.Source != event.Source || event.StatusCode < 400 || event.StatusCode > 599 {
		return false
	}
	if rule.Source == "local_auth" && (!isOpsLocalAuthReason(event.Reason) || rule.Reason != event.Reason) {
		return false
	}
	if rule.Platform != "" && rule.Platform != event.Platform {
		return false
	}
	if rule.GroupID != nil && (event.GroupID == nil || *rule.GroupID != *event.GroupID) {
		return false
	}
	statusMatch := len(rule.StatusCodes) == 0
	for _, status := range rule.StatusCodes {
		statusMatch = statusMatch || status == event.StatusCode
	}
	keywordMatch := len(rule.Keywords) == 0
	message := strings.ToLower(truncateString(event.Message, 8192))
	for _, keyword := range rule.Keywords {
		keywordMatch = keywordMatch || strings.Contains(message, strings.ToLower(strings.TrimSpace(keyword)))
	}
	if rule.MatchMode == "any" && len(rule.StatusCodes) > 0 && len(rule.Keywords) > 0 {
		return statusMatch || keywordMatch
	}
	return statusMatch && keywordMatch
}

func (s *OpsService) FilterOpsLogEvents(ctx context.Context, events []OpsLogFilterEvent) bool {
	if s == nil || len(events) == 0 || s.settingRepo == nil {
		return false
	}
	if !s.logFilterState.mu.TryLock() {
		return false
	}
	defer s.logFilterState.mu.Unlock()
	if !time.Now().Before(s.logFilterState.expiresAt) {
		readCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		rules, _, err := s.loadOpsLogFilterRules(readCtx)
		cancel()
		s.logFilterState.expiresAt = time.Now().Add(opsLogFilterCacheTTL)
		s.logFilterState.rules = rules
		if err != nil {
			return false
		}
		counts := make(map[string]uint64, len(rules))
		for _, rule := range rules {
			counts[rule.ID] = s.logFilterState.counts[rule.ID]
		}
		s.logFilterState.counts = counts
	}
	for _, rule := range s.logFilterState.rules {
		allMatch := true
		for _, event := range events {
			if !matchOpsLogFilter(rule, event) {
				allMatch = false
				break
			}
		}
		if allMatch {
			if s.logFilterState.counts == nil {
				s.logFilterState.counts = make(map[string]uint64)
			}
			s.logFilterState.counts[rule.ID]++
			return true
		}
	}
	return false
}

func BuildOpsLogFilterProposal(detail *OpsErrorLogDetail) *OpsLogFilterProposal {
	proposal := &OpsLogFilterProposal{}
	if detail == nil {
		return proposal
	}
	rule := &OpsLogFilterRule{ID: fmt.Sprintf("error-%d", detail.ID), Enabled: false, Platform: detail.Platform, GroupID: detail.GroupID, MatchMode: "all"}
	hasUpstream := detail.AccountID != nil || detail.UpstreamStatusCode != nil || detail.UpstreamErrorMessage != "" || detail.UpstreamErrorDetail != "" || (detail.UpstreamErrors != "" && detail.UpstreamErrors != "[]") || detail.Source == "upstream_http" || detail.Phase == "upstream"
	if !hasUpstream && detail.StatusCode == 401 {
		reason := strings.TrimPrefix(detail.Type, opsLocalAuthTypePrefix)
		if strings.HasPrefix(detail.Type, opsLocalAuthTypePrefix) && isOpsLocalAuthReason(reason) && detail.Phase == "auth" && detail.Source == "client_request" {
			proposal.Verified = true
		} else {
			var body struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal([]byte(detail.ErrorBody), &body)
			switch body.Code {
			case "USER_INACTIVE":
				reason = OpsLocalAuthUserInactive
			case "INVALID_API_KEY":
				reason = OpsLocalAuthInvalidAPIKey
			case "API_KEY_REQUIRED":
				reason = OpsLocalAuthAPIKeyRequired
			default:
				return proposal
			}
		}
		rule.Source, rule.Reason = "local_auth", reason
		if reason != OpsLocalAuthUserInactive {
			rule.GroupID = nil
		}
		rule.Name = "Local authentication: " + reason
	} else if hasUpstream {
		status := detail.StatusCode
		message := detail.Message
		if detail.UpstreamStatusCode != nil && *detail.UpstreamStatusCode >= 400 && *detail.UpstreamStatusCode <= 599 {
			status = *detail.UpstreamStatusCode
			message = detail.UpstreamErrorMessage
			if strings.TrimSpace(message) == "" {
				message = ExtractUpstreamErrorMessage([]byte(detail.UpstreamErrorDetail))
			}
			proposal.Verified = true
		} else {
			var attempts []OpsUpstreamErrorEvent
			if json.Unmarshal([]byte(detail.UpstreamErrors), &attempts) == nil && len(attempts) > 0 {
				last := attempts[len(attempts)-1]
				if last.UpstreamStatusCode >= 400 && last.UpstreamStatusCode <= 599 {
					status, message = last.UpstreamStatusCode, last.Message
					if strings.TrimSpace(message) == "" {
						message = ExtractUpstreamErrorMessage([]byte(last.Detail))
					}
					proposal.Verified = true
				}
			}
		}
		if status < 400 || status > 599 {
			return proposal
		}
		message = truncateString(ClientSafeUpstreamErrorMessage(logredact.RedactText(message)), 256)
		if strings.TrimSpace(message) == "" {
			return proposal
		}
		rule.Source, rule.Name = "upstream", fmt.Sprintf("Upstream %d", status)
		rule.StatusCodes, rule.Keywords = []int{status}, []string{message}
	} else {
		return proposal
	}
	proposal.Rule = rule
	proposal.RequiresGlobalConfirmation = rule.GroupID == nil && rule.Platform == ""
	return proposal
}
