package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

func TestAntigravityGenerateAuthURLRequiresAuthorizedCredential(t *testing.T) {
	t.Setenv(antigravity.AntigravityOAuthClientSecretEnv, "")
	service := NewAntigravityOAuthService(nil)
	t.Cleanup(service.sessionStore.Stop)

	_, err := service.GenerateAuthURL(context.Background(), nil)
	if err == nil {
		t.Fatal("expected compatibility OAuth to remain disabled without an authorized credential")
	}
	if !strings.Contains(err.Error(), antigravity.AntigravityOAuthClientSecretEnv) {
		t.Fatalf("error should identify the required environment variable: %v", err)
	}
}

func TestAntigravityGenerateAuthURLWithAuthorizedCredential(t *testing.T) {
	t.Setenv(antigravity.AntigravityOAuthClientSecretEnv, "operator-authorized-secret")
	service := NewAntigravityOAuthService(nil)
	t.Cleanup(service.sessionStore.Stop)

	result, err := service.GenerateAuthURL(context.Background(), nil)
	if err != nil {
		t.Fatalf("GenerateAuthURL() error = %v", err)
	}
	if result == nil || result.AuthURL == "" || result.SessionID == "" {
		t.Fatalf("GenerateAuthURL() returned incomplete result: %#v", result)
	}
}

func TestResolveDefaultTierID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		loadRaw map[string]any
		want    string
	}{
		{
			name:    "nil loadRaw",
			loadRaw: nil,
			want:    "",
		},
		{
			name: "missing allowedTiers",
			loadRaw: map[string]any{
				"paidTier": map[string]any{"id": "g1-pro-tier"},
			},
			want: "",
		},
		{
			name:    "empty allowedTiers",
			loadRaw: map[string]any{"allowedTiers": []any{}},
			want:    "",
		},
		{
			name: "tier missing id field",
			loadRaw: map[string]any{
				"allowedTiers": []any{
					map[string]any{"isDefault": true},
				},
			},
			want: "",
		},
		{
			name: "allowedTiers but no default",
			loadRaw: map[string]any{
				"allowedTiers": []any{
					map[string]any{"id": "free-tier", "isDefault": false},
					map[string]any{"id": "standard-tier", "isDefault": false},
				},
			},
			want: "",
		},
		{
			name: "default tier found",
			loadRaw: map[string]any{
				"allowedTiers": []any{
					map[string]any{"id": "free-tier", "isDefault": true},
					map[string]any{"id": "standard-tier", "isDefault": false},
				},
			},
			want: "free-tier",
		},
		{
			name: "default tier id with spaces",
			loadRaw: map[string]any{
				"allowedTiers": []any{
					map[string]any{"id": "  standard-tier  ", "isDefault": true},
				},
			},
			want: "standard-tier",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := resolveDefaultTierID(tc.loadRaw)
			if got != tc.want {
				t.Fatalf("resolveDefaultTierID() = %q, want %q", got, tc.want)
			}
		})
	}
}
