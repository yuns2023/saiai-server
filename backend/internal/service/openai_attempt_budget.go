package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

var (
	ErrOpenAIProviderAttemptBudgetExhausted          = errors.New("OpenAI acceptance attempt budget exhausted")
	ErrOpenAIProviderAttemptBudgetExpired            = errors.New("OpenAI acceptance attempt budget expired")
	ErrOpenAIProviderAttemptBudgetUnarmed            = errors.New("OpenAI acceptance attempt budget is not armed")
	ErrOpenAIProviderAttemptBudgetUnavailable        = errors.New("OpenAI acceptance attempt budget unavailable")
	ErrOpenAIProviderAttemptBudgetAccountUnsupported = errors.New("OpenAI acceptance budget requires a direct OpenAI upstream account")
)

// OpenAIProviderAttemptBudgetStore reserves before I/O, atomically across
// replicas. There is intentionally no local fallback or implicit arming.
type OpenAIProviderAttemptBudgetStore interface {
	ReserveOpenAIProviderAttempt(context.Context, string, int64, int64, time.Time) (int64, error)
}

type openAIProviderAttemptBudgetContextKey struct{}

type openAIProviderAttemptBudget struct {
	policy             config.OpenAIProviderAttemptBudgetConfig
	store              OpenAIProviderAttemptBudgetStore
	expiresAt          time.Time
	invalid            bool
	unsupportedAccount bool
}

func (s *OpenAIGatewayService) withOpenAIProviderAttemptBudget(ctx context.Context, c *gin.Context, account *Account) context.Context {
	if s == nil || s.cfg == nil || !s.cfg.Gateway.OpenAIProviderAttemptBudget.Enabled {
		return ctx
	}
	policy := s.cfg.Gateway.OpenAIProviderAttemptBudget
	apiKeyID := getAPIKeyIDFromContext(c)
	if apiKeyID > 0 && apiKeyID != policy.APIKeyID {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	store, _ := s.cache.(OpenAIProviderAttemptBudgetStore)
	expiresAt, _ := time.Parse(time.RFC3339, policy.ExpiresAt)
	budget := &openAIProviderAttemptBudget{policy: policy, store: store, expiresAt: expiresAt,
		invalid: apiKeyID <= 0 || policy.Validate() != nil,
		unsupportedAccount: account == nil || account.IsOpenAICodexNativeRelay() ||
			!account.IsOpenAIOAuth() && (!account.IsOpenAIApiKey() || !isOfficialOpenAIPlatformBaseURL(account.GetOpenAIBaseURL()))}
	return context.WithValue(ctx, openAIProviderAttemptBudgetContextKey{}, budget)
}

func reserveOpenAIProviderAttempt(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	budget, _ := ctx.Value(openAIProviderAttemptBudgetContextKey{}).(*openAIProviderAttemptBudget)
	if budget == nil {
		return nil
	}
	if budget.invalid || budget.store == nil {
		return ErrOpenAIProviderAttemptBudgetUnavailable
	}
	if budget.unsupportedAccount {
		return ErrOpenAIProviderAttemptBudgetAccountUnsupported
	}
	if !time.Now().Before(budget.expiresAt) {
		return ErrOpenAIProviderAttemptBudgetExpired
	}
	_, err := budget.store.ReserveOpenAIProviderAttempt(ctx, budget.policy.ID, budget.policy.APIKeyID,
		budget.policy.MaxAttempts, budget.expiresAt)
	if err == nil {
		return nil
	}
	if isOpenAIProviderAttemptBudgetError(err) {
		return err
	}
	// Cache/network errors may contain connection credentials. Neither return
	// nor log them at the model boundary.
	return ErrOpenAIProviderAttemptBudgetUnavailable
}

func isOpenAIProviderAttemptBudgetError(err error) bool {
	return errors.Is(err, ErrOpenAIProviderAttemptBudgetExhausted) ||
		errors.Is(err, ErrOpenAIProviderAttemptBudgetExpired) ||
		errors.Is(err, ErrOpenAIProviderAttemptBudgetUnarmed) ||
		errors.Is(err, ErrOpenAIProviderAttemptBudgetAccountUnsupported) ||
		errors.Is(err, ErrOpenAIProviderAttemptBudgetUnavailable)
}

func openAIProviderAttemptBudgetErrorDetails(err error) (int, string, string, bool) {
	switch {
	case errors.Is(err, ErrOpenAIProviderAttemptBudgetExhausted):
		return http.StatusTooManyRequests, "attempt_budget_exhausted", ErrOpenAIProviderAttemptBudgetExhausted.Error(), true
	case errors.Is(err, ErrOpenAIProviderAttemptBudgetExpired):
		return http.StatusTooManyRequests, "attempt_budget_expired", ErrOpenAIProviderAttemptBudgetExpired.Error(), true
	case errors.Is(err, ErrOpenAIProviderAttemptBudgetUnarmed):
		return http.StatusServiceUnavailable, "attempt_budget_unarmed", ErrOpenAIProviderAttemptBudgetUnarmed.Error(), true
	case errors.Is(err, ErrOpenAIProviderAttemptBudgetUnavailable):
		return http.StatusServiceUnavailable, "attempt_budget_unavailable", ErrOpenAIProviderAttemptBudgetUnavailable.Error(), true
	case errors.Is(err, ErrOpenAIProviderAttemptBudgetAccountUnsupported):
		return http.StatusServiceUnavailable, "attempt_budget_account_unsupported", ErrOpenAIProviderAttemptBudgetAccountUnsupported.Error(), true
	default:
		return 0, "", "", false
	}
}

// WriteOpenAIProviderAttemptBudgetError reports a local refusal without
// mislabelling it as a provider quota failure or fabricating a terminal event.
func WriteOpenAIProviderAttemptBudgetError(c *gin.Context, err error) bool {
	status, code, message, ok := openAIProviderAttemptBudgetErrorDetails(err)
	if !ok || c == nil {
		return false
	}
	c.JSON(status, gin.H{"error": gin.H{"type": code, "code": code, "message": message}})
	return true
}

func (s *OpenAIGatewayService) doOpenAIUpstream(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	if err := reserveOpenAIProviderAttempt(req.Context()); err != nil {
		return nil, err
	}
	if req.Context().Value(openAIProviderAttemptBudgetContextKey{}) != nil {
		// Redirects and replayable request bodies can hide a second dispatch
		// inside http.Client/Transport. Keep the original wire request intact,
		// but disable those implicit application retries for this lab scope.
		req = req.WithContext(WithNativeCodexRedirectPolicy(req.Context()))
		req.GetBody = nil
	}
	return s.httpUpstream.Do(req, proxyURL, accountID, concurrency)
}

// Passthrough frames remain opaque: every outgoing application frame spends
// one attempt, including generate=false, binary frames and future event types.
// Counting is attached to this request, never to a shared pooled connection.
type openAIProviderBudgetFrameConn struct {
	openaiwsv2.FrameConn
	budgetContext context.Context
}

func (c *openAIProviderBudgetFrameConn) WriteFrame(ctx context.Context, kind coderws.MessageType, payload []byte) error {
	if err := reserveOpenAIProviderAttempt(c.budgetContext); err != nil {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, err.Error(), err)
	}
	return c.FrameConn.WriteFrame(ctx, kind, payload)
}
