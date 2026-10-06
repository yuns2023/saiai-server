//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func opsLogFilterLocalRule() OpsLogFilterRule {
	return OpsLogFilterRule{
		ID: "local-auth", Name: "Inactive user", Enabled: true,
		Source: "local_auth", Reason: OpsLocalAuthUserInactive, MatchMode: "all",
	}
}

func opsLogFilterUpstreamRule() OpsLogFilterRule {
	return OpsLogFilterRule{
		ID: "upstream-error", Name: "Provider overload", Enabled: true,
		Source: "upstream", StatusCodes: []int{503}, Keywords: []string{"overloaded"}, MatchMode: "all",
	}
}

func newOpsLogFilterTestService(test *testing.T, rules []OpsLogFilterRule) (*OpsService, *runtimeSettingRepoStub) {
	test.Helper()
	repo := newRuntimeSettingRepoStub()
	opsService := &OpsService{settingRepo: repo}
	config, err := opsService.GetOpsLogFilters(context.Background())
	require.NoError(test, err)
	_, err = opsService.UpdateOpsLogFilters(context.Background(), &OpsLogFilterUpdate{Rules: rules, Revision: config.Revision})
	require.NoError(test, err)
	return opsService, repo
}

func TestOpsLogFilters_DefaultsWithoutWrites(test *testing.T) {
	for _, stored := range []string{"missing", "[]", "null"} {
		test.Run(stored, func(test *testing.T) {
			repo := newRuntimeSettingRepoStub()
			repo.values["ops_advanced_settings"] = `{"retention_days":30}`
			if stored != "missing" {
				repo.values[SettingKeyOpsLogFilters] = stored
			}
			opsService := &OpsService{settingRepo: repo}
			config, err := opsService.GetOpsLogFilters(context.Background())
			require.NoError(test, err)
			require.NotNil(test, config.Rules)
			require.Empty(test, config.Rules)
			require.NotNil(test, config.Counts)
			require.Empty(test, config.Counts)
			require.Equal(test, "process", config.CountScope)
			digest := sha256.Sum256([]byte("[]"))
			require.Equal(test, hex.EncodeToString(digest[:]), config.Revision)
			require.Zero(test, repo.setCalls)
			require.Equal(test, `{"retention_days":30}`, repo.values["ops_advanced_settings"])
			require.False(test, opsService.FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{{Source: "local_auth", Reason: OpsLocalAuthUserInactive, StatusCode: 401}}))
		})
	}
	config, err := (&OpsService{}).GetOpsLogFilters(context.Background())
	require.NoError(test, err)
	require.NotNil(test, config.Rules)
	require.Empty(test, config.Rules)
}

func TestOpsLogFilters_UpdatePersistsIndependentArrayAndConflicts(test *testing.T) {
	repo := newRuntimeSettingRepoStub()
	repo.values["ops_advanced_settings"] = `{"enabled":true}`
	opsService := &OpsService{settingRepo: repo}
	initial, err := opsService.GetOpsLogFilters(context.Background())
	require.NoError(test, err)
	rules := []OpsLogFilterRule{opsLogFilterLocalRule(), opsLogFilterUpstreamRule()}
	updated, err := opsService.UpdateOpsLogFilters(context.Background(), &OpsLogFilterUpdate{Rules: rules, Revision: initial.Revision})
	require.NoError(test, err)
	require.Equal(test, rules, updated.Rules)
	require.NotEqual(test, initial.Revision, updated.Revision)
	require.Equal(test, map[string]uint64{"local-auth": 0, "upstream-error": 0}, updated.Counts)
	require.Equal(test, "process", updated.CountScope)
	var stored []OpsLogFilterRule
	require.NoError(test, json.Unmarshal([]byte(repo.values[SettingKeyOpsLogFilters]), &stored))
	require.Equal(test, rules, stored)
	require.Equal(test, `{"enabled":true}`, repo.values["ops_advanced_settings"])
	require.Equal(test, 1, repo.setCalls)

	_, err = opsService.UpdateOpsLogFilters(context.Background(), &OpsLogFilterUpdate{Rules: nil, Revision: initial.Revision})
	require.Equal(test, http.StatusConflict, infraerrors.Code(err))
	require.Equal(test, "OPS_LOG_FILTER_CONFLICT", infraerrors.Reason(err))
	require.Equal(test, 1, repo.setCalls)
	reloaded, err := opsService.GetOpsLogFilters(context.Background())
	require.NoError(test, err)
	require.Equal(test, updated, reloaded)

	cleared, err := opsService.UpdateOpsLogFilters(context.Background(), &OpsLogFilterUpdate{Revision: updated.Revision})
	require.NoError(test, err)
	require.Equal(test, "[]", repo.values[SettingKeyOpsLogFilters])
	require.NotNil(test, cleared.Rules)
	require.Empty(test, cleared.Rules)
	require.Empty(test, cleared.Counts)
	require.Equal(test, initial.Revision, cleared.Revision)
}

