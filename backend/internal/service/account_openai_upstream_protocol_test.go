package service

import "testing"

func TestAccountGetOpenAIUpstreamProtocol(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    string
	}{
		{name: "nil", account: nil, want: OpenAIUpstreamProtocolPlatformCompat},
		{
			name:    "missing defaults to platform compatibility",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			want:    OpenAIUpstreamProtocolPlatformCompat,
		},
		{
			name: "native relay v1",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Extra:    map[string]any{"openai_upstream_protocol": OpenAIUpstreamProtocolCodexNativeRelayV1},
			},
			want: OpenAIUpstreamProtocolCodexNativeRelayV1,
		},
		{
			name: "unknown fails closed",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Extra:    map[string]any{"openai_upstream_protocol": "future_mode"},
			},
			want: OpenAIUpstreamProtocolPlatformCompat,
		},
		{
			name: "oauth cannot enable relay",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"openai_upstream_protocol": OpenAIUpstreamProtocolCodexNativeRelayV1},
			},
			want: OpenAIUpstreamProtocolPlatformCompat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.account.GetOpenAIUpstreamProtocol(); got != tt.want {
				t.Fatalf("GetOpenAIUpstreamProtocol() = %q, want %q", got, tt.want)
			}
		})
	}
}
