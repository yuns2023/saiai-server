package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type identityCacheStub struct {
	mu                  sync.Mutex
	accountFingerprints map[int64]*Fingerprint
	maskedSessionID     string
	slotMaskedSessions  map[string]string
	slotFingerprints    map[string]*Fingerprint
	carpoolDevices      map[string]*CarpoolDeviceRecord
	carpoolOverflow     map[string]*CarpoolOverflowRecord
	sharedBucketStates  map[string]*SharedBucketState
	sharedBucketBinds   map[string]int
	sharedBucketCount   map[int64]int
	singleDeviceSlots   map[string]int
	singleDeviceStates  map[string]*SingleDeviceSlotState
	pinnedDeviceBinds   map[string]map[int64]*PinnedDeviceBinding
	pinnedAccountBinds  map[string]*PinnedAccountBinding
}

func (s *identityCacheStub) GetFingerprint(_ context.Context, accountID int64) (*Fingerprint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accountFingerprints == nil {
		return nil, nil
	}
	if fp := s.accountFingerprints[accountID]; fp != nil {
		cp := *fp
		return &cp, nil
	}
	return nil, nil
}
func (s *identityCacheStub) SetFingerprint(_ context.Context, accountID int64, fp *Fingerprint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accountFingerprints == nil {
		s.accountFingerprints = map[int64]*Fingerprint{}
	}
	if fp == nil {
		delete(s.accountFingerprints, accountID)
		return nil
	}
	cp := *fp
	s.accountFingerprints[accountID] = &cp
	return nil
}
func (s *identityCacheStub) DeleteFingerprint(_ context.Context, accountID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accountFingerprints != nil {
		delete(s.accountFingerprints, accountID)
	}
	return nil
}
func (s *identityCacheStub) GetSlotFingerprint(_ context.Context, accountID int64, slot int) (*Fingerprint, error) {
	if s.slotFingerprints == nil {
		return nil, nil
	}
	return s.slotFingerprints[slotCacheKey(accountID, slot)], nil
}
func (s *identityCacheStub) SetSlotFingerprint(_ context.Context, accountID int64, slot int, fp *Fingerprint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slotFingerprints == nil {
		s.slotFingerprints = map[string]*Fingerprint{}
	}
	cp := *fp
	s.slotFingerprints[slotCacheKey(accountID, slot)] = &cp
	return nil
}
func (s *identityCacheStub) GetOrCreateCarpoolDevice(_ context.Context, accountID int64, originalDeviceID string, hints ClientHints, limit int, nowUnix int64) (*CarpoolDeviceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.carpoolDevices == nil {
		s.carpoolDevices = map[string]*CarpoolDeviceRecord{}
	}
	if s.carpoolOverflow == nil {
		s.carpoolOverflow = map[string]*CarpoolOverflowRecord{}
	}
	cacheKey := carpoolDeviceCacheKey(accountID, originalDeviceID)
	apply := func(r *CarpoolDeviceRecord) {
		if hints.UserAgent != "" {
			r.LastUserAgent = hints.UserAgent
		}
		if hints.OS != "" {
			r.LastOS = hints.OS
		}
		if hints.Arch != "" {
			r.LastArch = hints.Arch
		}
		if hints.Runtime != "" {
			r.LastRuntime = hints.Runtime
		}
		if hints.RuntimeVersion != "" {
			r.LastRuntimeVersion = hints.RuntimeVersion
		}
		if hints.SDKVersion != "" {
			r.LastSDKVersion = hints.SDKVersion
		}
	}
	if record := s.carpoolDevices[cacheKey]; record != nil {
		cp := *record
		cp.LastSeenAt = nowUnix
		apply(&cp)
		s.carpoolDevices[cacheKey] = &cp
		delete(s.carpoolOverflow, cacheKey)
		return &cp, nil
	}
	if len(s.carpoolDevices) < limit {
		record := &CarpoolDeviceRecord{
			DeviceKey:        carpoolDeviceKey(originalDeviceID),
			OriginalDeviceID: originalDeviceID,
			CreatedAt:        nowUnix,
			LastSeenAt:       nowUnix,
		}
		apply(record)
		cp := *record
		s.carpoolDevices[cacheKey] = &cp
		delete(s.carpoolOverflow, cacheKey)
		return record, nil
	}
	overflow := s.carpoolOverflow[cacheKey]
	if overflow == nil {
		overflow = &CarpoolOverflowRecord{
			DeviceKey:        carpoolDeviceKey(originalDeviceID),
			OriginalDeviceID: originalDeviceID,
			FirstRejectedAt:  nowUnix,
		}
	}
	overflow.LastRejectedAt = nowUnix
	overflow.RejectCount++
	overflow.LastUserAgent = hints.UserAgent
	cp := *overflow
	s.carpoolOverflow[cacheKey] = &cp
	return nil, ErrClaudeOAuthCarpoolDevicesFull
}
func (s *identityCacheStub) ListCarpoolDevices(_ context.Context, _ int64) ([]*CarpoolDeviceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.carpoolDevices == nil {
		return nil, nil
	}
	items := make([]*CarpoolDeviceRecord, 0, len(s.carpoolDevices))
	for _, item := range s.carpoolDevices {
		if item == nil {
			continue
		}
		cp := *item
		items = append(items, &cp)
	}
	return items, nil
}
func (s *identityCacheStub) ListCarpoolOverflowDevices(_ context.Context, _ int64) ([]*CarpoolOverflowRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.carpoolOverflow == nil {
		return nil, nil
	}
	items := make([]*CarpoolOverflowRecord, 0, len(s.carpoolOverflow))
	for _, item := range s.carpoolOverflow {
		if item == nil {
			continue
		}
		cp := *item
		items = append(items, &cp)
	}
	return items, nil
}
func (s *identityCacheStub) DeleteCarpoolDevice(_ context.Context, accountID int64, deviceKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.carpoolDevices != nil {
		delete(s.carpoolDevices, fmt.Sprintf("%d:%s", accountID, strings.TrimSpace(deviceKey)))
	}
	if s.carpoolOverflow != nil {
		delete(s.carpoolOverflow, fmt.Sprintf("%d:%s", accountID, strings.TrimSpace(deviceKey)))
	}
	return nil
}
func (s *identityCacheStub) EnsureSharedBucketTopology(_ context.Context, accountID int64, bucketCount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sharedBucketCount == nil {
		s.sharedBucketCount = map[int64]int{}
	}
	s.sharedBucketCount[accountID] = bucketCount
	if s.sharedBucketBinds != nil {
		for key, bucket := range s.sharedBucketBinds {
			if !strings.HasPrefix(key, fmt.Sprintf("%d:", accountID)) {
				continue
			}
			if bucket < 0 || bucket >= bucketCount {
				delete(s.sharedBucketBinds, key)
			}
		}
	}
	if s.sharedBucketStates != nil {
		for bucket := bucketCount; bucket < 32; bucket++ {
			delete(s.sharedBucketStates, sharedBucketStateCacheKey(accountID, bucket))
		}
	}
	if s.slotFingerprints != nil {
		for bucket := bucketCount; bucket < 32; bucket++ {
			delete(s.slotFingerprints, slotCacheKey(accountID, bucket))
		}
	}
	if s.slotMaskedSessions != nil {
		for bucket := bucketCount; bucket < 32; bucket++ {
			delete(s.slotMaskedSessions, slotCacheKey(accountID, bucket))
		}
	}
	return nil
}
func (s *identityCacheStub) GetOrAssignSharedBucket(_ context.Context, accountID int64, originalDeviceID string, bucketCount, preferredBucket int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sharedBucketBinds == nil {
		s.sharedBucketBinds = map[string]int{}
	}
	if preferredBucket < 0 || preferredBucket >= bucketCount {
		preferredBucket = 0
	}
	key := carpoolDeviceCacheKey(accountID, originalDeviceID)
	if bucket, ok := s.sharedBucketBinds[key]; ok && bucket >= 0 && bucket < bucketCount {
		return bucket, nil
	}
	s.sharedBucketBinds[key] = preferredBucket
	return preferredBucket, nil
}
func (s *identityCacheStub) GetSharedBucketState(_ context.Context, accountID int64, bucket int) (*SharedBucketState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sharedBucketStates == nil {
		return nil, nil
	}
	if state := s.sharedBucketStates[sharedBucketStateCacheKey(accountID, bucket)]; state != nil {
		cp := *state
		return &cp, nil
	}
	return nil, nil
}
func (s *identityCacheStub) SetSharedBucketState(_ context.Context, accountID int64, bucket int, state *SharedBucketState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state == nil {
		return nil
	}
	if s.sharedBucketStates == nil {
		s.sharedBucketStates = map[string]*SharedBucketState{}
	}
	cp := *state
	s.sharedBucketStates[sharedBucketStateCacheKey(accountID, bucket)] = &cp
	return nil
}
func (s *identityCacheStub) ListSharedBucketStates(_ context.Context, accountID int64, _ int) ([]*SharedBucketState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sharedBucketStates == nil {
		return nil, nil
	}
	items := make([]*SharedBucketState, 0, len(s.sharedBucketStates))
	for key, state := range s.sharedBucketStates {
		if !strings.HasPrefix(key, fmt.Sprintf("%d:", accountID)) || state == nil {
			continue
		}
		cp := *state
		items = append(items, &cp)
	}
	return items, nil
}
func (s *identityCacheStub) DeleteSharedBucketState(_ context.Context, accountID int64, slot int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sharedBucketStates != nil {
		delete(s.sharedBucketStates, sharedBucketStateCacheKey(accountID, slot))
	}
	if s.slotFingerprints != nil {
		delete(s.slotFingerprints, slotCacheKey(accountID, slot))
	}
	if s.slotMaskedSessions != nil {
		delete(s.slotMaskedSessions, slotCacheKey(accountID, slot))
	}
	return nil
}

