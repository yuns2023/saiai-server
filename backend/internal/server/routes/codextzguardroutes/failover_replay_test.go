//go:build unit

// Package codextzguardroutes_test runs in its own test binary because the
// gateway's process-wide extra-CA pool is loaded once (see smartswitchroutes).
package codextzguardroutes_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// In-connection quota failover on the native OAuth relay: a turn that was
// allowed while the guard was off waits upstream; meanwhile the group setting
// or the key binding changes; the exhausted account then reports a quota
// error. The replay must be re-checked against fresh state and must never
// reach the replacement account. Everything stays on loopback.

const (
	tzKey        = "TEST_ONLY_TZ_GUARD_KEY"
	tzGroupA     = int64(7)
	tzGroupB     = int64(8)
	tzAccountA   = int64(91)
	tzAccountB   = int64(92)
	tzTokenA     = "MOCK_ONLY_A"
	tzTokenB     = "MOCK_ONLY_B"
	tzCodexUA    = "codex_cli_rs/0.163.0 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1"
	tzTaggedTurn = `{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context><timezone>Asia/Shanghai</timezone></environment_context>"}]}]}`
)

type tzLab struct {
	mu       sync.Mutex
	groupID  int64
	groups   map[int64]*service.Group
	user     service.User
	accounts []service.Account

	firstFrameA chan struct{}
	releaseA    chan struct{}
	dialsB      atomic.Int32
	framesB     atomic.Int32
	frames      map[string][][]byte // provider-received frames by token
}

func (l *tzLab) setGuard(groupID int64, on bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.groups[groupID].CodexCNTimezoneGuard = on
}

func (l *tzLab) rebind(groupID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.groupID = groupID
}

