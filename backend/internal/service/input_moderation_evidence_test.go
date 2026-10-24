package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

const evidenceTestRoot = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const evidenceTestOldRoot = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

var evidenceTestCategories = []string{"Jailbreak", "PII", "Non-violent Illegal Acts", "Unethical Acts", "violence"}

// markerScoreClient scores a category high when its marker occurs in the text.
type markerScoreClient struct {
	markers map[string]map[string]float64
	texts   []string
}

func (c *markerScoreClient) scores(text string) map[string]float64 {
	scores := map[string]float64{}
	for _, category := range evidenceTestCategories {
		scores[category] = 0.01
	}
	for marker, hits := range c.markers {
		if strings.Contains(text, marker) {
			for category, score := range hits {
				if score > scores[category] {
					scores[category] = score
				}
			}
		}
	}
	return scores
}

func (c *markerScoreClient) Classify(_ context.Context, text string) (*InputModerationClassification, error) {
	c.texts = append(c.texts, text)
	return &InputModerationClassification{Safety: "safe", Scores: c.scores(text), ModelVersion: "TEST_ONLY"}, nil
}

func (c *markerScoreClient) ClassifyWithPolicy(ctx context.Context, text string, _ *InputModerationPolicy) (*InputModerationClassification, error) {
	return c.Classify(ctx, text)
}

type failingEvidenceSealer struct{ *moderationEvidenceCrypto }

func (failingEvidenceSealer) Seal(ModerationEvidenceAAD, []byte) (string, error) {
	return "", errors.New("TEST_ONLY seal failure")
}

func evidenceTestGroup(autoDisable bool, enforce ...string) *Group {
	return &Group{ID: 3, InputModerationEnabled: true, InputModerationAutoDisableUser: autoDisable, InputModerationMinConfidence: 90,
		InputModerationPolicy: &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: evidenceTestCategories, EnforcementCategories: enforce, Thresholds: map[string]int{}}}
}

func newEvidenceTestService(t *testing.T, client InputModerationClient, group *Group) (*InputModerationService, *moderationResultRepoStub) {
	t.Helper()
	repo := &moderationResultRepoStub{}
	repo.transition = &InputModerationTransition{Action: InputModerationActionWarning, StrikeCount: 1}
	svc := newInputModerationServiceForTest(client, repo, &inputModerationGroupRepoStub{groups: []*Group{group}}, nil, nil)
	svc.cfg.MaxInputChars = 32768
	svc.cfg.EvidenceRetentionDays = 90
	crypto, err := newModerationEvidenceCrypto(evidenceTestRoot, nil)
	require.NoError(t, err)
	svc.evidenceSealer = crypto
	t.Cleanup(svc.Stop)
	return svc, repo
}

func openEvidence(t *testing.T, evidence InputModerationEvidence) moderationEvidencePlaintext {
	t.Helper()
	crypto, err := newModerationEvidenceCrypto(evidenceTestRoot, nil)
	require.NoError(t, err)
	plaintext, err := crypto.Open(evidence.KeyID, evidence.AAD, evidence.Ciphertext)
	require.NoError(t, err)
	var content moderationEvidencePlaintext
	require.NoError(t, json.Unmarshal(plaintext, &content))
	return content
}

func TestModerationEvidenceCapturesThresholdEqualCheck(t *testing.T) {
	client := &markerScoreClient{markers: map[string]map[string]float64{"EXACT": {"PII": 0.90}}}
	svc, repo := newEvidenceTestService(t, client, evidenceTestGroup(false, "PII"))
	require.NoError(t, svc.process(InputModerationTask{RequestID: "r", JobID: "00000000-0000-4000-8000-0000000000a1", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "EXACT question"}))
	require.Len(t, repo.events, 1)
	require.Len(t, repo.events[0].Evidence, 1)
	evidence := repo.events[0].Evidence[0]
	require.Equal(t, []string{moderationCheckText}, evidence.Kinds)
	require.Equal(t, ModerationEvidenceAAD{JobID: "00000000-0000-4000-8000-0000000000a1", UserID: 1, GroupID: 3, CheckIndex: 0, Kinds: []string{moderationCheckText}}, evidence.AAD)
	require.NotContains(t, evidence.Ciphertext, "EXACT")
	content := openEvidence(t, evidence)
	require.Equal(t, "EXACT question", content.Text)
	require.Equal(t, []string{"PII"}, content.Categories)
	require.Equal(t, 0.90, content.Scores["PII"])
	require.Len(t, repo.results, 1)
	require.Equal(t, ModerationEvidenceStored, repo.results[0].Diagnostics.EvidenceStatus)
	require.Equal(t, 1, repo.results[0].Diagnostics.EvidenceCount)
	require.False(t, repo.results[0].Diagnostics.EvidenceTruncated)
}