func (s *identityCacheStub) GetOrCreateSingleDeviceSlot(_ context.Context, accountID int64, slotKey string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.singleDeviceSlots == nil {
		s.singleDeviceSlots = map[string]int{}
	}
	cacheKey := fmt.Sprintf("%d:%s", accountID, strings.TrimSpace(slotKey))
	if slot, ok := s.singleDeviceSlots[cacheKey]; ok {
		return slot, nil
	}
	slot := len(s.singleDeviceSlots)
	s.singleDeviceSlots[cacheKey] = slot
	return slot, nil
}

func (s *identityCacheStub) GetSingleDeviceSlotState(_ context.Context, accountID int64, slot int) (*SingleDeviceSlotState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.singleDeviceStates == nil {
		return nil, nil
	}
	if state := s.singleDeviceStates[sharedBucketStateCacheKey(accountID, slot)]; state != nil {
		cp := *state
		return &cp, nil
	}
	return nil, nil
}

func (s *identityCacheStub) SetSingleDeviceSlotState(_ context.Context, accountID int64, slot int, state *SingleDeviceSlotState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state == nil {
		return nil
	}
	if s.singleDeviceStates == nil {
		s.singleDeviceStates = map[string]*SingleDeviceSlotState{}
	}
	cp := *state
	s.singleDeviceStates[sharedBucketStateCacheKey(accountID, slot)] = &cp
	return nil
}

