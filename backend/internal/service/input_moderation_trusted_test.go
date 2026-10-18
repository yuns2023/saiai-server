package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type trustChangingModerationClient struct {
	users *inputModerationUserRepoStub
	calls int
}

func (c *trustChangingModerationClient) Classify(context.Context, string) (*InputModerationClassification, error) {
	c.calls++
	c.users.user.IsTrusted = true
	return &InputModerationClassification{Safety: "unsafe", Categories: []string{"PII"}, Scores: map[string]float64{"PII": .99}}, nil
}

func TestInputModerationTrustedUserSkipAndRevocation(t *testing.T) {
	users := &inputModerationUserRepoStub{user: &User{ID: 1, IsTrusted: true, Status: StatusActive}}
	groups := &inputModerationGroupRepoStub{groups: []*Group{{ID: 3, InputModerationEnabled: true}}}
	repo := &moderationResultRepoStub{}
	client := &inputModerationClientStub{result: &InputModerationClassification{Safety: "safe"}}
	svc := newInputModerationServiceForTest(client, repo, groups, users, nil)
	defer svc.Stop()
	task := InputModerationTask{UserID: 1, APIKeyID: 2, GroupID: 3, RequestID: "TEST_ONLY_TRUST", Text: "TEST_ONLY"}
	require.NoError(t, svc.process(task))
	require.Zero(t, client.calls)
	require.Empty(t, repo.events)
	require.Equal(t, "skipped", repo.results[0].Status)
	require.Equal(t, "trusted_user", repo.results[0].Reason)
	users.user.IsTrusted = false
	require.NoError(t, svc.process(task))
	require.Equal(t, 1, client.calls)
	require.Equal(t, "passed", repo.results[1].Status)
}

func TestInputModerationTrustGrantedDuringInferencePreventsPenalty(t *testing.T) {
	users := &inputModerationUserRepoStub{user: &User{ID: 1, Status: StatusActive}}
	groups := &inputModerationGroupRepoStub{groups: []*Group{{ID: 3, InputModerationEnabled: true, InputModerationAutoDisableUser: true, InputModerationCategories: []string{"PII"}}}}
	repo := &moderationResultRepoStub{}
	client := &trustChangingModerationClient{users: users}
	svc := newInputModerationServiceForTest(client, repo, groups, users, nil)
	defer svc.Stop()
	require.NoError(t, svc.process(InputModerationTask{UserID: 1, APIKeyID: 2, GroupID: 3, RequestID: "TEST_ONLY_PROMOTION", Text: "TEST_ONLY"}))
	require.Equal(t, 1, client.calls)
	require.Empty(t, repo.events)
	require.Zero(t, users.updateCalls)
	require.Equal(t, "trusted_user", repo.results[0].Reason)
}
