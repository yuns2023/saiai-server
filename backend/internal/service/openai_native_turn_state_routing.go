package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// SelectAccountForNativeCodexRequest routes a provider-issued turn token to
// its owner before ordinary session stickiness can pick a different account.
// It does not edit headers or payloads. Unknown state is still checked by the
// selected OAuth forwarder; API-key traffic keeps its existing behavior.
func (s *OpenAIGatewayService) SelectAccountForNativeCodexRequest(
	c *gin.Context, groupID *int64, userID int64, previousResponseID, sessionHash, requestedModel string,
	excludedIDs map[int64]struct{}, requiredTransport OpenAIUpstreamTransport, payload []byte,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	ctx := c.Request.Context()
	state := c.GetHeader(openAIWSTurnStateHeader)
	if state == "" {
		value := gjson.GetBytes(payload, "client_metadata.x-codex-turn-state")
		if value.Type == gjson.String {
			state = value.String()
		}
	}
	if state == "" {
		return s.SelectAccountWithSchedulerForUser(ctx, groupID, userID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport)
	}
	owner, err := s.getOpenAIWSStateStore().GetTurnStateAccountForUser(ctx, userID, state)
	if err != nil {
		return nil, OpenAIAccountScheduleDecision{}, err
	}
	if owner <= 0 {
		return s.SelectAccountWithSchedulerForUser(ctx, groupID, userID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport)
	}
	decision := OpenAIAccountScheduleDecision{Layer: "turn_state", SelectedAccountID: owner}
	if previousResponseID != "" {
		previousOwner, lookupErr := s.OpenAIContinuationAccountID(ctx, userID, previousResponseID)
		if lookupErr != nil {
			return nil, decision, lookupErr
		}
		if previousOwner > 0 && previousOwner != owner {
			return nil, decision, errOpenAITurnStateAccountMismatch
		}
	}
	if _, excluded := excludedIDs[owner]; excluded || !s.isOpenAIAccountCurrentlyInGroup(ctx, owner, groupID) {
		return nil, decision, errOpenAITurnStateAccountMismatch
	}
	account, err := s.getSchedulableAccount(ctx, owner)
	if err != nil || account == nil || !account.IsOpenAIOAuth() || !account.IsSchedulable() || shouldClearStickySession(account, requestedModel) {
		return nil, decision, errOpenAITurnStateAccountMismatch
	}
	if !(&defaultOpenAIAccountScheduler{service: s}).isAccountTransportCompatible(account, requiredTransport) {
		return nil, decision, errOpenAITurnStateAccountMismatch
	}
	decision.SelectedAccountType = account.Type
	result, acquireErr := s.tryAcquireAccountSlot(ctx, owner, account.Concurrency)
	if acquireErr == nil && result.Acquired {
		if strings.TrimSpace(sessionHash) != "" {
			_ = s.BindStickySession(ctx, groupID, sessionHash, owner)
		}
		return &AccountSelectionResult{Account: account, Acquired: true, ReleaseFunc: result.ReleaseFunc}, decision, nil
	}
	if s.concurrencyService == nil {
		return nil, decision, errOpenAITurnStateAccountMismatch
	}
	cfg := s.schedulingConfig()
	return &AccountSelectionResult{Account: account, WaitPlan: &AccountWaitPlan{
		AccountID: owner, MaxConcurrency: account.Concurrency, Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting,
	}}, decision, nil
}
