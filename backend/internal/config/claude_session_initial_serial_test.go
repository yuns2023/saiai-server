package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeSessionInitialSerialConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		cfg, err := Load()
		require.NoError(t, err)
		require.True(t, cfg.Gateway.ClaudeSessionInitialSerial.Enabled)
		require.Equal(t, 90*time.Second, cfg.Gateway.ClaudeSessionInitialSerial.WaitTimeout())
	})
	t.Run("environment disables and overrides wait", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		t.Setenv("GATEWAY_CLAUDE_SESSION_INITIAL_SERIAL_ENABLED", "false")
		t.Setenv("GATEWAY_CLAUDE_SESSION_INITIAL_SERIAL_WAIT_TIMEOUT_MS", "45000")
		cfg, err := Load()
		require.NoError(t, err)
		require.False(t, cfg.Gateway.ClaudeSessionInitialSerial.Enabled)
		require.Equal(t, 45*time.Second, cfg.Gateway.ClaudeSessionInitialSerial.WaitTimeout())
	})
	t.Run("wait remains bounded", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		t.Setenv("GATEWAY_CLAUDE_SESSION_INITIAL_SERIAL_WAIT_TIMEOUT_MS", "300001")
		_, err := Load()
		require.ErrorContains(t, err, "claude_session_initial_serial.wait_timeout_ms")
	})
}
