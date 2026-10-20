package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type configuredModerationClient struct {
	settings *service.JevSettingsService
	cache    *moderationContentCache
}

func NewConfiguredInputModerationClient(settings *service.JevSettingsService) service.InputModerationClient {
	return &configuredModerationClient{settings: settings, cache: newModerationContentCache()}
}
func (c *configuredModerationClient) Classify(ctx context.Context, text string) (*service.InputModerationClassification, error) {
	settings, _, err := c.settings.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	cfg := &config.Config{}
	cfg.Gateway.InputModeration = settings
	return c.cache.classify(ctx, settings, text, nil, func() (*service.InputModerationClassification, error) {
		return NewInputModerationClient(cfg).Classify(ctx, text)
	})
}

func (c *configuredModerationClient) ClassifyWithPolicy(ctx context.Context, text string, policy *service.InputModerationPolicy) (*service.InputModerationClassification, error) {
	settings, _, err := c.settings.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if settings.Engine == "qwen" {
		return nil, fmt.Errorf("versioned moderation policies require Jev")
	}
	cfg := &config.Config{}
	cfg.Gateway.InputModeration = settings
	return c.cache.classify(ctx, settings, text, policy, func() (*service.InputModerationClassification, error) {
		return newJevModerationClient(cfg).ClassifyWithPolicy(ctx, text, policy)
	})
}