func TestModerationEvidenceTruncatesBeyondFourChecks(t *testing.T) {
	// Five instruction bundles, each crossing a different category, plus the
	// joint check crossing nothing new: only four checks are kept.
	client := &markerScoreClient{markers: map[string]map[string]float64{}}
	var instructions []string
	for i, category := range evidenceTestCategories {
		marker := fmt.Sprintf("MARK%d", i)
		client.markers[marker] = map[string]float64{category: 0.95 + float64(i)/100}
		instructions = append(instructions, marker+" "+strings.Repeat("z", 9000))
	}
	svc, repo := newEvidenceTestService(t, client, evidenceTestGroup(false, "PII"))
	require.NoError(t, svc.process(InputModerationTask{RequestID: "r", JobID: "00000000-0000-4000-8000-0000000000a2", UserID: 1, APIKeyID: 2, GroupID: 3, Instructions: instructions}))
	require.Len(t, repo.events, 1)
	require.Len(t, repo.events[0].Evidence, moderationEvidenceMaxChecks)
	diagnostics := repo.results[0].Diagnostics
	require.True(t, diagnostics.EvidenceTruncated)
	require.Len(t, diagnostics.EvidenceOmittedCategories, 1)
	// The enforcement category is always covered first.
	require.NotContains(t, diagnostics.EvidenceOmittedCategories, "PII")
	stored := map[string]bool{}
	for _, evidence := range repo.events[0].Evidence {
		for _, category := range openEvidence(t, evidence).Categories {
			stored[category] = true
		}
	}
	require.True(t, stored["PII"])
	require.True(t, openEvidence(t, repo.events[0].Evidence[0]).Truncated)
	// The weakest margin (Jailbreak, index 0) is the one left out.
	require.Equal(t, []string{"Jailbreak"}, diagnostics.EvidenceOmittedCategories)
}

func TestModerationEvidenceCapturedOnCacheHit(t *testing.T) {
	client := &cachedModerationBudgetStub{checks: map[string]bool{"cached DANGER": true}}
	// The fixture scores PII .1; a threshold of 10 flags it.
	group := &Group{ID: 3, InputModerationEnabled: true, InputModerationMinConfidence: 10}
	svc, repo := newEvidenceTestService(t, nil, group)
	svc.client = client
	require.NoError(t, svc.process(InputModerationTask{RequestID: "r", JobID: "00000000-0000-4000-8000-0000000000a3", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "cached DANGER"}))
	require.Equal(t, 1, repo.results[0].Diagnostics.CacheHits)
	require.Len(t, repo.events[0].Evidence, 1)
	require.Equal(t, "cached DANGER", openEvidence(t, repo.events[0].Evidence[0]).Text)
}

