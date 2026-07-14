package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 预编译正则表达式（避免每次调用重新编译）
var (
	// 匹配 User-Agent 版本号: xxx/x.y.z
	userAgentVersionRegex = regexp.MustCompile(`/(\d+)\.(\d+)\.(\d+)`)
)

// 默认指纹值（当客户端未提供时使用）
var defaultFingerprint = Fingerprint{
	UserAgent:               "claude-cli/2.1.22 (external, cli)",
	StainlessLang:           "js",
	StainlessPackageVersion: "0.70.0",
	StainlessOS:             "Linux",
	StainlessArch:           "arm64",
	StainlessRuntime:        "node",
	StainlessRuntimeVersion: "v24.13.0",
	Accept:                  "application/json",
	ContentType:             "application/json",
	AcceptEncoding:          "gzip, deflate, br, zstd",
}

const claudeOAuthDeviceIDContext = "claude-oauth-device-id:v1"
const claudeOAuthCarpoolDeviceIDContext = "claude-oauth-device-id:v1-carpool"
const claudeOAuthSlotContext = "claude-oauth-slot:v2-device-id"
const claudeOAuthPinnedDeviceIDContext = "claude-oauth-device-id:v1-pinned"

const slotTransportAccountStride = int64(16)

var ErrClaudeOAuthCarpoolDevicesFull = errors.New("claude oauth carpool devices full")
var ErrClaudeOAuthPinnedDevicesFull = errors.New("claude oauth pinned devices full")

// Fingerprint represents account fingerprint data
type Fingerprint struct {
	ClientID                string
	UserAgent               string
	Accept                  string
	ContentType             string
	AcceptEncoding          string
	AcceptLanguage          string
	SecFetchMode            string
	StainlessLang           string
	StainlessPackageVersion string
	StainlessOS             string
	StainlessArch           string
	StainlessRuntime        string
	StainlessRuntimeVersion string
	StainlessRetryCount     string
	StainlessTimeout        string
	StainlessHelperMethod   string
	XApp                    string
	AnthropicBeta           string
	AnthropicVersion        string
	DangerousDirectAccess   string
	Slot                    int   `json:",omitempty"`
	LastRotatedAt           int64 `json:",omitempty"`
	NextRotationAt          int64 `json:",omitempty"`
	UpdatedAt               int64 `json:",omitempty"` // Unix timestamp，用于判断是否需要续期TTL
}

// ClientHints 从入站请求头抽取出来的客户端特征，用于在 device record 上做展示/审计。
// 源头是 Anthropic SDK 的 X-Stainless-* 头（跨 Windows/macOS/Linux 都会带）和 User-Agent。
// SDK 不带这些 header 的场景下对应字段为空。
type ClientHints struct {
	UserAgent      string `json:"user_agent,omitempty"`
	OS             string `json:"os,omitempty"`              // X-Stainless-Os: Windows / MacOS / Linux / FreeBSD / ...
	Arch           string `json:"arch,omitempty"`            // X-Stainless-Arch: x64 / arm64 / ...
	Runtime        string `json:"runtime,omitempty"`         // X-Stainless-Runtime: node / bun / deno
	RuntimeVersion string `json:"runtime_version,omitempty"` // X-Stainless-Runtime-Version: v24.3.0 / ...
	SDKVersion     string `json:"sdk_version,omitempty"`     // X-Stainless-Package-Version: 0.81.0 / ...
}

// ExtractClientHints 从 HTTP header 读取 Stainless 特征 + UA。空 header 返回零值。
func ExtractClientHints(headers http.Header) ClientHints {
	if headers == nil {
		return ClientHints{}
	}
	return ClientHints{
		UserAgent:      strings.TrimSpace(headers.Get("User-Agent")),
		OS:             strings.TrimSpace(headers.Get("X-Stainless-Os")),
		Arch:           strings.TrimSpace(headers.Get("X-Stainless-Arch")),
		Runtime:        strings.TrimSpace(headers.Get("X-Stainless-Runtime")),
		RuntimeVersion: strings.TrimSpace(headers.Get("X-Stainless-Runtime-Version")),
		SDKVersion:     strings.TrimSpace(headers.Get("X-Stainless-Package-Version")),
	}
}

type CarpoolDeviceRecord struct {
	DeviceKey        string `json:"device_key"`
	OriginalDeviceID string `json:"original_device_id"`
	CreatedAt        int64  `json:"created_at"`
	LastSeenAt       int64  `json:"last_seen_at"`
	LastUserAgent    string `json:"last_user_agent,omitempty"`
	// Last* client hints captured from the most recent request seen for this device.
	LastOS             string `json:"last_os,omitempty"`
	LastArch           string `json:"last_arch,omitempty"`
	LastRuntime        string `json:"last_runtime,omitempty"`
	LastRuntimeVersion string `json:"last_runtime_version,omitempty"`
	LastSDKVersion     string `json:"last_sdk_version,omitempty"`
}

type CarpoolOverflowRecord struct {
	DeviceKey        string `json:"device_key"`
	OriginalDeviceID string `json:"original_device_id"`
	FirstRejectedAt  int64  `json:"first_rejected_at"`
	LastRejectedAt   int64  `json:"last_rejected_at"`
	RejectCount      int64  `json:"reject_count"`
	LastUserAgent    string `json:"last_user_agent,omitempty"`
}

type CarpoolDeviceInfo struct {
	DeviceKey          string `json:"device_key"`
	OriginalDeviceID   string `json:"original_device_id"`
	PublicDeviceID     string `json:"public_device_id"`
	CreatedAt          int64  `json:"created_at"`
	LastSeenAt         int64  `json:"last_seen_at"`
	LastUserAgent      string `json:"last_user_agent,omitempty"`
	LastOS             string `json:"last_os,omitempty"`
	LastArch           string `json:"last_arch,omitempty"`
	LastRuntime        string `json:"last_runtime,omitempty"`
	LastRuntimeVersion string `json:"last_runtime_version,omitempty"`
	LastSDKVersion     string `json:"last_sdk_version,omitempty"`
}

type CarpoolOverflowDeviceInfo struct {
	DeviceKey        string `json:"device_key"`
	OriginalDeviceID string `json:"original_device_id"`
	FirstRejectedAt  int64  `json:"first_rejected_at"`
	LastRejectedAt   int64  `json:"last_rejected_at"`
	RejectCount      int64  `json:"reject_count"`
	LastUserAgent    string `json:"last_user_agent,omitempty"`
}

type CarpoolDeviceOverview struct {
	RecordedLimit int                          `json:"recorded_limit"`
	RecordedCount int                          `json:"recorded_count"`
	OverflowCount int                          `json:"overflow_count"`
	RecordedItems []*CarpoolDeviceInfo         `json:"recorded_items"`
	OverflowItems []*CarpoolOverflowDeviceInfo `json:"overflow_items"`
}

type SharedBucketState struct {
	Bucket        int    `json:"bucket"`
	LastSeenAt    int64  `json:"last_seen_at"`
	LastUserAgent string `json:"last_user_agent,omitempty"`
}

