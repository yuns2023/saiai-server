//go:build unit

package repository

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogClientEntrypointInAllInsertPaths(t *testing.T) {
	flag := &service.ClientEntrypointFlag{UA: "t3-code", Body: "t3-code", BodyStatus: "valid", Reason: "ua_not_allowed", GroupID: 9, Policy: "observe"}
	log := &service.UsageLog{RequestID: "client-entrypoint", APIKeyID: 2, ClientEntrypoint: flag}
	prepared := prepareUsageLogInsert(log)
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Equal(t, "jsonb", usageLogInsertArgTypes[len(usageLogInsertArgTypes)-1])
	body, ok := prepared.args[len(prepared.args)-1].(string)
	require.True(t, ok)
	require.JSONEq(t, `{"ua":"t3-code","body":"t3-code","body_status":"valid","reason":"ua_not_allowed","group_id":9,"policy":"observe"}`, body)
	require.Nil(t, prepareUsageLogInsert(&service.UsageLog{RequestID: "none", APIKeyID: 2}).args[len(prepared.args)-1])

	batchQuery, batchArgs := buildUsageLogBatchInsertQuery(
		[]string{usageLogBatchKey(log.RequestID, log.APIKeyID)},
		map[string]usageLogInsertPrepared{usageLogBatchKey(log.RequestID, log.APIKeyID): prepared},
	)
	require.Equal(t, 3, strings.Count(batchQuery, "client_entrypoint"))
	require.Equal(t, body, batchArgs[len(batchArgs)-1])

	bestEffortQuery, bestEffortArgs := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
	require.Equal(t, 3, strings.Count(bestEffortQuery, "client_entrypoint"))
	require.Equal(t, body, bestEffortArgs[len(bestEffortArgs)-1])
	require.Equal(t, 1, strings.Count(usageLogSelectColumns, "client_entrypoint"))

	var decoded service.ClientEntrypointFlag
	require.NoError(t, json.Unmarshal([]byte(body), &decoded))
	require.Equal(t, *flag, decoded)
}
