//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGatewayServiceRecordUsage_StopReasonPersistedWithoutChangingBilling(t *testing.T) {
	record := func(stopReason *string, finalOutput *int64) (*UsageLog, *UsageBillingCommand) {
		usageRepo := &openAIRecordUsageLogRepoStub{}
		billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
		svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
		err := svc.RecordUsage(context.Background(), &RecordUsageInput{
			Result: &ForwardResult{
				RequestID: "stop-reason-billing", Model: "claude-sonnet-4", Duration: time.Second,
				Usage:                    ClaudeUsage{InputTokens: 3, OutputTokens: 0, CacheCreationInputTokens: 200000},
				StopReason:               stopReason,
				UpstreamLastOutputTokens: finalOutput,
			},
			APIKey:            &APIKey{ID: 501, Quota: 100},
			User:              &User{ID: 601},
			Account:           &Account{ID: 701},
			ForceCacheBilling: true,
			APIKeyService:     &openAIRecordUsageAPIKeyQuotaStub{},
		})
		require.NoError(t, err)
		require.NotNil(t, usageRepo.lastLog)
		require.NotNil(t, billingRepo.lastCmd)
		return usageRepo.lastLog, billingRepo.lastCmd
	}

	refusal := "refusal"
	zero := int64(0)
	withLog, withCmd := record(&refusal, &zero)
	withoutLog, withoutCmd := record(nil, nil)

	require.NotNil(t, withLog.StopReason)
	require.Equal(t, "refusal", *withLog.StopReason)
	require.Nil(t, withoutLog.StopReason)

	// Billing outputs and fingerprints are independent of the observation.
	require.Equal(t, withoutLog.TotalCost, withLog.TotalCost)
	require.Equal(t, withoutLog.ActualCost, withLog.ActualCost)
	require.Equal(t, withoutLog.InputTokens, withLog.InputTokens)
	require.Equal(t, withoutLog.CacheReadTokens, withLog.CacheReadTokens)
	require.Equal(t, withoutLog.CacheCreationTokens, withLog.CacheCreationTokens)
	require.Equal(t, *withoutCmd, *withCmd)
}
