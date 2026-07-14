package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHydrateUsageLogEffectiveReasoningEffort_InheritsNearbyStream(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	sessionID := "sess-1"
	inboundEndpoint := "/v1/messages"
	createdAt := time.Date(2026, 7, 10, 2, 30, 0, 0, time.UTC)
	logs := []service.UsageLog{{
		APIKeyID:        23,
		SessionID:       &sessionID,
		Model:           "claude-sonnet-5",
		RequestType:     service.RequestTypeSync,
		InboundEndpoint: &inboundEndpoint,
		CreatedAt:       createdAt,
	}}

	mock.ExpectQuery("WITH target").
		WithArgs(
			0,
			int64(23),
			sessionID,
			"claude-sonnet-5",
			createdAt,
			int16(service.RequestTypeStream),
			int16(service.RequestTypeUnknown),
			int64(900),
		).
		WillReturnRows(sqlmock.NewRows([]string{"input_idx", "reasoning_effort"}).AddRow(0, "xhigh"))

	err := repo.hydrateUsageLogEffectiveReasoningEffort(context.Background(), logs)
	require.NoError(t, err)
	require.Nil(t, logs[0].ReasoningEffort)
	require.NotNil(t, logs[0].ReasoningEffortEffective)
	require.Equal(t, "xhigh", *logs[0].ReasoningEffortEffective)
	require.True(t, logs[0].ReasoningEffortInherited)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHydrateUsageLogEffectiveReasoningEffort_PreservesRawValue(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	reasoningEffort := "minimal"
	logs := []service.UsageLog{{
		APIKeyID:                 23,
		Model:                    "gpt-5.6-sol",
		RequestType:              service.RequestTypeStream,
		ReasoningEffort:          &reasoningEffort,
		ReasoningEffortEffective: nil,
	}}

	err := repo.hydrateUsageLogEffectiveReasoningEffort(context.Background(), logs)
	require.NoError(t, err)
	require.Same(t, logs[0].ReasoningEffort, logs[0].ReasoningEffortEffective)
	require.False(t, logs[0].ReasoningEffortInherited)
	require.NoError(t, mock.ExpectationsWereMet())
}