type SharedBucketInfo struct {
	Bucket                  int    `json:"bucket"`
	PublicDeviceID          string `json:"public_device_id"`
	LastSeenAt              int64  `json:"last_seen_at,omitempty"`
	LastUserAgent           string `json:"last_user_agent,omitempty"`
	UserAgent               string `json:"user_agent,omitempty"`
	StainlessLang           string `json:"stainless_lang,omitempty"`
	StainlessPackageVersion string `json:"stainless_package_version,omitempty"`
	StainlessOS             string `json:"stainless_os,omitempty"`
	StainlessArch           string `json:"stainless_arch,omitempty"`
	StainlessRuntime        string `json:"stainless_runtime,omitempty"`
	StainlessRuntimeVersion string `json:"stainless_runtime_version,omitempty"`
	XApp                    string `json:"x_app,omitempty"`
	AnthropicBeta           string `json:"anthropic_beta,omitempty"`
	AnthropicVersion        string `json:"anthropic_version,omitempty"`
	DangerousDirectAccess   string `json:"dangerous_direct_access,omitempty"`
}

type PinnedDeviceBinding struct {
	AccountID        int64  `json:"account_id"`
	AccountName      string `json:"account_name,omitempty"`
	AccountUUID      string `json:"account_uuid,omitempty"`
	SlotKey          string `json:"slot_key"`
	DeviceKey        string `json:"device_key"`
	OriginalDeviceID string `json:"original_device_id"`
	FirstBoundAt     int64  `json:"first_bound_at"`
	LastUsedAt       int64  `json:"last_used_at"`
}

type PinnedAccountBinding struct {
	GroupID          int64  `json:"group_id"`
	AccountID        int64  `json:"account_id"`
	AccountName      string `json:"account_name,omitempty"`
	AccountUUID      string `json:"account_uuid,omitempty"`
	SlotKey          string `json:"slot_key"`
	DeviceKey        string `json:"device_key"`
	OriginalDeviceID string `json:"original_device_id"`
	FirstBoundAt     int64  `json:"first_bound_at"`
	LastUsedAt       int64  `json:"last_used_at"`
}

// IdentityCache defines cache operations for identity service
type IdentityCache interface {
	GetFingerprint(ctx context.Context, accountID int64) (*Fingerprint, error)
	SetFingerprint(ctx context.Context, accountID int64, fp *Fingerprint) error
	DeleteFingerprint(ctx context.Context, accountID int64) error
	GetSlotFingerprint(ctx context.Context, accountID int64, slot int) (*Fingerprint, error)
	SetSlotFingerprint(ctx context.Context, accountID int64, slot int, fp *Fingerprint) error
	GetOrCreateCarpoolDevice(ctx context.Context, accountID int64, originalDeviceID string, hints ClientHints, limit int, nowUnix int64) (*CarpoolDeviceRecord, error)
	ListCarpoolDevices(ctx context.Context, accountID int64) ([]*CarpoolDeviceRecord, error)
	ListCarpoolOverflowDevices(ctx context.Context, accountID int64) ([]*CarpoolOverflowRecord, error)
	DeleteCarpoolDevice(ctx context.Context, accountID int64, deviceKey string) error
	EnsureSharedBucketTopology(ctx context.Context, accountID int64, bucketCount int) error
	GetOrAssignSharedBucket(ctx context.Context, accountID int64, originalDeviceID string, bucketCount, preferredBucket int) (int, error)
	GetSharedBucketState(ctx context.Context, accountID int64, bucket int) (*SharedBucketState, error)
	SetSharedBucketState(ctx context.Context, accountID int64, bucket int, state *SharedBucketState) error
	ListSharedBucketStates(ctx context.Context, accountID int64, bucketCount int) ([]*SharedBucketState, error)
	DeleteSharedBucketState(ctx context.Context, accountID int64, bucket int) error
	GetOrCreateSingleDeviceSlot(ctx context.Context, accountID int64, slotKey string) (int, error)
	GetSingleDeviceSlotState(ctx context.Context, accountID int64, slot int) (*SingleDeviceSlotState, error)
	SetSingleDeviceSlotState(ctx context.Context, accountID int64, slot int, state *SingleDeviceSlotState) error
	ListSingleDeviceSlotStates(ctx context.Context, accountID int64) ([]*SingleDeviceSlotState, error)
	GetPinnedDeviceBindings(ctx context.Context, groupID int64, originalDeviceID string) ([]*PinnedDeviceBinding, error)
	GetPinnedAccountBindings(ctx context.Context, groupID int64, accountIDs []int64) (map[int64]*PinnedAccountBinding, error)
	BindPinnedDeviceAccount(ctx context.Context, groupID int64, originalDeviceID string, deviceBinding *PinnedDeviceBinding, accountBinding *PinnedAccountBinding) error
	DeletePinnedDeviceAccountBinding(ctx context.Context, groupID int64, originalDeviceID string, accountID int64) error
	// GetMaskedSessionID 获取固定的会话ID（用于会话ID伪装功能）
	// 返回的 sessionID 是一个 UUID 格式的字符串
	// 如果不存在或已过期（15分钟无请求），返回空字符串
	GetMaskedSessionID(ctx context.Context, accountID int64) (string, error)
	// SetMaskedSessionID 设置固定的会话ID，TTL 为 15 分钟
	// 每次调用都会刷新 TTL
	SetMaskedSessionID(ctx context.Context, accountID int64, sessionID string) error
	GetSlotMaskedSessionID(ctx context.Context, accountID int64, slot int) (string, error)
	SetSlotMaskedSessionID(ctx context.Context, accountID int64, slot int, sessionID string) error
}

// IdentityService 管理OAuth账号的请求身份指纹
type IdentityService struct {
	cache          IdentityCache
	instanceSecret string
}

// NewIdentityService 创建新的IdentityService
func NewIdentityService(cache IdentityCache, instanceSecret string) *IdentityService {
	return &IdentityService{
		cache:          cache,
		instanceSecret: strings.TrimSpace(instanceSecret),
	}
}

// GetOrCreateFingerprint 获取或创建账号的指纹
// 如果缓存存在，检测user-agent版本，新版本则更新
// 如果缓存不存在，生成随机ClientID并从请求头创建指纹，然后缓存
func (s *IdentityService) GetOrCreateFingerprint(ctx context.Context, accountID int64, headers http.Header) (*Fingerprint, error) {
	// 尝试从缓存获取指纹
	cached, err := s.cache.GetFingerprint(ctx, accountID)
	if err == nil && cached != nil {
		needWrite := false

		// 检查客户端的user-agent是否是更新版本
		clientUA := headers.Get("User-Agent")
		if clientUA != "" && isNewerVersion(clientUA, cached.UserAgent) {
			// 版本升级：merge 语义 — 仅更新请求中实际携带的字段，保留缓存值
			// 避免缺失的头被硬编码默认值覆盖（如新 CLI 版本 + 旧 SDK 默认值的不一致）
			mergeHeadersIntoFingerprint(cached, headers)
			needWrite = true
			logger.LegacyPrintf("service.identity", "Updated fingerprint for account %d: %s (merge update)", accountID, clientUA)
		} else if time.Since(time.Unix(cached.UpdatedAt, 0)) > 24*time.Hour {
			// 距上次写入超过24小时，续期TTL
			needWrite = true
		}

		if needWrite {
			cached.UpdatedAt = time.Now().Unix()
			if err := s.cache.SetFingerprint(ctx, accountID, cached); err != nil {
				logger.LegacyPrintf("service.identity", "Warning: failed to refresh fingerprint for account %d: %v", accountID, err)
			}
		}
		return cached, nil
	}

	// 缓存不存在或解析失败，创建新指纹
	fp := s.createFingerprintFromHeaders(headers)

	// 生成随机ClientID
	fp.ClientID = generateClientID()
	fp.UpdatedAt = time.Now().Unix()

	// 保存到缓存（7天TTL，每24小时自动续期）
	if err := s.cache.SetFingerprint(ctx, accountID, fp); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to cache fingerprint for account %d: %v", accountID, err)
	}

	logger.LegacyPrintf("service.identity", "Created new fingerprint for account %d with client_id: %s", accountID, fp.ClientID)
	return fp, nil
}

