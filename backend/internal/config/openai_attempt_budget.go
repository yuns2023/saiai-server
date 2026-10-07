package config

import (
	"fmt"
	"strings"
	"time"
)

// OpenAIProviderAttemptBudgetConfig is an opt-in, API-key-scoped acceptance
// fence. Its Redis counter must be armed explicitly before provider traffic.
// The fixed deadline and immutable ID prevent a process restart from renewing
// the budget. This is independent of concurrency and successful usage billing.
type OpenAIProviderAttemptBudgetConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	ID          string `mapstructure:"id"`
	APIKeyID    int64  `mapstructure:"api_key_id"`
	MaxAttempts int64  `mapstructure:"max_attempts"`
	ExpiresAt   string `mapstructure:"expires_at"`
}

func (c OpenAIProviderAttemptBudgetConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.ID) < 1 || len(c.ID) > 64 || strings.IndexFunc(c.ID, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_'
	}) >= 0 {
		return fmt.Errorf("gateway.openai_provider_attempt_budget.id must be 1-64 ASCII letters, digits, hyphens or underscores")
	}
	if c.APIKeyID <= 0 || c.MaxAttempts <= 0 {
		return fmt.Errorf("gateway.openai_provider_attempt_budget requires positive api_key_id and max_attempts")
	}
	if _, err := time.Parse(time.RFC3339, c.ExpiresAt); err != nil {
		return fmt.Errorf("gateway.openai_provider_attempt_budget.expires_at must be a fixed RFC3339 deadline")
	}
	return nil
}
