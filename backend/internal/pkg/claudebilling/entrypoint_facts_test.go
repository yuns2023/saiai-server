package claudebilling

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseUserAgentEntrypoint(t *testing.T) {
	cases := []struct {
		name, ua, entrypoint, version string
		ok                            bool
	}{
		{"t3 code", "claude-cli/2.1.316 (external, t3-code, agent-sdk/0.3.276)", "t3-code", "2.1.316", true},
		{"cli", "claude-cli/2.1.316 (external, cli)", "cli", "2.1.316", true},
		{"cli with workload", "claude-cli/2.1.316 (external, cli, workload/cron)", "cli", "2.1.316", true},
		{"vscode", "claude-cli/2.1.280 (external, claude-vscode, agent-sdk/0.3.280)", "claude-vscode", "2.1.280", true},
		{"sdk-cli", "claude-cli/2.1.302 (external, sdk-cli)", "sdk-cli", "2.1.302", true},
		{"upper case", "Claude-CLI/2.1.316 (External, CLI)", "cli", "2.1.316", true},
		{"extra spaces and tabs", "claude-cli/2.1.316 \t (external,\t  cli  , x)", "cli", "2.1.316", true},
		{"version suffix", "claude-cli/2.1.316-beta.1 (external, cli)", "cli", "2.1.316", true},
		{"other client with cli token", "my-tool/1.0 (external, cli)", "", "", false},
		{"claude-cli not at start", "proxy claude-cli/2.1.316 (external, cli)", "", "", false},
		{"no parens", "claude-cli/2.1.316", "", "", false},
		{"one token in parens", "claude-cli/2.1.316 (cli)", "", "", false},
		{"no space before parens", "claude-cli/2.1.316(external, cli)", "", "", false},
		{"bad version", "claude-cli/2.1 (external, cli)", "", "", false},
		{"empty", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entrypoint, version, ok := ParseUserAgentEntrypoint(tc.ua)
			if entrypoint != tc.entrypoint || version != tc.version || ok != tc.ok {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", entrypoint, version, ok, tc.entrypoint, tc.version, tc.ok)
			}
		})
	}
}

func billingHeader(entrypoint string) string {
	return "x-anthropic-billing-header: cc_version=2.1.316.abc; cc_entrypoint=" + entrypoint + "; cch=12345;"
}

