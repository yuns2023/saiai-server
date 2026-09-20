package service

import "fmt"

const accountBlockedModelPatternsExtraKey = "blocked_model_patterns"

// NormalizeAccountBlockedModelPatternsExtra validates and normalizes the
// optional account-level model denylist stored in account.extra. Keeping this
// setting in extra avoids coupling a scheduling-only policy to credentials.
func NormalizeAccountBlockedModelPatternsExtra(extra map[string]any) error {
	if extra == nil {
		return nil
	}
	raw, ok := extra[accountBlockedModelPatternsExtraKey]
	if !ok {
		return nil
	}

	patterns, err := accountBlockedModelPatternsFromRaw(raw)
	if err != nil {
		return err
	}
	normalized, err := NormalizeBlockedModelPatterns(patterns)
	if err != nil {
		return err
	}
	extra[accountBlockedModelPatternsExtraKey] = normalized
	return nil
}

func accountBlockedModelPatternsFromRaw(raw any) ([]string, error) {
	switch value := raw.(type) {
	case nil:
		return []string{}, nil
	case []string:
		return append([]string(nil), value...), nil
	case []any:
		patterns := make([]string, 0, len(value))
		for _, item := range value {
			pattern, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("blocked_model_patterns must contain only strings")
			}
			patterns = append(patterns, pattern)
		}
		return patterns, nil
	default:
		return nil, fmt.Errorf("blocked_model_patterns must be an array of strings")
	}
}

func (a *Account) blockedModelPatterns() []string {
	if a == nil || a.Extra == nil {
		return nil
	}
	patterns, err := accountBlockedModelPatternsFromRaw(a.Extra[accountBlockedModelPatternsExtraKey])
	if err != nil {
		return nil
	}
	normalized, err := NormalizeBlockedModelPatterns(patterns)
	if err != nil {
		return nil
	}
	return normalized
}

// IsModelBlocked reports whether an account-level denylist blocks either the
// requested model or the account's mapped target. Alias normalization is
// handled by isModelBlockedByPatterns.
func (a *Account) IsModelBlocked(requestedModel string) bool {
	patterns := a.blockedModelPatterns()
	if len(patterns) == 0 {
		return false
	}
	if isModelBlockedByPatterns(patterns, requestedModel) {
		return true
	}
	mappedModel, matched := a.ResolveMappedModel(requestedModel)
	return matched && mappedModel != requestedModel && isModelBlockedByPatterns(patterns, mappedModel)
}
