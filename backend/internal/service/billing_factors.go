package service

// applyUserBillingFactors keeps the existing group/user rate in RateMultiplier
// and applies optional model and user factors only to the user-facing cost.
// Subscription window usage and account cost continue to use TotalCost.
func applyUserBillingFactors(cost *CostBreakdown, group *Group, user *User, billedModel string, subscription bool) (modelRate, userDiscount float64) {
	modelRate = group.ModelRateFor(billedModel)
	userDiscount = 1
	if !subscription {
		userDiscount = user.PaygDiscountRate()
	}
	if cost != nil {
		cost.ActualCost *= modelRate * userDiscount
	}
	return modelRate, userDiscount
}
