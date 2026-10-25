package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
)

// OpenAIQuotaResetService uses Codex's control plane, never a model request.
type OpenAIQuotaResetService struct {
	accountRepo AccountRepository
	proxyRepo   ProxyRepository
	tokens      *OpenAITokenProvider
	rateLimits  *RateLimitService
	baseURL     string
	// codexTransport applies the opt-in Codex TLS identity to /wham calls.
	codexTransport codexControlTransport
}

// SetCodexTransport enables the opt-in Codex TLS identity for /wham requests.
func (s *OpenAIQuotaResetService) SetCodexTransport(cfg *config.Config, upstream HTTPUpstream) {
	if s != nil {
		s.codexTransport = codexControlTransport{cfg: cfg, upstream: upstream}
	}
}

func NewOpenAIQuotaResetService(accounts AccountRepository, proxies ProxyRepository, tokens *OpenAITokenProvider, rateLimits *RateLimitService) *OpenAIQuotaResetService {
	return &OpenAIQuotaResetService{accountRepo: accounts, proxyRepo: proxies, tokens: tokens, rateLimits: rateLimits, baseURL: "https://chatgpt.com/backend-api/wham"}
}

type OpenAIResetCredit struct {
	ID          string  `json:"id"`
	ResetType   string  `json:"reset_type"`
	Status      string  `json:"status"`
	GrantedAt   string  `json:"granted_at"`
	ExpiresAt   *string `json:"expires_at"`
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
}

type OpenAIResetCredits struct {
	AvailableCount int                 `json:"available_count"`
	Credits        []OpenAIResetCredit `json:"credits"`
}

type OpenAIResetRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required,max=128"`
	CreditID       string `json:"credit_id" binding:"required,max=256"`
}

type OpenAIResetResult struct {
	Outcome      string `json:"outcome"`
	WindowsReset int    `json:"windows_reset"`
	QuotaSynced  bool   `json:"quota_synced"`
}

func resetError(status int, code, message string) error {
	return infraerrors.New(status, code, message)
}

func (s *OpenAIQuotaResetService) request(ctx context.Context, account *Account, method, path string, body any, target any) error {
	token, err := s.tokens.GetAccessToken(ctx, account)
	if err != nil || strings.TrimSpace(token) == "" {
		return resetError(http.StatusBadGateway, "CODEX_RESET_AUTH", "Unable to obtain Codex authentication; refresh or reauthorize the account")
	}
	proxyURL := ""
	if account.ProxyID != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID)
		if err != nil || proxy == nil {
			return resetError(http.StatusBadGateway, "CODEX_RESET_PROXY", "Unable to load the account proxy")
		}
		proxyURL = proxy.URL()
	}
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ChatGPT-Account-Id", account.GetChatGPTAccountID())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codexCLIUserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	var resp *http.Response
	if policy := s.codexTransport.policyFor(account, req); policy.Enabled() {
		// Same no-redirect semantics as the legacy client; the 10s budget
		// moves to the request context because the pool has no client timeout.
		reqCtx, cancel := context.WithTimeout(WithNativeCodexRedirectPolicy(req.Context()), 10*time.Second)
		defer cancel()
		resp, err = s.codexTransport.do(req.WithContext(reqCtx), proxyURL, account, policy)
	} else {
		pooled, clientErr := httpclient.GetClient(httpclient.Options{ProxyURL: proxyURL, Timeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second})
		if clientErr != nil {
			return resetError(http.StatusBadGateway, "CODEX_RESET_PROXY", "Unable to connect through the account proxy")
		}
		client := *pooled
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err = client.Do(req)
	}
	if err != nil {
		return resetError(http.StatusBadGateway, "CODEX_RESET_UNCERTAIN", "Codex control request failed; retry a reset with the same request ID")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Never expose provider bodies, which may contain account or credential data.
		return resetError(http.StatusBadGateway, "CODEX_RESET_UPSTREAM", "Codex rejected the control request; refresh authentication or retry with the same request ID")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || json.Unmarshal(data, target) != nil {
		return resetError(http.StatusBadGateway, "CODEX_RESET_RESPONSE", "Invalid Codex control response; retry a reset with the same request ID")
	}
	return nil
}

