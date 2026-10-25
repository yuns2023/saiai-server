package admin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateCodexVersionGateSettings(t *testing.T) {
	for _, ok := range [][2]string{{"", ""}, {"0.164.0", ""}, {"", "0.170.0-rc.1"}, {"0.164.0", "0.164.0"}, {"0.164.0-alpha.1", "0.164.0"}} {
		require.Empty(t, validateCodexVersionGateSettings(ok[0], ok[1]), ok)
	}
	require.Contains(t, validateCodexVersionGateSettings("v0.164.0", ""), "codex_min_version")
	require.Contains(t, validateCodexVersionGateSettings("", "0.170"), "codex_max_version")
	require.Contains(t, validateCodexVersionGateSettings("0.164.0", "0.164.0-rc.1"), "greater than or equal")
	require.Contains(t, validateCodexVersionGateSettings("0.170.0", "0.164.0"), "greater than or equal")
}
