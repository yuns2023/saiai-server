package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountDTOExposesTLSFingerprintPerPlatformDefault(t *testing.T) {
	enabled := func(a *service.Account) *bool { return AccountFromService(a).EnableTLSFingerprint }
	for _, tc := range []struct {
		name    string
		account *service.Account
		want    bool
	}{
		{"anthropic_oauth_default_on", &service.Account{Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth}, true},
		{"anthropic_oauth_opt_out", &service.Account{Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": false}}, false},
		{"openai_oauth_default_off", &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}, false},
		{"openai_oauth_opt_in", &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true}}, true},
		{"openai_apikey_never", &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: map[string]any{"enable_tls_fingerprint": true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := enabled(tc.account)
			if tc.want {
				require.NotNil(t, got)
				require.True(t, *got)
			} else {
				require.Nil(t, got)
			}
		})
	}
}
