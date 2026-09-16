package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestBuildOpsSystemLogsWhere_WithClientRequestIDAndUserID(t *testing.T) {
	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	userID := int64(12)
	accountID := int64(34)

	filter := &service.OpsSystemLogFilter{
		StartTime:       &start,
		EndTime:         &end,
		Level:           "warn",
		Component:       "http.access",
		RequestID:       "req-1",
		ClientRequestID: "creq-1",
		UserID:          &userID,
		AccountID:       &accountID,
		Platform:        "openai",
		Model:           "gpt-5",
		Query:           "timeout",
	}

	where, args, hasConstraint := buildOpsSystemLogsWhere(filter)
	if !hasConstraint {
		t.Fatalf("expected hasConstraint=true")
	}
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 11 {
		t.Fatalf("args len = %d, want 11", len(args))
	}
	if !contains(where, "COALESCE(l.client_request_id,'') = $") {
		t.Fatalf("where should include client_request_id condition: %s", where)
	}
	if !contains(where, "l.user_id = $") {
		t.Fatalf("where should include user_id condition: %s", where)
	}
}

func TestBuildOpsSystemLogsCleanupWhere_RequireConstraint(t *testing.T) {
	where, args, hasConstraint := buildOpsSystemLogsCleanupWhere(&service.OpsSystemLogCleanupFilter{})
	if hasConstraint {
		t.Fatalf("expected hasConstraint=false")
	}
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 0 {
		t.Fatalf("args len = %d, want 0", len(args))
	}
}

func TestBuildOpsSystemLogsCleanupWhere_WithClientRequestIDAndUserID(t *testing.T) {
	userID := int64(9)
	filter := &service.OpsSystemLogCleanupFilter{
		ClientRequestID: "creq-9",
		UserID:          &userID,
	}

	where, args, hasConstraint := buildOpsSystemLogsCleanupWhere(filter)
	if !hasConstraint {
		t.Fatalf("expected hasConstraint=true")
	}
	if len(args) != 2 {
		t.Fatalf("args len = %d, want 2", len(args))
	}
	if !contains(where, "COALESCE(l.client_request_id,'') = $") {
		t.Fatalf("where should include client_request_id condition: %s", where)
	}
	if !contains(where, "l.user_id = $") {
		t.Fatalf("where should include user_id condition: %s", where)
	}
}

func TestBuildOpsSystemLogsWhere_WithOAuthAttributionFilters(t *testing.T) {
	filter := &service.OpsSystemLogFilter{
		OAuthAccountType:     service.AccountTypeOAuth,
		OAuthTrafficMode:     service.ClaudeOAuthModePinned,
		OAuthSelectionSource: "sticky_pending",
		OAuthRequestKind:     "messages",
		OAuthStage:           "prepared",
	}

	where, args, hasConstraint := buildOpsSystemLogsWhere(filter)
	if !hasConstraint {
		t.Fatalf("expected hasConstraint=true")
	}
	if len(args) != 5 {
		t.Fatalf("args len = %d, want 5", len(args))
	}
	for _, fragment := range []string{
		"COALESCE(l.component,'') = 'audit.claude_oauth_attribution'",
		"l.extra->>'account_type'",
		"l.extra->>'traffic_mode'",
		"l.extra->>'selection_source'",
		"l.extra->>'request_kind'",
		"l.extra->>'stage'",
	} {
		if !contains(where, fragment) {
			t.Fatalf("where should include %q: %s", fragment, where)
		}
	}
}

func TestBuildOpsSystemLogsCleanupWhere_WithOAuthAttributionFilter(t *testing.T) {
	where, args, hasConstraint := buildOpsSystemLogsCleanupWhere(&service.OpsSystemLogCleanupFilter{
		OAuthSelectionSource: "sticky_confirmed",
	})
	if !hasConstraint {
		t.Fatalf("expected hasConstraint=true")
	}
	if len(args) != 1 || args[0] != "sticky_confirmed" {
		t.Fatalf("unexpected args: %#v", args)
	}
	if !contains(where, "audit.claude_oauth_attribution") || !contains(where, "l.extra->>'selection_source'") {
		t.Fatalf("unexpected where: %s", where)
	}
}

func contains(s string, sub string) bool {
	return strings.Contains(s, sub)
}