func (s *identityCacheStub) ListSingleDeviceSlotStates(_ context.Context, accountID int64) ([]*SingleDeviceSlotState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.singleDeviceStates == nil {
		return nil, nil
	}
	items := make([]*SingleDeviceSlotState, 0, len(s.singleDeviceStates))
	for key, state := range s.singleDeviceStates {
		if !strings.HasPrefix(key, fmt.Sprintf("%d:", accountID)) || state == nil {
			continue
		}
		cp := *state
		items = append(items, &cp)
	}
	return items, nil
}

func (s *identityCacheStub) GetPinnedDeviceBindings(_ context.Context, groupID int64, originalDeviceID string) ([]*PinnedDeviceBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pinnedDeviceBinds == nil {
		return nil, nil
	}
	items := s.pinnedDeviceBinds[pinnedDeviceCacheKey(groupID, originalDeviceID)]
	result := make([]*PinnedDeviceBinding, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		cp := *item
		result = append(result, &cp)
	}
	return result, nil
}

func (s *identityCacheStub) GetPinnedAccountBindings(_ context.Context, groupID int64, accountIDs []int64) (map[int64]*PinnedAccountBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[int64]*PinnedAccountBinding, len(accountIDs))
	if s.pinnedAccountBinds == nil {
		return result, nil
	}
	for _, accountID := range accountIDs {
		if item := s.pinnedAccountBinds[pinnedAccountCacheKey(groupID, accountID)]; item != nil {
			cp := *item
			result[accountID] = &cp
		}
	}
	return result, nil
}

