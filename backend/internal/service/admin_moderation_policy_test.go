//go:build unit

package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAdminModerationRejectsUnsupportedCategoriesAndUnsafePolicies(t *testing.T) {
	svc := &adminServiceImpl{}
	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{InputModerationCategories: []string{"Violent"}})
	require.Error(t, err)
	_, err = svc.CreateGroup(context.Background(), &CreateGroupInput{InputModerationPolicy: &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"violence"}, EnforcementCategories: []string{"violence"}}})
	require.Error(t, err)
}
func TestAdminModerationPreservesUnchangedLegacySelections(t *testing.T) {
	require.True(t, sameLegacyModerationCategories([]string{"Violent", "PII"}, []string{"PII", "Violent"}))
	require.False(t, sameLegacyModerationCategories([]string{"PII"}, []string{"Violent", "PII"}))
	require.Error(t, ValidateLegacyModerationCategories([]string{"Violent", "PII"}))
}

func TestAdminModerationUpdatePreservesPolicyAndRejectsUnsafeReplacement(t *testing.T) {
	policy := &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII", "violence"}, EnforcementCategories: []string{}}
	current := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, InputModerationPolicy: policy, InputModerationCategories: []string{"PII"}}
	repo := &groupRepoStubForAdmin{getByID: current}
	svc := &adminServiceImpl{groupRepo: repo}
	_, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{Name: "renamed"})
	require.NoError(t, err)
	require.Equal(t, policy, repo.updated.InputModerationPolicy)
	repo.updated = nil
	_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{InputModerationPolicy: &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"violence"}, EnforcementCategories: []string{"violence"}}})
	require.Error(t, err)
	require.Nil(t, repo.updated)
	current.InputModerationPolicy = nil
	current.InputModerationCategories = []string{"PII", "Violent"}
	unchanged := []string{"PII", "Violent"}
	_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{InputModerationCategories: &unchanged})
	require.NoError(t, err)
	require.Nil(t, repo.updated.InputModerationPolicy)
	changed := []string{"PII", "Copyright Violation"}
	repo.updated = nil
	_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{InputModerationCategories: &changed})
	require.Error(t, err)
	require.Nil(t, repo.updated)
}
