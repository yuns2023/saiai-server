package service

import (
	"encoding/json"
	"regexp"
)

// OpsErrorDiagnostics records checks actually evaluated by the gateway. It never
// contains raw identity headers, credentials, or request contents.
type OpsErrorDiagnostics struct {
	SchemaVersion       int                  `json:"schema_version"`
	Transport           string               `json:"transport"`
	TransportStatusCode int                  `json:"transport_status_code"`
	ClientPolicy        string               `json:"client_policy,omitempty"`
	ClientVersion       string               `json:"client_version,omitempty"`
	Checks              []OpsDiagnosticCheck `json:"checks,omitempty"`
}

type OpsDiagnosticCheck struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Passed   bool   `json:"passed"`
}

var opsDiagnosticVersionPattern = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}(-(?:alpha|beta|rc)(?:\.[0-9]{1,6}){0,3})?$`)

// NormalizeOpsErrorDiagnostics is an allowlist at the persistence boundary.
// Unknown fields and arbitrary actual/expected strings are never persisted.
func NormalizeOpsErrorDiagnostics(in *OpsErrorDiagnostics) *OpsErrorDiagnostics {
	if in == nil || in.SchemaVersion != 1 {
		return nil
	}
	out := &OpsErrorDiagnostics{SchemaVersion: 1}
	if in.Transport == "http" || in.Transport == "websocket" {
		out.Transport = in.Transport
	}
	if in.TransportStatusCode >= 100 && in.TransportStatusCode <= 599 {
		out.TransportStatusCode = in.TransportStatusCode
	}
	switch in.ClientPolicy {
	case "official_clients", "cli_only", "local_proxy_only":
		out.ClientPolicy = in.ClientPolicy
	}
	if opsDiagnosticVersionPattern.MatchString(in.ClientVersion) {
		out.ClientVersion = in.ClientVersion
	}
	seen := make(map[string]bool)
	for _, check := range in.Checks {
		if seen[check.Field] {
			continue
		}
		var expected, good, bad string
		switch check.Field {
		case "User-Agent":
			expected, good, bad = "recognized_client", "recognized", "unrecognized"
		case "originator":
			expected, good, bad = "recognized_if_present", "recognized", "unrecognized"
		case "terminal_client":
			expected, good, bad = "terminal_client", "recognized", "unrecognized"
		case "version", "chatgpt-account-id":
			expected, good, bad = "non_empty", "present", "missing"
		default:
			continue
		}
		optionalOriginator := check.Field == "originator" && check.Actual == "omitted"
		missingUA := check.Field == "User-Agent" && check.Actual == "missing"
		if check.Actual != good && check.Actual != bad && !optionalOriginator && !missingUA {
			continue
		}
		seen[check.Field] = true
		out.Checks = append(out.Checks, OpsDiagnosticCheck{
			Field: check.Field, Expected: expected, Actual: check.Actual, Passed: check.Actual == good || optionalOriginator,
		})
	}
	return out
}

func marshalOpsErrorDiagnostics(entry *OpsInsertErrorLogInput) {
	entry.Diagnostics = NormalizeOpsErrorDiagnostics(entry.Diagnostics)
	entry.DiagnosticsJSON = nil
	if entry.Diagnostics != nil {
		data, _ := json.Marshal(entry.Diagnostics)
		value := string(data)
		entry.DiagnosticsJSON = &value
	}
}
