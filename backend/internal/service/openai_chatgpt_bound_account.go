package service

import (
	"errors"
	"fmt"
	"time"
)

// ChatGPTBoundAccountUnavailableKind classifies why a native Chat request
// bound to one OAuth account (device proof, conversation, turn, upload or
// integrity owner) cannot use that account right now.
type ChatGPTBoundAccountUnavailableKind string

const (
	// The owner still belongs to the group and is admin-schedulable but is in
	// a time-bounded cooldown. The binding must be kept; the client retries.
	ChatGPTBoundAccountTemporary ChatGPTBoundAccountUnavailableKind = "temporary"
	// The owner can no longer serve this group without admin action. The
	// binding is stale; the client must register/start over on another account.
	ChatGPTBoundAccountPermanent ChatGPTBoundAccountUnavailableKind = "permanent"
	// Account storage failed; nothing is known about the owner.
	ChatGPTBoundAccountStore ChatGPTBoundAccountUnavailableKind = "store"
)

// Reasons are stable, low-cardinality identifiers for logs and Ops.
const (
	ChatGPTBoundReasonRateLimited        = "rate_limited"
	ChatGPTBoundReasonOverloaded         = "overloaded"
	ChatGPTBoundReasonTempUnschedulable  = "temp_unschedulable"
	ChatGPTBoundReasonScheduleRace       = "not_yet_schedulable"
	ChatGPTBoundReasonNotFound           = "account_not_found"
	ChatGPTBoundReasonInactive           = "account_inactive"
	ChatGPTBoundReasonAdminUnschedulable = "admin_unschedulable"
	ChatGPTBoundReasonNotInGroup         = "not_in_group"
	ChatGPTBoundReasonNotOpenAIOAuth     = "not_openai_oauth"
	ChatGPTBoundReasonExpired            = "account_expired"
	ChatGPTBoundReasonStoreError         = "account_store_error"
)

// chatGPTBoundScheduleRaceRetry covers the window where the account row
// already looks schedulable but the group query still excluded it (database
// NOW() vs. process clock, or a cooldown ending between the two reads).
const chatGPTBoundScheduleRaceRetry = 5 * time.Second

// ChatGPTBoundAccountUnavailableError is returned by GetChatGPTBoundAccount and
// SelectChatGPTBoundAccount. It still matches ErrNoAvailableAccounts with
// errors.Is (except for store errors, which unwrap to the storage error).
type ChatGPTBoundAccountUnavailableError struct {
	AccountID int64
	Kind      ChatGPTBoundAccountUnavailableKind
	Reason    string
	// RetryAt is set for temporary unavailability: the time when every active
	// cooldown on the owner has ended.
	RetryAt time.Time
	// Status is the stored account status for inactive owners (diagnostics).
	Status string
	Err    error
}

func (e *ChatGPTBoundAccountUnavailableError) Error() string {
	if e == nil {
		return ""
	}
	msg := fmt.Sprintf("bound account %d unavailable (%s: %s", e.AccountID, e.Kind, e.Reason)
	if e.Status != "" {
		msg += ", status " + e.Status
	}
	if !e.RetryAt.IsZero() {
		msg += " until " + e.RetryAt.UTC().Format(time.RFC3339)
	}
	msg += ")"
	if e.Kind == ChatGPTBoundAccountStore && e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *ChatGPTBoundAccountUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Kind == ChatGPTBoundAccountStore && e.Err != nil {
		return e.Err
	}
	return ErrNoAvailableAccounts
}

// AsChatGPTBoundAccountUnavailable extracts the typed classification.
func AsChatGPTBoundAccountUnavailable(err error) (*ChatGPTBoundAccountUnavailableError, bool) {
	var typed *ChatGPTBoundAccountUnavailableError
	if errors.As(err, &typed) && typed != nil {
		return typed, true
	}
	return nil, false
}

// ClassifyChatGPTBoundAccount mirrors the bound-owner eligibility rules
// (ListSchedulableByGroupIDAndPlatform + IsOpenAIOAuth + IsSchedulable) and
// reports why account cannot serve groupID at now. It returns nil when the
// account is eligible. Permanent reasons win over temporary ones: a disabled
// account that is also rate limited will not recover by waiting.
func ClassifyChatGPTBoundAccount(account *Account, groupID int64, now time.Time) *ChatGPTBoundAccountUnavailableError {
	if account == nil {
		return &ChatGPTBoundAccountUnavailableError{Kind: ChatGPTBoundAccountPermanent, Reason: ChatGPTBoundReasonNotFound}
	}
	permanent := func(reason string) *ChatGPTBoundAccountUnavailableError {
		return &ChatGPTBoundAccountUnavailableError{AccountID: account.ID, Kind: ChatGPTBoundAccountPermanent, Reason: reason}
	}
	if !account.IsOpenAIOAuth() {
		return permanent(ChatGPTBoundReasonNotOpenAIOAuth)
	}
	if !account.IsActive() {
		unavailable := permanent(ChatGPTBoundReasonInactive)
		unavailable.Status = account.Status
		return unavailable
	}
	if !account.Schedulable {
		return permanent(ChatGPTBoundReasonAdminUnschedulable)
	}
	inGroup := false
	for _, id := range account.GroupIDs {
		if id == groupID {
			inGroup = true
			break
		}
	}
	if !inGroup {
		return permanent(ChatGPTBoundReasonNotInGroup)
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		return permanent(ChatGPTBoundReasonExpired)
	}

	// Every active cooldown must end before the owner is schedulable again,
	// so the retry time is the latest of them. The reported reason is the
	// cooldown that ends last.
	var retryAt time.Time
	reason := ""
	consider := func(until *time.Time, why string) {
		if until == nil || !now.Before(*until) {
			return
		}
		if until.After(retryAt) {
			retryAt, reason = *until, why
		}
	}
	consider(account.TempUnschedulableUntil, ChatGPTBoundReasonTempUnschedulable)
	consider(account.OverloadUntil, ChatGPTBoundReasonOverloaded)
	consider(account.RateLimitResetAt, ChatGPTBoundReasonRateLimited)
	if reason == "" {
		return nil
	}
	return &ChatGPTBoundAccountUnavailableError{AccountID: account.ID, Kind: ChatGPTBoundAccountTemporary, Reason: reason, RetryAt: retryAt}
}