// createFingerprintFromHeaders 从请求头创建指纹
func (s *IdentityService) createFingerprintFromHeaders(headers http.Header) *Fingerprint {
	fp := &Fingerprint{}

	// 获取User-Agent
	if ua := headers.Get("User-Agent"); ua != "" {
		fp.UserAgent = ua
	} else {
		fp.UserAgent = defaultFingerprint.UserAgent
	}

	fp.Accept = getHeaderOrDefault(headers, "Accept", defaultFingerprint.Accept)
	fp.ContentType = getHeaderOrDefault(headers, "Content-Type", defaultFingerprint.ContentType)
	fp.AcceptEncoding = getHeaderOrDefault(headers, "Accept-Encoding", defaultFingerprint.AcceptEncoding)
	fp.AcceptLanguage = strings.TrimSpace(headers.Get("Accept-Language"))
	fp.SecFetchMode = strings.TrimSpace(headers.Get("Sec-Fetch-Mode"))

	// 获取x-stainless-*头，如果没有则使用默认值
	fp.StainlessRetryCount = strings.TrimSpace(headers.Get("X-Stainless-Retry-Count"))
	fp.StainlessTimeout = strings.TrimSpace(headers.Get("X-Stainless-Timeout"))
	fp.StainlessLang = getHeaderOrDefault(headers, "X-Stainless-Lang", defaultFingerprint.StainlessLang)
	fp.StainlessPackageVersion = getHeaderOrDefault(headers, "X-Stainless-Package-Version", defaultFingerprint.StainlessPackageVersion)
	fp.StainlessOS = getHeaderOrDefault(headers, "X-Stainless-OS", defaultFingerprint.StainlessOS)
	fp.StainlessArch = getHeaderOrDefault(headers, "X-Stainless-Arch", defaultFingerprint.StainlessArch)
	fp.StainlessRuntime = getHeaderOrDefault(headers, "X-Stainless-Runtime", defaultFingerprint.StainlessRuntime)
	fp.StainlessRuntimeVersion = getHeaderOrDefault(headers, "X-Stainless-Runtime-Version", defaultFingerprint.StainlessRuntimeVersion)
	fp.StainlessHelperMethod = strings.TrimSpace(headers.Get("X-Stainless-Helper-Method"))
	fp.XApp = getHeaderOrDefault(headers, "X-App", "cli")
	fp.AnthropicVersion = getHeaderOrDefault(headers, "anthropic-version", "2023-06-01")
	fp.DangerousDirectAccess = getHeaderOrDefault(headers, "anthropic-dangerous-direct-browser-access", "true")
	fp.AnthropicBeta = strings.TrimSpace(headers.Get("anthropic-beta"))

	return fp
}

// mergeHeadersIntoFingerprint 将请求头中实际存在的字段合并到现有指纹中（用于版本升级场景）
// 关键语义：请求中有的字段 → 用新值覆盖；缺失的头 → 保留缓存中的已有值
// 与 createFingerprintFromHeaders 的区别：后者用于首次创建，缺失头回退到 defaultFingerprint；
// 本函数用于升级更新，缺失头保留缓存值，避免将已知的真实值退化为硬编码默认值
func mergeHeadersIntoFingerprint(fp *Fingerprint, headers http.Header) {
	// User-Agent：版本升级的触发条件，一定存在
	if ua := headers.Get("User-Agent"); ua != "" {
		fp.UserAgent = ua
	}
	mergeHeader(headers, "Accept", &fp.Accept)
	mergeHeader(headers, "Content-Type", &fp.ContentType)
	mergeHeader(headers, "Accept-Encoding", &fp.AcceptEncoding)
	mergeHeader(headers, "Accept-Language", &fp.AcceptLanguage)
	mergeHeader(headers, "Sec-Fetch-Mode", &fp.SecFetchMode)
	mergeHeader(headers, "X-Stainless-Retry-Count", &fp.StainlessRetryCount)
	mergeHeader(headers, "X-Stainless-Timeout", &fp.StainlessTimeout)
	// X-Stainless-* 头：仅在请求中实际携带时才更新，否则保留缓存值
	mergeHeader(headers, "X-Stainless-Lang", &fp.StainlessLang)
	mergeHeader(headers, "X-Stainless-Package-Version", &fp.StainlessPackageVersion)
	mergeHeader(headers, "X-Stainless-OS", &fp.StainlessOS)
	mergeHeader(headers, "X-Stainless-Arch", &fp.StainlessArch)
	mergeHeader(headers, "X-Stainless-Runtime", &fp.StainlessRuntime)
	mergeHeader(headers, "X-Stainless-Runtime-Version", &fp.StainlessRuntimeVersion)
	mergeHeader(headers, "X-Stainless-Helper-Method", &fp.StainlessHelperMethod)
	mergeHeader(headers, "X-App", &fp.XApp)
	mergeHeader(headers, "anthropic-version", &fp.AnthropicVersion)
	mergeHeader(headers, "anthropic-dangerous-direct-browser-access", &fp.DangerousDirectAccess)
	mergeHeader(headers, "anthropic-beta", &fp.AnthropicBeta)
}

// mergeHeader 如果请求头中存在该字段则更新目标值，否则保留原值
func mergeHeader(headers http.Header, key string, target *string) {
	if v := headers.Get(key); v != "" {
		*target = v
	}
}

// getHeaderOrDefault 获取header值，如果不存在则返回默认值
func getHeaderOrDefault(headers http.Header, key, defaultValue string) string {
	if v := headers.Get(key); v != "" {
		return v
	}
	return defaultValue
}

