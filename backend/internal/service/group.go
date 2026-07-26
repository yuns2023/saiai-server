package service

import (
	"strings"
	"time"
)

const (
	claudeEnvironmentRewriteRoutingMarker = "__saiai_claude_environment_rewrite_v1__"

	ClaudeEnvironmentModeOff     = "off"
	ClaudeEnvironmentModeRewrite = "rewrite"
	ClaudeEnvironmentModeRemove  = "remove"
)

type Group struct {
	ID             int64
	Name           string
	Description    string
	Platform       string
	RateMultiplier float64
	IsExclusive    bool
	Status         string
	Hydrated       bool // indicates the group was loaded from a trusted repository source

	SubscriptionType    string
	FiveHourLimitUSD    *float64
	DailyLimitUSD       *float64
	WeeklyLimitUSD      *float64
	MonthlyLimitUSD     *float64
	DefaultValidityDays int

	// 图片生成计费配置（antigravity 和 gemini 平台使用）
	ImagePrice1K *float64
	ImagePrice2K *float64
	ImagePrice4K *float64

	// Sora 按次计费配置（阶段 1）
	SoraImagePrice360          *float64
	SoraImagePrice540          *float64
	SoraVideoPricePerRequest   *float64
	SoraVideoPricePerRequestHD *float64

	// Sora 存储配额
	SoraStorageQuotaBytes int64

	// Claude Code 客户端限制
	ClaudeCodeOnly                 bool
	AllowClaudeContext1MBeta       bool
	ClaudeOAuthRequestGateDisabled bool
	ClaudeEnvironmentMode          string
	// ClaudeEnvironmentRewrite is retained for compatibility with cached
	// snapshots and older API clients. New code should use
	// EffectiveClaudeEnvironmentMode.
	ClaudeEnvironmentRewrite bool
	FallbackGroupID          *int64
	// 无效请求兜底分组（仅 anthropic 平台使用）
	FallbackGroupIDOnInvalidRequest *int64

	// 模型路由配置
	// key: 模型匹配模式（支持 * 通配符，如 "claude-opus-*"）
	// value: 优先账号 ID 列表
	ModelRouting        map[string][]int64
	ModelRoutingEnabled bool

	// MCP XML 协议注入开关（仅 antigravity 平台使用）
	MCPXMLInject bool

	// 支持的模型系列（仅 antigravity 平台使用）
	// 可选值: claude, gemini_text, gemini_image
	SupportedModelScopes []string

	// 分组排序
	SortOrder int

	// OpenAI Messages 调度配置（仅 openai 平台使用）
	AllowMessagesDispatch bool

	CreatedAt time.Time
	UpdatedAt time.Time

	AccountGroups           []AccountGroup
	AccountCount            int64
	ActiveAccountCount      int64
	RateLimitedAccountCount int64
}

// DecodeGroupModelRouting separates SAIAI's group-scoped compatibility flags
// from the existing model_routing JSONB storage. Keeping the marker private to
// the repository boundary avoids a database migration and prevents it from
// being interpreted as a user-visible model rule.
func DecodeGroupModelRouting(stored map[string][]int64) (map[string][]int64, string) {
	if stored == nil {
		return nil, ClaudeEnvironmentModeOff
	}
	routing := make(map[string][]int64, len(stored))
	mode := ClaudeEnvironmentModeOff
	for pattern, accountIDs := range stored {
		if pattern == claudeEnvironmentRewriteRoutingMarker {
			if len(accountIDs) == 1 {
				switch accountIDs[0] {
				case 1:
					mode = ClaudeEnvironmentModeRewrite
				case 2:
					mode = ClaudeEnvironmentModeRemove
				}
			}
			continue
		}
		routing[pattern] = accountIDs
	}
	if len(routing) == 0 {
		routing = nil
	}
	return routing, mode
}

// EncodeGroupModelRouting persists the environment handling mode in the
// group's existing extensible JSONB field while preserving all model rules.
func EncodeGroupModelRouting(routing map[string][]int64, environmentMode string) map[string][]int64 {
	environmentMode = NormalizeClaudeEnvironmentMode(environmentMode, false)
	if routing == nil && environmentMode == ClaudeEnvironmentModeOff {
		return nil
	}
	stored := make(map[string][]int64, len(routing)+1)
	for pattern, accountIDs := range routing {
		if pattern == claudeEnvironmentRewriteRoutingMarker {
			continue
		}
		stored[pattern] = accountIDs
	}
	switch environmentMode {
	case ClaudeEnvironmentModeRewrite:
		stored[claudeEnvironmentRewriteRoutingMarker] = []int64{1}
	case ClaudeEnvironmentModeRemove:
		stored[claudeEnvironmentRewriteRoutingMarker] = []int64{2}
	}
	if len(stored) == 0 {
		return nil
	}
	return stored
}