func TestOpsLogFilters_UpdatePreconditions(test *testing.T) {
	opsService := &OpsService{settingRepo: newRuntimeSettingRepoStub()}
	for _, update := range []*OpsLogFilterUpdate{nil, {Rules: []OpsLogFilterRule{opsLogFilterLocalRule()}}} {
		_, err := opsService.UpdateOpsLogFilters(context.Background(), update)
		require.Equal(test, http.StatusBadRequest, infraerrors.Code(err))
		require.Equal(test, "OPS_LOG_FILTER_REVISION_REQUIRED", infraerrors.Reason(err))
	}
	var nilService *OpsService
	for _, unavailable := range []*OpsService{nilService, {}} {
		_, err := unavailable.UpdateOpsLogFilters(context.Background(), &OpsLogFilterUpdate{Revision: "revision"})
		require.Error(test, err)
	}
}

func TestOpsLogFilters_RejectInvalidRulesWithoutWriting(test *testing.T) {
	cases := []struct {
		name   string
		mutate func(*OpsLogFilterRule)
	}{
		{name: "empty ID", mutate: func(rule *OpsLogFilterRule) { rule.ID = "" }},
		{name: "long ID", mutate: func(rule *OpsLogFilterRule) { rule.ID = strings.Repeat("a", 81) }},
		{name: "non ASCII ID", mutate: func(rule *OpsLogFilterRule) { rule.ID = "规则" }},
		{name: "underscore ID", mutate: func(rule *OpsLogFilterRule) { rule.ID = "rule_id" }},
		{name: "space ID", mutate: func(rule *OpsLogFilterRule) { rule.ID = "rule id" }},
		{name: "blank name", mutate: func(rule *OpsLogFilterRule) { rule.Name = " \t" }},
		{name: "long name", mutate: func(rule *OpsLogFilterRule) { rule.Name = strings.Repeat("界", 121) }},
		{name: "unknown match mode", mutate: func(rule *OpsLogFilterRule) { rule.MatchMode = "unknown" }},
		{name: "missing match mode", mutate: func(rule *OpsLogFilterRule) { rule.MatchMode = "" }},
		{name: "unknown source", mutate: func(rule *OpsLogFilterRule) { rule.Source = "client_request" }},
		{name: "missing local reason", mutate: func(rule *OpsLogFilterRule) { rule.Reason = "" }},
		{name: "unknown local reason", mutate: func(rule *OpsLogFilterRule) { rule.Reason = "user_not_found" }},
		{name: "wire code as reason", mutate: func(rule *OpsLogFilterRule) { rule.Reason = "USER_INACTIVE" }},
		{name: "invalid platform", mutate: func(rule *OpsLogFilterRule) { rule.Platform = "unknown" }},
		{name: "zero group", mutate: func(rule *OpsLogFilterRule) { groupID := int64(0); rule.GroupID = &groupID }},
		{name: "negative group", mutate: func(rule *OpsLogFilterRule) { groupID := int64(-1); rule.GroupID = &groupID }},
		{name: "invalid key cannot identify group", mutate: func(rule *OpsLogFilterRule) {
			groupID := int64(42)
			rule.Reason, rule.GroupID = OpsLocalAuthInvalidAPIKey, &groupID
		}},
		{name: "missing key cannot identify group", mutate: func(rule *OpsLogFilterRule) {
			groupID := int64(42)
			rule.Reason, rule.GroupID = OpsLocalAuthAPIKeyRequired, &groupID
		}},
		{name: "low status", mutate: func(rule *OpsLogFilterRule) { rule.StatusCodes = []int{399} }},
		{name: "high status", mutate: func(rule *OpsLogFilterRule) { rule.StatusCodes = []int{600} }},
		{name: "empty keyword", mutate: func(rule *OpsLogFilterRule) { rule.Keywords = []string{""} }},
		{name: "whitespace keyword", mutate: func(rule *OpsLogFilterRule) { rule.Keywords = []string{" \t"} }},
		{name: "long keyword", mutate: func(rule *OpsLogFilterRule) { rule.Keywords = []string{strings.Repeat("界", 257)} }},
		{name: "too many keywords", mutate: func(rule *OpsLogFilterRule) {
			rule.Keywords = make([]string, 11)
			for keywordIndex := range rule.Keywords {
				rule.Keywords[keywordIndex] = "error"
			}
		}},
		{name: "too many statuses", mutate: func(rule *OpsLogFilterRule) {
			rule.StatusCodes = make([]int, 21)
			for statusIndex := range rule.StatusCodes {
				rule.StatusCodes[statusIndex] = 401
			}
		}},
		{name: "upstream status only", mutate: func(rule *OpsLogFilterRule) { *rule = opsLogFilterUpstreamRule(); rule.Keywords = nil }},
		{name: "upstream keyword only", mutate: func(rule *OpsLogFilterRule) { *rule = opsLogFilterUpstreamRule(); rule.StatusCodes = nil }},
		{name: "upstream any", mutate: func(rule *OpsLogFilterRule) { *rule = opsLogFilterUpstreamRule(); rule.MatchMode = "any" }},
		{name: "upstream local reason", mutate: func(rule *OpsLogFilterRule) {
			*rule = opsLogFilterUpstreamRule()
			rule.Reason = OpsLocalAuthInvalidAPIKey
		}},
	}
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			repo := newRuntimeSettingRepoStub()
			opsService := &OpsService{settingRepo: repo}
			config, err := opsService.GetOpsLogFilters(context.Background())
			require.NoError(test, err)
			rule := opsLogFilterLocalRule()
			scenario.mutate(&rule)
			_, err = opsService.UpdateOpsLogFilters(context.Background(), &OpsLogFilterUpdate{Rules: []OpsLogFilterRule{rule}, Revision: config.Revision})
			require.Equal(test, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(test, "OPS_LOG_FILTER_INVALID", infraerrors.Reason(err))
			require.Zero(test, repo.setCalls)
			require.NotContains(test, repo.values, SettingKeyOpsLogFilters)
		})
	}
	for _, ruleCount := range []int{2, 51} {
		test.Run(fmt.Sprintf("invalid rule collection %d", ruleCount), func(test *testing.T) {
			repo := newRuntimeSettingRepoStub()
			opsService := &OpsService{settingRepo: repo}
			config, err := opsService.GetOpsLogFilters(context.Background())
			require.NoError(test, err)
			rules := make([]OpsLogFilterRule, ruleCount)
			for ruleIndex := range rules {
				rules[ruleIndex] = opsLogFilterLocalRule()
				if ruleCount > 2 {
					rules[ruleIndex].ID = fmt.Sprintf("rule-%d", ruleIndex)
				}
			}
			_, err = opsService.UpdateOpsLogFilters(context.Background(), &OpsLogFilterUpdate{Rules: rules, Revision: config.Revision})
			require.Equal(test, http.StatusBadRequest, infraerrors.Code(err))
			require.Zero(test, repo.setCalls)
		})
	}
}

