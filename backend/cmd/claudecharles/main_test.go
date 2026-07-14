package main

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
)

func TestParseTraceAndAnalyzeEntry(t *testing.T) {
	body := mustBuildFinalBody(t, "2.1.80", "sdk-cli", "ping")
	tracePath := filepath.Join(t.TempDir(), "sample.trace")
	trace := fmt.Sprintf(
		"HTTP-Trace-Version: 1.0\r\nGenerator: Charles/5.0.3\r\n\r\n"+
			"Method: POST\r\nProtocol-Version: HTTP/1.1\r\nProtocol: https\r\nHost: api.anthropic.com\r\nFile: /v1/messages?beta=true\r\nRequest-Body-Size: %d\r\n"+
			"Request-Body:<<--EOF-1-\r\n%s\r\n--EOF-1-\r\n",
		len(body),
		string(body),
	)
	if err := os.WriteFile(tracePath, []byte(trace), 0o644); err != nil {
		t.Fatalf("failed to write trace: %v", err)
	}

	entries, err := parseTrace(tracePath)
	if err != nil {
		t.Fatalf("parseTrace returned error: %v", err)
	}
	if got, want := len(entries), 1; got != want {
		t.Fatalf("unexpected entry count: got %d want %d", got, want)
	}
	if got, want := entries[0].BodySize, len(body); got != want {
		t.Fatalf("unexpected body size: got %d want %d", got, want)
	}

	result := analyzeEntry(entries[0].Body)
	if !result.HasBilling {
		t.Fatal("expected billing header to be detected")
	}
	if !result.CCVersionMatch {
		t.Fatalf("expected cc_version match, got found=%s computed=%s", result.FoundCCVersion, result.ComputedCCVersion)
	}
	if !result.CCHMatch {
		t.Fatalf("expected cch match, got found=%s computed=%s", result.FoundCCH, result.ComputedCCH)
	}
	if result.CCHSeed != claudebilling.CCHSeed {
		t.Fatalf("expected v1 seed, got 0x%016x", result.CCHSeed)
	}
}

func TestParseCHLZAndAnalyzeEntry(t *testing.T) {
	body := mustBuildFinalBody(t, "2.1.81", "cli", "<system-reminder>\nThe following skills are available...\n")
	chlzPath := filepath.Join(t.TempDir(), "sample.chlz")
	writeCHLZ(t, chlzPath, body)

	entries, err := parseCHLZ(chlzPath)
	if err != nil {
		t.Fatalf("parseCHLZ returned error: %v", err)
	}
	if got, want := len(entries), 1; got != want {
		t.Fatalf("unexpected entry count: got %d want %d", got, want)
	}
	if got, want := entries[0].BodySize, len(body); got != want {
		t.Fatalf("unexpected body size: got %d want %d", got, want)
	}

	result := analyzeEntry(entries[0].Body)
	if !result.HasBilling {
		t.Fatal("expected billing header to be detected")
	}
	if !result.CCVersionMatch {
		t.Fatalf("expected cc_version match, got found=%s computed=%s", result.FoundCCVersion, result.ComputedCCVersion)
	}
	if !result.CCHMatch {
		t.Fatalf("expected cch match, got found=%s computed=%s", result.FoundCCH, result.ComputedCCH)
	}
	if result.CCHSeed != claudebilling.CCHSeed {
		t.Fatalf("expected v1 seed, got 0x%016x", result.CCHSeed)
	}
}

func TestAnalyzeEntryUsesV2CCHSeed(t *testing.T) {
	body := mustBuildFinalBody(t, "2.1.110", "cli", "ping")

	result := analyzeEntry(body)
	if !result.HasBilling {
		t.Fatal("expected billing header to be detected")
	}
	if !result.CCHMatch {
		t.Fatalf("expected cch match, got found=%s computed=%s", result.FoundCCH, result.ComputedCCH)
	}
	if result.CCHSeed != claudebilling.CCHSeedV2 {
		t.Fatalf("expected v2 seed, got 0x%016x", result.CCHSeed)
	}
}

func TestDetectFormat(t *testing.T) {
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "sample.trace")
	if err := os.WriteFile(tracePath, []byte("HTTP-Trace-Version: 1.0\r\n"), 0o644); err != nil {
		t.Fatalf("failed to write trace: %v", err)
	}
	if got, err := detectFormat(tracePath, "auto"); err != nil || got != "trace" {
		t.Fatalf("unexpected trace detection: got %q err=%v", got, err)
	}

	chlzPath := filepath.Join(dir, "sample.chlz")
	writeCHLZ(t, chlzPath, []byte(`{"ok":true}`))
	if got, err := detectFormat(chlzPath, "auto"); err != nil || got != "chlz" {
		t.Fatalf("unexpected chlz detection: got %q err=%v", got, err)
	}
}

func mustBuildFinalBody(t *testing.T, version, entrypoint, prompt string) []byte {
	t.Helper()
	placeholder := fmt.Sprintf(
		`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":%q}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=%s.000; cc_entrypoint=%s; cch=00000;"}]}`,
		prompt,
		version,
		entrypoint,
	)

	suffix := claudebilling.ComputeCCVersionSuffix(prompt, version)
	bodyWithVersion, err := claudebilling.ReplaceCCVersion([]byte(placeholder), version, suffix)
	if err != nil {
		t.Fatalf("ReplaceCCVersion returned error: %v", err)
	}
	normalized, match, err := claudebilling.NormalizeBodyForCCH(bodyWithVersion)
	if err != nil {
		t.Fatalf("NormalizeBodyForCCH returned error: %v", err)
	}
	seed, mode := claudebilling.CCHProfileForCCVersion(version)
	_, cch := claudebilling.ComputeCCHWithProfile(normalized, seed, mode)
	return claudebilling.ReplaceCCH(normalized, match, cch)
}

func writeCHLZ(t *testing.T, path string, body []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create chlz: %v", err)
	}
	defer func() {
		_ = f.Close()
	}()

	zw := zip.NewWriter(f)
	writeZipFile(t, zw, "0-meta.json", fmt.Sprintf(`{"method":"POST","host":"api.anthropic.com","path":"/v1/messages?beta=true","requestBodySize":%d}`, len(body)))
	writeZipFile(t, zw, "0-req.json", string(body))
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip: %v", err)
	}
}

func writeZipFile(t *testing.T, zw *zip.Writer, name string, content string) {
	t.Helper()
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("failed to create zip member %s: %v", name, err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write zip member %s: %v", name, err)
	}
}
