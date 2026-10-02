package openai

import "testing"

func TestIsCodexCLIRequest(t *testing.T) {
	tests := []struct {
		name string
		ua   string
		want bool
	}{
		{name: "codex_cli_rs 前缀", ua: "codex_cli_rs/0.1.0", want: true},
		{name: "codex_vscode 前缀", ua: "codex_vscode/1.2.3", want: true},
		{name: "大小写混合", ua: "Codex_CLI_Rs/0.1.0", want: true},
		{name: "复合 UA 包含 codex", ua: "Mozilla/5.0 codex_cli_rs/0.1.0", want: true},
		{name: "空白包裹", ua: "  codex_vscode/1.2.3  ", want: true},
		{name: "非 codex", ua: "curl/8.0.1", want: false},
		{name: "空字符串", ua: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCodexCLIRequest(tt.ua)
			if got != tt.want {
				t.Fatalf("IsCodexCLIRequest(%q) = %v, want %v", tt.ua, got, tt.want)
			}
		})
	}
}

func TestIsCodexTerminalRequest(t *testing.T) {
	tests := []struct {
		name string
		ua   string
		want bool
	}{
		{name: "Codex CLI", ua: "codex_cli_rs/0.104.0", want: true},
		{name: "Codex exec", ua: "codex_exec/0.1.0", want: true},
		{name: "VSCode excluded", ua: "codex_vscode/1.2.3", want: false},
		{name: "desktop excluded", ua: "codex_app/1.2.3", want: false},
		{name: "embedded CLI excluded", ua: "curl/7.76.1 codex_cli_rs/0.159.2", want: false},
		{name: "missing version excluded", ua: "codex_cli_rs/", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCodexTerminalRequest(tt.ua); got != tt.want {
				t.Fatalf("IsCodexTerminalRequest(%q) = %v, want %v", tt.ua, got, tt.want)
			}
		})
	}
}

func TestIsCodexOfficialClientRequest(t *testing.T) {
	tests := []struct {
		name string
		ua   string
		want bool
	}{
		{name: "codex_cli_rs 前缀", ua: "codex_cli_rs/0.98.0", want: true},
		{name: "codex_vscode 前缀", ua: "codex_vscode/1.0.0", want: true},
		{name: "codex_app 前缀", ua: "codex_app/0.1.0", want: true},
		{name: "codex_chatgpt_desktop 前缀", ua: "codex_chatgpt_desktop/1.0.0", want: true},
		{name: "codex_atlas 前缀", ua: "codex_atlas/1.0.0", want: true},
		{name: "codex_exec 前缀", ua: "codex_exec/0.1.0", want: true},
		{name: "codex_sdk_ts 前缀", ua: "codex_sdk_ts/0.1.0", want: true},
		{name: "Codex 桌面 UA", ua: "Codex Desktop/1.2.3", want: true},
		{name: "复合 UA 包含 codex_app", ua: "Mozilla/5.0 codex_app/0.1.0", want: false},
		{name: "curl embedded product", ua: "curl/7.76.1 codex_cli_rs/0.159.2", want: false},
		{name: "unknown product", ua: "codex_fake/0.159.2", want: false},
		{name: "unknown desktop product", ua: "Codex Something/1.0", want: false},
		{name: "missing version", ua: "codex_cli_rs/", want: false},
		{name: "version separated from product", ua: "codex_cli_rs/ 0.159.2", want: false},
		{name: "non-version token", ua: "codex_cli_rs/curl", want: false},
		{name: "nested product", ua: "codex_cli_rs/0.159.2/curl", want: false},
		{name: "prerelease with platform", ua: "codex_cli_rs/0.159.2-alpha.1+build (Linux 6.8; x86_64)", want: true},
		{name: "VSCode platform details", ua: "codex_vscode/0.159.2 (Mac OS 26.2.0; arm64) unknown (VS Code; 26.928.31416)", want: true},
		{name: "大小写混合", ua: "Codex_VSCode/1.2.3", want: true},
		{name: "非 codex", ua: "curl/8.0.1", want: false},
		{name: "空字符串", ua: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCodexOfficialClientRequest(tt.ua)
			if got != tt.want {
				t.Fatalf("IsCodexOfficialClientRequest(%q) = %v, want %v", tt.ua, got, tt.want)
			}
		})
	}
}

func TestIsCodexOfficialClientOriginator(t *testing.T) {
	tests := []struct {
		name       string
		originator string
		want       bool
	}{
		{name: "codex_cli_rs", originator: "codex_cli_rs", want: true},
		{name: "codex_vscode", originator: "codex_vscode", want: true},
		{name: "codex_app", originator: "codex_app", want: true},
		{name: "codex_chatgpt_desktop", originator: "codex_chatgpt_desktop", want: true},
		{name: "codex_atlas", originator: "codex_atlas", want: true},
		{name: "codex_exec", originator: "codex_exec", want: true},
		{name: "codex_sdk_ts", originator: "codex_sdk_ts", want: true},
		{name: "Codex 前缀", originator: "Codex Desktop", want: true},
		{name: "空白包裹", originator: "  codex_vscode  ", want: true},
		{name: "非 codex", originator: "my_client", want: false},
		{name: "unknown codex prefix", originator: "codex_fake", want: false},
		{name: "embedded originator", originator: "my_client codex_cli_rs", want: false},
		{name: "originator suffix", originator: "codex_cli_rs_attacker", want: false},
		{name: "originator version", originator: "codex_cli_rs/0.159.2", want: false},
		{name: "unknown codex label", originator: "Codex Something", want: false},
		{name: "空字符串", originator: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCodexOfficialClientOriginator(tt.originator)
			if got != tt.want {
				t.Fatalf("IsCodexOfficialClientOriginator(%q) = %v, want %v", tt.originator, got, tt.want)
			}
		})
	}
}

func TestIsCodexOfficialClientByHeaders(t *testing.T) {
	tests := []struct {
		name       string
		ua         string
		originator string
		want       bool
	}{
		{name: "仅 originator 命中 desktop", originator: "Codex Desktop", want: false},
		{name: "仅 originator 命中 vscode", originator: "codex_vscode", want: false},
		{name: "curl with official originator", ua: "curl/7.76.1", originator: "codex_cli_rs", want: false},
		{name: "embedded UA with official originator", ua: "curl/7.76.1 codex_cli_rs/0.159.2", originator: "codex_cli_rs", want: false},
		{name: "official UA with unknown originator", ua: "codex_cli_rs/0.159.2", originator: "codex_fake", want: false},
		{name: "official UA with non-Codex originator", ua: "codex_cli_rs/0.159.2", originator: "my_client", want: false},
		{name: "CLI with exact originator", ua: "codex_cli_rs/0.159.2", originator: "codex_cli_rs", want: true},
		{name: "app-server desktop originator", ua: "codex_cli_rs/0.153.4", originator: "Codex Desktop", want: true},
		{name: "app-server VSCode originator", ua: "codex_cli_rs/0.153.4", originator: "codex_vscode", want: true},
		{name: "仅 ua 命中 desktop", ua: "Codex Desktop/1.2.3", want: true},
		{name: "ua 与 originator 都未命中", ua: "curl/8.0.1", originator: "my_client", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCodexOfficialClientByHeaders(tt.ua, tt.originator)
			if got != tt.want {
				t.Fatalf("IsCodexOfficialClientByHeaders(%q, %q) = %v, want %v", tt.ua, tt.originator, got, tt.want)
			}
		})
	}
}
