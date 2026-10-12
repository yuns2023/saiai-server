package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type unsupportedCodexResetRepo struct{ service.AccountRepository }

func (unsupportedCodexResetRepo) GetByID(context.Context, int64) (*service.Account, error) {
	return &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}, nil
}

func TestAccountCodexResetValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AccountHandler{openAIQuotaResetService: service.NewOpenAIQuotaResetService(unsupportedCodexResetRepo{}, nil, nil, nil)}
	router := gin.New()
	router.GET("/accounts/:id/codex-reset-credits", h.ListCodexResetCredits)
	router.POST("/accounts/:id/codex-reset-credits/consume", h.ConsumeCodexResetCredit)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/accounts/bad/codex-reset-credits", ""},
		{"GET", "/accounts/0/codex-reset-credits", ""},
		{"GET", "/accounts/1/codex-reset-credits", ""},
		{"POST", "/accounts/bad/codex-reset-credits/consume", `{}`},
		{"POST", "/accounts/1/codex-reset-credits/consume", `{}`},
		{"POST", "/accounts/1/codex-reset-credits/consume", `{"credit_id":"credit","idempotency_key":""}`},
		{"POST", "/accounts/1/codex-reset-credits/consume", `{"credit_id":"credit","idempotency_key":"key"}`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}
