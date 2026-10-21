package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsErrorDiagnosticsPersistenceAllowlist(t *testing.T) {
	entry := &OpsInsertErrorLogInput{
		DiagnosticsJSON: new(string),
		Diagnostics: &OpsErrorDiagnostics{
			SchemaVersion: 1, Transport: "websocket", TransportStatusCode: 101,
			ClientPolicy: "local_proxy_only", ClientVersion: "Bearer TEST_ONLY_SECRET",
			Checks: []OpsDiagnosticCheck{
				{Field: "chatgpt-account-id", Expected: "TEST_ONLY_SECRET", Actual: "missing", Passed: true},
				{Field: "version", Actual: "present"},
				{Field: "Authorization", Actual: "TEST_ONLY_SECRET"},
				{Field: "chatgpt-account-id", Actual: "TEST_ONLY_SECRET"},
			},
		},
	}
	marshalOpsErrorDiagnostics(entry)
	require.NotNil(t, entry.DiagnosticsJSON)
	require.NotContains(t, *entry.DiagnosticsJSON, "TEST_ONLY")
	require.NotContains(t, *entry.DiagnosticsJSON, "Authorization")
	require.Empty(t, entry.Diagnostics.ClientVersion)
	require.Len(t, entry.Diagnostics.Checks, 2)
	require.Equal(t, OpsDiagnosticCheck{Field: "chatgpt-account-id", Expected: "non_empty", Actual: "missing", Passed: false}, entry.Diagnostics.Checks[0])
	var decoded OpsErrorDiagnostics
	require.NoError(t, json.Unmarshal([]byte(*entry.DiagnosticsJSON), &decoded))
	require.Equal(t, entry.Diagnostics.Checks, decoded.Checks)
	entry.Diagnostics = nil
	marshalOpsErrorDiagnostics(entry)
	require.Nil(t, entry.DiagnosticsJSON, "untrusted pre-serialized data must be discarded")
	require.Nil(t, NormalizeOpsErrorDiagnostics(&OpsErrorDiagnostics{SchemaVersion: 999}))
}

func TestOpsErrorDiagnosticVersions(t *testing.T) {
	for _, version := range []string{"0.163.0", "0.163.0-alpha.10.2", "1.2.3-rc.1"} {
		require.Equal(t, version, NormalizeOpsErrorDiagnostics(&OpsErrorDiagnostics{SchemaVersion: 1, ClientVersion: version}).ClientVersion)
	}
}
