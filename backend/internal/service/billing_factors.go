package service

// applyUserBillingFactors keeps the existing group/user rate in RateMultiplier
// and applies optional model and user factors only to the user-facing cost.
// Balance charges and subscription window usage both use ActualCost.
// Account cost continues to use TotalCost.
func applyUserBillingFactors(cost *CostBreakdown, group *Group, user *User, billedModel string) (modelRate, userDiscount float64) {
	modelRate = group.ModelRateFor(billedModel)
	userDiscount = user.PaygDiscountRate()
	if cost != nil {
		cost.ActualCost *= modelRate * userDiscount
	}
	return modelRate, userDiscount
}