func TestOpsLogFilters_AcceptRuleBoundaries(test *testing.T) {
	rules := make([]OpsLogFilterRule, 50)
	reasons := []string{OpsLocalAuthUserInactive, OpsLocalAuthInvalidAPIKey, OpsLocalAuthAPIKeyRequired}
	platforms := []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity}
	groupID := int64(1)
	for ruleIndex := range rules {
		rules[ruleIndex] = opsLogFilterLocalRule()
		rules[ruleIndex].ID = fmt.Sprintf("rule-%d", ruleIndex)
		rules[ruleIndex].Reason = reasons[ruleIndex%len(reasons)]
		rules[ruleIndex].Platform = platforms[ruleIndex%len(platforms)]
		rules[ruleIndex].GroupID = &groupID
		if rules[ruleIndex].Reason != OpsLocalAuthUserInactive {
			rules[ruleIndex].GroupID = nil
		}
	}
	rules[0].ID = strings.Repeat("A", 79) + "9"
	rules[0].Name = strings.Repeat("界", 120)
	rules[0].MatchMode = "any"
	rules[0].StatusCodes = []int{400, 599}
	rules[0].Keywords = []string{strings.Repeat("界", 256)}
	rules[1].ID = "1"
	_, repo := newOpsLogFilterTestService(test, rules)
	var stored []OpsLogFilterRule
	require.NoError(test, json.Unmarshal([]byte(repo.values[SettingKeyOpsLogFilters]), &stored))
	require.Equal(test, rules, stored)
}

func TestOpsLogFilters_MatchingGuardsAndOptionalConditions(test *testing.T) {
	groupID := int64(42)
	otherGroupID := int64(43)
	cases := []struct {
		name   string
		mutate func(*OpsLogFilterRule, *OpsLogFilterEvent)
		drop   bool
	}{
		{name: "reason only", drop: true},
		{name: "disabled", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) { rule.Enabled = false }},
		{name: "different local reason", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) { event.Reason = OpsLocalAuthInvalidAPIKey }},
		{name: "missing server reason", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			event.Reason = ""
			event.Message = `{"code":"USER_INACTIVE"}`
		}},
		{name: "wire reason is not provenance", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) { event.Reason = "USER_INACTIVE" }},
		{name: "missing source", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) { event.Source = "" }},
		{name: "upstream 401 is not local", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) { event.Source = "upstream" }},
		{name: "success is not an error", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) { event.StatusCode = 200 }},
		{name: "out of range status", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) { event.StatusCode = 600 }},
		{name: "matching scope", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.Platform = PlatformAnthropic
			rule.GroupID = &groupID
		}, drop: true},
		{name: "status only matches", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.MatchMode = "any"
			rule.StatusCodes = []int{401}
		}, drop: true},
		{name: "status only misses", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.MatchMode = "any"
			rule.StatusCodes = []int{403}
		}},
		{name: "keyword only matches", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.MatchMode = "any"
			rule.Keywords = []string{"not active"}
		}, drop: true},
		{name: "keyword only misses", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.MatchMode = "any"
			rule.Keywords = []string{"missing"}
		}},
		{name: "all both match", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.StatusCodes = []int{403, 401}
			rule.Keywords = []string{"unrelated", " NOT ACTIVE "}
		}, drop: true},
		{name: "all status misses", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.StatusCodes = []int{403}
			rule.Keywords = []string{"not active"}
		}},
		{name: "all keyword misses", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.StatusCodes = []int{401}
			rule.Keywords = []string{"missing"}
		}},
		{name: "any status matches", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.MatchMode = "any"
			rule.StatusCodes = []int{401}
			rule.Keywords = []string{"missing"}
		}, drop: true},
		{name: "any keyword matches", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.MatchMode = "any"
			rule.StatusCodes = []int{403}
			rule.Keywords = []string{"not active"}
		}, drop: true},
		{name: "any neither matches", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.MatchMode = "any"
			rule.StatusCodes = []int{403}
			rule.Keywords = []string{"missing"}
		}},
		{name: "any still requires platform", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.MatchMode = "any"
			rule.StatusCodes = []int{401}
			rule.Keywords = []string{"not active"}
			rule.Platform = PlatformOpenAI
		}},
		{name: "any still requires group", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) {
			rule.MatchMode = "any"
			rule.StatusCodes = []int{401}
			rule.Keywords = []string{"not active"}
			rule.GroupID = &otherGroupID
		}},
		{name: "scoped rule needs event group", mutate: func(rule *OpsLogFilterRule, event *OpsLogFilterEvent) { rule.GroupID = &groupID; event.GroupID = nil }},
	}
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			rule := opsLogFilterLocalRule()
			event := OpsLogFilterEvent{Source: "local_auth", Reason: OpsLocalAuthUserInactive, Platform: PlatformAnthropic, GroupID: &groupID, StatusCode: 401, Message: "User account is NOT ACTIVE"}
			if scenario.mutate != nil {
				scenario.mutate(&rule, &event)
			}
			opsService, _ := newOpsLogFilterTestService(test, []OpsLogFilterRule{rule})
			require.Equal(test, scenario.drop, opsService.FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{event}))
			config, err := opsService.GetOpsLogFilters(context.Background())
			require.NoError(test, err)
			var count uint64
			if scenario.drop {
				count = 1
			}
			require.Equal(test, count, config.Counts[rule.ID])
		})
	}
}

