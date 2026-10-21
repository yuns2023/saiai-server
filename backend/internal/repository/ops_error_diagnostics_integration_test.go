//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsErrorDiagnosticsRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewOpsRepository(integrationDB)
	diagnostics := &service.OpsErrorDiagnostics{SchemaVersion: 1, Transport: "websocket", TransportStatusCode: 101,
		Checks: []service.OpsDiagnosticCheck{{Field: "chatgpt-account-id", Expected: "non_empty", Actual: "missing", Passed: false}}}
	data, err := json.Marshal(diagnostics)
	require.NoError(t, err)
	text := string(data)
	entry := &service.OpsInsertErrorLogInput{RequestID: "diagnostics-single", ErrorPhase: "request", ErrorType: "invalid_request_error", Severity: "error", StatusCode: 400, CreatedAt: time.Now(), DiagnosticsJSON: &text}
	id, err := repo.InsertErrorLog(ctx, entry)
	require.NoError(t, err)
	stored, err := repo.GetErrorLogByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, diagnostics, stored.Diagnostics)
	entry.RequestID = "diagnostics-batch"
	count, err := repo.BatchInsertErrorLogs(ctx, []*service.OpsInsertErrorLogInput{entry})
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	var batchID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM ops_error_logs WHERE request_id = $1", entry.RequestID).Scan(&batchID))
	stored, err = repo.GetErrorLogByID(ctx, batchID)
	require.NoError(t, err)
	require.Equal(t, diagnostics, stored.Diagnostics)
	entry.DiagnosticsJSON = nil
	entry.RequestID = "diagnostics-legacy"
	id, err = repo.InsertErrorLog(ctx, entry)
	require.NoError(t, err)
	stored, err = repo.GetErrorLogByID(ctx, id)
	require.NoError(t, err)
	require.Nil(t, stored.Diagnostics)
}