func TestModerationEvidenceBothWritePathsAndStatuses(t *testing.T) {
	client := &markerScoreClient{markers: map[string]map[string]float64{"BAD": {"PII": 0.99}}}
	for _, tc := range []struct {
		name        string
		autoDisable bool
		mutate      func(*InputModerationService)
		wantStatus  string
		wantCount   int
	}{
		{"audit insert path", false, nil, ModerationEvidenceStored, 1},
		{"incident path", true, nil, ModerationEvidenceStored, 1},
		{"encrypt failure", true, func(s *InputModerationService) {
			crypto, ok := s.evidenceSealer.(*moderationEvidenceCrypto)
			require.True(t, ok)
			s.evidenceSealer = failingEvidenceSealer{crypto}
		}, ModerationEvidenceEncryptFailed, 0},
		{"no key", false, func(s *InputModerationService) { s.evidenceSealer = nil }, ModerationEvidenceNoKey, 0},
		{"retention disabled", false, func(s *InputModerationService) { s.cfg.EvidenceRetentionDays = 0 }, ModerationEvidenceDisabled, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newEvidenceTestService(t, client, evidenceTestGroup(tc.autoDisable, "PII"))
			if tc.mutate != nil {
				tc.mutate(svc)
			}
			require.NoError(t, svc.process(InputModerationTask{RequestID: "r", JobID: "00000000-0000-4000-8000-0000000000a4", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "BAD"}))
			require.Len(t, repo.events, 1, "the event is written whatever happens to evidence")
			require.Len(t, repo.events[0].Evidence, tc.wantCount)
			require.Len(t, repo.results, 1)
			require.Equal(t, "flagged", repo.results[0].Status)
			require.Equal(t, tc.wantStatus, repo.results[0].Diagnostics.EvidenceStatus)
			require.Equal(t, tc.wantCount, repo.results[0].Diagnostics.EvidenceCount)
		})
	}
}

func TestModerationEvidenceUnsupportedWithoutScores(t *testing.T) {
	client := &inputModerationClientStub{result: &InputModerationClassification{Safety: "unsafe", Categories: []string{"Jailbreak"}, ModelVersion: "qwen"}}
	svc, repo := newEvidenceTestService(t, client, &Group{ID: 3, InputModerationEnabled: true})
	require.NoError(t, svc.process(InputModerationTask{RequestID: "r", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "legacy"}))
	require.Empty(t, repo.events[0].Evidence)
	require.Equal(t, ModerationEvidenceUnsupported, repo.results[0].Diagnostics.EvidenceStatus)
	require.Empty(t, repo.events[0].IncidentFingerprint)
}

func TestModerationEvidenceSelectionNeverSubstitutesNonCrossingCheck(t *testing.T) {
	group := evidenceTestGroup(false, "PII")
	rule := moderationDecisionRule{group: group, policy: group.InputModerationPolicy}
	checks := []moderationCheckResult{{Check: moderationCheck{Index: 0, Text: "a"}, Result: &InputModerationClassification{Scores: map[string]float64{"PII": 0.5}}}}
	_, ok := selectModerationEvidence(checks, moderationCheckCrossings(checks, rule), []string{"PII"}, rule, moderationEvidenceMaxChecks)
	require.False(t, ok)
}

func TestModerationEvidenceCryptoAADAndPreviousKeys(t *testing.T) {
	old, err := newModerationEvidenceCrypto(evidenceTestOldRoot, nil)
	require.NoError(t, err)
	aad := ModerationEvidenceAAD{JobID: "job", UserID: 1, GroupID: 3, CheckIndex: 2, Kinds: []string{moderationCheckInstructions}}
	ciphertext, err := old.Seal(aad, []byte(`{"text":"secret"}`))
	require.NoError(t, err)
	rotated, err := newModerationEvidenceCrypto(evidenceTestRoot, []string{evidenceTestOldRoot})
	require.NoError(t, err)
	require.NotEqual(t, old.KeyID(), rotated.KeyID())
	plaintext, err := rotated.Open(old.KeyID(), aad, ciphertext)
	require.NoError(t, err)
	require.JSONEq(t, `{"text":"secret"}`, string(plaintext))
	// Any change to the authenticated context is rejected.
	for _, swapped := range []ModerationEvidenceAAD{
		{JobID: "other", UserID: 1, GroupID: 3, CheckIndex: 2, Kinds: aad.Kinds},
		{JobID: "job", UserID: 9, GroupID: 3, CheckIndex: 2, Kinds: aad.Kinds},
		{JobID: "job", UserID: 1, GroupID: 3, CheckIndex: 1, Kinds: aad.Kinds},
		{JobID: "job", UserID: 1, GroupID: 3, CheckIndex: 2, Kinds: []string{moderationCheckJoint}},
	} {
		_, err := rotated.Open(old.KeyID(), swapped, ciphertext)
		require.Error(t, err)
	}
	// Without the previous root the key id is unknown.
	current, err := newModerationEvidenceCrypto(evidenceTestRoot, nil)
	require.NoError(t, err)
	_, err = current.Open(old.KeyID(), aad, ciphertext)
	require.ErrorIs(t, err, errModerationEvidenceKeyUnknown)
	// Content MACs are keyed separately from encryption and differ per root.
	require.NotEqual(t, old.MAC("x"), current.MAC("x"))
}