func TestOpsLogFilters_UpstreamRequiresStatusKeywordAndSameRuleForAllAttempts(test *testing.T) {
	groupID := int64(42)
	overloadRule := opsLogFilterUpstreamRule()
	overloadRule.Platform = PlatformAnthropic
	overloadRule.GroupID = &groupID
	unauthorizedRule := opsLogFilterUpstreamRule()
	unauthorizedRule.ID = "upstream-auth"
	unauthorizedRule.StatusCodes = []int{401}
	unauthorizedRule.Keywords = []string{"invalid api key"}
	overload := OpsLogFilterEvent{Source: "upstream", Platform: PlatformAnthropic, GroupID: &groupID, StatusCode: 503, Message: "Provider OVERLOADED"}
	unauthorized := OpsLogFilterEvent{Source: "upstream", Platform: PlatformAnthropic, GroupID: &groupID, StatusCode: 401, Message: "Invalid API key"}
	statusOnly := overload
	statusOnly.Message = "connection closed"
	keywordOnly := overload
	keywordOnly.StatusCode = 502
	wrongPlatform := overload
	wrongPlatform.Platform = PlatformOpenAI
	wrongGroup := overload
	wrongGroup.GroupID = nil
	cases := []struct {
		name    string
		events  []OpsLogFilterEvent
		drop    bool
		countID string
	}{
		{name: "both predicates", events: []OpsLogFilterEvent{overload}, drop: true, countID: overloadRule.ID},
		{name: "status without keyword", events: []OpsLogFilterEvent{statusOnly}},
		{name: "keyword without status", events: []OpsLogFilterEvent{keywordOnly}},
		{name: "wrong platform", events: []OpsLogFilterEvent{wrongPlatform}},
		{name: "missing group", events: []OpsLogFilterEvent{wrongGroup}},
		{name: "all retries same rule", events: []OpsLogFilterEvent{overload, overload, overload}, drop: true, countID: overloadRule.ID},
		{name: "different rules cannot cover record", events: []OpsLogFilterEvent{overload, unauthorized}},
		{name: "unmatched retry keeps record", events: []OpsLogFilterEvent{overload, statusOnly}},
		{name: "upstream unauthorized matches upstream rule", events: []OpsLogFilterEvent{unauthorized}, drop: true, countID: unauthorizedRule.ID},
		{name: "empty attempts", events: nil},
	}
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			opsService, _ := newOpsLogFilterTestService(test, []OpsLogFilterRule{overloadRule, unauthorizedRule})
			require.Equal(test, scenario.drop, opsService.FilterOpsLogEvents(context.Background(), scenario.events))
			config, err := opsService.GetOpsLogFilters(context.Background())
			require.NoError(test, err)
			for _, rule := range []OpsLogFilterRule{overloadRule, unauthorizedRule} {
				var expected uint64
				if rule.ID == scenario.countID {
					expected = 1
				}
				require.Equal(test, expected, config.Counts[rule.ID])
			}
		})
	}
}

