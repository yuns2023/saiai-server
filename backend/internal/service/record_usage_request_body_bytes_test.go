//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The inbound size is admin diagnostics only: it reaches the usage row and
// changes no billing output, command or fingerprint.
func TestRecordUsage_RequestBodyBytesPersistedWithoutChangingBilling(t *testing.T) {
	size := int64(48 << 20)
	t.Run("gateway", func(t *testing.T) {
		record := func(bodyBytes *int64) (*UsageLog, *UsageBillingCommand) {
			usageRepo := &openAIRecordUsageLogRepoStub{}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result:             &ForwardResult{RequestID: "body-bytes-gateway", Model: "claude-sonnet-4", Duration: time.Second, Usage: ClaudeUsage{InputTokens: 3, OutputTokens: 5}},
				APIKey:             &APIKey{ID: 501, Quota: 100},
				User:               &User{ID: 601},
				Account:            &Account{ID: 701},
				RequestPayloadHash: "hash",
				RequestBodyBytes:   bodyBytes,
				APIKeyService:      &openAIRecordUsageAPIKeyQuotaStub{},
			}))
			return usageRepo.lastLog, billingRepo.lastCmd
		}
		withLog, withCmd := record(&size)
		withoutLog, withoutCmd := record(nil)
		require.Equal(t, size, *withLog.RequestBodyBytes)
		require.Nil(t, withoutLog.RequestBodyBytes)
		require.Equal(t, withoutLog.ActualCost, withLog.ActualCost)
		require.Equal(t, *withoutCmd, *withCmd)
	})
	t.Run("gemini_long_context", func(t *testing.T) {
		usageRepo := &openAIRecordUsageLogRepoStub{}
		billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
		svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
		require.NoError(t, svc.RecordUsageWithLongContext(context.Background(), &RecordUsageLongContextInput{
			Result:           &ForwardResult{RequestID: "body-bytes-gemini", Model: "gemini-2.5-pro", Duration: time.Second, Usage: ClaudeUsage{InputTokens: 3, OutputTokens: 5}},
			APIKey:           &APIKey{ID: 501, Quota: 100},
			User:             &User{ID: 601},
			Account:          &Account{ID: 701},
			RequestBodyBytes: &size,
			APIKeyService:    &openAIRecordUsageAPIKeyQuotaStub{},
		}))
		require.Equal(t, size, *usageRepo.lastLog.RequestBodyBytes)
	})
	t.Run("openai", func(t *testing.T) {
		record := func(bodyBytes *int64) (*UsageLog, *UsageBillingCommand) {
			usageRepo := &openAIRecordUsageLogRepoStub{}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result:             &OpenAIForwardResult{RequestID: "body-bytes-openai", Model: "gpt-5", Duration: time.Second, Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 6}},
				APIKey:             &APIKey{ID: 501, Quota: 100},
				User:               &User{ID: 601},
				Account:            &Account{ID: 701},
				RequestPayloadHash: "hash",
				RequestBodyBytes:   bodyBytes,
			}))
			return usageRepo.lastLog, billingRepo.lastCmd
		}
		withLog, withCmd := record(&size)
		withoutLog, withoutCmd := record(nil)
		require.Equal(t, size, *withLog.RequestBodyBytes)
		require.Nil(t, withoutLog.RequestBodyBytes)
		require.Equal(t, *withoutCmd, *withCmd)
		// The row holds a copy: later mutation of the caller's value is not visible.
		size++
		require.Equal(t, int64(48<<20), *withLog.RequestBodyBytes)
		size--
	})
}
