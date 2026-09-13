package service

// applyUserBillingFactors keeps the existing group/user rate in RateMultiplier
// and applies optional model and account factors only to the user-facing cost.
// Subscription window usage and account cost continue to use TotalCost.
func applyUserBillingFactors(cost *CostBreakdown, group *Group, account *Account, billedModel string, subscription bool) (modelRate, accountDiscount float64) {
	modelRate = group.ModelRateFor(billedModel)
	accountDiscount = 1
	if !subscription {
		accountDiscount = account.PaygDiscountRate()
	}
	if cost != nil {
		cost.ActualCost *= modelRate * accountDiscount
	}
	return modelRate, accountDiscount
}
