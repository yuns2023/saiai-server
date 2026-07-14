package service

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	"github.com/tidwall/gjson"
)

func TestBuildClaudeBillingHeaderPlaceholder(t *testing.T) {
	header, ok := buildClaudeBillingHeaderPlaceholder("claude-cli/2.1.80 (external, cli)", "cli")
	if !ok {
		t.Fatal("expected billing header placeholder")
	}
	if header != "x-anthropic-billing-header: cc_version=2.1.80.000; cc_entrypoint=cli; cch=00000;" {
		t.Fatalf("unexpected billing header placeholder: %s", header)
	}
}

func TestCreateTestPayload_OAuthIncludesBillingHeaderPlaceholder(t *testing.T) {
	payload, err := createTestPayload("claude-sonnet-4-6", true, "550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatalf("createTestPayload returned error: %v", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	if !bytes.Contains(body, []byte("x-anthropic-billing-header:")) {
		t.Fatalf("expected billing header in payload body: %s", string(body))
	}
	if !bytes.Contains(body, []byte("cc_version="+ExtractCLIVersion(claude.DefaultHeaders["User-Agent"])+".000;")) {
		t.Fatalf("expected placeholder cc_version in payload body: %s", string(body))
	}
	if !bytes.Contains(body, []byte("cch=00000;")) {
		t.Fatalf("expected placeholder cch in payload body: %s", string(body))
	}
	userID := gjson.GetBytes(body, "metadata.user_id").String()
	parsed := ParseMetadataUserID(userID)
	if parsed == nil || parsed.AccountUUID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("expected account_uuid in oauth payload body: %s", string(body))
	}
}

func TestCreateTestPayload_NonOAuthOmitsBillingHeaderPlaceholder(t *testing.T) {
	payload, err := createTestPayload("claude-sonnet-4-6", false, "")
	if err != nil {
		t.Fatalf("createTestPayload returned error: %v", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	if bytes.Contains(body, []byte("x-anthropic-billing-header:")) {
		t.Fatalf("did not expect billing header in non-oauth payload body: %s", string(body))
	}
}

func TestRewriteClaudeBillingHeaderForUserAgent(t *testing.T) {
	payload, err := createTestPayload("claude-sonnet-4-6", true, "550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatalf("createTestPayload returned error: %v", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	rewritten := rewriteClaudeBillingHeaderForUserAgent(body, "claude-cli/2.1.80 (external, cli)", 0)
	if bytes.Contains(rewritten, []byte(".000;")) {
		t.Fatalf("expected cc_version suffix to be rewritten: %s", string(rewritten))
	}
	if bytes.Contains(rewritten, []byte("cch=00000;")) {
		t.Fatalf("expected cch to be rewritten: %s", string(rewritten))
	}

	prompt, err := claudebilling.ExtractFirstUserText(rewritten)
	if err != nil {
		t.Fatalf("ExtractFirstUserText returned error: %v", err)
	}
	expectedSuffix := claudebilling.ComputeCCVersionSuffix(prompt, "2.1.80")
	if !bytes.Contains(rewritten, []byte("cc_version=2.1.80."+expectedSuffix+";")) {
		t.Fatalf("expected rewritten cc_version suffix in body: %s", string(rewritten))
	}

	normalized, _, err := claudebilling.NormalizeBodyForCCH(rewritten)
	if err != nil {
		t.Fatalf("NormalizeBodyForCCH returned error: %v", err)
	}
	_, expectedCCH := claudebilling.ComputeCCH(normalized)
	if !bytes.Contains(rewritten, []byte("cch="+expectedCCH+";")) {
		t.Fatalf("expected rewritten cch in body: %s", string(rewritten))
	}
}