// ApplyFingerprint 将指纹应用到请求头（覆盖原有的x-stainless-*头）
func (s *IdentityService) ApplyFingerprint(req *http.Request, fp *Fingerprint) {
	if fp == nil {
		return
	}

	// 设置user-agent
	if fp.UserAgent != "" {
		req.Header.Set("user-agent", fp.UserAgent)
	}
	if fp.Accept != "" {
		req.Header.Set("accept", fp.Accept)
	}
	if fp.ContentType != "" {
		req.Header.Set("content-type", fp.ContentType)
	}
	if fp.AcceptEncoding != "" {
		req.Header.Set("accept-encoding", fp.AcceptEncoding)
	}
	if fp.AcceptLanguage != "" {
		req.Header.Set("accept-language", fp.AcceptLanguage)
	}
	if fp.SecFetchMode != "" {
		req.Header.Set("sec-fetch-mode", fp.SecFetchMode)
	}

	// 设置x-stainless-*头
	if fp.StainlessRetryCount != "" {
		req.Header.Set("X-Stainless-Retry-Count", fp.StainlessRetryCount)
	}
	if fp.StainlessTimeout != "" {
		req.Header.Set("X-Stainless-Timeout", fp.StainlessTimeout)
	}
	if fp.StainlessLang != "" {
		req.Header.Set("X-Stainless-Lang", fp.StainlessLang)
	}
	if fp.StainlessPackageVersion != "" {
		req.Header.Set("X-Stainless-Package-Version", fp.StainlessPackageVersion)
	}
	if fp.StainlessOS != "" {
		req.Header.Set("X-Stainless-OS", fp.StainlessOS)
	}
	if fp.StainlessArch != "" {
		req.Header.Set("X-Stainless-Arch", fp.StainlessArch)
	}
	if fp.StainlessRuntime != "" {
		req.Header.Set("X-Stainless-Runtime", fp.StainlessRuntime)
	}
	if fp.StainlessRuntimeVersion != "" {
		req.Header.Set("X-Stainless-Runtime-Version", fp.StainlessRuntimeVersion)
	}
	if fp.StainlessHelperMethod != "" {
		req.Header.Set("X-Stainless-Helper-Method", fp.StainlessHelperMethod)
	}
	if fp.XApp != "" {
		req.Header.Set("x-app", fp.XApp)
	}
	if fp.AnthropicBeta != "" {
		req.Header.Set("anthropic-beta", fp.AnthropicBeta)
	}
	if fp.AnthropicVersion != "" {
		req.Header.Set("anthropic-version", fp.AnthropicVersion)
	}
	if fp.DangerousDirectAccess != "" {
		req.Header.Set("anthropic-dangerous-direct-browser-access", fp.DangerousDirectAccess)
	}
}

func (s *IdentityService) AssignOAuthSlot(accountID int64, originalMetadataUserID string) int {
	slotSource := oauthSlotSource(originalMetadataUserID)
	if slotSource == "" {
		return 0
	}

	mac := hmac.New(sha256.New, []byte(s.normalizedSecret()))
	_, _ = mac.Write([]byte(claudeOAuthSlotContext))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strconv.FormatInt(accountID, 10)))
	_, _ = mac.Write([]byte{':'})
	_, _ = mac.Write([]byte(slotSource))
	sum := mac.Sum(nil)
	raw := binary.BigEndian.Uint64(sum[:8])
	return int(raw & uint64(^uint(0)>>1))
}

func (s *IdentityService) assignOAuthBucket(accountID int64, originalMetadataUserID string, bucketCount int) int {
	if bucketCount <= 0 {
		return 0
	}
	return s.AssignOAuthSlot(accountID, originalMetadataUserID) % bucketCount
}

func (s *IdentityService) ResolveSharedBucket(ctx context.Context, account *Account, originalMetadataUserID string) (int, error) {
	if account == nil {
		return 0, nil
	}
	originalDeviceID := oauthSlotSource(originalMetadataUserID)
	if originalDeviceID == "" {
		return 0, nil
	}
	bucketCount := account.GetClaudeOAuthSharedBucketCount()
	if bucketCount <= 0 {
		return 0, nil
	}
	preferredBucket := s.assignOAuthBucket(account.ID, originalMetadataUserID, bucketCount)
	return s.cache.GetOrAssignSharedBucket(ctx, account.ID, originalDeviceID, bucketCount, preferredBucket)
}

func oauthSlotSource(originalMetadataUserID string) string {
	trimmed := strings.TrimSpace(originalMetadataUserID)
	if trimmed == "" {
		return ""
	}
	if parsed := ParseMetadataUserID(trimmed); parsed != nil && strings.TrimSpace(parsed.DeviceID) != "" {
		return strings.TrimSpace(parsed.DeviceID)
	}
	return trimmed
}

func carpoolDeviceKey(originalDeviceID string) string {
	trimmed := strings.TrimSpace(originalDeviceID)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

func (s *IdentityService) TransportAccountID(accountID int64, slot int) int64 {
	if accountID <= 0 {
		return accountID
	}
	if slot < 0 {
		slot = 0
	}
	return accountID*slotTransportAccountStride + int64(slot+1)
}

func (s *IdentityService) computeOAuthCarpoolDeviceID(accountID int64, originalDeviceID string) string {
	if accountID <= 0 {
		return ""
	}
	trimmed := strings.TrimSpace(originalDeviceID)
	if trimmed == "" {
		return ""
	}

	mac := hmac.New(sha256.New, []byte(s.normalizedSecret()))
	_, _ = mac.Write([]byte(claudeOAuthCarpoolDeviceIDContext))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strconv.FormatInt(accountID, 10)))
	_, _ = mac.Write([]byte{':'})
	_, _ = mac.Write([]byte(trimmed))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *IdentityService) computeOAuthSlotDeviceID(accountID int64, slot int) string {
	if accountID <= 0 {
		return ""
	}

	mac := hmac.New(sha256.New, []byte(s.normalizedSecret()))
	_, _ = mac.Write([]byte(claudeOAuthDeviceIDContext))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strconv.FormatInt(accountID, 10)))
	_, _ = mac.Write([]byte{':'})
	_, _ = mac.Write([]byte(strconv.Itoa(slot)))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *IdentityService) SharedSlotPublicDeviceID(accountID int64, slot int) string {
	return s.computeOAuthSlotDeviceID(accountID, slot)
}

func (s *IdentityService) LoadPinnedBindings(ctx context.Context, groupID int64, originalMetadataUserID string, accounts []*Account) (map[int64]*PinnedDeviceBinding, map[int64]*PinnedAccountBinding, error) {
	if s == nil || s.cache == nil || groupID <= 0 {
		return map[int64]*PinnedDeviceBinding{}, map[int64]*PinnedAccountBinding{}, nil
	}
	originalDeviceID := oauthSlotSource(originalMetadataUserID)
	if originalDeviceID == "" {
		return map[int64]*PinnedDeviceBinding{}, map[int64]*PinnedAccountBinding{}, nil
	}
	deviceBindingsRaw, err := s.cache.GetPinnedDeviceBindings(ctx, groupID, originalDeviceID)
	if err != nil {
		return nil, nil, err
	}
	deviceBindings := make(map[int64]*PinnedDeviceBinding, len(deviceBindingsRaw))
	for _, item := range deviceBindingsRaw {
		if item == nil {
			continue
		}
		cp := *item
		deviceBindings[item.AccountID] = &cp
	}
	accountIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account == nil || account.ID <= 0 {
			continue
		}
		accountIDs = append(accountIDs, account.ID)
	}
	accountBindings, err := s.cache.GetPinnedAccountBindings(ctx, groupID, accountIDs)
	if err != nil {
		return nil, nil, err
	}
	return deviceBindings, accountBindings, nil
}

