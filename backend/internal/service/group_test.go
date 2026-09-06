//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGroup_GetImagePrice_1K 测试 1K 尺寸返回正确价格
func TestGroup_GetImagePrice_1K(t *testing.T) {
	price := 0.10
	group := &Group{
		ImagePrice1K: &price,
	}

	result := group.GetImagePrice("1K")
	require.NotNil(t, result)
	require.InDelta(t, 0.10, *result, 0.0001)
}

// TestGroup_GetImagePrice_2K 测试 2K 尺寸返回正确价格
func TestGroup_GetImagePrice_2K(t *testing.T) {
	price := 0.15
	group := &Group{
		ImagePrice2K: &price,
	}

	result := group.GetImagePrice("2K")
	require.NotNil(t, result)
	require.InDelta(t, 0.15, *result, 0.0001)
}

// TestGroup_GetImagePrice_4K 测试 4K 尺寸返回正确价格
func TestGroup_GetImagePrice_4K(t *testing.T) {
	price := 0.30
	group := &Group{
		ImagePrice4K: &price,
	}

	result := group.GetImagePrice("4K")
	require.NotNil(t, result)
	require.InDelta(t, 0.30, *result, 0.0001)
}

// TestGroup_GetImagePrice_UnknownSize 测试未知尺寸回退 2K
func TestGroup_GetImagePrice_UnknownSize(t *testing.T) {
	price2K := 0.15
	group := &Group{
		ImagePrice2K: &price2K,
	}

	// 未知尺寸 "3K" 应该回退到 2K
	result := group.GetImagePrice("3K")
	require.NotNil(t, result)
	require.InDelta(t, 0.15, *result, 0.0001)

	// 空字符串也回退到 2K
	result = group.GetImagePrice("")
	require.NotNil(t, result)
	require.InDelta(t, 0.15, *result, 0.0001)
}

// TestGroup_GetImagePrice_NilValues 测试未配置时返回 nil
func TestGroup_GetImagePrice_NilValues(t *testing.T) {
	group := &Group{
		// 所有 ImagePrice 字段都是 nil
	}

	require.Nil(t, group.GetImagePrice("1K"))
	require.Nil(t, group.GetImagePrice("2K"))
	require.Nil(t, group.GetImagePrice("4K"))
	require.Nil(t, group.GetImagePrice("unknown"))
}

// TestGroup_GetImagePrice_PartialConfig 测试部分配置
func TestGroup_GetImagePrice_PartialConfig(t *testing.T) {
	price1K := 0.10
	group := &Group{
		ImagePrice1K: &price1K,
		// ImagePrice2K 和 ImagePrice4K 未配置
	}

	result := group.GetImagePrice("1K")
	require.NotNil(t, result)
	require.InDelta(t, 0.10, *result, 0.0001)

	// 2K 和 4K 返回 nil
	require.Nil(t, group.GetImagePrice("2K"))
	require.Nil(t, group.GetImagePrice("4K"))
}

func TestGroup_IsModelBlocked(t *testing.T) {
	group := &Group{BlockedModelPatterns: []string{
		"claude-fable-5-1",
		"claude-opus-*",
		"gpt-*-mini",
		"*-preview",
	}}

	tests := []struct {
		model   string
		blocked bool
	}{
		{model: "claude-fable-5-1", blocked: true},
		{model: " CLAUDE-FABLE-5-1 ", blocked: true},
		{model: "claude-opus-4-6", blocked: true},
		{model: "gpt-5-mini", blocked: true},
		{model: "vendor-preview", blocked: true},
		{model: "gpt-5", blocked: false},
		{model: "claude-sonnet-4-5", blocked: false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			require.Equal(t, tt.blocked, group.IsModelBlocked(tt.model))
		})
	}
}

func TestGroup_IsModelBlockedMatchesClaudeNormalizedAlias(t *testing.T) {
	group := &Group{BlockedModelPatterns: []string{"claude-sonnet-4-5-20250929"}}
	require.True(t, group.IsModelBlocked("claude-sonnet-4-5"))

	group = &Group{BlockedModelPatterns: []string{"claude-sonnet-4-5"}}
	require.True(t, group.IsModelBlocked("claude-sonnet-4-5-20250929"))
}

func TestNormalizeBlockedModelPatterns(t *testing.T) {
	got, err := NormalizeBlockedModelPatterns([]string{" Claude-Fable-* ", "claude-fable-*", "", "gpt-4o*"})
	require.NoError(t, err)
	require.Equal(t, []string{"claude-fable-*", "gpt-4o*"}, got)

	_, err = NormalizeBlockedModelPatterns([]string{"bad\npattern"})
	require.Error(t, err)
}