func (s *identityCacheStub) BindPinnedDeviceAccount(_ context.Context, groupID int64, originalDeviceID string, deviceBinding *PinnedDeviceBinding, accountBinding *PinnedAccountBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pinnedDeviceBinds == nil {
		s.pinnedDeviceBinds = map[string]map[int64]*PinnedDeviceBinding{}
	}
	if s.pinnedAccountBinds == nil {
		s.pinnedAccountBinds = map[string]*PinnedAccountBinding{}
	}
	deviceKey := pinnedDeviceCacheKey(groupID, originalDeviceID)
	if s.pinnedDeviceBinds[deviceKey] == nil {
		s.pinnedDeviceBinds[deviceKey] = map[int64]*PinnedDeviceBinding{}
	}
	if deviceBinding != nil {
		cp := *deviceBinding
		s.pinnedDeviceBinds[deviceKey][deviceBinding.AccountID] = &cp
	}
	if accountBinding != nil {
		cp := *accountBinding
		s.pinnedAccountBinds[pinnedAccountCacheKey(groupID, accountBinding.AccountID)] = &cp
	}
	return nil
}

func (s *identityCacheStub) DeletePinnedDeviceAccountBinding(_ context.Context, groupID int64, originalDeviceID string, accountID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	deviceKey := pinnedDeviceCacheKey(groupID, originalDeviceID)
	if s.pinnedDeviceBinds != nil {
		if items := s.pinnedDeviceBinds[deviceKey]; items != nil {
			delete(items, accountID)
		}
	}
	if s.pinnedAccountBinds != nil {
		delete(s.pinnedAccountBinds, pinnedAccountCacheKey(groupID, accountID))
	}
	return nil
}
func (s *identityCacheStub) GetMaskedSessionID(_ context.Context, _ int64) (string, error) {
	return s.maskedSessionID, nil
}
func (s *identityCacheStub) SetMaskedSessionID(_ context.Context, _ int64, sessionID string) error {
	s.maskedSessionID = sessionID
	return nil
}
func (s *identityCacheStub) GetSlotMaskedSessionID(_ context.Context, accountID int64, slot int) (string, error) {
	if s.slotMaskedSessions == nil {
		return "", nil
	}
	return s.slotMaskedSessions[slotCacheKey(accountID, slot)], nil
}
func (s *identityCacheStub) SetSlotMaskedSessionID(_ context.Context, accountID int64, slot int, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slotMaskedSessions == nil {
		s.slotMaskedSessions = map[string]string{}
	}
	s.slotMaskedSessions[slotCacheKey(accountID, slot)] = sessionID
	return nil
}

func slotCacheKey(accountID int64, slot int) string {
	return fmt.Sprintf("%d:%d", accountID, slot)
}

func sharedBucketStateCacheKey(accountID int64, bucket int) string {
	return fmt.Sprintf("%d:%d", accountID, bucket)
}

func carpoolDeviceCacheKey(accountID int64, originalDeviceID string) string {
	return fmt.Sprintf("%d:%s", accountID, carpoolDeviceKey(originalDeviceID))
}

func pinnedDeviceCacheKey(groupID int64, originalDeviceID string) string {
	return fmt.Sprintf("%d:%s", groupID, carpoolDeviceKey(originalDeviceID))
}

func pinnedAccountCacheKey(groupID, accountID int64) string {
	return fmt.Sprintf("%d:%d", groupID, accountID)
}