func (s *IdentityService) EnsurePinnedDeviceBinding(ctx context.Context, groupID int64, originalMetadataUserID string, account *Account) (*PinnedDeviceBinding, error) {
	if s == nil || s.cache == nil || account == nil || groupID <= 0 {
		return nil, nil
	}
	originalDeviceID := oauthSlotSource(originalMetadataUserID)
	if originalDeviceID == "" {
		return nil, nil
	}
	nowUnix := time.Now().Unix()
	deviceBindings, accountBindings, err := s.LoadPinnedBindings(ctx, groupID, originalMetadataUserID, []*Account{account})
	if err != nil {
		return nil, err
	}
	if existing := deviceBindings[account.ID]; existing != nil {
		existing.LastUsedAt = nowUnix
		accountBinding := &PinnedAccountBinding{
			GroupID:          groupID,
			AccountID:        account.ID,
			AccountName:      account.Name,
			AccountUUID:      oauthAccountUUID(account, existing.AccountUUID),
			SlotKey:          existing.SlotKey,
			DeviceKey:        existing.DeviceKey,
			OriginalDeviceID: existing.OriginalDeviceID,
			FirstBoundAt:     existing.FirstBoundAt,
			LastUsedAt:       nowUnix,
		}
		if err := s.cache.BindPinnedDeviceAccount(ctx, groupID, originalDeviceID, existing, accountBinding); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if occupied := accountBindings[account.ID]; occupied != nil && strings.TrimSpace(occupied.DeviceKey) != "" && occupied.DeviceKey != carpoolDeviceKey(originalDeviceID) {
		return nil, ErrClaudeOAuthPinnedDevicesFull
	}
	binding := &PinnedDeviceBinding{
		AccountID:        account.ID,
		AccountName:      account.Name,
		AccountUUID:      oauthAccountUUID(account, ""),
		SlotKey:          pinnedSlotKey(account.ID),
		DeviceKey:        carpoolDeviceKey(originalDeviceID),
		OriginalDeviceID: originalDeviceID,
		FirstBoundAt:     nowUnix,
		LastUsedAt:       nowUnix,
	}
	accountBinding := &PinnedAccountBinding{
		GroupID:          groupID,
		AccountID:        account.ID,
		AccountName:      account.Name,
		AccountUUID:      oauthAccountUUID(account, ""),
		SlotKey:          binding.SlotKey,
		DeviceKey:        binding.DeviceKey,
		OriginalDeviceID: originalDeviceID,
		FirstBoundAt:     nowUnix,
		LastUsedAt:       nowUnix,
	}
	if err := s.cache.BindPinnedDeviceAccount(ctx, groupID, originalDeviceID, binding, accountBinding); err != nil {
		return nil, err
	}
	return binding, nil
}

func (s *IdentityService) GetPinnedAccountBinding(ctx context.Context, groupID int64, account *Account) (*PinnedAccountBinding, error) {
	if s == nil || s.cache == nil || account == nil || groupID <= 0 {
		return nil, nil
	}
	bindings, err := s.cache.GetPinnedAccountBindings(ctx, groupID, []int64{account.ID})
	if err != nil {
		return nil, err
	}
	return bindings[account.ID], nil
}

func (s *IdentityService) DeletePinnedAccountBinding(ctx context.Context, groupID int64, account *Account) error {
	if s == nil || s.cache == nil || account == nil || groupID <= 0 {
		return nil
	}
	binding, err := s.GetPinnedAccountBinding(ctx, groupID, account)
	if err != nil {
		return err
	}
	if binding == nil || strings.TrimSpace(binding.OriginalDeviceID) == "" {
		return nil
	}
	return s.cache.DeletePinnedDeviceAccountBinding(ctx, groupID, binding.OriginalDeviceID, account.ID)
}

func pinnedSlotKey(accountID int64) string {
	return fmt.Sprintf("account:%d", accountID)
}

func (s *IdentityService) computeOAuthPinnedDeviceID(accountID int64, slotKey, originalDeviceID string) string {
	if accountID <= 0 {
		return ""
	}
	trimmedSlotKey := strings.TrimSpace(slotKey)
	trimmedDeviceID := strings.TrimSpace(originalDeviceID)
	if trimmedSlotKey == "" || trimmedDeviceID == "" {
		return ""
	}

	mac := hmac.New(sha256.New, []byte(s.normalizedSecret()))
	_, _ = mac.Write([]byte(claudeOAuthPinnedDeviceIDContext))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strconv.FormatInt(accountID, 10)))
	_, _ = mac.Write([]byte{':'})
	_, _ = mac.Write([]byte(trimmedSlotKey))
	_, _ = mac.Write([]byte{':'})
	_, _ = mac.Write([]byte(trimmedDeviceID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *IdentityService) EnsureCarpoolDeviceAllowed(ctx context.Context, account *Account, originalMetadataUserID string, headers http.Header) (*CarpoolDeviceRecord, error) {
	if account == nil {
		return nil, nil
	}
	originalDeviceID := oauthSlotSource(originalMetadataUserID)
	if originalDeviceID == "" {
		return nil, nil
	}
	hints := ExtractClientHints(headers)
	return s.cache.GetOrCreateCarpoolDevice(
		ctx,
		account.ID,
		originalDeviceID,
		hints,
		account.GetClaudeOAuthCarpoolDeviceLimit(),
		time.Now().Unix(),
	)
}

func (s *IdentityService) GetOrCreateSharedSlotFingerprint(ctx context.Context, account *Account, originalMetadataUserID string, headers http.Header) (int, *Fingerprint, error) {
	if account == nil {
		return 0, nil, nil
	}
	slot, err := s.ResolveSharedBucket(ctx, account, originalMetadataUserID)
	if err != nil {
		return 0, nil, err
	}
	now := time.Now()
	state := &SharedBucketState{
		Bucket:        slot,
		LastSeenAt:    now.Unix(),
		LastUserAgent: strings.TrimSpace(headers.Get("User-Agent")),
	}
	if err := s.cache.SetSharedBucketState(ctx, account.ID, slot, state); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to persist shared bucket state for account %d bucket %d: %v", account.ID, slot, err)
	}
	cached, err := s.cache.GetSlotFingerprint(ctx, account.ID, slot)
	if err == nil && cached != nil {
		if time.Since(time.Unix(cached.UpdatedAt, 0)) > 24*time.Hour {
			cached.UpdatedAt = now.Unix()
			if err := s.persistSlotFingerprint(ctx, account.ID, slot, cached); err != nil {
				logger.LegacyPrintf("service.identity", "Warning: failed to persist slot fingerprint for account %d slot %d: %v", account.ID, slot, err)
			}
		}
		return slot, cached, nil
	}

	fp := s.createSlotFingerprintFromHeaders(slot, headers, now)
	fp.ClientID = generateClientID()
	if err := s.persistSlotFingerprint(ctx, account.ID, slot, fp); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to cache slot fingerprint for account %d slot %d: %v", account.ID, slot, err)
	}
	logger.LegacyPrintf("service.identity", "Created slot fingerprint for account %d slot %d with user-agent %s", account.ID, slot, fp.UserAgent)
	return slot, fp, nil
}