func TestOpsLogFilters_CountsArePerRecordFirstRuleAndProcessLocal(test *testing.T) {
	first := opsLogFilterUpstreamRule()
	second := opsLogFilterUpstreamRule()
	second.ID = "second-rule"
	opsService, repo := newOpsLogFilterTestService(test, []OpsLogFilterRule{first, second})
	event := OpsLogFilterEvent{Source: "upstream", StatusCode: 503, Message: "overloaded"}
	require.True(test, opsService.FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{event, event}))
	require.True(test, opsService.FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{event}))
	config, err := opsService.GetOpsLogFilters(context.Background())
	require.NoError(test, err)
	require.Equal(test, map[string]uint64{first.ID: 2, second.ID: 0}, config.Counts)
	config.Counts[first.ID] = 99
	config, err = opsService.GetOpsLogFilters(context.Background())
	require.NoError(test, err)
	require.Equal(test, uint64(2), config.Counts[first.ID])
	anotherProcess := &OpsService{settingRepo: repo}
	otherConfig, err := anotherProcess.GetOpsLogFilters(context.Background())
	require.NoError(test, err)
	require.Equal(test, config.Revision, otherConfig.Revision)
	require.Equal(test, map[string]uint64{first.ID: 0, second.ID: 0}, otherConfig.Counts)
	require.Equal(test, "process", otherConfig.CountScope)
	updated, err := opsService.UpdateOpsLogFilters(context.Background(), &OpsLogFilterUpdate{Rules: []OpsLogFilterRule{first}, Revision: config.Revision})
	require.NoError(test, err)
	require.Equal(test, map[string]uint64{first.ID: 2}, updated.Counts)
}

func TestOpsLogFilters_BusyLockFailsOpenPromptly(test *testing.T) {
	rule := opsLogFilterUpstreamRule()
	opsService, _ := newOpsLogFilterTestService(test, []OpsLogFilterRule{rule})
	events := []OpsLogFilterEvent{{Source: "upstream", StatusCode: 503, Message: "overloaded"}}
	opsService.logFilterState.mu.Lock()
	completed := make(chan bool, 1)
	go func() {
		completed <- opsService.FilterOpsLogEvents(context.Background(), events)
	}()
	select {
	case filtered := <-completed:
		opsService.logFilterState.mu.Unlock()
		require.False(test, filtered)
	case <-time.After(500 * time.Millisecond):
		opsService.logFilterState.mu.Unlock()
		test.Fatal("runtime filtering blocked behind the configuration lock")
	}
	config, err := opsService.GetOpsLogFilters(context.Background())
	require.NoError(test, err)
	require.Zero(test, config.Counts[rule.ID])
	require.True(test, opsService.FilterOpsLogEvents(context.Background(), events))
}

func TestOpsLogFilters_ReloadPrunesProcessCounts(test *testing.T) {
	rule := opsLogFilterUpstreamRule()
	opsService, repo := newOpsLogFilterTestService(test, []OpsLogFilterRule{rule})
	events := []OpsLogFilterEvent{{Source: "upstream", StatusCode: 503, Message: "overloaded"}}
	for ruleIndex := range 55 {
		rule.ID = fmt.Sprintf("reloaded-rule-%d", ruleIndex)
		stored, err := json.Marshal([]OpsLogFilterRule{rule})
		require.NoError(test, err)
		repo.values[SettingKeyOpsLogFilters] = string(stored)
		opsService.logFilterState.expiresAt = time.Now().Add(-time.Second)
		require.True(test, opsService.FilterOpsLogEvents(context.Background(), events))
		require.Equal(test, map[string]uint64{rule.ID: 1}, opsService.logFilterState.counts)
	}
}

func TestOpsLogFilters_FailOpenAfterCacheExpiryWithoutReusingOldRules(test *testing.T) {
	cases := []struct {
		name      string
		stored    string
		loadError error
	}{
		{name: "malformed JSON", stored: "{"},
		{name: "wrong JSON shape", stored: "{}"},
		{name: "invalid stored rule", stored: `[{"id":"bad","name":"Bad rule","enabled":true,"source":"upstream","status_codes":[503],"match_mode":"all"}]`},
		{name: "setting load error", loadError: errors.New("mock setting read failure")},
	}
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			rule := opsLogFilterUpstreamRule()
			opsService, repo := newOpsLogFilterTestService(test, []OpsLogFilterRule{rule})
			events := []OpsLogFilterEvent{{Source: "upstream", StatusCode: 503, Message: "overloaded"}}
			require.True(test, opsService.FilterOpsLogEvents(context.Background(), events))
			validStored := repo.values[SettingKeyOpsLogFilters]
			repo.values[SettingKeyOpsLogFilters] = scenario.stored
			if scenario.loadError != nil {
				repo.getValueFn = func(key string) (string, error) { return "", scenario.loadError }
			}
			uncachedService := &OpsService{settingRepo: repo}
			require.False(test, uncachedService.FilterOpsLogEvents(context.Background(), events))
			require.False(test, uncachedService.FilterOpsLogEvents(context.Background(), events))
			opsService.logFilterState.expiresAt = time.Now().Add(-time.Second)
			require.False(test, opsService.FilterOpsLogEvents(context.Background(), events))
			require.False(test, opsService.FilterOpsLogEvents(context.Background(), events))
			require.Equal(test, uint64(1), opsService.logFilterState.counts[rule.ID])
			_, err := opsService.GetOpsLogFilters(context.Background())
			require.Error(test, err)
			repo.getValueFn = nil
			repo.values[SettingKeyOpsLogFilters] = validStored
			opsService.logFilterState.expiresAt = time.Now().Add(-time.Second)
			require.True(test, opsService.FilterOpsLogEvents(context.Background(), events))
			config, err := opsService.GetOpsLogFilters(context.Background())
			require.NoError(test, err)
			require.Equal(test, uint64(2), config.Counts[rule.ID])
		})
	}
	var nilService *OpsService
	require.False(test, nilService.FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{{StatusCode: 401}}))
	require.False(test, (&OpsService{}).FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{{StatusCode: 401}}))
}

