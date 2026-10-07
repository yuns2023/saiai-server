package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIProviderAttemptBudgetConfig(t *testing.T) {
	valid := OpenAIProviderAttemptBudgetConfig{Enabled: true, ID: "TEST_ONLY-acceptance_1", APIKeyID: 7,
		MaxAttempts: 5, ExpiresAt: "2030-01-01T00:00:00Z"}
	require.NoError(t, valid.Validate())
	for _, mutate := range []func(*OpenAIProviderAttemptBudgetConfig){
		func(c *OpenAIProviderAttemptBudgetConfig) { c.ID = "" },
		func(c *OpenAIProviderAttemptBudgetConfig) { c.ID = "secret/unsupported" },
		func(c *OpenAIProviderAttemptBudgetConfig) { c.APIKeyID = 0 },
		func(c *OpenAIProviderAttemptBudgetConfig) { c.MaxAttempts = 0 },
		func(c *OpenAIProviderAttemptBudgetConfig) { c.ExpiresAt = "1h" },
	} {
		candidate := valid
		mutate(&candidate)
		require.Error(t, candidate.Validate())
		candidate.Enabled = false
		require.NoError(t, candidate.Validate())
	}
	t.Run("environment", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		t.Setenv("GATEWAY_OPENAI_PROVIDER_ATTEMPT_BUDGET_ENABLED", "true")
		t.Setenv("GATEWAY_OPENAI_PROVIDER_ATTEMPT_BUDGET_ID", valid.ID)
		t.Setenv("GATEWAY_OPENAI_PROVIDER_ATTEMPT_BUDGET_API_KEY_ID", "7")
		t.Setenv("GATEWAY_OPENAI_PROVIDER_ATTEMPT_BUDGET_MAX_ATTEMPTS", "5")
		t.Setenv("GATEWAY_OPENAI_PROVIDER_ATTEMPT_BUDGET_EXPIRES_AT", valid.ExpiresAt)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, valid, cfg.Gateway.OpenAIProviderAttemptBudget)
	})
}
