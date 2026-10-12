package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Provider subscription limits describe the selected account, not the SAIAI
// user. Keep them for scheduling without presenting them as the user's quota.
func isOpenAICodexQuotaHeader(name string) bool {
	name = strings.ToLower(name)
	if !strings.HasPrefix(name, "x-") {
		return false
	}
	for _, suffix := range []string{
		"-primary-used-percent", "-primary-window-minutes", "-primary-reset-at", "-primary-reset-after-seconds",
		"-secondary-used-percent", "-secondary-window-minutes", "-secondary-reset-at", "-secondary-reset-after-seconds",
		"-primary-over-secondary-limit-percent", "-limit-name",
	} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return strings.HasPrefix(name, "x-codex-credits-") || name == "x-codex-rate-limit-reached-type" || name == "x-codex-promo-message"
}

func isOpenAICodexQuotaEvent(payload []byte) bool {
	return gjson.ValidBytes(payload) && gjson.GetBytes(payload, "type").String() == "codex.rate_limits"
}

func hideOpenAICodexQuota(account *Account) bool {
	return account != nil && (account.IsOpenAIOAuth() || account.IsOpenAICodexNativeRelay())
}

// Observe before filtering. Unsupported/model-specific limit families remain
// private but must never become account-wide scheduling observations.
func (s *OpenAIGatewayService) observeOpenAICodexQuotaEvent(ctx context.Context, account *Account, payload []byte) bool {
	if !hideOpenAICodexQuota(account) || !isOpenAICodexQuotaEvent(payload) {
		return false
	}
	if account.IsOpenAIOAuth() && !account.IsOpenAICodexNativeRelay() {
		s.updateCodexUsageSnapshot(ctx, account.ID, parseOpenAIWSCodexRateLimitEvent(payload, time.Now()))
	}
	return true
}

func (s *OpenAIGatewayService) observeOpenAICodexQuotaHeaders(ctx context.Context, account *Account, headers http.Header) {
	if account != nil && account.IsOpenAIOAuth() && !account.IsOpenAICodexNativeRelay() {
		s.UpdateCodexUsageSnapshotFromHeaders(ctx, account.ID, headers)
	}
}

// Handshake limits are sampled at connection creation. Do not resample them at
// turn completion or lease reuse: later quota events are fresher observations.
func openAIWSTurnResponseHeaders(account *Account, headers http.Header) http.Header {
	if !hideOpenAICodexQuota(account) {
		return headers
	}
	filtered := cloneHeader(headers)
	for name := range filtered {
		if isOpenAICodexQuotaHeader(name) {
			delete(filtered, name)
		}
	}
	return filtered
}
