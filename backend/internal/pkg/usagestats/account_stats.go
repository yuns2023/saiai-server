package usagestats

// AccountStats 账号使用统计
//
// cost: 账号口径费用（使用 total_cost * account_rate_multiplier）
// standard_cost: 标准费用（使用 total_cost，不含倍率）
// user_cost: 用户/API Key 口径费用（使用 actual_cost，受分组倍率影响）
// hosted_requests/hosted_cost: 托管分组请求数与折算用量（SUM(metered_cost)）；托管行 cost/user_cost 为 0
type AccountStats struct {
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	StandardCost float64 `json:"standard_cost"`
	UserCost     float64 `json:"user_cost"`

	HostedRequests int64   `json:"hosted_requests"`
	HostedCost     float64 `json:"hosted_cost"`

	// SubscriptionRevenue: 订阅折算收入（元）= Σ 订阅行 actual_cost × 分组当前 subscription_revenue_rate；
	// SubscriptionRevenueUnpricedCost: 分组未配置倍率（或无分组）的订阅消耗（$），未计入上面的折算。
	SubscriptionRevenue             float64 `json:"subscription_revenue"`
	SubscriptionRevenueUnpricedCost float64 `json:"subscription_revenue_unpriced_cost"`
}