func TestModerationIncidentFingerprint(t *testing.T) {
	bundle := "BUNDLE rules " + strings.Repeat("r", 100)
	run := func(t *testing.T, markers map[string]map[string]float64, task InputModerationTask, enforce ...string) (*moderationResultRepoStub, *markerScoreClient) {
		t.Helper()
		client := &markerScoreClient{markers: markers}
		svc, repo := newEvidenceTestService(t, client, evidenceTestGroup(true, enforce...))
		task.RequestID, task.UserID, task.APIKeyID, task.GroupID = "r", 1, 2, 3
		require.NoError(t, svc.process(task))
		require.Len(t, repo.events, 1)
		return repo, client
	}
	t.Run("joint and bundle both crossing get a stable fingerprint", func(t *testing.T) {
		markers := map[string]map[string]float64{"BUNDLE": {"PII": 0.99}}
		first, client := run(t, markers, InputModerationTask{Text: "turn one", Instructions: []string{bundle}}, "PII")
		require.NotEmpty(t, first.events[0].IncidentFingerprint)
		// The plan had no text-only check, so one lazy text-alone check ran.
		require.Contains(t, client.texts, "turn one")
		require.Len(t, first.events[0].Evidence, 2)
		second, _ := run(t, markers, InputModerationTask{Text: "turn two", Instructions: []string{bundle}}, "PII")
		require.Equal(t, first.events[0].IncidentFingerprint, second.events[0].IncidentFingerprint)
	})
	t.Run("new text crossing alone always counts", func(t *testing.T) {
		markers := map[string]map[string]float64{"BUNDLE": {"PII": 0.99}, "NEW": {"PII": 0.95}}
		repo, _ := run(t, markers, InputModerationTask{Text: "NEW text", Instructions: []string{bundle}}, "PII")
		require.Empty(t, repo.events[0].IncidentFingerprint)
	})
	t.Run("category only explained by joint check counts", func(t *testing.T) {
		markers := map[string]map[string]float64{"BUNDLE": {"PII": 0.99}, "\n\nturn": {"Jailbreak": 0.99}}
		repo, _ := run(t, markers, InputModerationTask{Text: "turn", Instructions: []string{bundle}}, "PII", "Jailbreak")
		require.Empty(t, repo.events[0].IncidentFingerprint)
	})
	t.Run("A then A+B changes the fingerprint", func(t *testing.T) {
		markersA := map[string]map[string]float64{"BUNDLE": {"PII": 0.99}}
		a, _ := run(t, markersA, InputModerationTask{Text: "turn", Instructions: []string{bundle}}, "PII", "Jailbreak")
		markersAB := map[string]map[string]float64{"BUNDLE": {"PII": 0.99, "Jailbreak": 0.99}}
		ab, _ := run(t, markersAB, InputModerationTask{Text: "turn", Instructions: []string{bundle}}, "PII", "Jailbreak")
		require.NotEmpty(t, a.events[0].IncidentFingerprint)
		require.NotEmpty(t, ab.events[0].IncidentFingerprint)
		require.NotEqual(t, a.events[0].IncidentFingerprint, ab.events[0].IncidentFingerprint)
	})
	t.Run("no key means no fingerprint", func(t *testing.T) {
		client := &markerScoreClient{markers: map[string]map[string]float64{"BUNDLE": {"PII": 0.99}}}
		svc, repo := newEvidenceTestService(t, client, evidenceTestGroup(true, "PII"))
		svc.evidenceSealer = nil
		require.NoError(t, svc.process(InputModerationTask{RequestID: "r", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "turn", Instructions: []string{bundle}}))
		require.Empty(t, repo.events[0].IncidentFingerprint)
	})
	t.Run("exhausted budget for the text-alone check counts the strike", func(t *testing.T) {
		client := &markerScoreClient{markers: map[string]map[string]float64{"BUNDLE": {"PII": 0.99}}}
		svc, _ := newEvidenceTestService(t, client, evidenceTestGroup(true, "PII"))
		task := InputModerationTask{RequestID: "r", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "turn", Instructions: []string{bundle}}
		task.Diagnostics = newModerationDiagnostics(task, 32768)
		task.Diagnostics.DetectorCalls = task.Diagnostics.MaxDetectorCalls
		group := evidenceTestGroup(true, "PII")
		checks := []moderationCheckResult{
			{Check: moderationCheck{Index: 0, Text: "x", Sources: []InputModerationCheckSource{{Kind: moderationCheckInstructions, Bundle: 1}}}, Result: &InputModerationClassification{Scores: map[string]float64{"PII": 0.99}}},
		}
		rule := moderationDecisionRule{group: group, policy: group.InputModerationPolicy}
		scores := svc.moderationTextAloneScores(task, group, group.InputModerationPolicy, &InputModerationClassification{Scores: map[string]float64{"PII": 0.99}}, checks)
		require.Nil(t, scores)
		require.Equal(t, "detector_calls", task.Diagnostics.Limit)
		fingerprint := moderationIncidentFingerprint(svc.evidenceSealer, task, JevExpandedRulesVersion, checks, moderationCheckCrossings(checks, rule), []string{"PII"}, rule,
			func() (bool, bool) { return moderationTextAloneCrosses(scores, rule) })
		require.Empty(t, fingerprint)
	})
}

