package tlsfingerprint

import (
	"strings"
	"testing"
)

func TestParseCodexUserAgent(t *testing.T) {
	cases := []struct {
		ua       string
		ok       bool
		version  [3]int
		platform CodexPlatform
	}{
		// Shapes seen in production /v1/responses usage logs.
		{"codex-tui/0.163.0 (Windows 10.0.26200; x86_64) unknown (codex-tui; 0.163.0)", true, [3]int{0, 163, 0}, CodexPlatformWindows},
		{"codex_exec/0.159.0 (Ubuntu 24.4.0; x86_64) VTE/6800 (codex_exec; 0.159.0)", true, [3]int{0, 159, 0}, CodexPlatformLinux},
		{"Codex Desktop/0.163.0-alpha.10.2 (Windows 10.0.26100; x86_64) unknown (Codex Desktop; 26.1013.4894)", true, [3]int{0, 163, 0}, CodexPlatformWindows},
		{"Codex Desktop/0.163.0-alpha.10.2 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1 (Codex Desktop; 26.1013.31256)", true, [3]int{0, 163, 0}, CodexPlatformMacOS},
		{"codex_vscode/0.162.0 (Mac OS 14.6.1; arm64) vscode/1.105.1 (codex_vscode; 0.162.0)", true, [3]int{0, 162, 0}, CodexPlatformMacOS},
		{"codex-tui/0.147.0 (Mac OS 26.0.1; arm64) iTerm.app/3.6.4 (codex-tui; 0.147.0)", true, [3]int{0, 147, 0}, CodexPlatformMacOS},
		{"codex_exec/0.125.0 (Debian 13.0.0; x86_64) xterm-256color (codex_exec; 0.125.0)", true, [3]int{0, 125, 0}, CodexPlatformLinux},
		{"codex_cli_rs/0.104.0 (Arch Linux Rolling Release; x86_64) tmux/3.5a", true, [3]int{0, 104, 0}, CodexPlatformLinux},
		{"  codex-tui/0.116.0 (Windows 10.0.19045; x86_64) WindowsTerminal  ", true, [3]int{0, 116, 0}, CodexPlatformWindows},
		// Unparsable.
		{"", false, [3]int{}, ""},
		{"codex_cli_rs/0.104.0", false, [3]int{}, ""}, // gateway synthetic UA
		{"codex-tui/0.163 (Windows 10; x86_64)", false, [3]int{}, ""},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36", false, [3]int{}, ""},
		{"Go-http-client/1.1", false, [3]int{}, ""},
		{"codex-tui/0.163.0 (Windows 10.0.26200)", false, [3]int{}, ""},
	}
	for _, tc := range cases {
		got, ok := ParseCodexUserAgent(tc.ua)
		if ok != tc.ok || (ok && (got.Version != tc.version || got.Platform != tc.platform)) {
			t.Errorf("%q: got %+v ok=%v, want %v %s ok=%v", tc.ua, got, ok, tc.version, tc.platform, tc.ok)
		}
	}
}

func codexUA(originator, version, os string) string {
	return originator + "/" + version + " (" + os + "; x86_64) unknown (" + originator + "; " + version + ")"
}

func TestCodexTransportPolicyForMatrix(t *testing.T) {
	const (
		linux = "Ubuntu 24.4.0"
		mac   = "Mac OS 15.5.0"
		win   = "Windows 10.0.26200"
	)
	ws := map[string]string{
		"0.115.0":            BaselineCodex0140WSRustlsRing, // pre-WS client: oldest WS baseline
		"0.80.0":             BaselineCodex0140WSRustlsRing,
		"0.116.0":            BaselineCodex0140WSRustlsRing,
		"0.137.0":            BaselineCodex0140WSRustlsRing,
		"0.140.9":            BaselineCodex0140WSRustlsRing,
		"0.141.0":            BaselineCodex0155WSRustlsAwslcPQ,
		"0.142.1":            BaselineCodex0155WSRustlsAwslcPQ,
		"0.142.2":            BaselineCodex0155WSRustlsAwslcPQ,
		"0.147.0":            BaselineCodex0155WSRustlsAwslcPQ,
		"0.155.1":            BaselineCodex0155WSRustlsAwslcPQ,
		"0.156.0":            BaselineCodex0163LinuxWSRustls,
		"0.156.0-alpha.1":    BaselineCodex0163LinuxWSRustls,
		"0.155.9-alpha.3":    BaselineCodex0155WSRustlsAwslcPQ,
		"0.159.0":            BaselineCodex0163LinuxWSRustls,
		"0.163.0-alpha.10.2": BaselineCodex0163LinuxWSRustls,
		"0.200.0":            BaselineCodex0163LinuxWSRustls,
	}
	for version, want := range ws {
		for _, os := range []string{linux, mac, win} {
			got := CodexTransportPolicyFor(RoleWS, codexUA("codex-tui", version, os))
			if got != matrixPolicy(want, RoleWS) {
				t.Errorf("ws %s %s: got %s, want %s", version, os, got.Baseline, want)
			}
		}
	}
	linuxHTTP := map[string]string{
		"0.80.0":          BaselineCodex0142LinuxHTTPOpenSSL35,
		"0.115.0":         BaselineCodex0142LinuxHTTPOpenSSL35,
		"0.116.0":         BaselineCodex0142LinuxHTTPOpenSSL35,
		"0.141.0":         BaselineCodex0142LinuxHTTPOpenSSL35,
		"0.142.1":         BaselineCodex0142LinuxHTTPOpenSSL35,
		"0.142.2-alpha.1": BaselineCodex0163LinuxHTTPOpenSSL, // prerelease selects as its base version
		"0.142.2":         BaselineCodex0163LinuxHTTPOpenSSL,
		"0.147.0":         BaselineCodex0163LinuxHTTPOpenSSL,
		"0.163.0":         BaselineCodex0163LinuxHTTPOpenSSL,
	}
	for version, want := range linuxHTTP {
		if got := CodexTransportPolicyFor(RoleHTTP, codexUA("codex_exec", version, linux)); got != matrixPolicy(want, RoleHTTP) {
			t.Errorf("http linux %s: got %s, want %s", version, got.Baseline, want)
		}
	}
	for _, version := range []string{"0.80.0", "0.137.0", "0.142.1", "0.142.2", "0.163.0-alpha.11.1", "0.300.0"} {
		if got := CodexTransportPolicyFor(RoleHTTP, codexUA("codex-tui", version, mac)); got.Baseline != BaselineCodex0163MacOSHTTPSecureTransport {
			t.Errorf("http mac %s: got %s", version, got.Baseline)
		}
		if got := CodexTransportPolicyFor(RoleHTTP, codexUA("codex-tui", version, win)); got.Baseline != BaselineCodex0163WindowsHTTPSChannel {
			t.Errorf("http windows %s: got %s", version, got.Baseline)
		}
	}
}

