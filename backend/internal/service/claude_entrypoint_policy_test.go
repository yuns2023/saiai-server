//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func validFacts(ua, body string) claudebilling.EntrypointFacts {
	return claudebilling.EntrypointFacts{UAEntrypoint: ua, UAVersion: "2.1.316", BodyEntrypoint: body, BodyStatus: claudebilling.EntrypointBodyValid}
}

func TestDecideClaudeEntrypoint(t *testing.T) {
	defaults := DefaultClaudeEntrypointAllowlist()
	cases := []struct {
		name      string
		facts     claudebilling.EntrypointFacts
		allowlist []string
		reason    string
	}{
		{"cli ok", validFacts("cli", "cli"), defaults, ClaudeEntrypointReasonOK},
		{"vscode ok", validFacts("claude-vscode", "claude-vscode"), defaults, ClaudeEntrypointReasonOK},
		{"t3 code", validFacts("t3-code", "t3-code"), defaults, ClaudeEntrypointReasonUANotAllowed},
		{"no ua entrypoint", validFacts("", "cli"), defaults, ClaudeEntrypointReasonUANotAllowed},
		{"body missing", claudebilling.EntrypointFacts{UAEntrypoint: "cli", BodyStatus: claudebilling.EntrypointBodyMissing}, defaults, ClaudeEntrypointReasonBodyMissing},
		{"body malformed", claudebilling.EntrypointFacts{UAEntrypoint: "cli", BodyStatus: claudebilling.EntrypointBodyMalformed}, defaults, ClaudeEntrypointReasonBodyMalformed},
		{"body ambiguous", claudebilling.EntrypointFacts{UAEntrypoint: "cli", BodyStatus: claudebilling.EntrypointBodyAmbiguous}, defaults, ClaudeEntrypointReasonBodyAmbiguous},
		{"unknown body status", claudebilling.EntrypointFacts{UAEntrypoint: "cli"}, defaults, ClaudeEntrypointReasonBodyAmbiguous},
		{"body not allowed", validFacts("cli", "t3-code"), defaults, ClaudeEntrypointReasonBodyNotAllowed},
		{"mismatch", validFacts("cli", "claude-vscode"), defaults, ClaudeEntrypointReasonMismatch},
		{"ua checked first", claudebilling.EntrypointFacts{UAEntrypoint: "t3-code", BodyStatus: claudebilling.EntrypointBodyMissing}, defaults, ClaudeEntrypointReasonUANotAllowed},
		{"empty allowlist", validFacts("cli", "cli"), []string{}, ClaudeEntrypointReasonUANotAllowed},
		{"custom allowlist", validFacts("t3-code", "t3-code"), []string{"t3-code"}, ClaudeEntrypointReasonOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := DecideClaudeEntrypoint(tc.facts, tc.allowlist)
			require.Equal(t, tc.reason, decision.Reason)
			require.Equal(t, tc.reason == ClaudeEntrypointReasonOK, decision.Compliant)
		})
	}
}

func TestClaudeEntrypointResult(t *testing.T) {
	ok := ClaudeEntrypointDecision{Compliant: true, Reason: ClaudeEntrypointReasonOK}
	bad := ClaudeEntrypointDecision{Reason: ClaudeEntrypointReasonMismatch}
	require.Equal(t, ClaudeEntrypointResultAllowed, ClaudeEntrypointResult(ClaudeEntrypointPolicyObserve, ok))
	require.Equal(t, ClaudeEntrypointResultAllowed, ClaudeEntrypointResult(ClaudeEntrypointPolicyEnforce, ok))
	require.Equal(t, ClaudeEntrypointResultViolationObserved, ClaudeEntrypointResult(ClaudeEntrypointPolicyObserve, bad))
	require.Equal(t, ClaudeEntrypointResultRejected, ClaudeEntrypointResult(ClaudeEntrypointPolicyEnforce, bad))
}

