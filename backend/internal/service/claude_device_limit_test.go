package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type claudeDeviceRepoStub struct {
	result     *ClaudeDeviceRegistrationResult
	deviceHash string
	deviceID   string
}

func (s *claudeDeviceRepoStub) CheckAndRegister(_ context.Context, _, _, _ int64, deviceHash, deviceID, _ string, _ int) (*ClaudeDeviceRegistrationResult, error) {
	s.deviceHash = deviceHash
	s.deviceID = deviceID
	return s.result, nil
}
func (*claudeDeviceRepoStub) AddBonusDevices(context.Context, int64, int64, int) error { return nil }
func (*claudeDeviceRepoStub) ListUserDevices(context.Context, int64, *int64) ([]ClaudeUserDevice, error) {
	return nil, nil
}
func (*claudeDeviceRepoStub) ListUserDeviceSummaries(context.Context, []int64) (map[int64]ClaudeUserDeviceSummary, error) {
	return nil, nil
}
func (*claudeDeviceRepoStub) RevokeUserDevice(context.Context, int64, int64) error { return nil }

func TestClaudeDeviceServiceCheckAndRegister(t *testing.T) {
	groupID := int64(3)
	repo := &claudeDeviceRepoStub{result: &ClaudeDeviceRegistrationResult{Allowed: true, Registered: true, EffectiveLimit: 1}}
	svc := NewClaudeDeviceService(repo)
	err := svc.CheckAndRegister(context.Background(), &APIKey{
		ID: 2, UserID: 1, GroupID: &groupID,
		Group: &Group{ID: groupID, ClaudeDeviceLimitMode: "enforce", ClaudeDeviceBaseLimit: 1},
	}, &ParsedRequest{MetadataUserID: `{"device_id":"device-a","account_uuid":"","session_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}`})
	require.NoError(t, err)
	require.Len(t, repo.deviceHash, 64)
	require.Equal(t, "device-a", repo.deviceID)
}

func TestClaudeDeviceServiceRejectsMissingOrOverLimitDevice(t *testing.T) {
	groupID := int64(3)
	svc := NewClaudeDeviceService(&claudeDeviceRepoStub{result: &ClaudeDeviceRegistrationResult{Allowed: false, EffectiveLimit: 1}})
	key := &APIKey{ID: 2, UserID: 1, GroupID: &groupID, Group: &Group{ID: groupID, ClaudeDeviceLimitMode: "enforce", ClaudeDeviceBaseLimit: 1}}

	var limitErr *ClaudeDeviceLimitError
	require.ErrorAs(t, svc.CheckAndRegister(context.Background(), key, &ParsedRequest{}), &limitErr)
	require.ErrorAs(t, svc.CheckAndRegister(context.Background(), key, &ParsedRequest{MetadataUserID: `{"device_id":"device-b","session_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}`}), &limitErr)
}