func (s *IdentityService) GetOrCreateSingleDeviceSlotFingerprint(ctx context.Context, account *Account, headers http.Header, freezeUserAgent bool) (int, string, *Fingerprint, error) {
	if account == nil {
		return 0, "", nil, nil
	}
	slotKey := NormalizeClaudeOAuthSingleDeviceSlotKey(headers.Get("User-Agent"))
	if slotKey == "" {
		slotKey = strings.ToLower(strings.TrimSpace(headers.Get("User-Agent")))
	}
	if slotKey == "" {
		return 0, "", nil, nil
	}
	slot, err := s.cache.GetOrCreateSingleDeviceSlot(ctx, account.ID, slotKey)
	if err != nil {
		return 0, "", nil, err
	}
	now := time.Now()
	state := &SingleDeviceSlotState{
		Slot:          slot,
		SlotKey:       slotKey,
		LastSeenAt:    now.Unix(),
		LastUserAgent: strings.TrimSpace(headers.Get("User-Agent")),
	}
	if err := s.cache.SetSingleDeviceSlotState(ctx, account.ID, slot, state); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to persist single_device slot state for account %d slot %d: %v", account.ID, slot, err)
	}
	cached, err := s.cache.GetSlotFingerprint(ctx, account.ID, slot)
	if err == nil && cached != nil {
		needWrite := false
		clientUA := headers.Get("User-Agent")
		if !freezeUserAgent && clientUA != "" && isNewerVersion(clientUA, cached.UserAgent) {
			mergeHeadersIntoFingerprint(cached, headers)
			needWrite = true
		} else if time.Since(time.Unix(cached.UpdatedAt, 0)) > 24*time.Hour {
			needWrite = true
		}
		if needWrite {
			cached.UpdatedAt = now.Unix()
			if err := s.persistSlotFingerprint(ctx, account.ID, slot, cached); err != nil {
				logger.LegacyPrintf("service.identity", "Warning: failed to persist single_device slot fingerprint for account %d slot %d: %v", account.ID, slot, err)
			}
		}
		return slot, slotKey, cached, nil
	}

	fp := s.createSlotFingerprintFromHeaders(slot, headers, now)
	fp.ClientID = generateClientID()
	if err := s.persistSlotFingerprint(ctx, account.ID, slot, fp); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to cache single_device slot fingerprint for account %d slot %d: %v", account.ID, slot, err)
	}
	logger.LegacyPrintf("service.identity", "Created single_device slot fingerprint for account %d slot %d key %s with user-agent %s", account.ID, slot, slotKey, fp.UserAgent)
	return slot, slotKey, fp, nil
}

func (s *IdentityService) ListSingleDeviceSlots(ctx context.Context, account *Account) ([]*SingleDeviceSlotInfo, error) {
	if account == nil {
		return nil, nil
	}
	states, err := s.cache.ListSingleDeviceSlotStates(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].Slot < states[j].Slot
	})
	items := make([]*SingleDeviceSlotInfo, 0, len(states))
	for _, state := range states {
		if state == nil {
			continue
		}
		item := &SingleDeviceSlotInfo{
			Slot:          state.Slot,
			SlotKey:       state.SlotKey,
			LastSeenAt:    state.LastSeenAt,
			LastUserAgent: state.LastUserAgent,
		}
		if fp, fpErr := s.cache.GetSlotFingerprint(ctx, account.ID, state.Slot); fpErr == nil && fp != nil {
			item.UserAgent = fp.UserAgent
			item.Accept = fp.Accept
			item.ContentType = fp.ContentType
			item.AcceptEncoding = fp.AcceptEncoding
			item.StainlessLang = fp.StainlessLang
			item.StainlessPackageVersion = fp.StainlessPackageVersion
			item.StainlessOS = fp.StainlessOS
			item.StainlessArch = fp.StainlessArch
			item.StainlessRuntime = fp.StainlessRuntime
			item.StainlessRuntimeVersion = fp.StainlessRuntimeVersion
			item.XApp = fp.XApp
			item.AnthropicVersion = fp.AnthropicVersion
		}
		items = append(items, item)
	}
	return items, nil
}

// GetOrCreateSlotFingerprint is kept as a compatibility wrapper for tests and legacy callers.
func (s *IdentityService) GetOrCreateSlotFingerprint(ctx context.Context, accountID int64, originalMetadataUserID string, headers http.Header) (int, *Fingerprint, error) {
	account := &Account{
		ID:       accountID,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode":                ClaudeOAuthModeShared,
			"claude_oauth_shared_bucket_count": DefaultClaudeOAuthSharedBucketCount,
		},
	}
	return s.GetOrCreateSharedSlotFingerprint(ctx, account, originalMetadataUserID, headers)
}