func TestClaudeEntrypointRejectionMessage(t *testing.T) {
	require.Equal(t,
		"This group does not allow this Claude client entrypoint (UA: t3-code, body: t3-code). Allowed: cli, claude-vscode, sdk-cli.",
		ClaudeEntrypointRejectionMessage(validFacts("t3-code", "t3-code"), DefaultClaudeEntrypointAllowlist()))
	require.Equal(t,
		"This group does not allow this Claude client entrypoint (UA: <none>, body: <ambiguous>). Allowed: none.",
		ClaudeEntrypointRejectionMessage(claudebilling.EntrypointFacts{BodyStatus: claudebilling.EntrypointBodyAmbiguous}, []string{}))
}

func TestGroupClaudeEntrypointAccessors(t *testing.T) {
	require.Equal(t, ClaudeEntrypointPolicyOff, (*Group)(nil).ClaudeEntrypointPolicyMode())
	require.Equal(t, ClaudeEntrypointPolicyOff, (&Group{ClaudeEntrypointPolicy: "bogus"}).ClaudeEntrypointPolicyMode())
	require.Equal(t, ClaudeEntrypointPolicyEnforce, (&Group{ClaudeEntrypointPolicy: " Enforce "}).ClaudeEntrypointPolicyMode())
	require.Equal(t, DefaultClaudeEntrypointAllowlist(), (&Group{}).EffectiveClaudeEntrypointAllowlist(), "nil = not loaded = default")
	require.Empty(t, (&Group{ClaudeEntrypointAllowlist: []string{}}).EffectiveClaudeEntrypointAllowlist(), "empty allows nothing")
}

func TestNormalizeClaudeEntrypointAllowlist(t *testing.T) {
	got, err := NormalizeClaudeEntrypointAllowlist([]string{" CLI ", "cli", "t3-code", "a.b_c-1"})
	require.NoError(t, err)
	require.Equal(t, []string{"cli", "t3-code", "a.b_c-1"}, got)

	got, err = NormalizeClaudeEntrypointAllowlist([]string{})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)

	for _, bad := range []string{"", " ", "-cli", "t3 code", "agent-sdk/0.3", "é", strings.Repeat("a", 65)} {
		_, err = NormalizeClaudeEntrypointAllowlist([]string{bad})
		require.Error(t, err, bad)
		require.Equal(t, "INVALID_CLAUDE_ENTRYPOINT_ALLOWLIST", infraerrors.Reason(err))
	}
	long := make([]string, 33)
	for i := range long {
		long[i] = "e" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	_, err = NormalizeClaudeEntrypointAllowlist(long)
	require.Error(t, err)
	_, err = NormalizeClaudeEntrypointAllowlist(append(long[:32:32], "ea"+"a")) // "eaa" duplicates long[0]: 32 items after dedupe
	require.NoError(t, err)
}

func TestAdminService_ClaudeEntrypointPolicyCreateValidation(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}
	base := func() *CreateGroupInput {
		return &CreateGroupInput{Name: "g", Platform: PlatformAnthropic, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1}
	}

	_, err := svc.CreateGroup(context.Background(), base())
	require.NoError(t, err)
	require.Equal(t, ClaudeEntrypointPolicyOff, repo.created.ClaudeEntrypointPolicy, "default off")
	require.Equal(t, DefaultClaudeEntrypointAllowlist(), repo.created.ClaudeEntrypointAllowlist, "absent → default list")

	in := base()
	in.ClaudeEntrypointPolicy = " Observe "
	in.ClaudeEntrypointAllowlist = []string{}
	_, err = svc.CreateGroup(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, ClaudeEntrypointPolicyObserve, repo.created.ClaudeEntrypointPolicy)
	require.NotNil(t, repo.created.ClaudeEntrypointAllowlist)
	require.Empty(t, repo.created.ClaudeEntrypointAllowlist, "explicit [] is stored")

	in = base()
	in.ClaudeEntrypointPolicy = "block"
	_, err = svc.CreateGroup(context.Background(), in)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "INVALID_CLAUDE_ENTRYPOINT_POLICY", infraerrors.Reason(err))

	in = base()
	in.Platform = PlatformOpenAI
	in.ClaudeEntrypointPolicy = ClaudeEntrypointPolicyEnforce
	_, err = svc.CreateGroup(context.Background(), in)
	require.Equal(t, "INVALID_CLAUDE_ENTRYPOINT_POLICY", infraerrors.Reason(err), "non-anthropic group")

	in = base()
	in.ClaudeEntrypointAllowlist = []string{"cli", "t3 code"}
	_, err = svc.CreateGroup(context.Background(), in)
	require.Equal(t, "INVALID_CLAUDE_ENTRYPOINT_ALLOWLIST", infraerrors.Reason(err))
}

