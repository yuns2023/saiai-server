//go:build unit

package repository

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRequestBodyBytesInAllInsertPaths(t *testing.T) {
	size := int64(12345)
	log := &service.UsageLog{RequestID: "body-bytes", APIKeyID: 2, RequestBodyBytes: &size}
	prepared := prepareUsageLogInsert(log)
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	last := len(usageLogInsertArgTypes) - 1
	require.Equal(t, "bigint", usageLogInsertArgTypes[last])
	want := sql.NullInt64{Int64: 12345, Valid: true}
	require.Equal(t, want, prepared.args[last])

	// NULL (unknown) and 0 (empty body) must stay distinct.
	require.Equal(t, sql.NullInt64{}, prepareUsageLogInsert(&service.UsageLog{RequestID: "none", APIKeyID: 2}).args[last])
	zero := int64(0)
	require.Equal(t, sql.NullInt64{Int64: 0, Valid: true}, prepareUsageLogInsert(&service.UsageLog{RequestID: "zero", APIKeyID: 2, RequestBodyBytes: &zero}).args[last])

	key := usageLogBatchKey(log.RequestID, log.APIKeyID)
	batchQuery, batchArgs := buildUsageLogBatchInsertQuery([]string{key}, map[string]usageLogInsertPrepared{key: prepared})
	// input CTE column list, INSERT column list and SELECT ... FROM input.
	require.Equal(t, 3, strings.Count(batchQuery, "request_body_bytes"))
	require.Contains(t, batchQuery, "::bigint")
	require.Equal(t, want, batchArgs[len(batchArgs)-1])

	bestEffortQuery, bestEffortArgs := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
	require.Equal(t, 3, strings.Count(bestEffortQuery, "request_body_bytes"))
	require.Equal(t, want, bestEffortArgs[len(bestEffortArgs)-1])

	require.Equal(t, 1, strings.Count(usageLogSelectColumns, "request_body_bytes"))
	require.True(t, strings.HasSuffix(usageLogSelectColumns, ", request_body_bytes"))
}
