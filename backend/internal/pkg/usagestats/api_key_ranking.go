package usagestats

// SpendingRankingSort selects the order of the admin user / API Key rankings.
// Values are whitelisted; repositories map them to fixed ORDER BY clauses.
type SpendingRankingSort string

const (
	// SpendingRankingSortCost is the default: standard total_cost, unchanged.
	SpendingRankingSortCost SpendingRankingSort = "cost"
	// SpendingRankingSortRequestBodyBytes ranks by total inbound request bytes.
	SpendingRankingSortRequestBodyBytes SpendingRankingSort = "request_body_bytes"
)

// ParseSpendingRankingSort accepts "" (default cost order) or a known value.
func ParseSpendingRankingSort(raw string) (SpendingRankingSort, bool) {
	switch SpendingRankingSort(raw) {
	case "", SpendingRankingSortCost:
		return SpendingRankingSortCost, true
	case SpendingRankingSortRequestBodyBytes:
		return SpendingRankingSortRequestBodyBytes, true
	default:
		return "", false
	}
}

// RequestBodySizeStats aggregates usage_logs.request_body_bytes for a ranking
// row. Rows with an unknown (NULL) size are excluded from every field: Total
// is 0 and Avg/Max are null when no row in the range has a known size. Avg is
// the mean over known sizes rounded to whole bytes (ROUND(AVG(...))).
type RequestBodySizeStats struct {
	RequestBodyBytesTotal      int64  `json:"request_body_bytes_total"`
	RequestBodyBytesAvg        *int64 `json:"request_body_bytes_avg"`
	RequestBodyBytesMax        *int64 `json:"request_body_bytes_max"`
	RequestBodyBytesKnownCount int64  `json:"request_body_bytes_known_count"`
}

// APIKeySpendingRankingItem identifies a Key without exposing its secret value.
type APIKeySpendingRankingItem struct {
	APIKeyID            int64   `json:"api_key_id"`
	KeyName             string  `json:"key_name"`
	UserID              int64   `json:"user_id"`
	Email               string  `json:"email"`
	Cost                float64 `json:"cost"` // standard total_cost; the ranking basis
	ActualCost          float64 `json:"actual_cost"`
	Requests            int64   `json:"requests"`
	HostedRequests      int64   `json:"hosted_requests"`
	Tokens              int64   `json:"tokens"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	RequestBodySizeStats
	SubscriptionRevenueStats
}

// APIKeySpendingRankingResponse totals cover all Keys in the selected scope,
// before the ranking limit is applied.
type APIKeySpendingRankingResponse struct {
	Ranking         []APIKeySpendingRankingItem `json:"ranking"`
	TotalCost       float64                     `json:"total_cost"`
	TotalActualCost float64                     `json:"total_actual_cost"`
	TotalRequests   int64                       `json:"total_requests"`
	TotalTokens     int64                       `json:"total_tokens"`
	TotalSubscriptionRevenueStats
}
