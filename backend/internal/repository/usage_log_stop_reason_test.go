//go:build unit

package repository

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogStopReasonInsertArgs(t *testing.T) {
	refusal := "refusal"
	prepared := prepareUsageLogInsert(&service.UsageLog{RequestID: "stop-reason", APIKeyID: 2, StopReason: &refusal})
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Equal(t, "text", usageLogInsertArgTypes[len(usageLogInsertArgTypes)-1])
	require.Equal(t, sql.NullString{String: "refusal", Valid: true}, prepared.args[len(prepared.args)-1])

	empty := prepareUsageLogInsert(&service.UsageLog{RequestID: "no-stop-reason", APIKeyID: 2})
	require.Equal(t, sql.NullString{}, empty.args[len(empty.args)-1])
}

func TestUsageLogStopReasonInAllInsertPaths(t *testing.T) {
	refusal := "refusal"
	log := &service.UsageLog{RequestID: "stop-reason-batch", APIKeyID: 2, StopReason: &refusal}
	prepared := prepareUsageLogInsert(log)

	batchQuery, batchArgs := buildUsageLogBatchInsertQuery(
		[]string{usageLogBatchKey(log.RequestID, log.APIKeyID)},
		map[string]usageLogInsertPrepared{usageLogBatchKey(log.RequestID, log.APIKeyID): prepared},
	)
	// input CTE columns, INSERT column list and SELECT list.
	require.Equal(t, 3, strings.Count(batchQuery, "stop_reason"))
	require.Equal(t, sql.NullString{String: "refusal", Valid: true}, batchArgs[len(batchArgs)-1])
	require.Contains(t, batchQuery, "ON CONFLICT (request_id, api_key_id) DO NOTHING")

	bestEffortQuery, bestEffortArgs := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
	require.Equal(t, 3, strings.Count(bestEffortQuery, "stop_reason"))
	require.Equal(t, sql.NullString{String: "refusal", Valid: true}, bestEffortArgs[len(bestEffortArgs)-1])

	require.True(t, strings.HasSuffix(usageLogSelectColumns, ", stop_reason"))
	require.Equal(t, 1, strings.Count(usageLogSelectColumns, "stop_reason"))
}
