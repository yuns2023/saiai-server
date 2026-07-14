package claudebilling

import (
	"bytes"
	"testing"
)

func TestComputeCCVersionSuffix(t *testing.T) {
	if got, want := ComputeCCVersionSuffix("hello", "2.1.80"), "e14"; got != want {
		t.Fatalf("unexpected suffix: got %q want %q", got, want)
	}
	if got, want := ComputeCCVersionSuffix("ping", "2.1.80"), "7aa"; got != want {
		t.Fatalf("unexpected suffix: got %q want %q", got, want)
	}
}

func TestPickCCVersionChars(t *testing.T) {
	if got, want := PickCCVersionChars("hello"), "o00"; got != want {
		t.Fatalf("unexpected picked chars: got %q want %q", got, want)
	}
}

// TestPickCCVersionChars_Unicode 验证 codepoint indexing 与客户端 JS 对齐。
// Regression: byte indexing reads UTF-8 continuation bytes instead of the
// codepoints selected by the client.
func TestPickCCVersionChars_Unicode(t *testing.T) {
	prompt := "甲乙丙丁戊己庚辛壬癸子丑寅卯辰巳午未申酉戌亥"
	// runes[4]='戊', runes[7]='辛', runes[20]='戌' (codepoint indexing)
	if got, want := PickCCVersionChars(prompt), "戊辛戌"; got != want {
		t.Fatalf("unicode picked chars: got %q want %q", got, want)
	}
	// End-to-end synthetic Unicode fixture.
	if got, want := ComputeCCVersionSuffix(prompt, "2.1.119"), "f28"; got != want {
		t.Fatalf("unicode suffix: got %q want %q", got, want)
	}
}

func TestComputeCCHFromPlaceholderBody(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.a46; cc_entrypoint=sdk-cli; cch=00000;"}],"metadata":{"user_id":"user_deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbe_account__session_11111111-1111-4111-8111-111111111111"},"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`)

	normalized, match, err := NormalizeBodyForCCH(body)
	if err != nil {
		t.Fatalf("NormalizeBodyForCCH returned error: %v", err)
	}
	if match.Value != "00000" {
		t.Fatalf("expected placeholder cch, got %q", match.Value)
	}

	sum, cch := ComputeCCH(normalized)
	if got, want := sum, uint64(0xd657416e0b90f0aa); got != want {
		t.Fatalf("unexpected xxh64 sum: got %#x want %#x", got, want)
	}
	if got, want := cch, "0f0aa"; got != want {
		t.Fatalf("unexpected cch: got %q want %q", got, want)
	}
}

func TestCandidateUserTexts(t *testing.T) {
	t.Run("array content, all text blocks returned in order, trimmed", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":[` +
			`{"type":"text","text":"<system-reminder>meta1</system-reminder>"},` +
			`{"type":"text","text":"<local-command-caveat>caveat</local-command-caveat>"},` +
			`{"type":"text","text":"  ping\n"},` +
			`{"type":"image","source":{}},` + // non-text block must be dropped
			`{"type":"text","text":"tail"}` +
			`]}]}`)
		got, err := CandidateUserTexts(body)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		want := []string{"<system-reminder>meta1</system-reminder>", "<local-command-caveat>caveat</local-command-caveat>", "ping", "tail"}
		if len(got) != len(want) {
			t.Fatalf("len=%d, want %d; got=%#v", len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("[%d]=%q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("string content returns single candidate", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"user","content":"  hi  "}]}`)
		got, err := CandidateUserTexts(body)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(got) != 1 || got[0] != "hi" {
			t.Fatalf("got=%#v", got)
		}
	})

	t.Run("skips assistant messages, uses all user messages", func(t *testing.T) {
		body := []byte(`{"messages":[` +
			`{"role":"assistant","content":"ignore"},` +
			`{"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]},` +
			`{"role":"user","content":[{"type":"text","text":"later"}]}` +
			`]}`)
		got, err := CandidateUserTexts(body)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(got) != 3 || got[0] != "first" || got[1] != "second" || got[2] != "later" {
			t.Fatalf("got=%#v", got)
		}
	})
}