func TestOpsLogFilters_PersistFailureDoesNotChangeActiveRules(test *testing.T) {
	rule := opsLogFilterLocalRule()
	opsService, repo := newOpsLogFilterTestService(test, []OpsLogFilterRule{rule})
	before, err := opsService.GetOpsLogFilters(context.Background())
	require.NoError(test, err)
	writeError := errors.New("mock setting write failure")
	repo.setFn = func(key, value string) error { return writeError }
	_, err = opsService.UpdateOpsLogFilters(context.Background(), &OpsLogFilterUpdate{Rules: []OpsLogFilterRule{opsLogFilterUpstreamRule()}, Revision: before.Revision})
	require.ErrorIs(test, err, writeError)
	after, err := opsService.GetOpsLogFilters(context.Background())
	require.NoError(test, err)
	require.Equal(test, before, after)
	require.Equal(test, 1, repo.setCalls)
	require.True(test, opsService.FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{{Source: "local_auth", Reason: OpsLocalAuthUserInactive, StatusCode: 401}}))
	require.False(test, opsService.FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{{Source: "upstream", StatusCode: 503, Message: "overloaded"}}))
}

func TestOpsLocalAuth_MarkersAreServerContextOnly(test *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, reason := range []string{OpsLocalAuthUserInactive, OpsLocalAuthInvalidAPIKey, OpsLocalAuthAPIKeyRequired} {
		test.Run(reason, func(test *testing.T) {
			requestContext, _ := gin.CreateTestContext(httptest.NewRecorder())
			requestContext.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			requestContext.Request.Header.Set("ops_local_auth_reject_reason", reason)
			require.Empty(test, GetOpsLocalAuthRejectReason(requestContext))
			MarkOpsLocalAuthRejected(requestContext, reason)
			require.Equal(test, reason, GetOpsLocalAuthRejectReason(requestContext))
			require.Equal(test, "gateway_auth_"+reason, OpsLocalAuthErrorType(reason))
		})
	}
	for _, reason := range []string{"", "unknown", "USER_INACTIVE", "gateway_auth_user_inactive"} {
		requestContext, _ := gin.CreateTestContext(httptest.NewRecorder())
		MarkOpsLocalAuthRejected(requestContext, reason)
		require.Empty(test, GetOpsLocalAuthRejectReason(requestContext))
		requestContext.Set("ops_local_auth_reject_reason", reason)
		require.Empty(test, GetOpsLocalAuthRejectReason(requestContext))
		require.Equal(test, "authentication_error", OpsLocalAuthErrorType(reason))
	}
	require.NotPanics(test, func() { MarkOpsLocalAuthRejected(nil, OpsLocalAuthUserInactive) })
	require.Empty(test, GetOpsLocalAuthRejectReason(nil))
}

