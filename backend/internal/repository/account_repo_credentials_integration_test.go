//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// The refresh flow: read the account, call upstream (seconds), save. A 429,
// a disable and an extra sample written in between must survive the save.
func (s *AccountRepoSuite) TestUpdateCredentials_KeepsStateWrittenDuringRefresh() {
	acc := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "refresh-race",
		Credentials: map[string]any{"access_token": "old", "refresh_token": "r1"},
		Extra:       map[string]any{"passive_usage_7d_utilization": 10.0},
	})
	stale, err := s.repo.GetByID(s.ctx, acc.ID)
	s.Require().NoError(err)

	resetAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	s.Require().NoError(s.repo.SetRateLimited(s.ctx, acc.ID, resetAt))
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, acc.ID, map[string]any{"passive_usage_7d_utilization": 55.0}))
	s.Require().NoError(s.repo.SetSchedulable(s.ctx, acc.ID, false))

	newCreds := map[string]any{"access_token": "new", "refresh_token": "r2", "_token_version": 123}
	_ = stale // the refresh would hold this object; only the credentials are written
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, acc.ID, newCreds))

	got, err := s.repo.GetByID(s.ctx, acc.ID)
	s.Require().NoError(err)
	s.Require().Equal("new", got.GetCredential("access_token"))
	s.Require().Equal("r2", got.GetCredential("refresh_token"))
	s.Require().NotNil(got.RateLimitResetAt, "429 written during the refresh survives")
	s.Require().True(got.RateLimitResetAt.Equal(resetAt))
	s.Require().False(got.Schedulable, "disable written during the refresh survives")
	s.Require().Equal(55.0, got.Extra["passive_usage_7d_utilization"])
}

func (s *AccountRepoSuite) TestUpdateCredentials_DeletedAccount() {
	acc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "refresh-deleted"})
	s.Require().NoError(s.repo.Delete(s.ctx, acc.ID))
	s.Require().ErrorIs(s.repo.UpdateCredentials(s.ctx, acc.ID, map[string]any{"access_token": "x"}), service.ErrAccountNotFound)
}

func (s *AccountRepoSuite) TestMergeCredentials_AccessTokenGuard() {
	acc := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "merge-guard",
		Credentials: map[string]any{"access_token": "current", "refresh_token": "r"},
	})

	// A late 401 from a request that used an older token does nothing.
	applied, err := s.repo.MergeCredentials(s.ctx, acc.ID, map[string]any{"expires_at": "2000-01-01T00:00:00Z"},
		service.CredentialsGuard{AccessToken: "older"})
	s.Require().NoError(err)
	s.Require().False(applied)
	got, err := s.repo.GetByID(s.ctx, acc.ID)
	s.Require().NoError(err)
	s.Require().Empty(got.GetCredential("expires_at"))

	applied, err = s.repo.MergeCredentials(s.ctx, acc.ID, map[string]any{"expires_at": "2000-01-01T00:00:00Z"},
		service.CredentialsGuard{AccessToken: "current"})
	s.Require().NoError(err)
	s.Require().True(applied)
	got, err = s.repo.GetByID(s.ctx, acc.ID)
	s.Require().NoError(err)
	s.Require().Equal("2000-01-01T00:00:00Z", got.GetCredential("expires_at"))
	s.Require().Equal("r", got.GetCredential("refresh_token"), "merge keeps the other keys")
}

func (s *AccountRepoSuite) TestMergeCredentials_EmptyKeysGuard() {
	acc := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "merge-empty",
		Credentials: map[string]any{"access_token": "t", "project_id": ""},
	})
	applied, err := s.repo.MergeCredentials(s.ctx, acc.ID, map[string]any{"project_id": "p1", "tier_id": "free"},
		service.CredentialsGuard{EmptyKeys: []string{"project_id"}})
	s.Require().NoError(err)
	s.Require().True(applied)

	// A slower detection does not overwrite the value set meanwhile.
	applied, err = s.repo.MergeCredentials(s.ctx, acc.ID, map[string]any{"project_id": "p2"},
		service.CredentialsGuard{EmptyKeys: []string{"project_id"}})
	s.Require().NoError(err)
	s.Require().False(applied)

	got, err := s.repo.GetByID(s.ctx, acc.ID)
	s.Require().NoError(err)
	s.Require().Equal("p1", got.GetCredential("project_id"))
	s.Require().Equal("free", got.GetCredential("tier_id"))
	s.Require().Equal("t", got.GetCredential("access_token"))
}

func (s *AccountRepoSuite) TestMergeCredentials_DeletedAccount() {
	acc := mustCreateAccount(s.T(), s.client, &service.Account{Name: "merge-deleted", Credentials: map[string]any{"access_token": "t"}})
	s.Require().NoError(s.repo.Delete(s.ctx, acc.ID))
	_, err := s.repo.MergeCredentials(s.ctx, acc.ID, map[string]any{"expires_at": "x"}, service.CredentialsGuard{})
	s.Require().ErrorIs(err, service.ErrAccountNotFound)
}

func (s *AccountRepoSuite) TestMergeCredentials_BlankCountsAsEmptyAndGuardsCombine() {
	acc := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "merge-blank",
		Credentials: map[string]any{"access_token": "t", "project_id": " \t\n "},
	})
	// Token mismatch fails the whole guard even though project_id is blank.
	applied, err := s.repo.MergeCredentials(s.ctx, acc.ID, map[string]any{"project_id": "p"},
		service.CredentialsGuard{AccessToken: "other", EmptyKeys: []string{"project_id", "tier_id"}})
	s.Require().NoError(err)
	s.Require().False(applied)

	applied, err = s.repo.MergeCredentials(s.ctx, acc.ID, map[string]any{"project_id": "p"},
		service.CredentialsGuard{AccessToken: "t", EmptyKeys: []string{"project_id", "tier_id"}})
	s.Require().NoError(err)
	s.Require().True(applied, "blank and missing keys count as empty")
	got, err := s.repo.GetByID(s.ctx, acc.ID)
	s.Require().NoError(err)
	s.Require().Equal("p", got.GetCredential("project_id"))
}

func (s *AccountRepoSuite) TestMergeCredentials_EveryEmptyKeyIsChecked() {
	acc := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "merge-second-key",
		Credentials: map[string]any{"access_token": "t", "project_id": "", "tier_id": "set"},
	})
	applied, err := s.repo.MergeCredentials(s.ctx, acc.ID, map[string]any{"project_id": "p", "tier_id": "x"},
		service.CredentialsGuard{AccessToken: "t", EmptyKeys: []string{"project_id", "tier_id"}})
	s.Require().NoError(err)
	s.Require().False(applied, "the second key is set, so the guard fails")
	got, err := s.repo.GetByID(s.ctx, acc.ID)
	s.Require().NoError(err)
	s.Require().Equal("set", got.GetCredential("tier_id"))
}
