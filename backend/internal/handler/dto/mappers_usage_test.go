package dto

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogFromService_IncludesOpenAIWSMode(t *testing.T) {
	t.Parallel()

	wsLog := &service.UsageLog{
		RequestID:    "req_1",
		Model:        "gpt-5.3-codex",
		OpenAIWSMode: true,
	}
	httpLog := &service.UsageLog{
		RequestID:    "resp_1",
		Model:        "gpt-5.3-codex",
		OpenAIWSMode: false,
	}

	require.True(t, UsageLogFromService(wsLog).OpenAIWSMode)
	require.False(t, UsageLogFromService(httpLog).OpenAIWSMode)
	require.True(t, UsageLogFromServiceAdmin(wsLog).OpenAIWSMode)
	require.False(t, UsageLogFromServiceAdmin(httpLog).OpenAIWSMode)
}

func TestUsageLogFromService_PrefersRequestTypeForLegacyFields(t *testing.T) {
	t.Parallel()

	log := &service.UsageLog{
		RequestID:    "req_2",
		Model:        "gpt-5.3-codex",
		RequestType:  service.RequestTypeWSV2,
		Stream:       false,
		OpenAIWSMode: false,
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.Equal(t, "ws_v2", userDTO.RequestType)
	require.True(t, userDTO.Stream)
	require.True(t, userDTO.OpenAIWSMode)
	require.Equal(t, "ws_v2", adminDTO.RequestType)
	require.True(t, adminDTO.Stream)
	require.True(t, adminDTO.OpenAIWSMode)
}

func TestUsageCleanupTaskFromService_RequestTypeMapping(t *testing.T) {
	t.Parallel()

	requestType := int16(service.RequestTypeStream)
	task := &service.UsageCleanupTask{
		ID:     1,
		Status: service.UsageCleanupStatusPending,
		Filters: service.UsageCleanupFilters{
			RequestType: &requestType,
		},
	}

	dtoTask := UsageCleanupTaskFromService(task)
	require.NotNil(t, dtoTask)
	require.NotNil(t, dtoTask.Filters.RequestType)
	require.Equal(t, "stream", *dtoTask.Filters.RequestType)
}

func TestRequestTypeStringPtrNil(t *testing.T) {
	t.Parallel()
	require.Nil(t, requestTypeStringPtr(nil))
}

func TestUsageLogFromService_IncludesServiceTierForUserAndAdmin(t *testing.T) {
	t.Parallel()

	serviceTier := "priority"
	inboundEndpoint := "/v1/chat/completions"
	upstreamEndpoint := "/v1/responses"
	log := &service.UsageLog{
		RequestID:             "req_3",
		Model:                 "gpt-5.4",
		ServiceTier:           &serviceTier,
		InboundEndpoint:       &inboundEndpoint,
		UpstreamEndpoint:      &upstreamEndpoint,
		AccountRateMultiplier: f64Ptr(1.5),
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.NotNil(t, userDTO.ServiceTier)
	require.Equal(t, serviceTier, *userDTO.ServiceTier)
	require.NotNil(t, userDTO.InboundEndpoint)
	require.Equal(t, inboundEndpoint, *userDTO.InboundEndpoint)
	require.NotNil(t, userDTO.UpstreamEndpoint)
	require.Equal(t, upstreamEndpoint, *userDTO.UpstreamEndpoint)
	require.NotNil(t, adminDTO.ServiceTier)
	require.Equal(t, serviceTier, *adminDTO.ServiceTier)
	require.NotNil(t, adminDTO.InboundEndpoint)
	require.Equal(t, inboundEndpoint, *adminDTO.InboundEndpoint)
	require.NotNil(t, adminDTO.UpstreamEndpoint)
	require.Equal(t, upstreamEndpoint, *adminDTO.UpstreamEndpoint)
	require.NotNil(t, adminDTO.AccountRateMultiplier)
	require.InDelta(t, 1.5, *adminDTO.AccountRateMultiplier, 1e-12)
}

func TestUsageLogFromServiceAdmin_IncludesSessionAccountDiagnostics(t *testing.T) {
	t.Parallel()

	log := &service.UsageLog{
		RequestID:           "req-session-switch",
		Model:               "claude-opus-4-8",
		SessionAccountCount: 2,
		SessionAccounts: []service.UsageLogSessionAccount{
			{ID: 198, Name: "session-account@example.com"},
			{ID: 200, Name: "shared-account-200"},
		},
	}

	adminDTO := UsageLogFromServiceAdmin(log)
	require.Equal(t, 2, adminDTO.SessionAccountCount)
	require.Equal(t, []AccountSummary{
		{ID: 198, Name: "session-account@example.com"},
		{ID: 200, Name: "shared-account-200"},
	}, adminDTO.SessionAccounts)

	userDTO := UsageLogFromService(log)
	require.Equal(t, "req-session-switch", userDTO.RequestID)
}

func TestUsageLogFromServiceAdmin_IncludesFailoverEvents(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	log := &service.UsageLog{
		RequestID: "req-failover",
		Model:     "claude-opus-4-8",
		FailoverEvents: []service.UsageLogFailoverEvent{
			{
				SourceAccountID:   1001,
				SourceAccountName: "synthetic-failover-account",
				StatusCode:        502,
				ErrorType:         "upstream_error",
				ErrorSource:       "upstream_http",
				Message:           "Recovered upstream error 502",
				Detail:            "write /tmp/test-capture/request: no space left on device",
				CreatedAt:         createdAt,
			},
		},
	}

	adminDTO := UsageLogFromServiceAdmin(log)
	require.Len(t, adminDTO.FailoverEvents, 1)
	require.Equal(t, AccountSummary{ID: 1001, Name: "synthetic-failover-account"}, adminDTO.FailoverEvents[0].SourceAccount)
	require.Equal(t, 502, adminDTO.FailoverEvents[0].StatusCode)
	require.Equal(t, "upstream_error", adminDTO.FailoverEvents[0].ErrorType)
	require.Equal(t, "upstream_http", adminDTO.FailoverEvents[0].ErrorSource)
	require.Equal(t, "Recovered upstream error 502", adminDTO.FailoverEvents[0].Message)
	require.Equal(t, "write /tmp/test-capture/request: no space left on device", adminDTO.FailoverEvents[0].Detail)
	require.Equal(t, createdAt, adminDTO.FailoverEvents[0].CreatedAt)

	userDTO := UsageLogFromService(log)
	require.Equal(t, "req-failover", userDTO.RequestID)
}

func f64Ptr(value float64) *float64 {
	return &value
}