// provider plays chatgpt.com: account A holds the first turn until released,
// then reports a replay-safe quota error; account B is only counted.
func (l *tzLab) provider(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if auth == tzTokenB {
		l.dialsB.Add(1)
	}
	conn, err := coderws.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	for {
		_, frame, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		l.mu.Lock()
		if l.frames == nil {
			l.frames = map[string][][]byte{}
		}
		l.frames[auth] = append(l.frames[auth], frame)
		l.mu.Unlock()
		if auth == tzTokenB {
			l.framesB.Add(1)
			_ = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_b","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`))
			continue
		}
		close(l.firstFrameA)
		select {
		case <-l.releaseA:
		case <-r.Context().Done():
			return
		}
		resetAt := time.Now().Add(90 * time.Minute).Unix()
		_ = conn.Write(r.Context(), coderws.MessageText, []byte(fmt.Sprintf(`{"type":"error","error":{"code":"usage_limit_reached","type":"usage_limit_reached","message":"usage limit reached","resets_at":%d}}`, resetAt)))
	}
}

type tzKeyRepo struct {
	service.APIKeyRepository
	lab *tzLab
}

func (r tzKeyRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	if key != tzKey {
		return nil, service.ErrAPIKeyNotFound
	}
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	user := r.lab.user
	group := *r.lab.groups[r.lab.groupID]
	groupID := r.lab.groupID
	return &service.APIKey{ID: 5, UserID: user.ID, Key: tzKey, Status: service.StatusActive, User: &user, GroupID: &groupID, Group: &group}, nil
}
func (tzKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type tzUserRepo struct {
	service.UserRepository
	lab *tzLab
}

func (r tzUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	user := r.lab.user
	return &user, nil
}

type tzAccountRepo struct {
	service.AccountRepository
	lab *tzLab
}

func (r tzAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for _, account := range r.lab.accounts {
		if account.ID == id {
			copied := account
			return &copied, nil
		}
	}
	return nil, service.ErrAccountNotFound
}
func (r tzAccountRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]service.Account, error) {
	return append([]service.Account(nil), r.lab.accounts...), nil
}
func (r tzAccountRepo) ListSchedulableByPlatform(context.Context, string) ([]service.Account, error) {
	return append([]service.Account(nil), r.lab.accounts...), nil
}
func (tzAccountRepo) SetRateLimited(context.Context, int64, time.Time) error   { return nil }
func (tzAccountRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }
func (tzAccountRepo) ClearRateLimit(context.Context, int64) error              { return nil }
func (tzAccountRepo) SetError(context.Context, int64, string) error            { return nil }
func (tzAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

type tzUsageRepo struct{ service.UsageLogRepository }

func (tzUsageRepo) CreateBestEffort(context.Context, *service.UsageLog) error { return nil }

type tzBillingRepo struct{}

func (tzBillingRepo) Apply(context.Context, *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

type tzCache struct{ service.GatewayCache }

func (tzCache) ReserveOpenAIProviderAttempt(context.Context, string, int64, int64, time.Time) (int64, error) {
	return 0, service.ErrOpenAIProviderAttemptBudgetUnarmed
}
func (tzCache) GetSessionAccountID(context.Context, int64, string) (int64, error) { return 0, nil }
func (tzCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}
func (tzCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error { return nil }
func (tzCache) DeleteSessionAccountID(context.Context, int64, string) error           { return nil }

type tzConcurrency struct{ service.ConcurrencyCache }

func (tzConcurrency) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (tzConcurrency) ReleaseUserSlot(context.Context, int64, string) error { return nil }
func (tzConcurrency) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (tzConcurrency) ReleaseAccountSlot(context.Context, int64, string) error { return nil }
func (tzConcurrency) GetAccountsLoadBatch(_ context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	result := make(map[int64]*service.AccountLoadInfo, len(accounts))
	for _, account := range accounts {
		result[account.ID] = &service.AccountLoadInfo{AccountID: account.ID}
	}
	return result, nil
}

// The extra-CA pool is loaded once per process, so every test shares one
// mock CA created before any test runs.
var tzTLS *tls.Config

func TestMain(m *testing.M) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "SAIAI MOCK ONLY"},
		DNSNames: []string{"chatgpt.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	dir, err := os.MkdirTemp("", "codextzguardroutes-ca-")
	if err != nil {
		panic(err)
	}
	path := filepath.Join(dir, "public-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		panic(err)
	}
	if err := os.Setenv("SAIAI_EXTRA_CA_FILE", path); err != nil {
		panic(err)
	}
	tzTLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// tzConnectProxy tunnels only chatgpt.com:443 to the loopback provider.
func tzConnectProxy(t *testing.T, providerAddr string) (string, int) {
	t.Helper()
	var conns sync.WaitGroup
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "chatgpt.com:443" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		upstream, err := net.DialTimeout("tcp", providerAddr, 3*time.Second)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = upstream.Close() }()
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		downstream, buffer, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = downstream.Close() }()
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		conns.Add(1)
		defer conns.Done()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, buffer); close(done) }()
		_, _ = io.Copy(downstream, upstream)
		_ = downstream.Close()
		<-done
	}))
	t.Cleanup(proxy.Close)
	host, port, err := net.SplitHostPort(proxy.Listener.Addr().String())
	require.NoError(t, err)
	portNumber, err := strconv.Atoi(port)
	require.NoError(t, err)
	return host, portNumber
}

func newTZLab(t *testing.T) (*tzLab, *httptest.Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	lab := &tzLab{
		groupID: tzGroupA,
		groups: map[int64]*service.Group{
			tzGroupA: {ID: tzGroupA, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard},
			tzGroupB: {ID: tzGroupB, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard},
		},
		user:        service.User{ID: 55, Status: service.StatusActive, Role: service.RoleUser, Balance: 100, Concurrency: 5},
		firstFrameA: make(chan struct{}),
		releaseA:    make(chan struct{}),
	}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(lab.provider))
	provider.TLS = tzTLS
	provider.StartTLS()
	t.Cleanup(provider.Close)
	host, port := tzConnectProxy(t, provider.Listener.Addr().String())
	proxyID := int64(1)
	account := func(id int64, token string) service.Account {
		return service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, Concurrency: 5, Priority: int(id), GroupIDs: []int64{tzGroupA, tzGroupB},
			Credentials: map[string]any{"access_token": token, "chatgpt_account_id": "MOCK_ONLY_WORKSPACE_" + token},
			Extra:       map[string]any{"responses_websockets_v2_enabled": true}, ProxyID: &proxyID,
			Proxy: &service.Proxy{ID: proxyID, Protocol: "http", Host: host, Port: port}}
	}
	// Account A sorts first (lower priority value).
	lab.accounts = []service.Account{account(tzAccountA, tzTokenA), account(tzAccountB, tzTokenB)}
	lab.accounts[0].Priority, lab.accounts[1].Priority = 1, 50

	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxBodySize = 1 << 20
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Pricing.DataDir, cfg.Pricing.UpdateIntervalHours = t.TempDir(), 24
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"), []byte(`{"gpt-5.5":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`), 0o600))
	pricing := service.NewPricingService(cfg, nil)
	require.NoError(t, pricing.Initialize())
	t.Cleanup(pricing.Stop)
	cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.OAuthEnabled, cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true, true, true

	keys := tzKeyRepo{lab: lab}
	users := tzUserRepo{lab: lab}
	keyService := service.NewAPIKeyService(keys, users, nil, nil, nil, nil, cfg)
	billingCache := service.NewBillingCacheService(nil, users, nil, keys, cfg)
	t.Cleanup(billingCache.Stop)
	concurrency := service.NewConcurrencyService(tzConcurrency{})
	svc := service.NewOpenAIGatewayService(tzAccountRepo{lab: lab}, tzUsageRepo{}, tzBillingRepo{},
		nil, nil, nil, tzCache{}, cfg, nil, concurrency, service.NewBillingService(cfg, pricing), nil, billingCache,
		repository.NewHTTPUpstream(cfg), &service.DeferredService{}, nil)
	t.Cleanup(svc.CloseOpenAIWSPool)
	gatewayHandler := handler.NewOpenAIGatewayHandler(svc, concurrency, billingCache, keyService, nil, nil, nil, cfg, nil)
	router := gin.New()
	routes.RegisterGatewayRoutes(router, &handler.Handlers{Gateway: &handler.GatewayHandler{}, OpenAIGateway: gatewayHandler},
		middleware.NewAPIKeyAuthMiddleware(keyService, nil, nil, cfg), keyService, nil, nil, nil, cfg)
	gateway := httptest.NewServer(router)
	t.Cleanup(gateway.Close)
	return lab, gateway
}

func runTZFailover(t *testing.T, change func(*tzLab), expectClose bool) (*tzLab, []byte, error) {
	lab, gateway := newTZLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tzKey)
	headers.Set("User-Agent", tzCodexUA)
	headers.Set("originator", "codex_cli_rs")
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	// Guard off: the tagged turn is forwarded to account A, which holds it.
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(tzTaggedTurn)))
	select {
	case <-lab.firstFrameA:
	case <-ctx.Done():
		t.Fatal("account A never received the first turn")
	}
	change(lab)
	close(lab.releaseA)

	_, frame, readErr := conn.Read(ctx)
	if readErr != nil {
		return lab, nil, readErr
	}
	if !expectClose {
		return lab, frame, nil
	}
	_, _, closeErr := conn.Read(ctx)
	return lab, frame, closeErr
}

func TestCodexTimezoneGuardRejectsQuotaFailoverReplayAfterSwitchOn(t *testing.T) {
	lab, frame, closeErr := runTZFailover(t, func(lab *tzLab) { lab.setGuard(tzGroupA, true) }, true)
	require.Equal(t, "error", gjson.GetBytes(frame, "type").String(), string(frame))
	require.Equal(t, "codex_timezone_reinit_required", gjson.GetBytes(frame, "error.code").String())
	require.Error(t, closeErr)
	require.Zero(t, lab.framesB.Load(), "the replay never reaches the replacement account")
	require.Zero(t, lab.dialsB.Load())
}

func TestOpenAIWSQuotaFailoverReplayEndsSessionAfterRebind(t *testing.T) {
	lab, frame, closeErr := runTZFailover(t, func(lab *tzLab) { lab.rebind(tzGroupB) }, true)
	require.Equal(t, "error", gjson.GetBytes(frame, "type").String(), string(frame))
	require.Equal(t, "api_key_group_changed", gjson.GetBytes(frame, "error.code").String())
	require.Error(t, closeErr)
	require.Zero(t, lab.framesB.Load())
	require.Zero(t, lab.dialsB.Load())
}

// Control: with nothing changed the same flow fails over to account B, so the
// two tests above exercise the real replay path.
func TestOpenAIWSQuotaFailoverReplayControl(t *testing.T) {
	lab, frame, _ := runTZFailover(t, func(*tzLab) {}, false)
	require.Equal(t, "response.completed", gjson.GetBytes(frame, "type").String(), string(frame))
	require.Equal(t, int32(1), lab.framesB.Load())
}

// Rewrite mode end to end on the native OAuth relay: the first frame sent to
// account A, its quota failover replay on account B and a later turn on B
// all carry the rewritten zone; the client's frames are never rejected.
func TestCodexTimezoneRewriteWSFirstFrameFailoverReplayAndLaterTurn(t *testing.T) {
	lab, gateway := newTZLab(t)
	lab.mu.Lock()
	lab.groups[tzGroupA].CodexCNTimezoneGuard = true
	lab.groups[tzGroupA].CodexTimezoneAction = service.CodexTimezoneActionRewrite
	lab.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tzKey)
	headers.Set("User-Agent", tzCodexUA)
	headers.Set("originator", "codex_cli_rs")
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(tzTaggedTurn)))
	select {
	case <-lab.firstFrameA:
	case <-ctx.Done():
		t.Fatal("account A never received the first turn")
	}
	close(lab.releaseA)
	_, frame, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(frame, "type").String(), string(frame))

	laterTurn := strings.Replace(tzTaggedTurn, "Asia/Shanghai", "Asia/Hong_Kong", 1)
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(laterTurn)))
	_, frame, err = conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(frame, "type").String(), string(frame))

	rewritten := strings.Replace(tzTaggedTurn, "Asia/Shanghai", "America/Los_Angeles", 1)
	lab.mu.Lock()
	framesA := append([][]byte(nil), lab.frames[tzTokenA]...)
	framesB := append([][]byte(nil), lab.frames[tzTokenB]...)
	lab.mu.Unlock()
	require.Len(t, framesA, 1)
	require.Equal(t, rewritten, string(framesA[0]), "first frame")
	require.Len(t, framesB, 2)
	require.Equal(t, rewritten, string(framesB[0]), "failover replay")
	require.Equal(t, rewritten, string(framesB[1]), "later turn")
}
