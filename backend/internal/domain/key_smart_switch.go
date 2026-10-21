package domain

// KeySmartSwitchPolicy is an ordered, opt-in routing policy. Version is managed
// by the server and used to reject stale edits.
type KeySmartSwitchPolicy struct {
	PaygBudgetUSD *float64 `json:"payg_budget_usd"`
	Enabled       bool     `json:"enabled"`
	GroupIDs      []int64  `json:"group_ids"`
	PaygConfirmed bool     `json:"payg_confirmed"`
	Version       int64    `json:"version"`
}

func (p *KeySmartSwitchPolicy) Clone() *KeySmartSwitchPolicy {
	if p == nil {
		return nil
	}
	copy := *p
	if p.PaygBudgetUSD != nil {
		budget := *p.PaygBudgetUSD
		copy.PaygBudgetUSD = &budget
	}
	copy.GroupIDs = append([]int64{}, p.GroupIDs...)
	return &copy
}

// KeySmartSwitchTrace contains no request bodies, credentials, or upstream errors.
type KeySmartSwitchTrace struct {
	OriginalGroupID int64  `json:"original_group_id"`
	PolicyVersion   int64  `json:"policy_version"`
	Reason          string `json:"reason"`
}
