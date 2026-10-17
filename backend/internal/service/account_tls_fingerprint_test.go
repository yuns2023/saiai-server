package service

import "testing"

func TestAccountTLSFingerprintDefaultAndOptOut(t *testing.T) {
	for _, tc := range []struct {
		name, platform, kind string
		extra                map[string]any
		want                 bool
	}{
		{"oauth default", "anthropic", "oauth", nil, true},
		{"setup token default", "anthropic", "setup-token", nil, true},
		{"oauth explicit off", "anthropic", "oauth", map[string]any{"enable_tls_fingerprint": false}, false},
		{"setup token explicit off", "anthropic", "setup-token", map[string]any{"enable_tls_fingerprint": false}, false},
		{"explicit on", "anthropic", "oauth", map[string]any{"enable_tls_fingerprint": true}, true},
		{"api key unsupported", "anthropic", "apikey", map[string]any{"enable_tls_fingerprint": true}, false},
		{"other platform unsupported", "openai", "oauth", map[string]any{"enable_tls_fingerprint": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Account{Platform: tc.platform, Type: tc.kind, Extra: tc.extra}
			if got := a.IsTLSFingerprintEnabled(); got != tc.want {
				t.Fatalf("enabled = %v, want %v", got, tc.want)
			}
		})
	}
}