func TestAdminService_ClaudeEntrypointPolicyUpdateValidation(t *testing.T) {
	stored := func() *Group {
		return &Group{ID: 1, Platform: PlatformAnthropic, Status: StatusActive, RateMultiplier: 1,
			ClaudeEntrypointPolicy: ClaudeEntrypointPolicyObserve, ClaudeEntrypointAllowlist: []string{"cli", "t3-code"}}
	}
	repo := &groupRepoStubForAdmin{getByID: stored()}
	svc := &adminServiceImpl{groupRepo: repo}

	_, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{})
	require.NoError(t, err)
	require.Equal(t, ClaudeEntrypointPolicyObserve, repo.updated.ClaudeEntrypointPolicy, "absent keeps policy")
	require.Equal(t, []string{"cli", "t3-code"}, repo.updated.ClaudeEntrypointAllowlist, "absent keeps allowlist")

	repo.getByID = stored()
	empty := []string{}
	enforce := "ENFORCE"
	_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{ClaudeEntrypointPolicy: &enforce, ClaudeEntrypointAllowlist: &empty})
	require.NoError(t, err)
	require.Equal(t, ClaudeEntrypointPolicyEnforce, repo.updated.ClaudeEntrypointPolicy)
	require.NotNil(t, repo.updated.ClaudeEntrypointAllowlist)
	require.Empty(t, repo.updated.ClaudeEntrypointAllowlist, "explicit [] clears the list")

	repo.getByID = stored()
	bad := "deny"
	_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{ClaudeEntrypointPolicy: &bad})
	require.Equal(t, "INVALID_CLAUDE_ENTRYPOINT_POLICY", infraerrors.Reason(err))

	repo.getByID = stored()
	badList := []string{"CLI", "<script>"}
	_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{ClaudeEntrypointAllowlist: &badList})
	require.Equal(t, "INVALID_CLAUDE_ENTRYPOINT_ALLOWLIST", infraerrors.Reason(err))

	// Switching the platform away from anthropic with an active policy is
	// rejected; switching the policy off in the same request is fine.
	repo.getByID = stored()
	_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{Platform: PlatformOpenAI})
	require.Equal(t, "INVALID_CLAUDE_ENTRYPOINT_POLICY", infraerrors.Reason(err))
	repo.getByID = stored()
	off := "off"
	_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{Platform: PlatformOpenAI, ClaudeEntrypointPolicy: &off})
	require.NoError(t, err)

	repo.getByID = &Group{ID: 2, Platform: PlatformGemini, Status: StatusActive, RateMultiplier: 1}
	observe := "observe"
	_, err = svc.UpdateGroup(context.Background(), 2, &UpdateGroupInput{ClaudeEntrypointPolicy: &observe})
	require.Equal(t, "INVALID_CLAUDE_ENTRYPOINT_POLICY", infraerrors.Reason(err))
}