// NormalizeClaudeEnvironmentMode resolves the new tri-state mode while
// accepting the former boolean switch as a rewrite-mode fallback.
func NormalizeClaudeEnvironmentMode(mode string, legacyRewrite bool) string {
	switch mode {
	case ClaudeEnvironmentModeOff, ClaudeEnvironmentModeRewrite, ClaudeEnvironmentModeRemove:
		return mode
	case "":
		if legacyRewrite {
			return ClaudeEnvironmentModeRewrite
		}
	}
	return ClaudeEnvironmentModeOff
}

func (g *Group) EffectiveClaudeEnvironmentMode() string {
	if g == nil {
		return ClaudeEnvironmentModeOff
	}
	return NormalizeClaudeEnvironmentMode(g.ClaudeEnvironmentMode, g.ClaudeEnvironmentRewrite)
}

func (g *Group) IsActive() bool {
	return g.Status == StatusActive
}

func (g *Group) IsSubscriptionType() bool {
	return g.SubscriptionType == SubscriptionTypeSubscription
}

func (g *Group) IsFreeSubscription() bool {
	return g.IsSubscriptionType() && g.RateMultiplier == 0
}

func (g *Group) HasFiveHourLimit() bool {
	return g.FiveHourLimitUSD != nil && *g.FiveHourLimitUSD > 0
}

func (g *Group) HasDailyLimit() bool {
	return g.DailyLimitUSD != nil && *g.DailyLimitUSD > 0
}

func (g *Group) HasWeeklyLimit() bool {
	return g.WeeklyLimitUSD != nil && *g.WeeklyLimitUSD > 0
}

func (g *Group) HasMonthlyLimit() bool {
	return g.MonthlyLimitUSD != nil && *g.MonthlyLimitUSD > 0
}

// GetImagePrice 根据 image_size 返回对应的图片生成价格
// 如果分组未配置价格，返回 nil（调用方应使用默认值）
func (g *Group) GetImagePrice(imageSize string) *float64 {
	switch imageSize {
	case "1K":
		return g.ImagePrice1K
	case "2K":
		return g.ImagePrice2K
	case "4K":
		return g.ImagePrice4K
	default:
		// 未知尺寸默认按 2K 计费
		return g.ImagePrice2K
	}
}

// GetSoraImagePrice 根据 Sora 图片尺寸返回价格（360/540）
func (g *Group) GetSoraImagePrice(imageSize string) *float64 {
	switch imageSize {
	case "360":
		return g.SoraImagePrice360
	case "540":
		return g.SoraImagePrice540
	default:
		return g.SoraImagePrice360
	}
}

// IsGroupContextValid reports whether a group from context has the fields required for routing decisions.
func IsGroupContextValid(group *Group) bool {
	if group == nil {
		return false
	}
	if group.ID <= 0 {
		return false
	}
	if !group.Hydrated {
		return false
	}
	if group.Platform == "" || group.Status == "" {
		return false
	}
	return true
}

// GetRoutingAccountIDs 根据请求模型获取路由账号 ID 列表
// 返回匹配的优先账号 ID 列表，如果没有匹配规则则返回 nil
func (g *Group) GetRoutingAccountIDs(requestedModel string) []int64 {
	if !g.ModelRoutingEnabled || len(g.ModelRouting) == 0 || requestedModel == "" {
		return nil
	}

	// 1. 精确匹配优先
	if accountIDs, ok := g.ModelRouting[requestedModel]; ok && len(accountIDs) > 0 {
		return accountIDs
	}

	// 2. 通配符匹配（前缀匹配）
	for pattern, accountIDs := range g.ModelRouting {
		if matchModelPattern(pattern, requestedModel) && len(accountIDs) > 0 {
			return accountIDs
		}
	}

	return nil
}

// matchModelPattern 检查模型是否匹配模式
// 支持 * 通配符，如 "claude-opus-*" 匹配 "claude-opus-4-20250514"
func matchModelPattern(pattern, model string) bool {
	if pattern == model {
		return true
	}

	// 处理 * 通配符（仅支持末尾通配符）
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(model, prefix)
	}

	return false
}
