package domain

// InputModerationPolicy is optional: nil preserves legacy four-category semantics.
type InputModerationPolicy struct {
	RulesVersion          string         `json:"rules_version"`
	AuditCategories       []string       `json:"audit_categories"`
	EnforcementCategories []string       `json:"enforcement_categories"`
	Thresholds            map[string]int `json:"thresholds"`
}
