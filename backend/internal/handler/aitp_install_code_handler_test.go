//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	installHandlerUser  = int64(7)
	installHandlerKeyID = int64(41)
)

// Built at run time so secret scanners do not flag a fake key.
var installHandlerKey = "sk-" + strings.Repeat("feedfacecafebeef", 4)

type installHandlerSettingRepo struct {
	service.SettingRepository
	mu     sync.Mutex
	values map[string]string
}

func (r *installHandlerSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (r *installHandlerSettingRepo) set(k, v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[k] = v
}

type installHandlerKeyRepo struct {
	service.APIKeyRepository
	mu   sync.Mutex
	keys map[int64]service.APIKey
}

func (r *installHandlerKeyRepo) GetByID(_ context.Context, id int64) (*service.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.keys[id]
	if !ok {
		return nil, service.ErrAPIKeyNotFound
	}
	return &k, nil
}

type installHandlerUserRepo struct {
	service.UserRepository
}

func (installHandlerUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, Status: service.StatusActive}, nil
}

// installHandlerStore is an in-memory AITPInstallCodeStore with an atomic consume.
type installHandlerStore struct {
	mu       sync.Mutex
	codes    map[string][]byte
	counters map[string]int64
	err      error
}

func (s *installHandlerStore) Save(_ context.Context, code string, rec *service.AITPInstallCodeRecord, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	if _, ok := s.codes[code]; ok {
		return false, nil
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return false, err
	}
	s.codes[code] = data
	return true, nil
}

func (s *installHandlerStore) Consume(_ context.Context, code string) (*service.AITPInstallCodeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	data, ok := s.codes[code]
	if !ok {
		return nil, nil
	}
	delete(s.codes, code)
	var rec service.AITPInstallCodeRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *installHandlerStore) bump(key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	s.counters[key]++
	return s.counters[key], nil
}

func (s *installHandlerStore) CountIssued(_ context.Context, userID int64, _ time.Duration) (int64, error) {
	return s.bump(fmt.Sprintf("issued:%d", userID))
}

func (s *installHandlerStore) CountRedeemAttempt(_ context.Context, clientIP string, _ time.Duration) (int64, error) {
	return s.bump("ip:" + clientIP)
}

func (s *installHandlerStore) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

type installHandlerFixture struct {
	router   *gin.Engine
	settings *installHandlerSettingRepo
	store    *installHandlerStore
}

func newInstallHandlerFixture(t *testing.T) *installHandlerFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &installHandlerFixture{
		settings: &installHandlerSettingRepo{values: map[string]string{
			service.SettingKeyAITPInstallCodeEnabled: "true",
			service.SettingKeyAPIBaseURL:             "https://ccyou.dev/v1",
		}},
		store: &installHandlerStore{codes: map[string][]byte{}, counters: map[string]int64{}},
	}
	keys := &installHandlerKeyRepo{keys: map[int64]service.APIKey{
		installHandlerKeyID: {ID: installHandlerKeyID, UserID: installHandlerUser, Key: installHandlerKey, Status: service.StatusActive},
		42:                  {ID: 42, UserID: 8, Key: "sk-someone-else", Status: service.StatusActive},
	}}
	svc := service.NewAITPInstallCodeService(
		service.NewSettingService(f.settings, &config.Config{}), keys, installHandlerUserRepo{}, f.store)
	h := NewAITPInstallCodeHandler(svc)

	r := gin.New()
	// The production logging middlewares, so the redaction is covered end to end.
	r.Use(middleware2.RequestLogger(), middleware2.Logger())
	r.POST("/i/:code", h.Redeem)
	r.GET("/i/:code", h.RedeemMethodNotAllowed)
	r.HEAD("/i/:code", h.RedeemMethodNotAllowed)
	authed := r.Group("/api/v1")
	authed.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: installHandlerUser})
		c.Next()
	})
	authed.POST("/keys/:id/install-code", h.Create)
	f.router = r
	return f
}