func TestAPIKeyService_SnapshotRoundTripsClaudeEntrypointPolicy(t *testing.T) {
	svc := &APIKeyService{}
	groupID := int64(17)
	for _, allowlist := range [][]string{{"cli", "t3-code"}, {}} {
		key := &APIKey{
			ID: 1, UserID: 2, User: &User{ID: 2}, GroupID: &groupID,
			Group: &Group{ID: groupID, Status: StatusActive, Platform: PlatformAnthropic,
				ClaudeEntrypointPolicy: ClaudeEntrypointPolicyEnforce, ClaudeEntrypointAllowlist: allowlist},
		}
		snapshot := svc.snapshotFromAPIKey(key)
		require.Equal(t, 5, snapshot.SchemaVersion)
		encoded, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
		require.NoError(t, err)
		require.Contains(t, string(encoded), `"claude_entrypoint_policy":"enforce"`)
		var decoded APIKeyAuthCacheEntry
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		restored, ok, err := svc.applyAuthCacheEntry("k", &decoded)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, ClaudeEntrypointPolicyEnforce, restored.Group.ClaudeEntrypointPolicy)
		require.NotNil(t, restored.Group.ClaudeEntrypointAllowlist, "[] must not decode as nil (= default list)")
		require.Equal(t, allowlist, restored.Group.ClaudeEntrypointAllowlist)

		// v4 snapshots predate the policy and would decode it as off.
		decoded.Snapshot.SchemaVersion = 4
		restored, ok, err = svc.applyAuthCacheEntry("k", &decoded)
		require.NoError(t, err)
		require.False(t, ok)
		require.Nil(t, restored)
	}
}

type entrypointObservationRepoStub struct {
	mu       sync.Mutex
	upserts  [][]ClaudeEntrypointObservation
	deleted  []string
	err      error
	listDays []string
}

func (r *entrypointObservationRepoStub) UpsertClaudeEntrypointObservations(_ context.Context, rows []ClaudeEntrypointObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upserts = append(r.upserts, append([]ClaudeEntrypointObservation(nil), rows...))
	return r.err
}

func (r *entrypointObservationRepoStub) DeleteClaudeEntrypointObservationsBefore(_ context.Context, day string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, day)
	return 0, nil
}

func (r *entrypointObservationRepoStub) ListClaudeEntrypointObservations(_ context.Context, _ int64, since string, _ int) ([]ClaudeEntrypointObservationSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listDays = append(r.listDays, since)
	return nil, nil
}

