package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRewriteClaudeEnvironmentBlock(t *testing.T) {
	input := strings.Join([]string{
		"# Before",
		"keep me",
		"# Environment",
		"You have been invoked in the following environment:",
		"- Primary working directory: C:\\Users\\sToa",
		"- Is a git repository: false",
		"- Platform: win32",
		"- Shell: bash",
		"- OS Version: Windows 10 Pro 10.0.19045",
		"- You are powered by the model opus-4-8.",
		"# Context management",
		"- Platform: keep this",
	}, "\r\n")

	got, changed := rewriteClaudeEnvironmentBlock(input)
	require.True(t, changed)
	require.Contains(t, got, "# Runtime Context\r\n")
	require.Contains(t, got, claudeRuntimeIntro)
	require.Contains(t, got, "- Active working folder is C:\\Users\\sToa")
	require.Contains(t, got, "- Git repository status is false")
	require.Contains(t, got, "- Runtime platform is win32")
	require.Contains(t, got, "- Command shell is bash")
	require.Contains(t, got, "- Operating system build is Windows 10 Pro 10.0.19045")
	require.Contains(t, got, "- The active model is opus-4-8.")
	require.Contains(t, got, "# Context management\r\n- Platform: keep this")
}

func TestRewriteClaudeEnvironmentInBodyFindsAnySystemTextBlock(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-1","system":[{"type":"text","text":"unrelated"},{"type":"text","text":"# Environment\n- Platform: linux\n"}]}`)

	got, changed := rewriteClaudeEnvironmentInBody(body)
	require.True(t, changed)
	require.Equal(t, "unrelated", gjson.GetBytes(got, "system.0.text").String())
	require.Equal(t, "# Runtime Context\n- Runtime platform is linux\n", gjson.GetBytes(got, "system.1.text").String())
}

func TestRewriteClaudeEnvironmentInBodySupportsStringSystem(t *testing.T) {
	body := []byte(`{"system":"# Environment\n- Shell: zsh\n"}`)

	got, changed := rewriteClaudeEnvironmentInBody(body)
	require.True(t, changed)
	require.Equal(t, "# Runtime Context\n- Command shell is zsh\n", gjson.GetBytes(got, "system").String())
}

func TestRewriteClaudeEnvironmentInBodyLeavesMissingSectionByteExact(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"# Other\n- Platform: linux\n"}]}`)

	got, changed := rewriteClaudeEnvironmentInBody(body)
	require.False(t, changed)
	require.Equal(t, body, got)
}

func TestRemoveClaudeEnvironmentBlockPreservesFollowingSection(t *testing.T) {
	input := "# Before\r\nkeep\r\n# Environment\r\n- Platform: win32\r\n# Context management\r\nkeep this\r\n"

	got, changed := removeClaudeEnvironmentBlock(input)

	require.True(t, changed)
	require.Equal(t, "# Before\r\nkeep\r\n# Context management\r\nkeep this\r\n", got)
}

func TestRemoveClaudeEnvironmentInBodyDeletesEnvironmentOnlyTextBlock(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"keep"},{"type":"text","text":"# Environment\n- Platform: linux\n","cache_control":{"type":"ephemeral"}},{"type":"text","text":"also keep"}]}`)

	got, changed := applyClaudeEnvironmentModeInBody(body, ClaudeEnvironmentModeRemove)

	require.True(t, changed)
	require.Equal(t, int64(2), gjson.GetBytes(got, "system.#").Int())
	require.Equal(t, "keep", gjson.GetBytes(got, "system.0.text").String())
	require.Equal(t, "also keep", gjson.GetBytes(got, "system.1.text").String())
	require.NotContains(t, string(got), claudeEnvironmentHeading)
}

func TestRemoveClaudeEnvironmentInBodyKeepsTextAroundSection(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"before\n# Environment\n- Shell: bash\n# After\nvalue\n"}]}`)

	got, changed := applyClaudeEnvironmentModeInBody(body, ClaudeEnvironmentModeRemove)

	require.True(t, changed)
	require.Equal(t, "before\n# After\nvalue\n", gjson.GetBytes(got, "system.0.text").String())
}

func TestRewriteClaudeEnvironmentIfEnabledIsOptInAndRepairsCCH(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.abc; cc_entrypoint=cli; cch=abcde;\n# Environment\n- Platform: linux\n"}]}`)

	service := &GatewayService{}
	disabledCtx := context.WithValue(context.Background(), ctxkey.Group, hydratedAnthropicGroup(false))
	require.Equal(t, body, service.rewriteClaudeEnvironmentIfEnabled(disabledCtx, body, nil))

	enabledCtx := context.WithValue(context.Background(), ctxkey.Group, hydratedAnthropicGroup(true))
	got := service.rewriteClaudeEnvironmentIfEnabled(enabledCtx, body, nil)
	require.Contains(t, string(got), "# Runtime Context")
	require.Contains(t, string(got), "cc_version=2.1.80.abc")
	require.NotContains(t, string(got), "cch=abcde")
}

