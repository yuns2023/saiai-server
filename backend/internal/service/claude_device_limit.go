package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type ClaudeDeviceRegistrationResult struct {
	Allowed        bool
	Existing       bool
	Registered     bool
	OverLimitAudit bool
	ActiveDevices  int
	EffectiveLimit int
}

type ClaudeUserDevice struct {
	ID          int64      `json:"id"`
	UserID      int64      `json:"user_id"`
	GroupID     int64      `json:"group_id"`
	DeviceHash  string     `json:"device_hash"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

type ClaudeDeviceRepository interface {
	CheckAndRegister(ctx context.Context, userID, groupID, apiKeyID int64, deviceHash, mode string, baseLimit int) (*ClaudeDeviceRegistrationResult, error)
	AddBonusDevices(ctx context.Context, userID, groupID int64, count int) error
	ListUserDevices(ctx context.Context, userID int64, groupID *int64) ([]ClaudeUserDevice, error)
	RevokeUserDevice(ctx context.Context, userID, deviceID int64) error
}

func (s *ClaudeDeviceService) ListUserDevices(ctx context.Context, userID int64, groupID *int64) ([]ClaudeUserDevice, error) {
	return s.repo.ListUserDevices(ctx, userID, groupID)
}

func (s *ClaudeDeviceService) RevokeUserDevice(ctx context.Context, userID, deviceID int64) error {
	return s.repo.RevokeUserDevice(ctx, userID, deviceID)
}

type ClaudeDeviceLimitError struct {
	Limit int
}

func (e *ClaudeDeviceLimitError) Error() string {
	return fmt.Sprintf("device limit reached for Claude Code (%d)", e.Limit)
}

type ClaudeDeviceService struct {
	repo ClaudeDeviceRepository
}

func NewClaudeDeviceService(repo ClaudeDeviceRepository) *ClaudeDeviceService {
	return &ClaudeDeviceService{repo: repo}
}

func (s *ClaudeDeviceService) CheckAndRegister(ctx context.Context, apiKey *APIKey, parsed *ParsedRequest) error {
	if s == nil || s.repo == nil || apiKey == nil || apiKey.Group == nil || apiKey.GroupID == nil || apiKey.UserID <= 0 {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(apiKey.Group.ClaudeDeviceLimitMode))
	if mode == "" || mode == "off" {
		return nil
	}
	metadataUserID := ""
	if parsed != nil {
		metadataUserID = parsed.MetadataUserID
	}
	identity := ParseMetadataUserID(strings.TrimSpace(metadataUserID))
	if identity == nil || strings.TrimSpace(identity.DeviceID) == "" {
		if mode == "audit" {
			slog.Warn("claude_device_identity_missing_audit", "user_id", apiKey.UserID, "group_id", *apiKey.GroupID)
			return nil
		}
		return &ClaudeDeviceLimitError{Limit: apiKey.Group.ClaudeDeviceBaseLimit}
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(identity.DeviceID)))
	result, err := s.repo.CheckAndRegister(
		ctx, apiKey.UserID, *apiKey.GroupID, apiKey.ID, hex.EncodeToString(sum[:]), mode, apiKey.Group.ClaudeDeviceBaseLimit,
	)
	if err != nil {
		return err
	}
	if result != nil && result.OverLimitAudit {
		slog.Warn("claude_device_limit_audit", "user_id", apiKey.UserID, "group_id", *apiKey.GroupID, "active_devices", result.ActiveDevices, "effective_limit", result.EffectiveLimit)
	}
	if result != nil && !result.Allowed {
		return &ClaudeDeviceLimitError{Limit: result.EffectiveLimit}
	}
	return nil
}