func TestClaudeEntrypointObservationService_AggregatesAndFlushes(t *testing.T) {
	repo := &entrypointObservationRepoStub{}
	svc := NewClaudeEntrypointObservationService(repo)
	now := time.Date(2026, 11, 2, 23, 59, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	in := ClaudeEntrypointObservationInput{
		GroupID: 7, Facts: validFacts("t3-code", "t3-code"), Policy: ClaudeEntrypointPolicyObserve,
		Result: ClaudeEntrypointResultViolationObserved, Reason: ClaudeEntrypointReasonUANotAllowed,
		UserAgent: "claude-cli/2.1.316 (external, t3-code, agent-sdk/0.3.276)",
	}
	svc.Record(in)
	now = now.Add(30 * time.Second)
	in.UserAgent = "claude-cli/2.1.316 (external, t3-code, agent-sdk/0.3.277)\x00"
	svc.Record(in)
	svc.Record(ClaudeEntrypointObservationInput{GroupID: 7, Facts: claudebilling.EntrypointFacts{BodyStatus: claudebilling.EntrypointBodyMissing},
		Policy: ClaudeEntrypointPolicyEnforce, Result: ClaudeEntrypointResultRejected, Reason: ClaudeEntrypointReasonUANotAllowed})

	svc.Flush(context.Background())
	require.Len(t, repo.upserts, 1)
	rows := repo.upserts[0]
	require.Len(t, rows, 2)
	byResult := map[string]ClaudeEntrypointObservation{}
	for _, row := range rows {
		byResult[row.Result] = row
	}
	observed := byResult[ClaudeEntrypointResultViolationObserved]
	require.Equal(t, int64(2), observed.Requests)
	require.Equal(t, "2026-11-02", observed.Day, "UTC day")
	require.Equal(t, "t3-code", observed.UAEntrypoint)
	require.Equal(t, "2.1.316", observed.CLIVersion)
	require.Equal(t, "claude-cli/2.1.316 (external, t3-code, agent-sdk/0.3.277)", observed.SampleUserAgent, "latest sample, control chars stripped")
	rejected := byResult[ClaudeEntrypointResultRejected]
	require.Equal(t, claudebilling.EntrypointNone, rejected.UAEntrypoint)
	require.Equal(t, claudebilling.EntrypointNone, rejected.BodyEntrypoint)
	require.Equal(t, "", rejected.CLIVersion)

	svc.Flush(context.Background())
	require.Len(t, repo.upserts, 1, "nothing pending → no write")
}

func TestClaudeEntrypointObservationService_BoundedAndDropsOnError(t *testing.T) {
	repo := &entrypointObservationRepoStub{err: errors.New("db down")}
	svc := NewClaudeEntrypointObservationService(repo)
	for i := 0; i < claudeEntrypointObservationMaxKeys+10; i++ {
		svc.Record(ClaudeEntrypointObservationInput{GroupID: int64(i), Facts: validFacts("cli", "cli"),
			Policy: ClaudeEntrypointPolicyObserve, Result: ClaudeEntrypointResultAllowed, Reason: ClaudeEntrypointReasonOK})
	}
	svc.mu.Lock()
	require.Len(t, svc.pending, claudeEntrypointObservationMaxKeys)
	require.Equal(t, int64(10), svc.dropped)
	svc.mu.Unlock()

	svc.Flush(context.Background())
	require.Len(t, repo.upserts, 1)
	require.Len(t, repo.upserts[0], claudeEntrypointObservationMaxKeys)
	svc.mu.Lock()
	require.Empty(t, svc.pending, "failed batch is dropped, not retried")
	require.Zero(t, svc.dropped)
	svc.mu.Unlock()
}

func TestClaudeEntrypointObservationService_StopFlushesAndListBounds(t *testing.T) {
	repo := &entrypointObservationRepoStub{}
	svc := NewClaudeEntrypointObservationService(repo)
	svc.now = func() time.Time { return time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC) }
	svc.Start()
	svc.Record(ClaudeEntrypointObservationInput{GroupID: 1, Facts: validFacts("cli", "cli"),
		Policy: ClaudeEntrypointPolicyObserve, Result: ClaudeEntrypointResultAllowed, Reason: ClaudeEntrypointReasonOK})
	svc.Stop()
	svc.Stop()
	repo.mu.Lock()
	require.Len(t, repo.upserts, 1, "Stop flushes")
	repo.mu.Unlock()

	_, err := svc.List(context.Background(), 1, 7)
	require.NoError(t, err)
	_, err = svc.List(context.Background(), 1, 1000)
	require.NoError(t, err)
	require.Equal(t, []string{"2026-10-27", "2026-08-05"}, repo.listDays)

	svc.DeleteExpired(context.Background())
	require.Equal(t, []string{"2026-08-04"}, repo.deleted)

	var nilSvc *ClaudeEntrypointObservationService
	nilSvc.Record(ClaudeEntrypointObservationInput{})
	nilSvc.Stop()
	require.True(t, nilSvc.ShouldLogViolation(1, "a", "b"))
	unstarted := NewClaudeEntrypointObservationService(repo)
	unstarted.Stop()
}

func TestClaudeEntrypointObservationService_ShouldLogViolationRateLimits(t *testing.T) {
	svc := NewClaudeEntrypointObservationService(nil)
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	require.True(t, svc.ShouldLogViolation(1, "t3-code", "t3-code"))
	require.False(t, svc.ShouldLogViolation(1, "t3-code", "t3-code"))
	require.True(t, svc.ShouldLogViolation(2, "t3-code", "t3-code"), "per group")
	require.True(t, svc.ShouldLogViolation(1, "cli", "t3-code"), "per entrypoint pair")
	now = now.Add(time.Minute)
	require.True(t, svc.ShouldLogViolation(1, "t3-code", "t3-code"))
}
