package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type configuredModerationClient struct{ settings *service.JevSettingsService }

func NewConfiguredInputModerationClient(settings *service.JevSettingsService) service.InputModerationClient {
	return &configuredModerationClient{settings: settings}
}
func (c *configuredModerationClient) Classify(ctx context.Context, text string) (*service.InputModerationClassification, error) {
	settings, _, err := c.settings.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	cfg := &config.Config{}
	cfg.Gateway.InputModeration = settings
	return NewInputModerationClient(cfg).Classify(ctx, text)
}
