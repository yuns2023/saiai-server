package service

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"reflect"
	"sort"
	"strings"
)

const JevLegacyRulesVersion = "jev-v2"
const JevExpandedRulesVersion = "jev-expanded-v1"

type InputModerationPolicy = domain.InputModerationPolicy

//go:embed jev_category_registry.json
var jevCategoryData []byte

type JevCategory struct {
	DescriptionZH      string `json:"description_zh"`
	DescriptionEN      string `json:"description_en"`
	ID                 string `json:"id"`
	LabelZH            string `json:"label_zh"`
	LabelEN            string `json:"label_en"`
	Legacy             bool   `json:"legacy"`
	EnforcementAllowed bool   `json:"enforcement_allowed"`
	Rule               string `json:"rule,omitempty"`
}

func jevCategories() []JevCategory {
	var categories []JevCategory
	if err := json.Unmarshal(jevCategoryData, &categories); err != nil {
		panic("invalid bundled Jev category registry")
	}
	return categories
}
func JevCategoryCatalog() []JevCategory {
	categories := jevCategories()
	for i := range categories {
		categories[i].Rule = ""
	}
	return categories
}
func ValidateInputModerationPolicy(p *InputModerationPolicy) error {
	if p == nil {
		return nil
	}
	if p.RulesVersion != JevLegacyRulesVersion && p.RulesVersion != JevExpandedRulesVersion {
		return fmt.Errorf("unsupported moderation rules version")
	}
	catalog := map[string]JevCategory{}
	for _, c := range jevCategories() {
		catalog[c.ID] = c
	}
	audit := map[string]bool{}
	for _, id := range p.AuditCategories {
		c, ok := catalog[id]
		if !ok || (p.RulesVersion == JevLegacyRulesVersion && !c.Legacy) || audit[id] {
			return fmt.Errorf("unknown, duplicate or unsupported audit category")
		}
		audit[id] = true
	}
	enforced := map[string]bool{}
	for _, id := range p.EnforcementCategories {
		c, ok := catalog[id]
		if !ok || !audit[id] || !c.EnforcementAllowed || enforced[id] {
			return fmt.Errorf("enforcement category must be audited and approved for enforcement")
		}
		enforced[id] = true
	}
	for id, threshold := range p.Thresholds {
		if !audit[id] || threshold < 1 || threshold > 100 {
			return fmt.Errorf("category threshold must reference an audited category and be between 1 and 100")
		}
	}
	return nil
}
func EffectiveInputModerationPolicy(g *Group) *InputModerationPolicy {
	if g.InputModerationPolicy != nil {
		b, _ := json.Marshal(g.InputModerationPolicy)
		var p InputModerationPolicy
		_ = json.Unmarshal(b, &p)
		return &p
	}
	p := &InputModerationPolicy{RulesVersion: JevLegacyRulesVersion, Thresholds: map[string]int{}}
	for _, c := range jevCategories() {
		if c.Legacy {
			p.AuditCategories = append(p.AuditCategories, c.ID)
			if moderationCategoriesMatch(g.InputModerationCategories, []string{c.ID}) {
				p.EnforcementCategories = append(p.EnforcementCategories, c.ID)
			}
		}
	}
	return p
}
func JevRulesForPolicy(p *InputModerationPolicy) (map[string]string, error) {
	if err := ValidateInputModerationPolicy(p); err != nil {
		return nil, err
	}
	rules := map[string]string{}
	for _, c := range jevCategories() {
		if p == nil {
			if c.Legacy {
				rules[c.ID] = c.Rule
			}
			continue
		}
		for _, id := range p.AuditCategories {
			if c.ID == id {
				rules[id] = c.Rule
			}
		}
	}
	return rules, nil
}
func sameModerationDetection(a, b *InputModerationPolicy) bool {
	if a.RulesVersion != b.RulesVersion {
		return false
	}
	x := append([]string{}, a.AuditCategories...)
	y := append([]string{}, b.AuditCategories...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}
func moderationPolicyMatches(selected, actual []string) bool {
	for _, a := range actual {
		for _, s := range selected {
			if a == s {
				return true
			}
		}
	}
	return false
}
func moderationPolicyThreshold(p *InputModerationPolicy, category string, fallback int) int {
	if v, ok := p.Thresholds[category]; ok {
		return v
	}
	return fallback
}

type InputModerationPolicyClient interface {
	ClassifyWithPolicy(context.Context, string, *InputModerationPolicy) (*InputModerationClassification, error)
}

type ModerationDecisionSnapshot struct {
	CooldownMinutes   int                    `json:"cooldown_minutes"`
	DisableAfterHits  int                    `json:"disable_after_hits"`
	StrikeWindowHours int                    `json:"strike_window_hours"`
	DedupeMinutes     int                    `json:"dedupe_minutes"`
	Policy            *InputModerationPolicy `json:"policy"`
	AutoEnforce       bool                   `json:"auto_enforce"`
	ActionMode        string                 `json:"action_mode"`
	GroupUpdatedAt    string                 `json:"group_updated_at"`
}

func moderationDecisionSnapshot(g *Group) *ModerationDecisionSnapshot {
	p := EffectiveInputModerationPolicy(g)
	if p.Thresholds == nil {
		p.Thresholds = map[string]int{}
	}
	for _, id := range p.AuditCategories {
		if _, ok := p.Thresholds[id]; !ok {
			p.Thresholds[id] = g.InputModerationMinConfidence
		}
	}
	return &ModerationDecisionSnapshot{CooldownMinutes: g.InputModerationCooldownMinutes, DisableAfterHits: g.InputModerationDisableAfterHits, StrikeWindowHours: g.InputModerationStrikeWindowHours, DedupeMinutes: g.InputModerationDedupeMinutes, Policy: p, AutoEnforce: g.InputModerationAutoDisableUser, ActionMode: g.InputModerationActionMode, GroupUpdatedAt: g.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")}
}

func ValidateLegacyModerationCategories(categories []string) error {
	for _, id := range categories {
		found := false
		for _, c := range jevCategories() {
			if c.Legacy && strings.EqualFold(strings.TrimSpace(id), c.ID) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unsupported legacy Jev category; use a versioned policy")
		}
	}
	return nil
}
func sameLegacyModerationCategories(a, b []string) bool {
	x := normalizeInputModerationCategories(a)
	y := normalizeInputModerationCategories(b)
	for i := range x {
		x[i] = strings.ToLower(x[i])
	}
	for i := range y {
		y[i] = strings.ToLower(y[i])
	}
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}
