package service

import (
	"encoding/json"
	"strings"
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

func TestOpsErrorDiagnosticHosts(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"Edge.Example.Test.:443", "edge.example.test"},
		{"gateway.example.test", "gateway.example.test"},
		{"localhost:8080", "localhost"},
		{"192.0.2.10:443", "192.0.2.10"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"2001:db8::1", "2001:db8::1"},
		{"edge.example.test, other.example.test", ""},
		{"https://edge.example.test/path?api_key=TEST_ONLY_SECRET", ""},
		{"TEST_ONLY_SECRET@edge.example.test", ""},
		{"edge.example.test\r\nAuthorization: TEST_ONLY_SECRET", ""},
		{"edge.example.test:70000", ""},
		{"edge.example.test:+443", ""},
		{"edge.example.test:", ""},
		{"edge..test", ""},
		{"-edge.test", ""},
		{"[2001:db8::1%TEST_ONLY_SECRET]", ""},
		{strings.Repeat("a", 64) + ".test", ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			entry := &OpsInsertErrorLogInput{Diagnostics: &OpsErrorDiagnostics{
				SchemaVersion: 1, RequestHost: tc.raw, ForwardedHost: tc.raw,
			}}
			marshalOpsErrorDiagnostics(entry)
			require.Equal(t, tc.want, entry.Diagnostics.RequestHost)
			require.Equal(t, tc.want, entry.Diagnostics.ForwardedHost)
			require.Equal(t, entry.Diagnostics, NormalizeOpsErrorDiagnostics(entry.Diagnostics), "persistence/read normalization must be idempotent")
			require.NotContains(t, *entry.DiagnosticsJSON, "TEST_ONLY_SECRET")
		})
	}
}
