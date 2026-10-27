//go:build unit

package repository

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogClientAttestationInAllInsertPaths(t *testing.T) {
	status := int16(4)
	log := &service.UsageLog{RequestID: "client-attestation", APIKeyID: 2, ClientAttestation: &status}
	prepared := prepareUsageLogInsert(log)
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Equal(t, "smallint", usageLogInsertArgTypes[len(usageLogInsertArgTypes)-1])
	require.Equal(t, sql.NullInt16{Int16: 4, Valid: true}, prepared.args[len(prepared.args)-1])
	require.Equal(t, sql.NullInt16{}, prepareUsageLogInsert(&service.UsageLog{RequestID: "none", APIKeyID: 2}).args[len(prepared.args)-1])

	batchQuery, batchArgs := buildUsageLogBatchInsertQuery(
		[]string{usageLogBatchKey(log.RequestID, log.APIKeyID)},
		map[string]usageLogInsertPrepared{usageLogBatchKey(log.RequestID, log.APIKeyID): prepared},
	)
	require.Equal(t, 3, strings.Count(batchQuery, "client_attestation"))
	require.Equal(t, sql.NullInt16{Int16: 4, Valid: true}, batchArgs[len(batchArgs)-1])

	bestEffortQuery, bestEffortArgs := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
	require.Equal(t, 3, strings.Count(bestEffortQuery, "client_attestation"))
	require.Equal(t, sql.NullInt16{Int16: 4, Valid: true}, bestEffortArgs[len(bestEffortArgs)-1])
	require.Equal(t, 1, strings.Count(usageLogSelectColumns, "client_attestation"))
}