func TestParseBodyEntrypoint(t *testing.T) {
	block := func(text string) string {
		return `{"type":"text","text":` + quote(text) + `}`
	}
	cases := []struct {
		name, body, entrypoint, status string
	}{
		{"system string", `{"system":` + quote(billingHeader("cli")+"\nYou are Claude Code.") + `,"messages":[]}`, "cli", EntrypointBodyValid},
		{"first block", `{"system":[` + block(billingHeader("t3-code")) + `,` + block("You are Claude Code.") + `]}`, "t3-code", EntrypointBodyValid},
		{"middle block", `{"system":[` + block("a") + `,` + block(billingHeader("sdk-cli")) + `,` + block("b") + `]}`, "sdk-cli", EntrypointBodyValid},
		{"last block", `{"system":[` + block("a") + `,` + block(billingHeader("claude-vscode")) + `]}`, "claude-vscode", EntrypointBodyValid},
		{"value case and space", `{"system":[` + block("x-anthropic-billing-header: cc_version=2.1.316.abc; cc_entrypoint= CLI ; cch=12345;") + `]}`, "cli", EntrypointBodyValid},
		{"unicode escaped", `{"system":[{"type":"\u0074ext","text":"x-anthropic-billing-header: cc_version=2.1.316.abc; cc_entrypoint=\u0063\u004Ci; cch=12345;"}]}`, "cli", EntrypointBodyValid},
		{"escaped system key", `{"sys\u0074em":[` + block(billingHeader("cli")) + `]}`, "cli", EntrypointBodyValid},
		{"non-text block ignored", `{"system":[{"type":"image","text":` + quote(billingHeader("cli")) + `}]}`, "", EntrypointBodyMissing},
		{"two headers same value", `{"system":[` + block(billingHeader("cli")) + `,` + block(billingHeader("cli")) + `]}`, "", EntrypointBodyAmbiguous},
		{"two headers different values", `{"system":[` + block(billingHeader("cli")) + `,` + block(billingHeader("t3-code")) + `]}`, "", EntrypointBodyAmbiguous},
		{"two headers in one text", `{"system":` + quote(billingHeader("cli")+"\n"+billingHeader("cli")) + `}`, "", EntrypointBodyAmbiguous},
		{"two cc_entrypoint in header", `{"system":[` + block("x-anthropic-billing-header: cc_version=2.1.316.abc; cc_entrypoint=cli; cc_entrypoint=t3-code; cch=12345;") + `]}`, "", EntrypointBodyAmbiguous},
		{"cc_entrypoint on next line ignored", `{"system":` + quote("x-anthropic-billing-header: cc_version=2.1.316.abc; cch=12345;\ncc_entrypoint=cli;") + `}`, "", EntrypointBodyMalformed},
		{"empty cc_entrypoint", `{"system":[` + block("x-anthropic-billing-header: cc_version=2.1.316.abc; cc_entrypoint=; cch=12345;") + `]}`, "", EntrypointBodyMalformed},
		{"blank cc_entrypoint", `{"system":[` + block("x-anthropic-billing-header: cc_version=2.1.316.abc; cc_entrypoint=  ; cch=12345;") + `]}`, "", EntrypointBodyMalformed},
		{"unterminated cc_entrypoint", `{"system":[` + block("x-anthropic-billing-header: cc_version=2.1.316.abc; cc_entrypoint=cli") + `]}`, "", EntrypointBodyMalformed},
		{"no cc_entrypoint", `{"system":[` + block("x-anthropic-billing-header: cc_version=2.1.316.abc; cch=12345;") + `]}`, "", EntrypointBodyMalformed},
		{"no header", `{"system":[` + block("You are Claude Code.") + `]}`, "", EntrypointBodyMissing},
		{"system missing, fake header in messages", `{"messages":[{"role":"user","content":` + quote(billingHeader("cli")) + `}]}`, "", EntrypointBodyMissing},
		{"system missing, fake header in tools", `{"tools":[{"name":"x","description":` + quote(billingHeader("cli")) + `}]}`, "", EntrypointBodyMissing},
		{"header only in messages, system without", `{"system":"hi","messages":[{"role":"user","content":` + quote(billingHeader("cli")) + `}]}`, "", EntrypointBodyMissing},
		{"system object", `{"system":{"type":"text","text":` + quote(billingHeader("cli")) + `}}`, "", EntrypointBodyMissing},
		{"duplicate system key", `{"system":[` + block(billingHeader("cli")) + `],"system":[` + block(billingHeader("t3-code")) + `]}`, "", EntrypointBodyAmbiguous},
		{"duplicate system key, one escaped", `{"system":"","sys\u0074em":[` + block(billingHeader("cli")) + `]}`, "", EntrypointBodyAmbiguous},
		{"not an object", `[1,2]`, "", EntrypointBodyMissing},
		{"duplicate text key in block", `{"system":[{"type":"text","text":` + quote(billingHeader("cli")) + `,"text":` + quote(billingHeader("t3-code")) + `}]}`, "", EntrypointBodyAmbiguous},
		{"duplicate type key in block", `{"system":[{"type":"image","type":"text","text":` + quote(billingHeader("cli")) + `}]}`, "", EntrypointBodyAmbiguous},
		{"field name suffix is not cc_entrypoint", `{"system":` + quote("x-anthropic-billing-header: cc_version=2.1.316.abc; not_cc_entrypoint=cli; cch=12345;") + `}`, "", EntrypointBodyMalformed},
		{"cc_entrypoint plus suffixed field", `{"system":` + quote("x-anthropic-billing-header: x_cc_entrypoint=t3-code; cc_entrypoint=cli;") + `}`, "cli", EntrypointBodyValid},
		{"duplicate with unterminated last", `{"system":` + quote("x-anthropic-billing-header: cc_entrypoint=cli; cc_entrypoint=t3-code") + `}`, "", EntrypointBodyAmbiguous},
		{"many separators", `{"system":` + quote("x-anthropic-billing-header: cc_entrypoint=cli;"+strings.Repeat(";", 100000)) + `}`, "cli", EntrypointBodyValid},
		{"header without trailing fields", `{"system":` + quote("x-anthropic-billing-header: cc_entrypoint=cli;") + `}`, "cli", EntrypointBodyValid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			original := string(body)
			entrypoint, status := ParseBodyEntrypoint(body)
			if entrypoint != tc.entrypoint || status != tc.status {
				t.Fatalf("got (%q, %q), want (%q, %q)", entrypoint, status, tc.entrypoint, tc.status)
			}
			if string(body) != original {
				t.Fatalf("body was mutated")
			}
		})
	}
}

func TestParseEntrypointFacts(t *testing.T) {
	facts := ParseEntrypointFacts("claude-cli/2.1.316 (external, t3-code, agent-sdk/0.3.276)",
		[]byte(`{"system":[{"type":"text","text":`+quote(billingHeader("t3-code"))+`}]}`))
	want := EntrypointFacts{UAEntrypoint: "t3-code", UAVersion: "2.1.316", BodyEntrypoint: "t3-code", BodyStatus: EntrypointBodyValid}
	if facts != want {
		t.Fatalf("got %+v, want %+v", facts, want)
	}
}

func TestSanitizeEntrypointForRecord(t *testing.T) {
	cases := map[string]string{
		"":                       EntrypointNone,
		"  ":                     EntrypointNone,
		" CLI ":                  "cli",
		"agent-sdk/0.3.276":      "agent-sdk/0.3.276",
		"t3 code":                EntrypointInvalid,
		"cli\"><script>":         EntrypointInvalid,
		"é":                      EntrypointInvalid,
		strings.Repeat("a", 100): strings.Repeat("a", 64),
	}
	for in, want := range cases {
		if got := SanitizeEntrypointForRecord(in); got != want {
			t.Fatalf("SanitizeEntrypointForRecord(%q) = %q, want %q", in, got, want)
		}
	}
}

func quote(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