func TestIdentityService_RewriteUserID_PreservesTopLevelFieldOrder(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"original-account-uuid",
		"00000000-0000-4000-8000-000000000006",
		"2.1.78",
	)
	body := []byte(`{"alpha":1,"messages":[],"metadata":{"user_id":` + strconvQuote(originalUserID) + `},"max_tokens":64000,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"stream":true}`)

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"claude_user_id": "shared-user-123",
			"account_uuid":   "forced-account-uuid",
		},
	}

	result, err := svc.RewriteUserID(body, account, 2, "claude-cli/2.1.78 (external, cli)")
	require.NoError(t, err)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"messages"`, `"metadata"`, `"max_tokens"`, `"thinking"`, `"output_config"`, `"stream"`)
	require.NotContains(t, resultStr, originalUserID)
	rewrittenUserID := gjson.GetBytes(result, "metadata.user_id").String()
	rewrittenParsed := ParseMetadataUserID(rewrittenUserID)
	require.NotNil(t, rewrittenParsed)
	require.Equal(t, "forced-account-uuid", rewrittenParsed.AccountUUID)
	require.Contains(t, resultStr, `"metadata":{"user_id":"`)
}

func TestIdentityService_RewriteUserIDWithMasking_PreservesTopLevelFieldOrder(t *testing.T) {
	cache := &identityCacheStub{slotMaskedSessions: map[string]string{"123:2": "11111111-2222-4333-8444-555555555555"}}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))

	originalUserID := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"",
		"00000000-0000-4000-8000-000000000006",
		"2.1.78",
	)
	body := []byte(`{"alpha":1,"messages":[],"metadata":{"user_id":` + strconvQuote(originalUserID) + `},"max_tokens":64000,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"stream":true}`)

	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"session_id_masking_enabled": true,
			"claude_user_id":             "shared-user-123",
			"account_uuid":               "forced-account-uuid",
		},
	}

	result, err := svc.RewriteUserIDWithMasking(context.Background(), body, account, 2, "claude-cli/2.1.78 (external, cli)")
	require.NoError(t, err)
	resultStr := string(result)

	assertJSONTokenOrder(t, resultStr, `"alpha"`, `"messages"`, `"metadata"`, `"max_tokens"`, `"thinking"`, `"output_config"`, `"stream"`)
	require.Contains(t, resultStr, "11111111-2222-4333-8444-555555555555")
	rewrittenUserID := gjson.GetBytes(result, "metadata.user_id").String()
	rewrittenParsed := ParseMetadataUserID(rewrittenUserID)
	require.NotNil(t, rewrittenParsed)
	require.Equal(t, "forced-account-uuid", rewrittenParsed.AccountUUID)
	require.True(t, strings.Contains(resultStr, `"metadata":{"user_id":"`))
}

func TestIdentityService_ComputeOAuthSlotDeviceID_IsStablePerSlot(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))

	v1 := svc.computeOAuthSlotDeviceID(123, 1)
	v2 := svc.computeOAuthSlotDeviceID(123, 1)
	v3 := svc.computeOAuthSlotDeviceID(123, 2)

	require.Len(t, v1, 64)
	require.Len(t, v2, 64)
	require.Equal(t, v1, v2)
	require.NotEqual(t, v1, v3)
}

func TestIdentityService_AssignOAuthSlot_UsesOriginalDeviceID(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))

	userIDA := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"",
		"00000000-0000-4000-8000-000000000006",
		"2.1.80",
	)
	userIDB := FormatMetadataUserID(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"",
		"11111111-2222-4333-8444-555555555555",
		"2.1.80",
	)

	require.Equal(t, svc.AssignOAuthSlot(123, userIDA), svc.AssignOAuthSlot(123, userIDB))
}