// hookScoreClient runs onText when the given text is classified.
type hookScoreClient struct {
	markerScoreClient
	text   string
	onText func()
}

func (c *hookScoreClient) ClassifyWithPolicy(ctx context.Context, text string, p *InputModerationPolicy) (*InputModerationClassification, error) {
	if text == c.text && c.onText != nil {
		c.onText()
	}
	return c.markerScoreClient.ClassifyWithPolicy(ctx, text, p)
}

func TestModerationTextAloneCheckPrecedesFinalReread(t *testing.T) {
	bundle := "BUNDLE rules " + strings.Repeat("r", 100)
	for _, tc := range []struct {
		name   string
		change func(*inputModerationGroupRepoStub, *inputModerationUserRepoStub)
		reason string
	}{
		{"moderation disabled during the check", func(g *inputModerationGroupRepoStub, _ *inputModerationUserRepoStub) {
			disabled := *g.groups[0]
			disabled.InputModerationEnabled = false
			g.groups = append(g.groups, &disabled)
		}, "group_disabled"},
		{"user trusted during the check", func(_ *inputModerationGroupRepoStub, u *inputModerationUserRepoStub) { u.user.IsTrusted = true }, "trusted_user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups := &inputModerationGroupRepoStub{groups: []*Group{evidenceTestGroup(true, "PII")}}
			users := &inputModerationUserRepoStub{user: &User{ID: 1, Status: StatusActive}}
			client := &hookScoreClient{markerScoreClient: markerScoreClient{markers: map[string]map[string]float64{"BUNDLE": {"PII": 0.99}}}, text: "turn"}
			client.onText = func() { tc.change(groups, users) }
			repo := &moderationResultRepoStub{}
			svc := newInputModerationServiceForTest(client, repo, groups, users, nil)
			svc.cfg.MaxInputChars = 32768
			svc.cfg.EvidenceRetentionDays = 90
			crypto, err := newModerationEvidenceCrypto(evidenceTestRoot, nil)
			require.NoError(t, err)
			svc.evidenceSealer = crypto
			defer svc.Stop()
			require.NoError(t, svc.process(InputModerationTask{RequestID: "r", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "turn", Instructions: []string{bundle}}))
			require.Contains(t, client.texts, "turn", "the text-alone check ran")
			require.Empty(t, repo.events, "no event, evidence or strike under the old policy")
			require.Equal(t, "skipped", repo.results[len(repo.results)-1].Status)
			require.Equal(t, tc.reason, repo.results[len(repo.results)-1].Reason)
		})
	}
}

