package domain

// InputModerationPolicy is optional: nil preserves legacy four-category semantics.
type InputModerationPolicy struct {
	RulesVersion          string         `json:"rules_version"`
	AuditCategories       []string       `json:"audit_categories"`
	EnforcementCategories []string       `json:"enforcement_categories"`
	Thresholds            map[string]int `json:"thresholds"`
	// InputScope selects what is submitted to the detector: "user_input"
	// (default when empty) or "full" (also transmitted project instructions).
	InputScope string `json:"input_scope,omitempty"`
}