func TestNormalizeBodyForCCH_ScopesToBillingHeader(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"old x-anthropic-billing-header: cc_version=2.1.123.605; cc_entrypoint=sdk-cli; cch=4b2ec; and cch=abcde in conversation"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.129.d0d; cc_entrypoint=cli; cch=3e615;"}]}`)

	normalized, match, err := NormalizeBodyForCCH(body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if match.Value != "3e615" {
		t.Fatalf("match.Value=%q, want 3e615", match.Value)
	}
	if !bytes.Contains(normalized, []byte("old x-anthropic-billing-header: cc_version=2.1.123.605; cc_entrypoint=sdk-cli; cch=4b2ec; and cch=abcde")) {
		t.Fatalf("conversation cch snippets were modified: %s", normalized)
	}
	if !bytes.Contains(normalized, []byte("cc_entrypoint=cli; cch=00000;")) {
		t.Fatalf("billing cch was not normalized: %s", normalized)
	}
}

func TestBillingHeaderExtractionScopesToSystem(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"old x-anthropic-billing-header: cc_version=2.1.123.605; cc_entrypoint=sdk-cli; cch=4b2ec;"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.129.5a6; cc_entrypoint=cli; cch=129df;"}]}`)

	version, suffix := ExtractCCVersionFromBody(body)
	if version != "2.1.129" || suffix != "5a6" {
		t.Fatalf("unexpected cc_version: %s.%s", version, suffix)
	}
	entrypoint, ok := ExtractCCEntrypointFromBody(body)
	if !ok || entrypoint != "cli" {
		t.Fatalf("unexpected entrypoint: %q ok=%v", entrypoint, ok)
	}
	replaced, err := ReplaceCCVersion(body, "2.1.130", "abc")
	if err != nil {
		t.Fatalf("replace err: %v", err)
	}
	if !bytes.Contains(replaced, []byte("cc_version=2.1.123.605; cc_entrypoint=sdk-cli; cch=4b2ec;")) {
		t.Fatalf("history header was changed: %s", replaced)
	}
	if !bytes.Contains(replaced, []byte("cc_version=2.1.130.abc; cc_entrypoint=cli; cch=129df;")) {
		t.Fatalf("system header was not changed: %s", replaced)
	}
}

func TestBillingHeaderExtractionAllowsActivePartialHeader(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"old x-anthropic-billing-header: cc_version=2.1.123.605; cc_entrypoint=sdk-cli; cch=4b2ec;"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.ecf; cc_entrypoint=sdk-cli;"}]}`)

	version, suffix := ExtractCCVersionFromBody(body)
	if version != "2.1.185" || suffix != "ecf" {
		t.Fatalf("unexpected cc_version: %s.%s", version, suffix)
	}
	entrypoint, ok := ExtractCCEntrypointFromBody(body)
	if !ok || entrypoint != "sdk-cli" {
		t.Fatalf("unexpected entrypoint: %q ok=%v", entrypoint, ok)
	}
	if _, _, err := NormalizeBodyForCCH(body); err == nil {
		t.Fatalf("NormalizeBodyForCCH should still require cch")
	}
}

func TestMatchCCVersionSuffix(t *testing.T) {
	// SHA256("59cf53e54c78" + pick("ping") + "2.1.80")[:3] 上面测试已验证是 "7aa"
	candidates := []string{
		"<system-reminder>ignored meta</system-reminder>",
		"<local-command-caveat>caveat</local-command-caveat>",
		"ping",
	}
	matched, hit := MatchCCVersionSuffix(candidates, "2.1.80", "7aa")
	if !matched {
		t.Fatalf("expected match, got candidates=%#v", candidates)
	}
	if hit != "ping" {
		t.Fatalf("expected hit=%q, got %q", "ping", hit)
	}

	if matched, _ := MatchCCVersionSuffix(candidates, "2.1.80", "000"); matched {
		t.Fatalf("expected no match for suffix=000")
	}
}

func TestComputeCCHCandidates_DualSeed(t *testing.T) {
	// 合成 body（已含 cch=00000 占位），无 PII，可以提交到 repo。
	// 期望 cch 用 Python xxhash 预先算好（xxh64 低 20 位）。
	normalized := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hello dual seed"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.a46; cc_entrypoint=sdk-cli; cch=00000;"}]}`)
	const (
		expectedV1FullCCH     = "da76a" // xxh64(normalized, CCHSeed)   & 0xFFFFF
		expectedV2FullCCH     = "48796" // xxh64(normalized, CCHSeedV2) & 0xFFFFF
		expectedV2FilteredCCH = "0c461" // xxh64(filtered, CCHSeedV2)   & 0xFFFFF
	)

	if _, got := ComputeCCH(normalized); got != expectedV1FullCCH {
		t.Fatalf("ComputeCCH (v1) = %q, want %q", got, expectedV1FullCCH)
	}
	if _, got := ComputeCCHWithSeed(normalized, CCHSeedV2); got != expectedV2FullCCH {
		t.Fatalf("ComputeCCHWithSeed v2 = %q, want %q", got, expectedV2FullCCH)
	}

	cands := ComputeCCHCandidates(normalized)
	if len(cands) != 3 {
		t.Fatalf("len(candidates) = %d, want 3", len(cands))
	}
	wantByProfile := map[struct {
		seed uint64
		mode CCHInputMode
	}]string{
		{CCHSeed, CCHInputModeFullBody}:         expectedV1FullCCH,
		{CCHSeedV2, CCHInputModeFullBody}:       expectedV2FullCCH,
		{CCHSeedV2, CCHInputModeFilteredBodyV2}: expectedV2FilteredCCH,
	}
	for _, c := range cands {
		key := struct {
			seed uint64
			mode CCHInputMode
		}{c.Seed, c.Mode}
		want, ok := wantByProfile[key]
		if !ok {
			t.Fatalf("unexpected profile in candidates: 0x%016x/%s", c.Seed, c.Mode)
		}
		if c.Value != want {
			t.Fatalf("candidate seed=0x%016x mode=%s value=%q, want %q", c.Seed, c.Mode, c.Value, want)
		}
		delete(wantByProfile, key)
	}
	if len(wantByProfile) != 0 {
		t.Fatalf("missing candidates for profiles: %v", wantByProfile)
	}
}

func TestComputeCCHCandidates_FilteredBodyV2MatchesClaude2185Shape(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5-20251001","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# userEmail\nThe user's email address is user@example.com.\n# currentDate\nToday's date is 2026-06-21.\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n</system-reminder>\n\n"},{"type":"text","text":"ping","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.185.ecf; cc_entrypoint=sdk-cli; cch=90c4c;"},{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK.","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"Only reply pong","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[],"metadata":{"user_id":"{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"account_uuid\":\"00000000-0000-4000-8000-000000000000\",\"session_id\":\"00000000-0000-4000-8000-000000000001\"}"},"max_tokens":32000,"thinking":{"budget_tokens":31999,"type":"enabled"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"diagnostics":{"previous_message_id":null},"stream":true}`)
	normalized, match, err := NormalizeBodyForCCH(body)
	if err != nil {
		t.Fatalf("NormalizeBodyForCCH returned error: %v", err)
	}
	if match.Value != "90c4c" {
		t.Fatalf("observed cch = %q, want 90c4c", match.Value)
	}

	cands := ComputeCCHCandidates(normalized)
	cand, ok := SelectCCHCandidateForMatch(cands, match.Value, "2.1.185")
	if !ok {
		t.Fatalf("expected 2.1.185 filtered candidate to match; candidates=%#v", cands)
	}
	if cand.Seed != CCHSeedV2 || cand.Mode != CCHInputModeFilteredBodyV2 || cand.Value != "90c4c" {
		t.Fatalf("matched candidate = %#v, want seed=v2 mode=filtered value=90c4c", cand)
	}

	if _, got := ComputeCCHWithSeed(normalized, CCHSeed); got != "366f2" {
		t.Fatalf("v1 full body cch = %q, want 366f2", got)
	}
	if _, got := ComputeCCHWithSeed(normalized, CCHSeedV2); got != "2d574" {
		t.Fatalf("v2 full body cch = %q, want 2d574", got)
	}
	if got := len(CCHInputForMode(normalized, CCHInputModeFilteredBodyV2)); got != 1303 {
		t.Fatalf("filtered body len = %d, want 1303", got)
	}
}

