package handler

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	pkgopenai "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const opsClientDiagnosticsKey = "ops_client_diagnostics"

func captureCodexPolicyDiagnostics(c *gin.Context, policy string) {
	if c == nil {
		return
	}
	d := &service.OpsErrorDiagnostics{SchemaVersion: 1, ClientPolicy: policy, ClientVersion: strings.TrimSpace(c.GetHeader("version"))}
	add := func(field string, passed bool, good, bad string) {
		actual := bad
		if passed {
			actual = good
		}
		d.Checks = append(d.Checks, service.OpsDiagnosticCheck{Field: field, Actual: actual})
	}
	add("User-Agent", pkgopenai.IsCodexOfficialClientRequest(c.GetHeader("User-Agent")), "recognized", "unrecognized")
	if strings.TrimSpace(c.GetHeader("User-Agent")) == "" {
		d.Checks[0].Actual = "missing"
	}
	add("originator", pkgopenai.IsCodexOfficialClientOriginator(c.GetHeader("originator")), "recognized", "unrecognized")
	if strings.TrimSpace(c.GetHeader("originator")) == "" {
		d.Checks[1].Actual = "omitted"
	}
	if policy == "cli_only" {
		add("terminal_client", pkgopenai.IsCodexTerminalRequest(c.GetHeader("User-Agent")), "recognized", "unrecognized")
	}
	if policy == "local_proxy_only" {
		for _, field := range []string{"version", "chatgpt-account-id"} {
			add(field, strings.TrimSpace(c.GetHeader(field)) != "", "present", "missing")
		}
	}
	c.Set(opsClientDiagnosticsKey, service.NormalizeOpsErrorDiagnostics(d))
}

func attachOpsErrorDiagnostics(c *gin.Context, entry *service.OpsInsertErrorLogInput) {
	if c == nil || entry == nil {
		return
	}
	d := &service.OpsErrorDiagnostics{SchemaVersion: 1}
	if value, ok := c.Get(opsClientDiagnosticsKey); ok {
		if snapshot, ok := value.(*service.OpsErrorDiagnostics); ok && snapshot != nil {
			*d = *snapshot
		}
	}
	d.Transport = "http"
	if strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		d.Transport = "websocket"
	}
	if c.Writer != nil {
		d.TransportStatusCode = c.Writer.Status()
	}
	entry.Diagnostics = service.NormalizeOpsErrorDiagnostics(d)
	// Compatible response paths intentionally omit the gateway's request ID.
	// Preserve the internal correlation key without changing client headers.
	if c.Request != nil {
		if requestID, ok := c.Request.Context().Value(ctxkey.RequestID).(string); ok && strings.TrimSpace(requestID) != "" {
			entry.RequestID = requestID
		}
	}
}
