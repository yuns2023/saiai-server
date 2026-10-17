package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestTLSFingerprintConfigurationMigration(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		rejected   bool
		enabled    bool
	}{
		{"automatic", "gateway:\n  tls_fingerprint:\n    enabled: true\n", false, true},
		{"disabled", "gateway:\n  tls_fingerprint:\n    enabled: false\n", false, false},
		{"legacy-template", "gateway:\n  tls_fingerprint:\n    profiles:\n      custom:\n        name: TEST_ONLY_TEMPLATE\n", true, false},
		{"empty-legacy-map", "gateway:\n  tls_fingerprint:\n    profiles: {}\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			dir := t.TempDir()
			t.Setenv("DATA_DIR", dir)
			t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if tc.rejected {
				if err == nil || !strings.Contains(err.Error(), "gateway.tls_fingerprint.profiles is no longer supported") {
					t.Fatalf("expected migration error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Gateway.TLSFingerprint.Enabled != tc.enabled {
				t.Fatal("global switch changed")
			}
		})
	}
}

func TestTLSFingerprintLegacyEnvironmentRejected(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("GATEWAY_TLS_FINGERPRINT_PROFILES", "TEST_ONLY_REMOVED_CONFIGURATION")
	_, err := LoadForBootstrap()
	if err == nil || !strings.Contains(err.Error(), "gateway.tls_fingerprint.profiles is no longer supported") {
		t.Fatalf("expected migration error, got %v", err)
	}
	if strings.Contains(err.Error(), "TEST_ONLY_REMOVED_CONFIGURATION") {
		t.Fatal("configuration value leaked in error")
	}
}