func (s *OpenAIQuotaResetService) account(ctx context.Context, id int64) (*Account, error) {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if account == nil || !account.IsOpenAIOAuth() || strings.TrimSpace(account.GetChatGPTAccountID()) == "" {
		return nil, resetError(http.StatusBadRequest, "CODEX_RESET_UNSUPPORTED", "Usage limit resets require an OpenAI OAuth account with a ChatGPT account ID")
	}
	return account, nil
}

func (s *OpenAIQuotaResetService) List(ctx context.Context, id int64) (*OpenAIResetCredits, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	account, err := s.account(ctx, id)
	if err != nil {
		return nil, err
	}
	var details struct {
		AvailableCount *int                 `json:"available_count"`
		Credits        *[]OpenAIResetCredit `json:"credits"`
	}
	if err := s.request(ctx, account, http.MethodGet, "/rate-limit-reset-credits", nil, &details); err != nil {
		return nil, err
	}
	if details.AvailableCount == nil || *details.AvailableCount < 0 || details.Credits == nil {
		return nil, resetError(http.StatusBadGateway, "CODEX_RESET_RESPONSE", "Codex did not return reset credit details")
	}
	return &OpenAIResetCredits{AvailableCount: *details.AvailableCount, Credits: *details.Credits}, nil
}

func (s *OpenAIQuotaResetService) Consume(ctx context.Context, id int64, input OpenAIResetRequest) (*OpenAIResetResult, error) {
	if strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 128 || strings.TrimSpace(input.CreditID) == "" || len(input.CreditID) > 256 {
		return nil, resetError(http.StatusBadRequest, "CODEX_RESET_INPUT", "A credit ID and idempotency key are required")
	}
	account, err := s.account(ctx, id)
	if err != nil {
		return nil, err
	}
	// Finish reconciliation even if the browser disconnects during consumption.
	operationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
	defer cancel()
	var upstream struct {
		Code         string `json:"code"`
		WindowsReset int    `json:"windows_reset"`
	}
	body := map[string]string{"redeem_request_id": input.IdempotencyKey, "credit_id": input.CreditID}
	if err := s.request(operationCtx, account, http.MethodPost, "/rate-limit-reset-credits/consume", body, &upstream); err != nil {
		return nil, err
	}
	switch upstream.Code {
	case "reset", "already_redeemed", "nothing_to_reset", "no_credit":
	default:
		return nil, resetError(http.StatusBadGateway, "CODEX_RESET_RESPONSE", "Unknown Codex reset outcome; retry with the same request ID")
	}
	if upstream.WindowsReset < 0 {
		return nil, resetError(http.StatusBadGateway, "CODEX_RESET_RESPONSE", "Incomplete Codex reset outcome; retry with the same request ID")
	}
	// Official Codex defaults omitted windows_reset to zero.
	result := &OpenAIResetResult{Outcome: upstream.Code, WindowsReset: upstream.WindowsReset}
	if upstream.Code != "no_credit" {
		// A failed refresh must not turn a consumed credit into an API failure that
		// encourages consumption of another credit. Preserve the definitive outcome.
		if err := s.syncUsage(operationCtx, account); err == nil {
			result.QuotaSynced = true
		} else {
			slog.Warn("codex_reset_usage_sync_failed", "account_id", account.ID, "outcome", upstream.Code)
		}
	}
	return result, nil
}

type codexControlWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowSeconds *int     `json:"limit_window_seconds"`
	ResetAt       *int64   `json:"reset_at"`
}