func TestBuildOpsLogFilterProposal_LocalProvenanceAndLegacyCandidates(test *testing.T) {
	groupID := int64(42)
	cases := []struct {
		name      string
		errorType string
		phase     string
		source    string
		body      string
		reason    string
		verified  bool
	}{
		{name: "verified inactive user", errorType: "gateway_auth_user_inactive", phase: "auth", source: "client_request", reason: OpsLocalAuthUserInactive, verified: true},
		{name: "verified invalid key", errorType: "gateway_auth_invalid_api_key", phase: "auth", source: "client_request", reason: OpsLocalAuthInvalidAPIKey, verified: true},
		{name: "verified missing key", errorType: "gateway_auth_api_key_required", phase: "auth", source: "client_request", reason: OpsLocalAuthAPIKeyRequired, verified: true},
		{name: "legacy inactive user", errorType: "authentication_error", phase: "auth", source: "client_request", body: `{"code":"USER_INACTIVE"}`, reason: OpsLocalAuthUserInactive},
		{name: "legacy invalid key", errorType: "authentication_error", phase: "auth", source: "client_request", body: `{"code":"INVALID_API_KEY"}`, reason: OpsLocalAuthInvalidAPIKey},
		{name: "legacy missing key", body: `{"code":"API_KEY_REQUIRED"}`, reason: OpsLocalAuthAPIKeyRequired},
		{name: "wrong marker phase requires confirmation", errorType: "gateway_auth_user_inactive", phase: "request", source: "client_request", body: `{"code":"USER_INACTIVE"}`, reason: OpsLocalAuthUserInactive},
		{name: "wrong marker source requires confirmation", errorType: "gateway_auth_invalid_api_key", phase: "auth", source: "gateway", body: `{"code":"INVALID_API_KEY"}`, reason: OpsLocalAuthInvalidAPIKey},
		{name: "wrong marker phase without legacy body", errorType: "gateway_auth_user_inactive", phase: "request", source: "client_request"},
		{name: "unsupported marker", errorType: "gateway_auth_unknown", phase: "auth", source: "client_request"},
		{name: "generic authentication error", errorType: "authentication_error", phase: "auth", source: "client_request", body: `{"error":{"message":"Invalid API key"}}`},
		{name: "malformed legacy body", body: "{"},
	}
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			detail := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{ID: 17, StatusCode: 401, Type: scenario.errorType, Phase: scenario.phase, Source: scenario.source, Platform: PlatformAnthropic, GroupID: &groupID}, ErrorBody: scenario.body}
			proposal := BuildOpsLogFilterProposal(detail)
			require.NotNil(test, proposal)
			require.Equal(test, scenario.verified, proposal.Verified)
			if scenario.reason == "" {
				require.Nil(test, proposal.Rule)
				return
			}
			require.NotNil(test, proposal.Rule)
			require.Equal(test, "error-17", proposal.Rule.ID)
			require.Equal(test, "local_auth", proposal.Rule.Source)
			require.Equal(test, scenario.reason, proposal.Rule.Reason)
			require.Equal(test, PlatformAnthropic, proposal.Rule.Platform)
			if scenario.reason == OpsLocalAuthUserInactive {
				require.Equal(test, &groupID, proposal.Rule.GroupID)
			} else {
				require.Nil(test, proposal.Rule.GroupID)
			}
			require.False(test, proposal.Rule.Enabled)
			require.False(test, proposal.RequiresGlobalConfirmation)
			require.NoError(test, validateOpsLogFilterRules([]OpsLogFilterRule{*proposal.Rule}))
			opsService, _ := newOpsLogFilterTestService(test, []OpsLogFilterRule{*proposal.Rule})
			event := OpsLogFilterEvent{Source: "local_auth", Reason: scenario.reason, StatusCode: 401, Platform: PlatformAnthropic, GroupID: &groupID}
			require.False(test, opsService.FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{event}))
			configuration, err := opsService.GetOpsLogFilters(context.Background())
			require.NoError(test, err)
			confirmedRule := *proposal.Rule
			confirmedRule.Enabled = true
			_, err = opsService.UpdateOpsLogFilters(context.Background(), &OpsLogFilterUpdate{Rules: []OpsLogFilterRule{confirmedRule}, Revision: configuration.Revision})
			require.NoError(test, err)
			event.Reason = ""
			event.Message = scenario.body
			require.False(test, opsService.FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{event}))
			event.Reason = scenario.reason
			require.True(test, opsService.FilterOpsLogEvents(context.Background(), []OpsLogFilterEvent{event}))
		})
	}
}

func TestBuildOpsLogFilterProposal_UpstreamEvidenceNeverCreatesLocalRule(test *testing.T) {
	accountID := int64(12)
	upstreamStatus := 401
	cases := []struct {
		name     string
		mutate   func(*OpsErrorLogDetail)
		verified bool
	}{
		{name: "selected provider account", mutate: func(detail *OpsErrorLogDetail) { detail.AccountID = &accountID }},
		{name: "upstream HTTP status", mutate: func(detail *OpsErrorLogDetail) {
			detail.UpstreamStatusCode = &upstreamStatus
			detail.UpstreamErrorMessage = "Invalid API key"
		}, verified: true},
		{name: "upstream message", mutate: func(detail *OpsErrorLogDetail) { detail.UpstreamErrorMessage = "Invalid API key" }},
		{name: "upstream detail", mutate: func(detail *OpsErrorLogDetail) { detail.UpstreamErrorDetail = "provider rejected credentials" }},
		{name: "recorded retry attempts", mutate: func(detail *OpsErrorLogDetail) {
			detail.UpstreamErrors = `[{"upstream_status_code":401,"message":"Invalid API key"}]`
		}, verified: true},
		{name: "malformed attempts", mutate: func(detail *OpsErrorLogDetail) { detail.UpstreamErrors = "{" }},
		{name: "wrong attempt shape", mutate: func(detail *OpsErrorLogDetail) { detail.UpstreamErrors = "{}" }},
		{name: "earlier valid status but unknown last attempt", mutate: func(detail *OpsErrorLogDetail) {
			detail.UpstreamErrors = `[{"upstream_status_code":401},{"kind":"request_error"}]`
		}},
		{name: "earlier valid status but successful last attempt", mutate: func(detail *OpsErrorLogDetail) {
			detail.UpstreamErrors = `[{"upstream_status_code":401},{"upstream_status_code":200}]`
		}},
		{name: "upstream source", mutate: func(detail *OpsErrorLogDetail) { detail.Source = "upstream_http" }},
		{name: "upstream phase", mutate: func(detail *OpsErrorLogDetail) { detail.Phase = "upstream" }},
	}
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			detail := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{ID: 18, StatusCode: 401, Phase: "auth", Source: "client_request", Type: "gateway_auth_invalid_api_key", Message: "Invalid API key"}, ErrorBody: `{"code":"INVALID_API_KEY"}`}
			scenario.mutate(detail)
			proposal := BuildOpsLogFilterProposal(detail)
			require.NotNil(test, proposal.Rule)
			require.Equal(test, "upstream", proposal.Rule.Source)
			require.Empty(test, proposal.Rule.Reason)
			require.Equal(test, []int{401}, proposal.Rule.StatusCodes)
			require.Equal(test, []string{"Invalid API key"}, proposal.Rule.Keywords)
			require.Equal(test, scenario.verified, proposal.Verified)
			require.False(test, proposal.Rule.Enabled)
			require.NoError(test, validateOpsLogFilterRules([]OpsLogFilterRule{*proposal.Rule}))
		})
	}
}