func TestIdentityService_GetOrCreateSlotFingerprint_SharedFingerprintRemainsStableWithoutAutoRotation(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))

	headers := http.Header{
		"User-Agent":                  []string{"claude-cli/2.1.80 (external, cli)"},
		"X-Stainless-Lang":            []string{"js"},
		"X-Stainless-Package-Version": []string{"0.74.0"},
		"X-Stainless-OS":              []string{"Linux"},
		"X-Stainless-Arch":            []string{"x64"},
		"X-Stainless-Runtime":         []string{"node"},
		"X-Stainless-Runtime-Version": []string{"v24.3.0"},
		"X-App":                       []string{"cli"},
		"anthropic-beta":              []string{"claude-code-20250219,interleaved-thinking-2025-05-14"},
	}

	slot, fp1, err := svc.GetOrCreateSlotFingerprint(context.Background(), 123, "user_a", headers)
	require.NoError(t, err)
	require.NotNil(t, fp1)
	require.Equal(t, int64(0), fp1.NextRotationAt)
	require.Equal(t, "claude-cli/2.1.80 (external, cli)", fp1.UserAgent)
	require.Equal(t, slot, fp1.Slot)

	updatedHeaders := headers.Clone()
	updatedHeaders.Set("User-Agent", "claude-cli/2.1.90 (external, cli)")
	updatedHeaders.Set("anthropic-beta", "claude-code-20250219,effort-2025-11-24")
	_, fp2, err := svc.GetOrCreateSlotFingerprint(context.Background(), 123, "user_a", updatedHeaders)
	require.NoError(t, err)
	require.Equal(t, fp1.UserAgent, fp2.UserAgent)
	require.Equal(t, fp1.AnthropicBeta, fp2.AnthropicBeta)
}

func TestIdentityService_DeleteSharedBucket_KeepsDeviceBindingButRelearnsFingerprint(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                     "forced-account-uuid",
			"claude_oauth_mode":                ClaudeOAuthModeShared,
			"claude_oauth_shared_bucket_count": 2,
		},
	}
	originalUserID := FormatMetadataUserID(strings.Repeat("a", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.81")
	firstHeaders := http.Header{"User-Agent": []string{"claude-cli/2.1.81 (external, cli)"}}
	secondHeaders := http.Header{"User-Agent": []string{"claude-cli/2.1.99 (external, cli)"}}

	firstBucket, firstFP, err := svc.GetOrCreateSharedSlotFingerprint(context.Background(), account, originalUserID, firstHeaders)
	require.NoError(t, err)
	require.NotNil(t, firstFP)

	err = svc.DeleteSharedBucket(context.Background(), account, firstBucket)
	require.NoError(t, err)

	secondBucket, secondFP, err := svc.GetOrCreateSharedSlotFingerprint(context.Background(), account, originalUserID, secondHeaders)
	require.NoError(t, err)
	require.NotNil(t, secondFP)
	require.Equal(t, firstBucket, secondBucket)
	require.Equal(t, "claude-cli/2.1.99 (external, cli)", secondFP.UserAgent)
}

func TestIdentityService_EnsurePinnedDeviceBinding_PersistsAndReusesBinding(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))
	account := &Account{
		ID:       321,
		Name:     "acc-pinned-a",
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":      "pinned-account-a",
			"claude_oauth_mode": ClaudeOAuthModePinned,
		},
	}
	userID := FormatMetadataUserID(strings.Repeat("a", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.81")

	first, err := svc.EnsurePinnedDeviceBinding(context.Background(), 10, userID, account)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Equal(t, account.ID, first.AccountID)
	require.Equal(t, pinnedSlotKey(account.ID), first.SlotKey)

	second, err := svc.EnsurePinnedDeviceBinding(context.Background(), 10, userID, account)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, first.DeviceKey, second.DeviceKey)
	require.Equal(t, first.SlotKey, second.SlotKey)
}