// codexWhamUsage is the subset of GET /wham/usage the gateway consumes.
type codexWhamUsage struct {
	RateLimit *struct {
		Allowed      *bool               `json:"allowed"`
		LimitReached *bool               `json:"limit_reached"`
		Primary      *codexControlWindow `json:"primary_window"`
		Secondary    *codexControlWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

// codexWhamUsageSnapshot validates the /wham/usage windows and converts them
// into the usage snapshot shape shared with response headers. It also returns
// the latest reset time of an exhausted window (zero if none). Reset
// reconciliation requires both windows; the background usage refresh accepts
// a missing (null) window, like partial Codex rate-limit headers, as long as
// at least one valid window is present.
func codexWhamUsageSnapshot(usage *codexWhamUsage, now time.Time, requireBoth bool) (*OpenAICodexUsageSnapshot, time.Time, error) {
	if usage == nil || usage.RateLimit == nil {
		return nil, time.Time{}, resetError(http.StatusBadGateway, "CODEX_USAGE_MISSING", "Codex usage windows are unavailable")
	}
	limit := usage.RateLimit
	snapshot := &OpenAICodexUsageSnapshot{UpdatedAt: now.Format(time.RFC3339), ObservedAt: now}
	windows := []*codexControlWindow{limit.Primary, limit.Secondary}
	seen := map[int]bool{}
	var exhaustedUntil time.Time
	present := 0
	for i, window := range windows {
		if window == nil && !requireBoth {
			continue
		}
		present++
		if window == nil || window.UsedPercent == nil || window.WindowSeconds == nil || window.ResetAt == nil ||
			math.IsNaN(*window.UsedPercent) || math.IsInf(*window.UsedPercent, 0) || *window.UsedPercent < 0 || *window.UsedPercent > 100 ||
			(*window.WindowSeconds != 18000 && *window.WindowSeconds != 604800) || seen[*window.WindowSeconds] || *window.ResetAt <= now.Unix() || *window.ResetAt > now.Add(366*24*time.Hour).Unix() {
			return nil, time.Time{}, resetError(http.StatusBadGateway, "CODEX_USAGE_MISSING", "Codex returned incomplete usage windows")
		}
		seen[*window.WindowSeconds] = true
		minutes := *window.WindowSeconds / 60
		seconds := int(*window.ResetAt - now.Unix())
		if i == 0 {
			snapshot.PrimaryUsedPercent, snapshot.PrimaryWindowMinutes, snapshot.PrimaryResetAfterSeconds = window.UsedPercent, &minutes, &seconds
		} else {
			snapshot.SecondaryUsedPercent, snapshot.SecondaryWindowMinutes, snapshot.SecondaryResetAfterSeconds = window.UsedPercent, &minutes, &seconds
		}
		if *window.UsedPercent >= 100 && time.Unix(*window.ResetAt, 0).After(exhaustedUntil) {
			exhaustedUntil = time.Unix(*window.ResetAt, 0)
		}
	}
	if present == 0 {
		return nil, time.Time{}, resetError(http.StatusBadGateway, "CODEX_USAGE_MISSING", "Codex usage windows are unavailable")
	}
	return snapshot, exhaustedUntil, nil
}

func (s *OpenAIQuotaResetService) syncUsage(ctx context.Context, account *Account) error {
	// Drain in-flight writes before fetching authoritative usage. Observations
	// received after this boundary may legitimately describe later traffic.
	state, release, err := s.rateLimits.lockCodexUsageWrite(ctx, account.ID)
	if err != nil {
		return err
	}
	defer release()
	boundary := time.Now()
	var usage codexWhamUsage
	if err := s.request(ctx, account, http.MethodGet, "/usage", nil, &usage); err != nil {
		return err
	}
	now := time.Now().UTC()
	snapshot, exhaustedUntil, err := codexWhamUsageSnapshot(&usage, now, true)
	if err != nil {
		return err
	}
	limit := usage.RateLimit
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, buildCodexUsageExtraUpdates(snapshot, now)); err != nil {
		return err
	}
	if state != nil {
		state.resetAt.Store(&boundary)
	}
	if limit.Allowed != nil && *limit.Allowed && limit.LimitReached != nil && !*limit.LimitReached {
		if err := s.accountRepo.ClearRateLimit(ctx, account.ID); err != nil {
			return err
		}
		if s.rateLimits != nil {
			s.rateLimits.clearOpenAINoReset429Through(account.ID, boundary)
		}
	} else if !exhaustedUntil.IsZero() {
		if err := s.accountRepo.SetRateLimited(ctx, account.ID, exhaustedUntil); err != nil {
			return err
		}
	}
	return nil
}
