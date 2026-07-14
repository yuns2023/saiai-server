package main

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
)

func TestComputeCCHFromPlaceholderBody(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.a46; cc_entrypoint=sdk-cli; cch=00000;"}],"metadata":{"user_id":"user_deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbe_account__session_11111111-1111-4111-8111-111111111111"},"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)

	normalized, match, err := normalizeBodyForCCH(body)
	if err != nil {
		t.Fatalf("normalizeBodyForCCH returned error: %v", err)
	}
	if match.Value != "00000" {
		t.Fatalf("expected placeholder cch, got %q", match.Value)
	}

	sum, cch := computeCCH(normalized)
	if got, want := sum, uint64(0xd657416e0b90f0aa); got != want {
		t.Fatalf("unexpected xxh64 sum: got %#x want %#x", got, want)
	}
	if got, want := cch, "0f0aa"; got != want {
		t.Fatalf("unexpected cch: got %q want %q", got, want)
	}
}

func TestComputeCCHWithSeedUsesV2(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.110.a46; cc_entrypoint=sdk-cli; cch=00000;"}],"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)

	normalized, _, err := normalizeBodyForCCH(body)
	if err != nil {
		t.Fatalf("normalizeBodyForCCH returned error: %v", err)
	}
	sum, cch := computeCCHWithSeed(normalized, claudebilling.SeedForCCVersion("2.1.110"))
	wantSum, wantCCH := claudebilling.ComputeCCHWithSeed(normalized, claudebilling.CCHSeedV2)
	if sum != wantSum || cch != wantCCH {
		t.Fatalf("v2 cch = %#x %s, want %#x %s", sum, cch, wantSum, wantCCH)
	}
}

func TestNormalizeBodyForCCHReplacesExistingValue(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.a46; cc_entrypoint=sdk-cli; cch=abcde;"}]}`)

	normalized, match, err := normalizeBodyForCCH(body)
	if err != nil {
		t.Fatalf("normalizeBodyForCCH returned error: %v", err)
	}
	if got, want := match.Value, "abcde"; got != want {
		t.Fatalf("unexpected matched cch: got %q want %q", got, want)
	}
	if got, want := string(normalized), `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.a46; cc_entrypoint=sdk-cli; cch=00000;"}]}`; got != want {
		t.Fatalf("unexpected normalized body: got %q want %q", got, want)
	}
}