func TestSeedForCCVersion(t *testing.T) {
	tests := []struct {
		version string
		want    uint64
	}{
		{"", CCHSeed},
		{"2.1.108", CCHSeed},
		{"2.1.109", CCHSeed},
		{"2.1.110", CCHSeedV2},
		{"2.1.119", CCHSeedV2},
		{"v2.1.110", CCHSeedV2},
	}

	for _, tt := range tests {
		if got := SeedForCCVersion(tt.version); got != tt.want {
			t.Fatalf("SeedForCCVersion(%q) = 0x%016x, want 0x%016x", tt.version, got, tt.want)
		}
	}
}

func TestCCHInputModeForCCVersion(t *testing.T) {
	tests := []struct {
		version string
		want    CCHInputMode
	}{
		{"", CCHInputModeFullBody},
		{"2.1.108", CCHInputModeFullBody},
		{"2.1.110", CCHInputModeFullBody},
		{"2.1.171", CCHInputModeFullBody},
		{"2.1.172", CCHInputModeFilteredBodyV2},
		{"2.1.185", CCHInputModeFilteredBodyV2},
	}

	for _, tt := range tests {
		if got := CCHInputModeForCCVersion(tt.version); got != tt.want {
			t.Fatalf("CCHInputModeForCCVersion(%q) = %s, want %s", tt.version, got, tt.want)
		}
	}
}