func TestIdentityService_EnsurePinnedDeviceBinding_RejectsOccupiedAccount(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))
	account := &Account{
		ID:       654,
		Name:     "acc-pinned-b",
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":      "pinned-account-b",
			"claude_oauth_mode": ClaudeOAuthModePinned,
		},
	}
	firstUserID := FormatMetadataUserID(strings.Repeat("a", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.81")
	secondUserID := FormatMetadataUserID(strings.Repeat("b", 64), "", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "2.1.81")

	_, err := svc.EnsurePinnedDeviceBinding(context.Background(), 11, firstUserID, account)
	require.NoError(t, err)

	_, err = svc.EnsurePinnedDeviceBinding(context.Background(), 11, secondUserID, account)
	require.ErrorIs(t, err, ErrClaudeOAuthPinnedDevicesFull)
}

func TestIdentityService_SharedFingerprintDoesNotPopulateAccountLevelFingerprint(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                     "forced-account-uuid",
			"claude_oauth_mode":                ClaudeOAuthModeShared,
			"claude_oauth_shared_bucket_count": 2,
		},
	}
	headers := http.Header{"User-Agent": []string{"claude-cli/2.1.81 (external, cli)"}}
	originalUserID := FormatMetadataUserID(strings.Repeat("d", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.81")

	_, _, err := svc.GetOrCreateSharedSlotFingerprint(context.Background(), account, originalUserID, headers)
	require.NoError(t, err)
	require.Empty(t, cache.accountFingerprints)
}

func TestIdentityService_DeleteSharedBucket_ClearsAccountLevelFingerprint(t *testing.T) {
	cache := &identityCacheStub{
		accountFingerprints: map[int64]*Fingerprint{
			123: {UserAgent: "claude-cli/2.1.81 (external, cli)"},
		},
	}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                     "forced-account-uuid",
			"claude_oauth_mode":                ClaudeOAuthModeShared,
			"claude_oauth_shared_bucket_count": 2,
		},
	}

	err := svc.DeleteSharedBucket(context.Background(), account, 1)
	require.NoError(t, err)
	require.Empty(t, cache.accountFingerprints)
}

func TestIdentityService_ResolveSharedBucket_PrunesOutOfRangeBindingsWhenBucketCountShrinks(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"account_uuid":                     "forced-account-uuid",
			"claude_oauth_mode":                ClaudeOAuthModeShared,
			"claude_oauth_shared_bucket_count": 4,
		},
	}
	originalUserID := FormatMetadataUserID(strings.Repeat("b", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.81")
	deviceID := oauthSlotSource(originalUserID)
	deviceKey := carpoolDeviceCacheKey(account.ID, deviceID)
	cache.sharedBucketBinds = map[string]int{deviceKey: 3}

	bucket, err := svc.ResolveSharedBucket(context.Background(), account, originalUserID)
	require.NoError(t, err)
	require.Equal(t, 3, bucket)

	account.Extra["claude_oauth_shared_bucket_count"] = 2
	reboundBucket, err := svc.ResolveSharedBucket(context.Background(), account, originalUserID)
	require.NoError(t, err)
	require.NotEqual(t, 3, reboundBucket)
	require.GreaterOrEqual(t, reboundBucket, 0)
	require.Less(t, reboundBucket, 2)
	require.Equal(t, reboundBucket, cache.sharedBucketBinds[deviceKey])
}

func TestIdentityService_GetOrCreateSharedSlotFingerprint_ConcurrentSameDeviceReusesSingleSlot(t *testing.T) {
	cache := &identityCacheStub{}
	svc := NewIdentityService(cache, strings.Repeat("x", 32))
	account := &Account{
		ID:       123,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode":                ClaudeOAuthModeShared,
			"claude_oauth_shared_bucket_count": 2,
		},
	}
	headers := http.Header{
		"User-Agent": []string{"claude-cli/2.1.81 (external, cli)"},
	}
	originalUserID := FormatMetadataUserID(strings.Repeat("a", 64), "", "11111111-2222-4333-8444-555555555555", "2.1.81")

	const workers = 8
	var wg sync.WaitGroup
	slots := make(chan int, workers)
	errs := make(chan error, workers)
	start := make(chan struct{})

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			slot, _, err := svc.GetOrCreateSharedSlotFingerprint(context.Background(), account, originalUserID, headers)
			if err != nil {
				errs <- err
				return
			}
			slots <- slot
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	close(slots)

	for err := range errs {
		require.NoError(t, err)
	}

	seen := make(map[int]int)
	for slot := range slots {
		seen[slot]++
	}
	require.Len(t, seen, 1)

	states, err := cache.ListSharedBucketStates(context.Background(), account.ID, account.GetClaudeOAuthSharedBucketCount())
	require.NoError(t, err)
	require.Len(t, states, 1)
}

func strconvQuote(v string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + `"`
}
