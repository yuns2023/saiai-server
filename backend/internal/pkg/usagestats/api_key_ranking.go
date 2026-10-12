package usagestats

// APIKeySpendingRankingItem identifies a Key without exposing its secret value.
type APIKeySpendingRankingItem struct {
	APIKeyID            int64   `json:"api_key_id"`
	KeyName             string  `json:"key_name"`
	UserID              int64   `json:"user_id"`
	Email               string  `json:"email"`
	ActualCost          float64 `json:"actual_cost"`
	Requests            int64   `json:"requests"`
	Tokens              int64   `json:"tokens"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
}

// APIKeySpendingRankingResponse totals cover all Keys in the selected scope,
// before the ranking limit is applied.
type APIKeySpendingRankingResponse struct {
	Ranking         []APIKeySpendingRankingItem `json:"ranking"`
	TotalActualCost float64                     `json:"total_actual_cost"`
	TotalRequests   int64                       `json:"total_requests"`
	TotalTokens     int64                       `json:"total_tokens"`
}
