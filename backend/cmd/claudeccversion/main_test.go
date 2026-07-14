package main

import "testing"

func TestComputeCCVersionSuffix(t *testing.T) {
	if got, want := computeCCVersionSuffix("hello", "2.1.80"), "e14"; got != want {
		t.Fatalf("unexpected suffix: got %q want %q", got, want)
	}
	if got, want := computeCCVersionSuffix("ping", "2.1.80"), "7aa"; got != want {
		t.Fatalf("unexpected suffix: got %q want %q", got, want)
	}
}

func TestPickCCVersionChars(t *testing.T) {
	if got, want := pickCCVersionChars("hello"), "o00"; got != want {
		t.Fatalf("unexpected picked chars: got %q want %q", got, want)
	}
}

func TestExtractFirstUserText(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"ignore"},{"role":"user","content":[{"type":"text","text":"hello world"},{"type":"text","text":"unused"}]}]}`)
	got, err := extractFirstUserText(body)
	if err != nil {
		t.Fatalf("extractFirstUserText returned error: %v", err)
	}
	if want := "hello world"; got != want {
		t.Fatalf("unexpected prompt: got %q want %q", got, want)
	}
}

func TestExtractFirstUserText_SkipsMetaPrefixes(t *testing.T) {
	// 2.1.81+ 的 <available-deferred-tools> 和 2.1.119 interactive 的 <system-reminder>
	// 都是 Claude Code 自动注入的 meta block，客户端算 cc_version suffix 时会跳过。
	body := []byte(`{"messages":[{"role":"user","content":"<available-deferred-tools>ignore me</available-deferred-tools>"},{"role":"user","content":[{"type":"text","text":"<system-reminder>\nThe following skills are available...</system-reminder>"},{"type":"text","text":"ping"}]}]}`)
	got, err := extractFirstUserText(body)
	if err != nil {
		t.Fatalf("extractFirstUserText returned error: %v", err)
	}
	if want := "ping"; got != want {
		t.Fatalf("unexpected prompt: got %q want %q", got, want)
	}
}

func TestExtractFirstUserText_PrefersOrdinaryStringBeforeLaterArray(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"plain string prompt"},{"role":"user","content":[{"type":"text","text":"later array text"}]}]}`)
	got, err := extractFirstUserText(body)
	if err != nil {
		t.Fatalf("extractFirstUserText returned error: %v", err)
	}
	if want := "plain string prompt"; got != want {
		t.Fatalf("unexpected prompt: got %q want %q", got, want)
	}
}

func TestComputeCCVersionSuffix_181(t *testing.T) {
	prompt := "<system-reminder>\nThe following skills are available for use with the Skill tool:\n\n- update-config: Use this skill to configure the Claude Code harness via settings.json.\n"
	if got, want := computeCCVersionSuffix(prompt, "2.1.81"), "df2"; got != want {
		t.Fatalf("unexpected suffix: got %q want %q", got, want)
	}
}

func TestExtractCCVersionFromBody(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.a46; cc_entrypoint=sdk-cli; cch=00000;"}]}`)
	version, suffix := extractCCVersionFromBody(body)
	if got, want := version, "2.1.80"; got != want {
		t.Fatalf("unexpected version: got %q want %q", got, want)
	}
	if got, want := suffix, "a46"; got != want {
		t.Fatalf("unexpected suffix: got %q want %q", got, want)
	}
}
