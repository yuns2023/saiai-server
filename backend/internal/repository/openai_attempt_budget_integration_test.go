//go:build integration

package repository

import "testing"

func TestOpenAIProviderAttemptBudgetStoreIntegration(t *testing.T) {
	testOpenAIProviderAttemptBudgetStore(t, testRedis(t))
}