func (s *IdentityService) ListCarpoolDevices(ctx context.Context, account *Account) (*CarpoolDeviceOverview, error) {
	if account == nil {
		return &CarpoolDeviceOverview{}, nil
	}
	recorded, err := s.cache.ListCarpoolDevices(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	overflow, err := s.cache.ListCarpoolOverflowDevices(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	sort.Slice(recorded, func(i, j int) bool {
		return recorded[i].CreatedAt < recorded[j].CreatedAt
	})
	sort.Slice(overflow, func(i, j int) bool {
		if overflow[i].LastRejectedAt == overflow[j].LastRejectedAt {
			return overflow[i].DeviceKey < overflow[j].DeviceKey
		}
		return overflow[i].LastRejectedAt > overflow[j].LastRejectedAt
	})
	result := &CarpoolDeviceOverview{
		RecordedLimit: account.GetClaudeOAuthCarpoolDeviceLimit(),
		RecordedCount: len(recorded),
		OverflowCount: len(overflow),
		RecordedItems: make([]*CarpoolDeviceInfo, 0, len(recorded)),
		OverflowItems: make([]*CarpoolOverflowDeviceInfo, 0, len(overflow)),
	}
	for _, item := range recorded {
		if item == nil {
			continue
		}
		result.RecordedItems = append(result.RecordedItems, &CarpoolDeviceInfo{
			DeviceKey:          item.DeviceKey,
			OriginalDeviceID:   item.OriginalDeviceID,
			PublicDeviceID:     s.computeOAuthCarpoolDeviceID(account.ID, item.OriginalDeviceID),
			CreatedAt:          item.CreatedAt,
			LastSeenAt:         item.LastSeenAt,
			LastUserAgent:      item.LastUserAgent,
			LastOS:             item.LastOS,
			LastArch:           item.LastArch,
			LastRuntime:        item.LastRuntime,
			LastRuntimeVersion: item.LastRuntimeVersion,
			LastSDKVersion:     item.LastSDKVersion,
		})
	}
	for _, item := range overflow {
		if item == nil {
			continue
		}
		result.OverflowItems = append(result.OverflowItems, &CarpoolOverflowDeviceInfo{
			DeviceKey:        item.DeviceKey,
			OriginalDeviceID: item.OriginalDeviceID,
			FirstRejectedAt:  item.FirstRejectedAt,
			LastRejectedAt:   item.LastRejectedAt,
			RejectCount:      item.RejectCount,
			LastUserAgent:    item.LastUserAgent,
		})
	}
	return result, nil
}

func (s *IdentityService) DeleteCarpoolDevice(ctx context.Context, account *Account, deviceKey string) error {
	if account == nil {
		return nil
	}
	return s.cache.DeleteCarpoolDevice(ctx, account.ID, deviceKey)
}

func (s *IdentityService) ListSharedBuckets(ctx context.Context, account *Account) ([]*SharedBucketInfo, error) {
	if account == nil {
		return nil, nil
	}
	bucketCount := account.GetClaudeOAuthSharedBucketCount()
	states, err := s.cache.ListSharedBucketStates(ctx, account.ID, bucketCount)
	if err != nil {
		return nil, err
	}
	stateByBucket := make(map[int]*SharedBucketState, len(states))
	for _, state := range states {
		if state != nil {
			stateByBucket[state.Bucket] = state
		}
	}
	buckets := make([]*SharedBucketInfo, 0, bucketCount)
	for bucket := 0; bucket < bucketCount; bucket++ {
		info := &SharedBucketInfo{
			Bucket:         bucket,
			PublicDeviceID: s.computeOAuthSlotDeviceID(account.ID, bucket),
		}
		if state := stateByBucket[bucket]; state != nil {
			info.LastSeenAt = state.LastSeenAt
			info.LastUserAgent = state.LastUserAgent
		}
		if fp, fpErr := s.cache.GetSlotFingerprint(ctx, account.ID, bucket); fpErr == nil && fp != nil {
			info.UserAgent = fp.UserAgent
			info.StainlessLang = fp.StainlessLang
			info.StainlessPackageVersion = fp.StainlessPackageVersion
			info.StainlessOS = fp.StainlessOS
			info.StainlessArch = fp.StainlessArch
			info.StainlessRuntime = fp.StainlessRuntime
			info.StainlessRuntimeVersion = fp.StainlessRuntimeVersion
			info.XApp = fp.XApp
			info.AnthropicBeta = fp.AnthropicBeta
			info.AnthropicVersion = fp.AnthropicVersion
			info.DangerousDirectAccess = fp.DangerousDirectAccess
		}
		buckets = append(buckets, info)
	}
	return buckets, nil
}

func (s *IdentityService) DeleteSharedBucket(ctx context.Context, account *Account, bucket int) error {
	if account == nil {
		return nil
	}
	if err := s.cache.DeleteSharedBucketState(ctx, account.ID, bucket); err != nil {
		return err
	}
	return s.cache.DeleteFingerprint(ctx, account.ID)
}

func (s *IdentityService) createSlotFingerprintFromHeaders(slot int, headers http.Header, now time.Time) *Fingerprint {
	fp := s.createFingerprintFromHeaders(headers)
	fp.Slot = slot
	fp.UpdatedAt = now.Unix()
	return fp
}

func (s *IdentityService) persistSlotFingerprint(ctx context.Context, accountID int64, slot int, fp *Fingerprint) error {
	if fp == nil {
		return nil
	}
	if err := s.cache.SetSlotFingerprint(ctx, accountID, slot, fp); err != nil {
		return err
	}
	return nil
}

func (s *IdentityService) normalizedSecret() string {
	if strings.TrimSpace(s.instanceSecret) != "" {
		return s.instanceSecret
	}
	return claudeOAuthDeviceIDContext
}

type oauthUserIDRewriteOptions struct {
	PreserveSetupTokenAccountUUID bool
}

func firstOAuthUserIDRewriteOptions(opts []oauthUserIDRewriteOptions) oauthUserIDRewriteOptions {
	if len(opts) > 0 {
		return opts[0]
	}
	return oauthUserIDRewriteOptions{}
}

func oauthAccountUUID(account *Account, fallback string) string {
	return oauthAccountUUIDWithOptions(account, fallback, oauthUserIDRewriteOptions{})
}

func oauthAccountUUIDWithOptions(account *Account, fallback string, opts oauthUserIDRewriteOptions) string {
	if account != nil {
		if account.GetClaudeOAuthMode() == ClaudeOAuthModeSingleDevice {
			if v := strings.TrimSpace(account.GetExtraString("account_uuid")); v != "" {
				return v
			}
		}
		if account.Type == AccountTypeSetupToken {
			if opts.PreserveSetupTokenAccountUUID {
				return fallback
			}
			return ""
		}
		if v := strings.TrimSpace(account.GetExtraString("account_uuid")); v != "" {
			return v
		}
	}
	return fallback
}

func rewriteParsedUserID(body []byte, currentUserID string, parsed *ParsedUserID, deviceID, accountUUID, sessionID, fingerprintUA string) ([]byte, error) {
	if len(body) == 0 || parsed == nil {
		return body, nil
	}
	version := ExtractCLIVersion(fingerprintUA)
	newUserID := FormatMetadataUserID(deviceID, accountUUID, sessionID, version)
	if newUserID == currentUserID {
		return body, nil
	}
	newBody, err := sjson.SetBytes(body, "metadata.user_id", newUserID)
	if err != nil {
		return body, nil
	}
	return newBody, nil
}

// RewriteUserID 重写 body 中的 metadata.user_id。
// 使用账号侧配置的 account_uuid，替换 device_id 和 session_id。
// 若账号侧未配置 account_uuid，则回退到请求体中的 account_uuid。
// 支持旧拼接格式和新 JSON 格式的 user_id 解析，
// 根据 fingerprintUA 版本选择输出格式。
//
// 重要：此函数使用 json.RawMessage 保留其他字段的原始字节，
// 避免重新序列化导致 thinking 块等内容被修改。
func (s *IdentityService) RewriteCarpoolUserID(body []byte, account *Account, fingerprintUA string, opts ...oauthUserIDRewriteOptions) ([]byte, error) {
	if len(body) == 0 || account == nil {
		return body, nil
	}
	rewriteOpts := firstOAuthUserIDRewriteOptions(opts)

	metadata := gjson.GetBytes(body, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return body, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return body, nil
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return body, nil
	}
	userID := userIDResult.String()
	if userID == "" {
		return body, nil
	}

	// 解析 user_id（兼容旧拼接格式和新 JSON 格式）
	parsed := ParseMetadataUserID(userID)
	if parsed == nil {
		return body, nil
	}
	deviceID := s.computeOAuthCarpoolDeviceID(account.ID, parsed.DeviceID)
	if deviceID == "" {
		return body, nil
	}
	accountUUID := oauthAccountUUIDWithOptions(account, parsed.AccountUUID, rewriteOpts)
	newSessionID := generateUUIDFromSeed(fmt.Sprintf("%d::%s", account.ID, parsed.SessionID))
	return rewriteParsedUserID(body, userID, parsed, deviceID, accountUUID, newSessionID, fingerprintUA)
}

func (s *IdentityService) RewriteSharedUserID(body []byte, account *Account, slot int, fingerprintUA string, opts ...oauthUserIDRewriteOptions) ([]byte, error) {
	if len(body) == 0 || account == nil {
		return body, nil
	}
	rewriteOpts := firstOAuthUserIDRewriteOptions(opts)

	deviceID := s.computeOAuthSlotDeviceID(account.ID, slot)
	if deviceID == "" {
		return body, nil
	}

	metadata := gjson.GetBytes(body, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return body, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return body, nil
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return body, nil
	}
	userID := userIDResult.String()
	if userID == "" {
		return body, nil
	}

	parsed := ParseMetadataUserID(userID)
	if parsed == nil {
		return body, nil
	}
	accountUUID := oauthAccountUUIDWithOptions(account, parsed.AccountUUID, rewriteOpts)
	newSessionID := generateUUIDFromSeed(fmt.Sprintf("%d:%d::%s", account.ID, slot, parsed.SessionID))
	return rewriteParsedUserID(body, userID, parsed, deviceID, accountUUID, newSessionID, fingerprintUA)
}

func (s *IdentityService) RewritePinnedUserID(body []byte, account *Account, slotKey, fingerprintUA string, opts ...oauthUserIDRewriteOptions) ([]byte, error) {
	if len(body) == 0 || account == nil {
		return body, nil
	}
	rewriteOpts := firstOAuthUserIDRewriteOptions(opts)

	metadata := gjson.GetBytes(body, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return body, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return body, nil
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return body, nil
	}
	userID := userIDResult.String()
	if userID == "" {
		return body, nil
	}

	parsed := ParseMetadataUserID(userID)
	if parsed == nil {
		return body, nil
	}
	deviceID := s.computeOAuthPinnedDeviceID(account.ID, slotKey, parsed.DeviceID)
	if deviceID == "" {
		return body, nil
	}
	accountUUID := oauthAccountUUIDWithOptions(account, parsed.AccountUUID, rewriteOpts)
	newSessionID := generateUUIDFromSeed(fmt.Sprintf("%d:%s::%s", account.ID, slotKey, parsed.SessionID))
	return rewriteParsedUserID(body, userID, parsed, deviceID, accountUUID, newSessionID, fingerprintUA)
}

func (s *IdentityService) RewriteUserID(body []byte, account *Account, slot int, fingerprintUA string, opts ...oauthUserIDRewriteOptions) ([]byte, error) {
	return s.RewriteSharedUserID(body, account, slot, fingerprintUA, opts...)
}

func (s *IdentityService) RewriteSingleDeviceUserID(body []byte, account *Account, fingerprintUA string, opts ...oauthUserIDRewriteOptions) ([]byte, error) {
	if len(body) == 0 || account == nil {
		return body, nil
	}
	rewriteOpts := firstOAuthUserIDRewriteOptions(opts)
	fixedDeviceID := strings.TrimSpace(account.GetClaudeOAuthFixedDeviceID())
	if fixedDeviceID == "" {
		return body, nil
	}
	metadata := gjson.GetBytes(body, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return body, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return body, nil
	}
	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return body, nil
	}
	userID := userIDResult.String()
	if userID == "" {
		return body, nil
	}
	parsed := ParseMetadataUserID(userID)
	if parsed == nil {
		return body, nil
	}
	accountUUID := oauthAccountUUIDWithOptions(account, parsed.AccountUUID, rewriteOpts)
	newSessionID := generateUUIDFromSeed(fmt.Sprintf("%d::%s", account.ID, parsed.SessionID))
	return rewriteParsedUserID(body, userID, parsed, fixedDeviceID, accountUUID, newSessionID, fingerprintUA)
}

// RewriteUserIDWithMasking 重写body中的metadata.user_id，支持会话ID伪装
// 如果账号启用了会话ID伪装（session_id_masking_enabled），
// 则在完成常规重写后，将 session 部分替换为固定的伪装ID（15分钟内保持不变）
//
// 重要：此函数使用 json.RawMessage 保留其他字段的原始字节，
// 避免重新序列化导致 thinking 块等内容被修改。
func (s *IdentityService) RewriteUserIDWithMasking(ctx context.Context, body []byte, account *Account, slot int, fingerprintUA string, opts ...oauthUserIDRewriteOptions) ([]byte, error) {
	rewriteOpts := firstOAuthUserIDRewriteOptions(opts)
	// 先执行常规的 RewriteUserID 逻辑
	newBody, err := s.RewriteUserID(body, account, slot, fingerprintUA, rewriteOpts)
	if err != nil {
		return newBody, err
	}

	// 检查是否启用会话ID伪装
	if !account.IsSessionIDMaskingEnabled() {
		return newBody, nil
	}

	metadata := gjson.GetBytes(newBody, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return newBody, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return newBody, nil
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return newBody, nil
	}
	userID := userIDResult.String()
	if userID == "" {
		return newBody, nil
	}

	// 解析已重写的 user_id
	uidParsed := ParseMetadataUserID(userID)
	if uidParsed == nil {
		return newBody, nil
	}

	// 获取或生成固定的伪装 session ID
	maskedSessionID, err := s.cache.GetSlotMaskedSessionID(ctx, account.ID, slot)
	if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to get masked session ID for account %d slot %d: %v", account.ID, slot, err)
		return newBody, nil
	}

	if maskedSessionID == "" {
		// 首次或已过期，生成新的伪装 session ID
		maskedSessionID = generateRandomUUID()
		logger.LegacyPrintf("service.identity", "Generated new masked session ID for account %d slot %d: %s", account.ID, slot, maskedSessionID)
	}

	// 刷新 TTL（每次请求都刷新，保持 15 分钟有效期）
	if err := s.cache.SetSlotMaskedSessionID(ctx, account.ID, slot, maskedSessionID); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to set masked session ID for account %d slot %d: %v", account.ID, slot, err)
	}

	// 用 FormatMetadataUserID 重建（保持与 RewriteUserID 相同的格式）
	version := ExtractCLIVersion(fingerprintUA)
	accountUUID := oauthAccountUUIDWithOptions(account, uidParsed.AccountUUID, rewriteOpts)
	newUserID := FormatMetadataUserID(uidParsed.DeviceID, accountUUID, maskedSessionID, version)

	slog.Debug("session_id_masking_applied",
		"account_id", account.ID,
		"before", userID,
		"after", newUserID,
	)

	if newUserID == userID {
		return newBody, nil
	}

	maskedBody, setErr := sjson.SetBytes(newBody, "metadata.user_id", newUserID)
	if setErr != nil {
		return newBody, nil
	}
	return maskedBody, nil
}