func TestAllowsMissingCCHInTokenMode(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"", false},
		{"2.1.178", false},
		{"2.1.179", false},
		{"2.1.181", true},
		{"2.1.185", true},
		{"v2.1.181", true},
	}

	for _, tt := range tests {
		if got := AllowsMissingCCHInTokenMode(tt.version); got != tt.want {
			t.Fatalf("AllowsMissingCCHInTokenMode(%q) = %t, want %t", tt.version, got, tt.want)
		}
	}
}

func TestSelectCCHSeedForMatch_TiebreaksCollisionByVersion(t *testing.T) {
	candidates := []CCHCandidate{
		{Seed: CCHSeed, Value: "abcde"},
		{Seed: CCHSeedV2, Value: "abcde"},
	}

	seed, ok := SelectCCHSeedForMatch(candidates, "abcde", "2.1.110")
	if !ok || seed != CCHSeedV2 {
		t.Fatalf("v2 collision seed = 0x%016x ok=%t, want 0x%016x true", seed, ok, CCHSeedV2)
	}

	seed, ok = SelectCCHSeedForMatch(candidates, "abcde", "2.1.108")
	if !ok || seed != CCHSeed {
		t.Fatalf("v1 collision seed = 0x%016x ok=%t, want 0x%016x true", seed, ok, CCHSeed)
	}
}

func TestSelectCCHSeedForMatch_SingleMatchBeatsVersionPreference(t *testing.T) {
	candidates := []CCHCandidate{
		{Seed: CCHSeed, Value: "11111"},
		{Seed: CCHSeedV2, Value: "22222"},
	}

	seed, ok := SelectCCHSeedForMatch(candidates, "22222", "2.1.108")
	if !ok || seed != CCHSeedV2 {
		t.Fatalf("single v2 seed = 0x%016x ok=%t, want 0x%016x true", seed, ok, CCHSeedV2)
	}

	seed, ok = SelectCCHSeedForMatch(candidates, "33333", "2.1.110")
	if ok || seed != 0 {
		t.Fatalf("no match seed = 0x%016x ok=%t, want 0 false", seed, ok)
	}
}

func TestReplaceCCVersion(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.a46; cc_entrypoint=sdk-cli; cch=00000;"}]}`)
	out, err := ReplaceCCVersion(body, "2.1.80", "e14")
	if err != nil {
		t.Fatalf("ReplaceCCVersion returned error: %v", err)
	}
	if got, want := string(out), `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.e14; cc_entrypoint=sdk-cli; cch=00000;"}]}`; got != want {
		t.Fatalf("unexpected output: got %q want %q", got, want)
	}
}