func (f *installHandlerFixture) do(method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

type installCreateResponse struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
	Data   struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"data"`
}

func (f *installHandlerFixture) create(t *testing.T, keyID int64, shell string) (*httptest.ResponseRecorder, installCreateResponse) {
	t.Helper()
	w := f.do(http.MethodPost, fmt.Sprintf("/api/v1/keys/%d/install-code", keyID), fmt.Sprintf(`{"shell":%q}`, shell), nil)
	var resp installCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	return w, resp
}

func (f *installHandlerFixture) issue(t *testing.T, shell string) string {
	t.Helper()
	w, resp := f.create(t, installHandlerKeyID, shell)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, resp.Data.Code, service.AITPInstallCodeLength)
	return resp.Data.Code
}

func requireScriptResponse(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	body := w.Body.String()
	require.NotContains(t, strings.ToLower(body), "<html")
	require.NotContains(t, strings.ToLower(body), "<!doctype")
	return body
}

func TestAITPInstallCodeHandler_CreateAndRedeem(t *testing.T) {
	f := newInstallHandlerFixture(t)
	before := time.Now()
	w, resp := f.create(t, installHandlerKeyID, "unix")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.WithinDuration(t, before.Add(10*time.Minute), resp.Data.ExpiresAt, 5*time.Second)
	require.NotContains(t, w.Body.String(), installHandlerKey, "the create response never carries the key")

	body := requireScriptResponse(t, f.do(http.MethodPost, "/i/"+resp.Data.Code, "", nil))
	require.Contains(t, body, "curl -fsSL https://aitp.lol/setup.sh -o \"$tmp\"")
	require.Contains(t, body, "--base-url 'https://ccyou.dev' --api-key '"+installHandlerKey+"'")
	require.True(t, strings.HasSuffix(body, "\naitp_install\n"))

	// Second use: error script, no key.
	body = requireScriptResponse(t, f.do(http.MethodPost, "/i/"+resp.Data.Code, "", nil))
	require.NotContains(t, body, installHandlerKey)
	require.Contains(t, body, "exit 1")

	// PowerShell code (cmd tab) redeems to the PowerShell line.
	code := f.issue(t, "cmd")
	body = requireScriptResponse(t, f.do(http.MethodPost, "/i/"+strings.ToLower(code), "", map[string]string{"User-Agent": "curl/8"}))
	require.Equal(t, "& ([scriptblock]::Create((irm https://aitp.lol/setup.ps1))) -BaseUrl 'https://ccyou.dev' -ApiKey '"+installHandlerKey+"'\n", body)
}

func TestAITPInstallCodeHandler_GetAndHeadNeverConsume(t *testing.T) {
	f := newInstallHandlerFixture(t)
	code := f.issue(t, "unix")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, target := range []string{"/i/" + code, "/i/bad", "/i/0123456789AB"} {
			w := f.do(method, target, "", nil)
			require.Equal(t, http.StatusMethodNotAllowed, w.Code, method+" "+target)
			require.Equal(t, http.MethodPost, w.Header().Get("Allow"))
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			require.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
			require.NotContains(t, w.Body.String(), installHandlerKey)
			require.NotContains(t, strings.ToLower(w.Body.String()), "<html")
		}
	}
	body := requireScriptResponse(t, f.do(http.MethodPost, "/i/"+code, "", nil))
	require.Contains(t, body, installHandlerKey, "GET and HEAD left the code unused")
}

func TestAITPInstallCodeHandler_FailuresAreScripts(t *testing.T) {
	f := newInstallHandlerFixture(t)
	code := f.issue(t, "unix")

	// Malformed and unknown codes: sh error script by default, PowerShell for irm.
	for _, target := range []string{"/i/bad", "/i/0123456789AB", "/i/" + strings.Repeat("Z", 13), "/i/%27%3B"} {
		body := requireScriptResponse(t, f.do(http.MethodPost, target, "", nil))
		require.Contains(t, body, ">&2")
		require.True(t, strings.HasSuffix(body, "exit 1\n"), body)
		body = requireScriptResponse(t, f.do(http.MethodPost, target, "", map[string]string{"User-Agent": "Mozilla/5.0 (Windows NT 10.0) WindowsPowerShell/5.1"}))
		require.True(t, strings.HasPrefix(body, "throw '"), body)
	}

	// Setting turned off after creation: the code is consumed, the script refuses.
	f.settings.set(service.SettingKeyAITPInstallCodeEnabled, "false")
	body := requireScriptResponse(t, f.do(http.MethodPost, "/i/"+code, "", nil))
	require.NotContains(t, body, installHandlerKey)
	require.Contains(t, body, "exit 1")

	// Redis down: fail closed.
	f.settings.set(service.SettingKeyAITPInstallCodeEnabled, "true")
	code = f.issue(t, "powershell")
	f.store.setErr(errors.New("redis down"))
	body = requireScriptResponse(t, f.do(http.MethodPost, "/i/"+code, "", map[string]string{"User-Agent": "Mozilla/5.0 PowerShell/7.4"}))
	require.True(t, strings.HasPrefix(body, "throw '"), "the stored shell is unreadable, so the User-Agent decides: %s", body)
	require.NotContains(t, body, installHandlerKey)
	// Redis failed before anything was consumed, so the code still works once it is back.
	f.store.setErr(nil)
	body = requireScriptResponse(t, f.do(http.MethodPost, "/i/"+code, "", nil))
	require.Contains(t, body, installHandlerKey)
}

func TestAITPInstallCodeHandler_ConcurrentDoublePost(t *testing.T) {
	f := newInstallHandlerFixture(t)
	code := f.issue(t, "unix")
	const n = 8
	bodies := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bodies <- f.do(http.MethodPost, "/i/"+code, "", nil).Body.String()
		}()
	}
	wg.Wait()
	close(bodies)
	withKey := 0
	for body := range bodies {
		if strings.Contains(body, installHandlerKey) {
			withKey++
		}
	}
	require.Equal(t, 1, withKey)
}

func TestAITPInstallCodeHandler_CreateRefusals(t *testing.T) {
	f := newInstallHandlerFixture(t)

	w, resp := f.create(t, 42, "unix")
	require.Equal(t, http.StatusNotFound, w.Code, "another user's key id: %s", w.Body.String())
	require.Empty(t, resp.Data.Code)

	w, resp = f.create(t, installHandlerKeyID, "fish")
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, "AITP_INSTALL_CODE_SHELL_INVALID", resp.Reason)

	w = f.do(http.MethodPost, "/api/v1/keys/abc/install-code", `{"shell":"unix"}`, nil)
	require.Equal(t, http.StatusBadRequest, w.Code)

	f.settings.set(service.SettingKeyAPIBaseURL, "")
	w, resp = f.create(t, installHandlerKeyID, "unix")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "AITP_INSTALL_CODE_BASE_URL_INVALID", resp.Reason)
	f.settings.set(service.SettingKeyAPIBaseURL, "https://ccyou.dev")

	f.settings.set(service.SettingKeyAITPInstallCodeEnabled, "false")
	w, resp = f.create(t, installHandlerKeyID, "unix")
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "AITP_INSTALL_CODE_DISABLED", resp.Reason)
	f.settings.set(service.SettingKeyAITPInstallCodeEnabled, "true")

	for i := 0; i < service.AITPInstallCodeIssueLimit; i++ {
		f.issue(t, "unix")
	}
	w, resp = f.create(t, installHandlerKeyID, "unix")
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "AITP_INSTALL_CODE_RATE_LIMITED", resp.Reason)
}

func TestAITPInstallCodeHandler_PerIPRedeemLimit(t *testing.T) {
	f := newInstallHandlerFixture(t)
	code := f.issue(t, "unix")
	for i := 0; i < service.AITPInstallCodeRedeemLimit; i++ {
		requireScriptResponse(t, f.do(http.MethodPost, "/i/0000000000AB", "", nil))
	}
	body := requireScriptResponse(t, f.do(http.MethodPost, "/i/"+code, "", nil))
	require.NotContains(t, body, installHandlerKey, "over the per-IP limit")
	require.Contains(t, body, "exit 1")
}

// Neither the code nor the key may reach a log line (zap logs incl. the access log, and
// the standard logger used by response.ErrorFrom).
func TestAITPInstallCodeHandler_LogsNeverContainCodeOrKey(t *testing.T) {
	sink, restore := captureHandlerStructuredLog(t)
	defer restore()
	var std bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&std)
	defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()

	f := newInstallHandlerFixture(t)
	code := f.issue(t, "unix")
	requireScriptResponse(t, f.do(http.MethodPost, "/i/"+code, "", nil))
	requireScriptResponse(t, f.do(http.MethodPost, "/i/"+strings.ToLower(code), "", nil))
	f.do(http.MethodGet, "/i/"+code, "", nil)
	code2 := f.issue(t, "powershell")
	f.store.setErr(errors.New("redis down"))
	requireScriptResponse(t, f.do(http.MethodPost, "/i/"+code2, "", nil))
	f.create(t, installHandlerKeyID, "unix") // 503 → response.ErrorFrom logs via the std logger
	f.store.setErr(nil)

	sink.mu.Lock()
	events := viewEvents(sink)
	sink.mu.Unlock()
	require.NotEmpty(t, events)

	sawRedacted, sawAudit := false, false
	for _, ev := range events {
		text := ev.String()
		for _, secret := range []string{code, strings.ToLower(code), code2, installHandlerKey} {
			require.NotContains(t, text, secret)
		}
		if strings.Contains(text, "/i/[redacted]") {
			sawRedacted = true
		}
		if ev.fields["component"] == "audit.aitp_install_code" {
			sawAudit = true
		}
	}
	require.True(t, sawRedacted, "the access log keeps the route, without the code")
	require.True(t, sawAudit)
	for _, secret := range []string{code, code2, installHandlerKey} {
		require.NotContains(t, std.String(), secret)
	}
}

type loggerEventView struct {
	message string
	fields  map[string]any
}

func (v *loggerEventView) String() string {
	return fmt.Sprintf("%s %v", v.message, v.fields)
}

// viewEvents copies the sink's events; the caller holds sink.mu.
func viewEvents(sink *handlerInMemoryLogSink) []*loggerEventView {
	out := make([]*loggerEventView, 0, len(sink.events))
	for _, ev := range sink.events {
		if ev == nil {
			continue
		}
		out = append(out, &loggerEventView{message: ev.Message, fields: ev.Fields})
	}
	return out
}