// generateRandomUUID 生成随机 UUID v4 格式字符串
func generateRandomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// fallback: 使用时间戳生成
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		b = h[:16]
	}

	// 设置 UUID v4 版本和变体位
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// generateClientID 生成64位十六进制客户端ID（32字节随机数）
func generateClientID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// 极罕见的情况，使用时间戳+固定值作为fallback
		logger.LegacyPrintf("service.identity", "Warning: crypto/rand.Read failed: %v, using fallback", err)
		// 使用SHA256(当前纳秒时间)作为fallback
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return hex.EncodeToString(h[:])
	}
	return hex.EncodeToString(b)
}

// generateUUIDFromSeed 从种子生成确定性UUID v4格式字符串
func generateUUIDFromSeed(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	bytes := hash[:16]

	// 设置UUID v4版本和变体位
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// parseUserAgentVersion 解析user-agent版本号
// 例如：claude-cli/2.1.2 -> (2, 1, 2)
func parseUserAgentVersion(ua string) (major, minor, patch int, ok bool) {
	// 匹配 xxx/x.y.z 格式
	matches := userAgentVersionRegex.FindStringSubmatch(ua)
	if len(matches) != 4 {
		return 0, 0, 0, false
	}
	major, _ = strconv.Atoi(matches[1])
	minor, _ = strconv.Atoi(matches[2])
	patch, _ = strconv.Atoi(matches[3])
	return major, minor, patch, true
}

// extractProduct 提取 User-Agent 中 "/" 前的产品名
// 例如：claude-cli/2.1.22 (external, cli) -> "claude-cli"
func extractProduct(ua string) string {
	if idx := strings.Index(ua, "/"); idx > 0 {
		return strings.ToLower(ua[:idx])
	}
	return ""
}

// isNewerVersion 比较版本号，判断newUA是否比cachedUA更新
// 要求产品名一致（防止浏览器 UA 如 Mozilla/5.0 误判为更新版本）
func isNewerVersion(newUA, cachedUA string) bool {
	// 校验产品名一致性
	newProduct := extractProduct(newUA)
	cachedProduct := extractProduct(cachedUA)
	if newProduct == "" || cachedProduct == "" || newProduct != cachedProduct {
		return false
	}

	newMajor, newMinor, newPatch, newOk := parseUserAgentVersion(newUA)
	cachedMajor, cachedMinor, cachedPatch, cachedOk := parseUserAgentVersion(cachedUA)

	if !newOk || !cachedOk {
		return false
	}

	// 比较版本号
	if newMajor > cachedMajor {
		return true
	}
	if newMajor < cachedMajor {
		return false
	}

	if newMinor > cachedMinor {
		return true
	}
	if newMinor < cachedMinor {
		return false
	}

	return newPatch > cachedPatch
}