func TestRemoveClaudeEnvironmentModeRepairsCCH(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.abc; cc_entrypoint=cli; cch=abcde;\n# Environment\n- Platform: linux\n"}]}`)
	ctx := context.WithValue(context.Background(), ctxkey.Group, hydratedAnthropicGroupWithMode(ClaudeEnvironmentModeRemove))

	got := (&GatewayService{}).rewriteClaudeEnvironmentIfEnabled(ctx, body, nil)

	require.NotContains(t, string(got), claudeEnvironmentHeading)
	require.Contains(t, string(got), "cc_version=2.1.80.abc")
	require.NotContains(t, string(got), "cch=abcde")
}

func TestRewriteClaudeEnvironmentIfEnabledPreservesPlaceholderCCH(t *testing.T) {
	body := []byte(`{"system":"x-anthropic-billing-header: cc_version=2.1.80.abc; cc_entrypoint=cli; cch=00000;\n# Environment\n- Shell: bash\n"}`)
	service := &GatewayService{}
	ctx := context.WithValue(context.Background(), ctxkey.Group, hydratedAnthropicGroup(true))

	got := service.rewriteClaudeEnvironmentIfEnabled(ctx, body, nil)
	require.Contains(t, string(got), "# Runtime Context")
	require.Contains(t, string(got), "cch=00000")
}

func TestRewriteClaudeEnvironmentIfEnabledDoesNotSynthesizeMissingCCH(t *testing.T) {
	body := []byte(`{"system":"x-anthropic-billing-header: cc_version=2.1.185.abc; cc_entrypoint=cli;\n# Environment\n- Shell: bash\n"}`)
	service := &GatewayService{}
	ctx := context.WithValue(context.Background(), ctxkey.Group, hydratedAnthropicGroup(true))

	got := service.rewriteClaudeEnvironmentIfEnabled(ctx, body, nil)
	require.Contains(t, string(got), "# Runtime Context")
	require.NotContains(t, string(got), "cch=")
}

func TestRewriteClaudeEnvironmentIfEnabledRequiresAnthropicGroup(t *testing.T) {
	body := []byte(`{"system":"# Environment\n- Platform: linux\n"}`)
	group := hydratedAnthropicGroup(true)
	group.Platform = PlatformGemini
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)

	got := (&GatewayService{}).rewriteClaudeEnvironmentIfEnabled(ctx, body, nil)
	require.Equal(t, body, got)
}

func TestGroupModelRoutingEnvironmentRewriteMarkerRoundTrip(t *testing.T) {
	stored := EncodeGroupModelRouting(map[string][]int64{"claude-opus-*": {2, 3}}, ClaudeEnvironmentModeRewrite)
	routing, mode := DecodeGroupModelRouting(stored)

	require.Equal(t, ClaudeEnvironmentModeRewrite, mode)
	require.Equal(t, map[string][]int64{"claude-opus-*": {2, 3}}, routing)
	require.NotContains(t, routing, claudeEnvironmentRewriteRoutingMarker)
}

func TestGroupModelRoutingEnvironmentRemoveMarkerRoundTrip(t *testing.T) {
	stored := EncodeGroupModelRouting(nil, ClaudeEnvironmentModeRemove)
	routing, mode := DecodeGroupModelRouting(stored)

	require.Equal(t, ClaudeEnvironmentModeRemove, mode)
	require.Nil(t, routing)
	require.Equal(t, []int64{2}, stored[claudeEnvironmentRewriteRoutingMarker])
}

func hydratedAnthropicGroup(rewrite bool) *Group {
	return &Group{
		ID:                       1,
		Platform:                 PlatformAnthropic,
		Status:                   StatusActive,
		Hydrated:                 true,
		ClaudeEnvironmentRewrite: rewrite,
	}
}

func hydratedAnthropicGroupWithMode(mode string) *Group {
	group := hydratedAnthropicGroup(false)
	group.ClaudeEnvironmentMode = mode
	return group
}
