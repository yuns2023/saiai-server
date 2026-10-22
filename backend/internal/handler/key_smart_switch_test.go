//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSmartSwitchAdvanceDoesNotRetryCommittedOrCanceledRequest(t *testing.T) {
	for _, committed := range []bool{true, false} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
		calls := 0
		c.Set("smart_switch_next", middleware.SmartSwitchNext(func(map[int64]bool) (*service.SmartSwitchSelection, error) { calls++; return nil, nil }))
		if committed {
			c.Writer.WriteHeaderNow()
		} else {
			ctx, cancel := context.WithCancel(c.Request.Context())
			cancel()
			c.Request = c.Request.WithContext(ctx)
		}
		key := &service.APIKey{}
		var sub *service.UserSubscription
		require.False(t, advanceSmartSwitch(c, &key, &sub, map[int64]bool{}, nil))
		require.Zero(t, calls)
	}
}

func TestSmartSwitchAdvanceUpdatesEffectiveSubscription(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	first, second := int64(1), int64(2)
	key := &service.APIKey{GroupID: &first}
	sub := &service.UserSubscription{ID: 1}
	c.Set("smart_switch_next", middleware.SmartSwitchNext(func(excluded map[int64]bool) (*service.SmartSwitchSelection, error) {
		require.True(t, excluded[first])
		return &service.SmartSwitchSelection{Key: &service.APIKey{GroupID: &second, Group: &service.Group{ID: second}}}, nil
	}))
	billing := service.NewBillingCacheService(nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple})
	t.Cleanup(billing.Stop)
	require.True(t, advanceSmartSwitch(c, &key, &sub, map[int64]bool{}, billing))
	require.Equal(t, second, *key.GroupID)
	require.Nil(t, sub)
	actual, ok := middleware.GetAPIKeyFromContext(c)
	require.True(t, ok)
	require.Same(t, key, actual)
}

func TestSmartSwitchRebindReplacesPreviousGroupClaudeState(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	first, second := int64(1), int64(2)
	c.Request = c.Request.WithContext(service.WithClaudeOAuthGroupMode(c.Request.Context(), first, service.ClaudeOAuthModePinned))
	h := &GatewayHandler{gatewayService: &service.GatewayService{}}
	key := &service.APIKey{GroupID: &second, Group: &service.Group{ID: second}}
	parsed := &service.ParsedRequest{MetadataUserID: "TEST_ONLY_USER", Model: "TEST_ONLY_MODEL"}

	sessionKey, boundAccountID, source, ok := h.rebindSmartSwitchGroupState(c, key, service.PlatformAnthropic, parsed, false)
	require.True(t, ok)
	require.Zero(t, boundAccountID)
	require.Empty(t, source)
	expected, _, err := h.gatewayService.GenerateStickySessionHashForRequest(context.Background(), &second, service.PlatformAnthropic, parsed)
	require.NoError(t, err)
	require.Equal(t, expected, sessionKey)
	// The pinned mode resolved for the first group no longer owns the context.
	mode, err := h.gatewayService.ResolveClaudeOAuthGroupMode(c.Request.Context(), &first, service.PlatformAnthropic)
	require.NoError(t, err)
	require.Empty(t, mode)
}