// evidenceRepoStub records the call order of the admin evidence flow.
type evidenceRepoStub struct {
	record    *InputModerationEvidenceRecord
	accessErr error
	accesses  []InputModerationEvidenceAccess
	holds     int64
	accessAt  time.Time
}

func (r *evidenceRepoStub) LoadInputModerationEvidence(context.Context, int64) (*InputModerationEvidenceRecord, error) {
	return r.record, nil
}
func (r *evidenceRepoStub) RecordInputModerationEvidenceAccess(_ context.Context, access InputModerationEvidenceAccess) (time.Time, error) {
	r.accesses = append(r.accesses, access)
	if r.accessAt.IsZero() {
		return time.Now(), r.accessErr
	}
	return r.accessAt, r.accessErr
}
func (r *evidenceRepoStub) HoldInputModerationEvidence(_ context.Context, access InputModerationEvidenceAccess, _ time.Time) (int64, error) {
	r.accesses = append(r.accesses, access)
	return r.holds, nil
}
func (r *evidenceRepoStub) ReleaseInputModerationEvidence(_ context.Context, access InputModerationEvidenceAccess) (int64, error) {
	r.accesses = append(r.accesses, access)
	return r.holds, nil
}
func (r *evidenceRepoStub) ListHeldInputModerationEvidence(context.Context, int, int) ([]HeldInputModerationEvidence, int64, error) {
	return nil, 0, nil
}
func (r *evidenceRepoStub) PurgeExpiredInputModerationEvidence(context.Context, SQLExecer, int) (int64, error) {
	return 0, nil
}
func (r *evidenceRepoStub) PurgeInputModerationEvidenceAccess(context.Context, SQLExecer, time.Time, int) (int64, error) {
	return 0, nil
}

func sealedEvidenceRecord(t *testing.T, jobID string) *InputModerationEvidenceRecord {
	t.Helper()
	crypto, err := newModerationEvidenceCrypto(evidenceTestRoot, nil)
	require.NoError(t, err)
	aad := ModerationEvidenceAAD{JobID: jobID, UserID: 1, GroupID: 3, CheckIndex: 1, Kinds: []string{moderationCheckInstructions}}
	plaintext, _ := json.Marshal(moderationEvidencePlaintext{Text: "TEST_ONLY_EVIDENCE", Scores: map[string]float64{"PII": .99}, Categories: []string{"PII"},
		Truncated: true, OmittedCategories: []string{"violence"}})
	ciphertext, err := crypto.Seal(aad, plaintext)
	require.NoError(t, err)
	return &InputModerationEvidenceRecord{EventID: 5, JobID: jobID, Rows: []InputModerationEvidenceRow{{
		CheckIndex: 1, Kinds: aad.Kinds, AAD: aad, CharCount: 18, KeyID: crypto.KeyID(), Ciphertext: ciphertext, ExpiresAt: time.Now().Add(time.Hour),
	}}}
}

func newEvidenceAdminService(repo InputModerationEvidenceRepository) *InputModerationEvidenceService {
	crypto, _ := newModerationEvidenceCrypto(evidenceTestRoot, nil)
	return &InputModerationEvidenceService{repo: repo, crypto: crypto}
}