func TestCodexTransportPolicyForProductionUserAgents(t *testing.T) {
	cases := []struct {
		ua       string
		http, ws string
	}{
		{"codex-tui/0.163.0 (Windows 10.0.26200; x86_64) unknown (codex-tui; 0.163.0)",
			BaselineCodex0163WindowsHTTPSChannel, BaselineCodex0163LinuxWSRustls},
		{"codex_exec/0.147.0 (Ubuntu 22.4.0; x86_64) VTE/6800 (codex_exec; 0.147.0)",
			BaselineCodex0163LinuxHTTPOpenSSL, BaselineCodex0155WSRustlsAwslcPQ},
		{"Codex Desktop/0.163.0-alpha.10.2 (Windows 10.0.26100; x86_64) unknown (Codex Desktop; 26.1013.4894)",
			BaselineCodex0163WindowsHTTPSChannel, BaselineCodex0163LinuxWSRustls},
		{"Codex Desktop/0.163.0-alpha.10.2 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1 (Codex Desktop; 26.1013.31256)",
			BaselineCodex0163MacOSHTTPSecureTransport, BaselineCodex0163LinuxWSRustls},
		{"codex_vscode/0.159.0 (Windows 10.0.22631; x86_64) unknown (codex_vscode; 0.159.0)",
			BaselineCodex0163WindowsHTTPSChannel, BaselineCodex0163LinuxWSRustls},
		{"codex-tui/0.162.0 (Mac OS 15.6.1; arm64) ghostty/1.2.0 (codex-tui; 0.162.0)",
			BaselineCodex0163MacOSHTTPSecureTransport, BaselineCodex0163LinuxWSRustls},
		// Unparsable: newest Linux baselines (= the pre-matrix defaults).
		{"codex_cli_rs/0.104.0", BaselineCodex0163LinuxHTTPOpenSSL, BaselineCodex0163LinuxWSRustls},
		{"", BaselineCodex0163LinuxHTTPOpenSSL, BaselineCodex0163LinuxWSRustls},
		{"Go-http-client/1.1", BaselineCodex0163LinuxHTTPOpenSSL, BaselineCodex0163LinuxWSRustls},
	}
	for _, tc := range cases {
		h, w := CodexTransportPolicyFor(RoleHTTP, tc.ua), CodexTransportPolicyFor(RoleWS, tc.ua)
		if h != matrixPolicy(tc.http, RoleHTTP) || w != matrixPolicy(tc.ws, RoleWS) {
			t.Errorf("%q: got %s / %s, want %s / %s", tc.ua, h.Baseline, w.Baseline, tc.http, tc.ws)
		}
		if h.Validate() != nil || w.Validate() != nil {
			t.Errorf("%q: selected an unregistered baseline", tc.ua)
		}
	}
	if CodexTransportPolicyFor(RoleHTTP, "") != CodexHTTPTransportPolicy() || CodexTransportPolicyFor(RoleWS, "x") != CodexWSTransportPolicy() {
		t.Fatal("unparsable UA must select the default (newest Linux) policies")
	}
	if CodexTransportPolicyFor("other", "codex-tui/0.163.0 (Windows 10; x86_64)").Enabled() {
		t.Fatal("unknown role must not select a baseline")
	}
}

// Every rule names a registered baseline of its role, and every registered
// baseline is reachable from some User-Agent.
func TestCodexBaselineRulesConsistent(t *testing.T) {
	used := map[string]bool{}
	for _, r := range codexBaselineRules {
		if err := matrixPolicy(r.baseline, r.role).Validate(); err != nil {
			t.Fatalf("rule %+v: %v", r, err)
		}
		used[r.baseline] = true
	}
	for name := range clientHelloBaselines {
		if !used[name] {
			t.Fatalf("baseline %s is not selectable", name)
		}
	}
}

// Policy keys carry the baseline id (pool/transport separation) but never
// any part of the User-Agent.
func TestCodexPolicyKeyHasNoUserAgent(t *testing.T) {
	ua := "codex-tui/0.147.0 (Mac OS 26.0.1; arm64) iTerm.app/3.6.4 (codex-tui; 0.147.0)"
	keys := map[string]bool{}
	for _, role := range []Role{RoleHTTP, RoleWS} {
		k := CodexTransportPolicyFor(role, ua).Key()
		if k == "" || keys[k] {
			t.Fatalf("bad or duplicate key %q", k)
		}
		keys[k] = true
		for _, part := range []string{"iTerm", "Mac OS", "0.147.0", "codex-tui"} {
			if strings.Contains(k, part) {
				t.Fatalf("policy key %q leaks UA part %q", k, part)
			}
		}
	}
}