func TestExtractFirstUserText(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"ignore"},{"role":"user","content":[{"type":"text","text":"hello world"},{"type":"text","text":"unused"}]}]}`)
	got, err := ExtractFirstUserText(body)
	if err != nil {
		t.Fatalf("ExtractFirstUserText returned error: %v", err)
	}
	if want := "hello world"; got != want {
		t.Fatalf("unexpected prompt: got %q want %q", got, want)
	}
}

func TestExtractFirstUserText_SkipsMetaPrefixes(t *testing.T) {
	// Claude Code 2.1.119 interactive：array content 里 <system-reminder> 是 meta 块，
	// 客户端算 cc_version suffix 时跳过它用下一条 text。`<available-deferred-tools>`
	// 是更早引入的 meta 形式（2.1.81），同样要跳。
	body := []byte(`{"messages":[{"role":"user","content":"<available-deferred-tools>ignore me</available-deferred-tools>"},{"role":"user","content":[{"type":"text","text":"<system-reminder>\nThe following skills are available...</system-reminder>"},{"type":"text","text":"ping"}]}]}`)
	got, err := ExtractFirstUserText(body)
	if err != nil {
		t.Fatalf("ExtractFirstUserText returned error: %v", err)
	}
	if want := "ping"; got != want {
		t.Fatalf("unexpected prompt: got %q want %q", got, want)
	}
}

func TestExtractFirstUserText_PrefersOrdinaryStringBeforeLaterArray(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"plain string prompt"},{"role":"user","content":[{"type":"text","text":"later array text"}]}]}`)
	got, err := ExtractFirstUserText(body)
	if err != nil {
		t.Fatalf("ExtractFirstUserText returned error: %v", err)
	}
	if want := "plain string prompt"; got != want {
		t.Fatalf("unexpected prompt: got %q want %q", got, want)
	}
}

func TestExtractFirstUserText_FallsBackToString(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"ping"}]}`)
	got, err := ExtractFirstUserText(body)
	if err != nil {
		t.Fatalf("ExtractFirstUserText returned error: %v", err)
	}
	if want := "ping"; got != want {
		t.Fatalf("unexpected prompt: got %q want %q", got, want)
	}
}

func TestExtractCCVersionFromBody_AllDigitSuffix(t *testing.T) {
	// 2.1.104 + 某些 prompt 会算出纯数字 suffix（例：662）
	// 正则必须把 M.m.p 段和 3-hex suffix 分开，否则贪婪匹配会把 .662 吞到 version 里
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.104.662; cc_entrypoint=cli; cch=25518;"}]}`)
	version, suffix := ExtractCCVersionFromBody(body)
	if version != "2.1.104" {
		t.Fatalf("expected version 2.1.104, got %q", version)
	}
	if suffix != "662" {
		t.Fatalf("expected suffix 662, got %q", suffix)
	}
}

func TestComputeCCVersionSuffix_181Samples(t *testing.T) {
	prompt := "<system-reminder>\nThe following skills are available for use with the Skill tool:\n\n- update-config: Use this skill to configure the Claude Code harness via settings.json.\n"
	if got, want := ComputeCCVersionSuffix(prompt, "2.1.81"), "df2"; got != want {
		t.Fatalf("unexpected suffix: got %q want %q", got, want)
	}
}

func TestExtractCCVersionAndEntrypointFromBody(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.a46; cc_entrypoint=sdk-cli; cch=00000;"}]}`)
	version, suffix := ExtractCCVersionFromBody(body)
	if got, want := version, "2.1.80"; got != want {
		t.Fatalf("unexpected version: got %q want %q", got, want)
	}
	if got, want := suffix, "a46"; got != want {
		t.Fatalf("unexpected suffix: got %q want %q", got, want)
	}
	entrypoint, ok := ExtractCCEntrypointFromBody(body)
	if !ok {
		t.Fatal("expected entrypoint to be found")
	}
	if got, want := entrypoint, "sdk-cli"; got != want {
		t.Fatalf("unexpected entrypoint: got %q want %q", got, want)
	}
}
