package main

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
)

func TestBillingRoundTrip(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.a46; cc_entrypoint=sdk-cli; cch=00000;"}],"metadata":{"user_id":"user_deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbe_account__session_11111111-1111-4111-8111-111111111111"},"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)

	prompt, err := claudebilling.ExtractFirstUserText(body)
	if err != nil {
		t.Fatalf("ExtractFirstUserText returned error: %v", err)
	}
	version, _ := claudebilling.ExtractCCVersionFromBody(body)
	suffix := claudebilling.ComputeCCVersionSuffix(prompt, version)
	bodyWithVersion, err := claudebilling.ReplaceCCVersion(body, version, suffix)
	if err != nil {
		t.Fatalf("ReplaceCCVersion returned error: %v", err)
	}
	normalized, match, err := claudebilling.NormalizeBodyForCCH(bodyWithVersion)
	if err != nil {
		t.Fatalf("NormalizeBodyForCCH returned error: %v", err)
	}
	seed, mode := claudebilling.CCHProfileForCCVersion(version)
	_, cch := claudebilling.ComputeCCHWithProfile(normalized, seed, mode)
	finalBody := claudebilling.ReplaceCCH(normalized, match, cch)

	text := string(finalBody)
	if !strings.Contains(text, "cc_version=2.1.80.7aa;") {
		t.Fatalf("expected updated cc_version in %q", text)
	}
	if !strings.Contains(text, "cch=7b4ce;") {
		t.Fatalf("expected updated cch in %q", text)
	}
}

func TestBillingRoundTripUsesV2Seed(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.110.a46; cc_entrypoint=sdk-cli; cch=00000;"}],"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)

	prompt, err := claudebilling.ExtractFirstUserText(body)
	if err != nil {
		t.Fatalf("ExtractFirstUserText returned error: %v", err)
	}
	version, _ := claudebilling.ExtractCCVersionFromBody(body)
	suffix := claudebilling.ComputeCCVersionSuffix(prompt, version)
	bodyWithVersion, err := claudebilling.ReplaceCCVersion(body, version, suffix)
	if err != nil {
		t.Fatalf("ReplaceCCVersion returned error: %v", err)
	}
	normalized, match, err := claudebilling.NormalizeBodyForCCH(bodyWithVersion)
	if err != nil {
		t.Fatalf("NormalizeBodyForCCH returned error: %v", err)
	}
	seed, mode := claudebilling.CCHProfileForCCVersion(version)
	_, cch := claudebilling.ComputeCCHWithProfile(normalized, seed, mode)
	finalBody := claudebilling.ReplaceCCH(normalized, match, cch)

	_, v1CCH := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeed)
	_, v2CCH := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeedV2)
	text := string(finalBody)
	if strings.Contains(text, "cch="+v1CCH+";") {
		t.Fatalf("unexpected v1 cch in %q", text)
	}
	if !strings.Contains(text, "cch="+v2CCH+";") {
		t.Fatalf("expected v2 cch in %q", text)
	}
}
