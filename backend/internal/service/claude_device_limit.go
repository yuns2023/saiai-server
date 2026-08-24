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
	DeviceID    string     `json:"device_id,omitempty"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

// ClaudeUserDeviceSummary is the aggregate shown in the admin user list.
// Counts are summed across the user's Claude device-enabled groups because a
// registration is scoped to a group.
type ClaudeUserDeviceSummary struct {
	ActiveDevices  int
	EffectiveLimit *int
}

type ClaudeDeviceRepository interface {
	CheckAndRegister(ctx context.Context, userID, groupID, apiKeyID int64, deviceHash, deviceID, mode string, baseLimit int) (*ClaudeDeviceRegistrationResult, error)
	AddBonusDevices(ctx context.Context, userID, groupID int64, count int) error
	ListUserDevices(ctx context.Context, userID int64, groupID *int64) ([]ClaudeUserDevice, error)
	ListUserDeviceSummaries(ctx context.Context, userIDs []int64) (map[int64]ClaudeUserDeviceSummary, error)
	RevokeUserDevice(ctx context.Context, userID, deviceID int64) error
}

func (s *ClaudeDeviceService) ListUserDevices(ctx context.Context, userID int64, groupID *int64) ([]ClaudeUserDevice, error) {
	return s.repo.ListUserDevices(ctx, userID, groupID)
}

func (s *ClaudeDeviceService) ListUserDeviceSummaries(ctx context.Context, userIDs []int64) (map[int64]ClaudeUserDeviceSummary, error) {
	if s == nil || s.repo == nil || len(userIDs) == 0 {
		return map[int64]ClaudeUserDeviceSummary{}, nil
	}
	return s.repo.ListUserDeviceSummaries(ctx, userIDs)
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

func claudeDeviceLogFields(apiKey *APIKey, groupID int64, deviceID string) []any {
	fields := []any{"user_id", apiKey.UserID, "group_id", groupID}
	if apiKey.User != nil && strings.TrimSpace(apiKey.User.Username) != "" {
		fields = append(fields, "username", strings.TrimSpace(apiKey.User.Username))
	}
	if deviceID = strings.TrimSpace(deviceID); deviceID != "" {
		fields = append(fields, "device_ref", ClaudeDeviceLogRef(deviceID), "device_id_last4", deviceIDLast4(deviceID))
	}
	return fields
}

// ClaudeDeviceLogRef returns a stable, non-reversible identifier for log
// correlation. It intentionally does not expose the original device_id.
func ClaudeDeviceLogRef(deviceID string) string {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(deviceID))
	return "dev_" + hex.EncodeToString(sum[:])[:12]
}

func deviceIDLast4(deviceID string) string {
	runes := []rune(strings.TrimSpace(deviceID))
	if len(runes) <= 4 {
		return string(runes)
	}
	return string(runes[len(runes)-4:])
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
			fields := claudeDeviceLogFields(apiKey, *apiKey.GroupID, "")
			fields = append(fields, "mode", mode)
			slog.Log(ctx, slog.LevelWarn, "claude_device_identity_missing_audit", fields...)
			return nil
		}
		fields := claudeDeviceLogFields(apiKey, *apiKey.GroupID, "")
		fields = append(fields, "mode", mode)
		slog.Log(ctx, slog.LevelWarn, "claude_device_identity_missing_rejected", fields...)
		return &ClaudeDeviceLimitError{Limit: apiKey.Group.ClaudeDeviceBaseLimit}
	}
	deviceID := strings.TrimSpace(identity.DeviceID)
	sum := sha256.Sum256([]byte(deviceID))
	result, err := s.repo.CheckAndRegister(
		ctx, apiKey.UserID, *apiKey.GroupID, apiKey.ID, hex.EncodeToString(sum[:]), deviceID, mode, apiKey.Group.ClaudeDeviceBaseLimit,
	)
	if err != nil {
		fields := claudeDeviceLogFields(apiKey, *apiKey.GroupID, deviceID)
		fields = append(fields, "mode", mode, "error", err)
		slog.Log(ctx, slog.LevelError, "claude_device_registration_error", fields...)
		return err
	}
	if result == nil {
		return nil
	}
	fields := claudeDeviceLogFields(apiKey, *apiKey.GroupID, deviceID)
	fields = append(fields, "mode", mode, "active_devices", result.ActiveDevices, "effective_limit", result.EffectiveLimit)
	if result.Registered {
		slog.Log(ctx, slog.LevelInfo, "claude_device_registered", fields...)
	} else if result.Existing {
		slog.Log(ctx, slog.LevelDebug, "claude_device_seen", fields...)
	}
	if result.OverLimitAudit {
		slog.Log(ctx, slog.LevelWarn, "claude_device_limit_audit", fields...)
	}
	if !result.Allowed {
		slog.Log(ctx, slog.LevelWarn, "claude_device_limit_rejected", fields...)
		return &ClaudeDeviceLimitError{Limit: result.EffectiveLimit}
	}
	return nil
}