func TestModerationEvidenceViewAuditsBeforeDecrypt(t *testing.T) {
	jwt := InputModerationEvidenceActor{UserID: 7, AuthMethod: ModerationEvidenceAuthJWT}
	repo := &evidenceRepoStub{record: sealedEvidenceRecord(t, "job-1")}
	view, err := newEvidenceAdminService(repo).View(context.Background(), 5, jwt)
	require.NoError(t, err)
	require.Equal(t, "TEST_ONLY_EVIDENCE", view.Checks[0].Text)
	// Truncation travels inside the authenticated plaintext, not the result row.
	require.True(t, view.Truncated)
	require.Equal(t, []string{"violence"}, view.OmittedCategories)
	require.Equal(t, []InputModerationEvidenceAccess{{EventID: 5, AdminUserID: 7, AuthMethod: "jwt", Action: "view"}}, repo.accesses)

	// Audit failure: no text is returned.
	repo = &evidenceRepoStub{record: sealedEvidenceRecord(t, "job-1"), accessErr: errors.New("TEST_ONLY audit down")}
	view, err = newEvidenceAdminService(repo).View(context.Background(), 5, jwt)
	require.Error(t, err)
	require.Nil(t, view)

	// Shared admin API key cannot read, and nothing is loaded or audited.
	repo = &evidenceRepoStub{record: sealedEvidenceRecord(t, "job-1")}
	_, err = newEvidenceAdminService(repo).View(context.Background(), 5, InputModerationEvidenceActor{UserID: 7, AuthMethod: "admin_api_key"})
	require.ErrorIs(t, err, ErrModerationEvidenceForbidden)
	require.Empty(t, repo.accesses)

	// Expired, unheld evidence is not loaded by the repository: not found, no audit.
	repo = &evidenceRepoStub{}
	_, err = newEvidenceAdminService(repo).View(context.Background(), 5, jwt)
	require.ErrorIs(t, err, ErrModerationEvidenceNotFound)
	require.Empty(t, repo.accesses)

	// Evidence that expired while the audit row committed is not returned.
	repo = &evidenceRepoStub{record: sealedEvidenceRecord(t, "job-1"), accessAt: time.Now().Add(2 * time.Hour)}
	_, err = newEvidenceAdminService(repo).View(context.Background(), 5, jwt)
	require.ErrorIs(t, err, ErrModerationEvidenceNotFound)
	require.Len(t, repo.accesses, 1)

	// A row moved to another event (different job) is rejected.
	record := sealedEvidenceRecord(t, "job-1")
	record.JobID = "job-2"
	_, err = newEvidenceAdminService(&evidenceRepoStub{record: record}).View(context.Background(), 5, jwt)
	require.ErrorIs(t, err, ErrModerationEvidenceUndecryptable)
}

func TestModerationEvidenceHoldValidation(t *testing.T) {
	jwt := InputModerationEvidenceActor{UserID: 7, AuthMethod: ModerationEvidenceAuthJWT}
	repo := &evidenceRepoStub{holds: 1}
	svc := newEvidenceAdminService(repo)
	for _, days := range []int{0, 366} {
		_, err := svc.Hold(context.Background(), 5, jwt, days, "case")
		require.ErrorIs(t, err, ErrModerationEvidenceInvalidHold)
	}
	_, err := svc.Hold(context.Background(), 5, jwt, 30, " ")
	require.ErrorIs(t, err, ErrModerationEvidenceInvalidHold)
	_, err = svc.Hold(context.Background(), 5, jwt, 30, "case 42")
	require.NoError(t, err)
	repo.holds = 0
	require.ErrorIs(t, svc.Release(context.Background(), 5, jwt, ""), ErrModerationEvidenceNotFound)
}

// connCheckingEvidenceRepo fails unless cleanup runs on the lock connection.
type connCheckingEvidenceRepo struct {
	evidenceRepoStub
	calls int
}

func (r *connCheckingEvidenceRepo) PurgeExpiredInputModerationEvidence(ctx context.Context, db SQLExecer, _ int) (int64, error) {
	r.calls++
	if _, ok := db.(*sql.Conn); !ok {
		return 0, errors.New("cleanup must use the lock connection")
	}
	_, err := db.ExecContext(ctx, "DELETE evidence")
	return 0, err
}
func (r *connCheckingEvidenceRepo) PurgeInputModerationEvidenceAccess(ctx context.Context, db SQLExecer, _ time.Time, _ int) (int64, error) {
	r.calls++
	_, err := db.ExecContext(ctx, "DELETE access")
	return 0, err
}

func TestModerationEvidenceCleanupUsesLockConnectionWithOnePooledConn(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("DELETE evidence").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE access").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SELECT pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 0))
	repo := &connCheckingEvidenceRepo{}
	done := make(chan struct{})
	go func() { NewModerationEvidenceCleanupService(db, repo, time.Hour).runOnce(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup blocked waiting for a second connection")
	}
	require.Equal(t, 2, repo.calls)
	require.NoError(t, mock.ExpectationsWereMet())
}
