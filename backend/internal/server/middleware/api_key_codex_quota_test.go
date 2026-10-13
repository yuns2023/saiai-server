//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexTotalKeyQuotaIsNon429AndReadable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, state := range []struct {
		name, status string
		quota, used  float64
	}{
		{"persisted", service.StatusAPIKeyQuotaExhausted, 0, 0},
		{"runtime", service.StatusActive, 10, 10},
	} {
		for _, route := range []struct{ method, path string }{
			{http.MethodPost, "/v1/responses"},
			{http.MethodPost, "/v1/responses/compact"},
			{http.MethodPost, "/responses"},
			{http.MethodPost, "/responses/compact"},
			{http.MethodGet, "/v1/responses"},
			{http.MethodGet, "/responses"},
		} {
			t.Run(state.name+"/"+route.method+route.path, func(t *testing.T) {
				user := &service.User{ID: 7, Status: service.StatusActive, Balance: 10}
				key := &service.APIKey{ID: 100, Key: "TEST_ONLY", UserID: user.ID, User: user,
					Status: state.status, Quota: state.quota, QuotaUsed: state.used}
				repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
				cfg := &config.Config{RunMode: config.RunModeStandard}
				auth := apiKeyAuthWithSubscription(service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg), nil, nil, cfg)
				router := gin.New()
				reachedHandler := false
				router.Handle(route.method, route.path, auth, func(c *gin.Context) { reachedHandler = true })
				router.GET("/v1/usage", auth, func(c *gin.Context) { c.Status(http.StatusOK) })
				w := httptest.NewRecorder()
				req := httptest.NewRequest(route.method, route.path+"?probe=1", nil)
				req.Header.Set("Authorization", "Bearer TEST_ONLY")
				if route.method == http.MethodGet {
					req.Header.Set("Connection", "Upgrade")
					req.Header.Set("Upgrade", "websocket")
				}
				router.ServeHTTP(w, req)
				require.Equal(t, http.StatusForbidden, w.Code)
				require.False(t, reachedHandler, "quota rejection must not reach generation or upgrade")
				require.Empty(t, w.Header().Get("Retry-After"))
				var body struct {
					ErrorResponse
					Error struct {
						ErrorResponse
						Type string `json:"type"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				require.Equal(t, "API_KEY_QUOTA_EXHAUSTED", body.Error.Code)
				require.Equal(t, "permission_error", body.Error.Type)
				require.Contains(t, body.Error.Message, "API key 额度已用完")
				require.Contains(t, body.Error.Message, "联系管理员")
				require.Equal(t, body.ErrorResponse, body.Error.ErrorResponse, "preserve legacy clients and Ops attribution")
				w = httptest.NewRecorder()
				req = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
				req.Header.Set("Authorization", "Bearer TEST_ONLY")
				router.ServeHTTP(w, req)
				require.Equal(t, http.StatusOK, w.Code, "spent Keys can still inspect their usage")
			})
		}
	}
}

// Opt-in official-client probe: no credentials, no provider, loopback only.
// Set SAIAI_CODEX_QUOTA_TEST_BINARY to a verified official Codex executable.
func TestCodexOfficialClientReadsGatewayQuotaMessage(t *testing.T) {
	binary := os.Getenv("SAIAI_CODEX_QUOTA_TEST_BINARY")
	if binary == "" {
		t.Skip("official Codex binary not supplied")
	}
	gin.SetMode(gin.TestMode)
	user := &service.User{ID: 7, Status: service.StatusActive, Balance: 10}
	key := &service.APIKey{ID: 100, Key: "TEST_ONLY", UserID: user.ID, User: user, Status: service.StatusAPIKeyQuotaExhausted}
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	auth := apiKeyAuthWithSubscription(service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg), nil, nil, cfg)
	router := gin.New()
	router.POST("/v1/responses", auth, func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	codexHome := filepath.Join(root, "codex")
	work := filepath.Join(root, "work")
	for _, dir := range []string{home, codexHome, work} {
		require.NoError(t, os.Mkdir(dir, 0700))
	}
	configText := `model = "gpt-5"
model_provider = "quota_mock"
[model_providers.quota_mock]
name = "OpenAI"
base_url = "` + server.URL + `/v1"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
http_headers = { Authorization = "Bearer TEST_ONLY" }
request_max_retries = 1
stream_max_retries = 1
[analytics]
enabled = false
`
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(configText), 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "exec", "--json", "--skip-git-repo-check", "--ephemeral", "-C", work, "ping")
	cmd.Env = []string{"HOME=" + home, "CODEX_HOME=" + codexHome, "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "RUST_LOG=error"}
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "quota rejection must fail the turn")
	require.NoError(t, ctx.Err(), "official client probe timed out")
	require.Contains(t, string(output), `"type":"turn.failed"`)
	require.Contains(t, string(output), "API key 额度已用完，请联系管理员调整额度或更换 API key。")
	require.NotContains(t, string(output), "429 Too Many Requests")
	require.NotContains(t, string(output), "exceeded retry limit")
}

func TestCodexQuotaResponseDoesNotChangeOtherProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/messages", "/v1/models", "/api/v1/client/bootstrap", "/v1/responses-other", "/responses-other"} {
		t.Run(path, func(t *testing.T) {
			router := gin.New()
			router.POST(path, abortAPIKeyQuotaExhausted)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
			require.Equal(t, http.StatusTooManyRequests, w.Code)
			require.JSONEq(t, `{"code":"API_KEY_QUOTA_EXHAUSTED","message":"API key 额度已用完"}`, w.Body.String())
		})
	}
}

func TestCodexSubscriptionWindowLimitRemains429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limit := 1.0
	group := &service.Group{ID: 42, Status: service.StatusActive, Platform: service.PlatformOpenAI,
		Hydrated: true, SubscriptionType: service.SubscriptionTypeSubscription, DailyLimitUSD: &limit}
	user := &service.User{ID: 7, Status: service.StatusActive, Balance: 10}
	key := &service.APIKey{ID: 100, Key: "TEST_ONLY", UserID: user.ID, User: user, Status: service.StatusActive,
		GroupID: &group.ID, Group: group}
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	now := time.Now()
	subRepo := &stubUserSubscriptionRepo{getActive: func(context.Context, int64, int64) (*service.UserSubscription, error) {
		return &service.UserSubscription{ID: 55, UserID: user.ID, GroupID: group.ID, Status: service.SubscriptionStatusActive,
			ExpiresAt: now.Add(24 * time.Hour), DailyWindowStart: &now, DailyUsageUSD: 10}, nil
	}}
	subscriptionService := service.NewSubscriptionService(nil, subRepo, nil, nil, cfg)
	t.Cleanup(subscriptionService.Stop)
	auth := apiKeyAuthWithSubscription(service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg), subscriptionService, nil, cfg)
	router := gin.New()
	router.POST("/v1/responses", auth, func(c *gin.Context) { t.Error("window-limited request reached generation") })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer TEST_ONLY")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Contains(t, w.Body.String(), "USAGE_LIMIT_EXCEEDED")
}
