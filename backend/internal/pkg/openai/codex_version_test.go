package openai

import "testing"

func TestParseCodexSemver(t *testing.T) {
	valid := map[string]string{
		"0.164.0":              "0.164.0",
		"1.2.3-alpha.1":        "1.2.3-alpha.1",
		"1.2.3-alpha-1.0a":     "1.2.3-alpha-1.0a",
		"1.2.3+build.5":        "1.2.3",
		"1.2.3-rc.1+sha.abcde": "1.2.3-rc.1",
		"10.20.30":             "10.20.30",
	}
	for in, want := range valid {
		v, ok := ParseCodexSemver(in)
		if !ok || v.String() != want {
			t.Fatalf("%q: ok=%v got %q want %q", in, ok, v.String(), want)
		}
	}
	for _, in := range []string{"", "v1.2.3", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.3-", "1.2.3-01", "1.2.3-a..b", "1.2.3+", "1.2.3+a_b", "1.2.x", " 1.2.3", "1.2.3 "} {
		if _, ok := ParseCodexSemver(in); ok {
			t.Fatalf("%q parsed", in)
		}
	}
}

func TestCodexSemverCompare(t *testing.T) {
	// SemVer 2.0.0 §11 precedence chain.
	ordered := []string{
		"0.163.0", "0.164.0-alpha", "0.164.0-alpha.1", "0.164.0-alpha.beta", "0.164.0-beta",
		"0.164.0-beta.2", "0.164.0-beta.11", "0.164.0-rc.1", "0.164.0", "0.164.1", "0.165.0", "1.0.0",
	}
	for i := range ordered {
		for j := range ordered {
			a, _ := ParseCodexSemver(ordered[i])
			b, _ := ParseCodexSemver(ordered[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Fatalf("Compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	huge, ok := ParseCodexSemver("1.2.3-18446744073709551616")
	require := func(cond bool, msg string) {
		t.Helper()
		if !cond {
			t.Fatal(msg)
		}
	}
	alpha, _ := ParseCodexSemver("1.2.3-alpha")
	small, _ := ParseCodexSemver("1.2.3-9")
	require(ok, "huge numeric prerelease parses")
	require(huge.Compare(alpha) < 0, "numeric prerelease sorts before alphanumeric regardless of size")
	require(small.Compare(huge) < 0, "numeric identifiers compare by value")
	a, _ := ParseCodexSemver("1.0.0+a")
	b, _ := ParseCodexSemver("1.0.0+b")
	if a.Compare(b) != 0 {
		t.Fatal("build metadata must not affect precedence")
	}
}

func TestCodexEngineVersion(t *testing.T) {
	cases := []struct {
		ua, originator, header string
		product, version       string
		isCodex, known         bool
	}{
		{"codex_cli_rs/0.164.0 (Ubuntu 22.4.0; x86_64) xterm", "codex_cli_rs", "0.164.0", "codex_cli_rs", "0.164.0", true, true},
		{"codex-tui/0.165.0-alpha.2 (Mac OS 15.1.0; arm64)", "", "0.165.0-alpha.2", "codex-tui", "0.165.0-alpha.2", true, true},
		{"Codex Desktop/0.164.0 (Windows 10.0.26100; x86_64)", "Codex Desktop", "0.164.0", "codex desktop", "0.164.0", true, true},
		{"codex_vscode/0.164.0", "", "0.163.0", "codex_vscode", "", true, false}, // disagree
		{"codex_exec/0.164.0", "", "", "codex_exec", "", true, false},            // missing header
		{"codex_cli_rs/0.164", "", "0.164", "codex_cli_rs", "", true, false},     // not SemVer
		{"curl/8.0", "", "0.164.0", "", "", false, false},
		{"codex_cli_rs/0.164.0", "curl", "0.164.0", "", "", false, false}, // non-Codex originator
	}
	for _, tc := range cases {
		product, version, isCodex, known := CodexEngineVersion(tc.ua, tc.originator, tc.header)
		got := ""
		if known {
			got = version.String()
		}
		if product != tc.product || got != tc.version || isCodex != tc.isCodex || known != tc.known {
			t.Fatalf("%q/%q: got (%q, %q, %v, %v)", tc.ua, tc.header, product, got, isCodex, known)
		}
	}
}
