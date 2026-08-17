package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type redeemClaudeDeviceGroupRepoStub struct {
	GroupRepository
	group *Group
	err   error
}

func (s *redeemClaudeDeviceGroupRepoStub) GetByID(context.Context, int64) (*Group, error) {
	return s.group, s.err
}

func TestValidateClaudeDeviceRedeemCode(t *testing.T) {
	groupID := int64(7)

	tests := []struct {
		name    string
		code    *RedeemCode
		group   *Group
		repoErr error
		wantErr string
	}{
		{
			name:  "anthropic group",
			code:  &RedeemCode{Type: RedeemTypeClaudeDevice, Value: 2, GroupID: &groupID},
			group: &Group{ID: groupID, Platform: PlatformAnthropic},
		},
		{
			name:  "antigravity group",
			code:  &RedeemCode{Type: RedeemTypeClaudeDevice, Value: 1, GroupID: &groupID},
			group: &Group{ID: groupID, Platform: PlatformAntigravity},
		},
		{
			name:    "OpenAI group",
			code:    &RedeemCode{Type: RedeemTypeClaudeDevice, Value: 1, GroupID: &groupID},
			group:   &Group{ID: groupID, Platform: PlatformOpenAI},
			wantErr: "require an Anthropic or Antigravity group",
		},
		{
			name:    "fractional quota",
			code:    &RedeemCode{Type: RedeemTypeClaudeDevice, Value: 1.5, GroupID: &groupID},
			group:   &Group{ID: groupID, Platform: PlatformAnthropic},
			wantErr: "positive integer value",
		},
		{
			name:    "group lookup failure",
			code:    &RedeemCode{Type: RedeemTypeClaudeDevice, Value: 1, GroupID: &groupID},
			repoErr: errors.New("database unavailable"),
			wantErr: "database unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &RedeemService{groupRepo: &redeemClaudeDeviceGroupRepoStub{group: tt.group, err: tt.repoErr}}
			err := svc.validateClaudeDeviceRedeemCode(context.Background(), tt.code)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