func TestBuildOpsLogFilterProposal_SanitizesAndBoundsUpstreamKeyword(test *testing.T) {
	upstreamStatus := 503
	cases := []struct {
		name      string
		message   string
		forbidden string
		expected  string
	}{
		{name: "redacts mock credentials", message: "overloaded access_token=mock-sensitive-value", forbidden: "mock-sensitive-value", expected: "overloaded access_token=***"},
		{name: "provider neutral", message: "ReClaude client state unavailable", forbidden: "ReClaude", expected: DeviceAuthorizationUnavailableClientMessage},
		{name: "bounded valid UTF8", message: strings.Repeat("界", 300)},
	}
	for _, scenario := range cases {
		test.Run(scenario.name, func(test *testing.T) {
			detail := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{ID: 19, StatusCode: 502, Message: "fallback message"}, UpstreamStatusCode: &upstreamStatus, UpstreamErrorMessage: scenario.message}
			proposal := BuildOpsLogFilterProposal(detail)
			require.NotNil(test, proposal.Rule)
			require.Equal(test, []int{503}, proposal.Rule.StatusCodes)
			require.Len(test, proposal.Rule.Keywords, 1)
			keyword := proposal.Rule.Keywords[0]
			require.True(test, utf8.ValidString(keyword))
			require.LessOrEqual(test, len(keyword), 256)
			if scenario.forbidden != "" {
				require.NotContains(test, keyword, scenario.forbidden)
			}
			if scenario.expected != "" {
				require.Equal(test, scenario.expected, keyword)
			}
		})
	}
}

func TestBuildOpsLogFilterProposal_UsesSameUpstreamEvidenceForRuleAndVerification(test *testing.T) {
	detail := &OpsErrorLogDetail{
		OpsErrorLog:    OpsErrorLog{ID: 9, StatusCode: 502, Message: "Upstream request failed"},
		UpstreamErrors: `[{"upstream_status_code":401,"message":"Invalid API key"}]`,
	}
	proposal := BuildOpsLogFilterProposal(detail)
	require.True(test, proposal.Verified)
	require.NotNil(test, proposal.Rule)
	require.Equal(test, []int{401}, proposal.Rule.StatusCodes)
	require.Equal(test, []string{"Invalid API key"}, proposal.Rule.Keywords)
}

func TestBuildOpsLogFilterProposal_DoesNotBorrowClientMessageForUpstreamStatus(test *testing.T) {
	upstreamStatus := 401
	for _, detail := range []*OpsErrorLogDetail{
		{
			OpsErrorLog:        OpsErrorLog{ID: 9, StatusCode: 502, Message: "Upstream request failed"},
			UpstreamStatusCode: &upstreamStatus,
		},
		{
			OpsErrorLog:    OpsErrorLog{ID: 9, StatusCode: 502, Message: "Upstream request failed"},
			UpstreamErrors: `[{"upstream_status_code":401}]`,
		},
	} {
		proposal := BuildOpsLogFilterProposal(detail)
		require.Nil(test, proposal.Rule)
	}
}

func TestBuildOpsLogFilterProposal_GlobalConfirmationAndUnsupportedDetails(test *testing.T) {
	groupID := int64(42)
	for _, scope := range []struct {
		name     string
		platform string
		groupID  *int64
		global   bool
	}{
		{name: "global", global: true},
		{name: "platform", platform: PlatformAnthropic},
		{name: "group", groupID: &groupID},
		{name: "platform and group", platform: PlatformAnthropic, groupID: &groupID},
	} {
		test.Run(scope.name, func(test *testing.T) {
			detail := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{ID: 20, StatusCode: 401, Type: "gateway_auth_user_inactive", Phase: "auth", Source: "client_request", Platform: scope.platform, GroupID: scope.groupID}, UpstreamErrors: "[]"}
			proposal := BuildOpsLogFilterProposal(detail)
			require.NotNil(test, proposal.Rule)
			require.True(test, proposal.Verified)
			require.Equal(test, scope.global, proposal.RequiresGlobalConfirmation)
		})
	}
	for _, detail := range []*OpsErrorLogDetail{
		nil,
		{OpsErrorLog: OpsErrorLog{ID: 21, StatusCode: 500, Message: "generic internal failure"}},
		{OpsErrorLog: OpsErrorLog{ID: 21, StatusCode: 401, Message: "Invalid API key"}},
		{OpsErrorLog: OpsErrorLog{ID: 21, StatusCode: 200, Phase: "upstream", Message: "not an error"}},
		{OpsErrorLog: OpsErrorLog{ID: 21, StatusCode: 503, Phase: "upstream", Message: " \t"}},
	} {
		proposal := BuildOpsLogFilterProposal(detail)
		require.NotNil(test, proposal)
		require.Nil(test, proposal.Rule)
		require.False(test, proposal.Verified)
		require.False(test, proposal.RequiresGlobalConfirmation)
	}
}
