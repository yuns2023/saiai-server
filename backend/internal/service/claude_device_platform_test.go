package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cdpStoreStub struct {
	mu     sync.Mutex
	obs    []ClaudeDevicePlatformObservation
	status ClaudeDevicePlatformWriteStatus
	err    error
	page   ClaudeDevicePlatformFlaggedPage
	query  ClaudeDevicePlatformFlaggedQuery
}

func (s *cdpStoreStub) RecordClaudeDevicePlatform(_ context.Context, obs ClaudeDevicePlatformObservation, _ ClaudeDevicePlatformLimits) (ClaudeDevicePlatformWriteStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.obs = append(s.obs, obs)
	if s.status == "" {
		return ClaudeDevicePlatformWriteOK, s.err
	}
	return s.status, s.err
}

func (s *cdpStoreStub) ListFlaggedClaudeDevicePlatforms(_ context.Context, q ClaudeDevicePlatformFlaggedQuery) (ClaudeDevicePlatformFlaggedPage, error) {
	s.query = q
	return s.page, nil
}

type cdpUsersStub struct {
	names map[int64]ClaudeDevicePlatformUserName
}

func (s cdpUsersStub) LookupClaudeDevicePlatformUsers(_ context.Context, ids []int64) (map[int64]ClaudeDevicePlatformUserName, error) {
	out := map[int64]ClaudeDevicePlatformUserName{}
	for _, id := range ids {
		if n, ok := s.names[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

var cdpDeviceA = strings.Repeat("ab", 32)

func drainCDPQueue(svc *ClaudeDevicePlatformService) []ClaudeDevicePlatformObservation {
	var out []ClaudeDevicePlatformObservation
	for {
		select {
		case obs := <-svc.queue:
			out = append(out, obs)
		default:
			return out
		}
	}
}

func TestClaudeDevicePlatformService_ObserveQueuesSmallValues(t *testing.T) {
	svc := NewClaudeDevicePlatformService(&cdpStoreStub{}, nil)
	now := time.Unix(1_800_000_000, 0)
	svc.now = func() time.Time { return now }
	key := &APIKey{ID: 7, UserID: 3, Name: "laptop"}
	svc.ObserveRequest(cdpDeviceA, cdpHeaders("MacOS", "arm64"), cdpEnvBlock("darwin", "Darwin 24.3.0"), key)
	got := drainCDPQueue(svc)
	require.Len(t, got, 1)
	digest, _, _ := claudeDeviceIdentity(cdpDeviceA)
	require.Equal(t, ClaudeDevicePlatformObservation{
		Digest: digest, DisplayPrefix: "abababab", Family: "macos", Arch: "arm64", Major: "15",
		UserID: 3, APIKeyID: 7, ObservedAt: now.Unix(),
	}, got[0])

	svc.ObserveRequest("not-a-device", nil, nil, key)
	require.Empty(t, drainCDPQueue(svc))
	require.Equal(t, int64(1), svc.Stats().InvalidDeviceID)
}

func TestClaudeDevicePlatformService_QueueFullDrops(t *testing.T) {
	svc := NewClaudeDevicePlatformService(&cdpStoreStub{}, nil)
	svc.queue = make(chan ClaudeDevicePlatformObservation, 1)
	for i := 0; i < 3; i++ {
		svc.ObserveRequest(cdpDeviceA, cdpHeaders("Linux", "x64"), nil, &APIKey{ID: int64(i + 1), UserID: int64(i + 1)})
	}
	require.Equal(t, int64(1), svc.Stats().Enqueued)
	require.Equal(t, int64(2), svc.Stats().QueueFull)
}

func TestClaudeDevicePlatformService_DedupeOnlySkipsSameState(t *testing.T) {
	store := &cdpStoreStub{}
	svc := NewClaudeDevicePlatformService(store, nil)
	now := time.Unix(1_800_000_000, 0)
	svc.now = func() time.Time { return now }
	key := &APIKey{ID: 1, UserID: 1}
	observeAndWrite := func(headers http.Header, system any, k *APIKey) int {
		svc.ObserveRequest(cdpDeviceA, headers, system, k)
		queued := drainCDPQueue(svc)
		for _, obs := range queued {
			svc.write(obs)
		}
		return len(queued)
	}
	require.Equal(t, 1, observeAndWrite(cdpHeaders("MacOS", "arm64"), nil, key))
	require.Equal(t, 0, observeAndWrite(cdpHeaders("MacOS", "arm64"), nil, key), "same state within 5 min")
	require.Equal(t, 1, observeAndWrite(cdpHeaders("Windows", "arm64"), nil, key), "platform change commits")
	require.Equal(t, 1, observeAndWrite(cdpHeaders("MacOS", "arm64"), cdpEnvBlock("darwin", "Darwin 25.0.0"), key), "major change commits")
	require.Equal(t, 1, observeAndWrite(cdpHeaders("MacOS", "arm64"), nil, &APIKey{ID: 9, UserID: 2}), "user change commits")
	now = now.Add(claudeDevicePlatformDedupeInterval)
	require.Equal(t, 1, observeAndWrite(cdpHeaders("MacOS", "arm64"), nil, key), "interval elapsed")
	require.Equal(t, int64(5), svc.Stats().Written)

	// A failed write is not remembered.
	store.err = context.DeadlineExceeded
	now = now.Add(claudeDevicePlatformDedupeInterval)
	require.Equal(t, 1, observeAndWrite(cdpHeaders("MacOS", "arm64"), nil, key))
	require.Equal(t, 1, observeAndWrite(cdpHeaders("MacOS", "arm64"), nil, key))
	require.Equal(t, int64(2), svc.Stats().WriteErrors)
}

func TestClaudeDevicePlatformDedupe_Bounded(t *testing.T) {
	d := newClaudeDevicePlatformDedupe(3)
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < 5; i++ {
		d.remember(ClaudeDevicePlatformObservation{Digest: "d", UserID: int64(i)}, now)
	}
	require.Equal(t, 3, d.order.Len())
	require.Len(t, d.items, 3)
	require.False(t, d.recent(ClaudeDevicePlatformObservation{Digest: "d", UserID: 0}, now))
	require.True(t, d.recent(ClaudeDevicePlatformObservation{Digest: "d", UserID: 4}, now))
}

func TestClaudeDevicePlatformService_WorkersWriteAndStop(t *testing.T) {
	store := &cdpStoreStub{status: ClaudeDevicePlatformWriteUserCap}
	svc := NewClaudeDevicePlatformService(store, nil)
	svc.Start()
	svc.ObserveRequest(cdpDeviceA, cdpHeaders("Linux", "x64"), nil, &APIKey{ID: 1, UserID: 1})
	require.Eventually(t, func() bool { return svc.Stats().UserCap == 1 }, 2*time.Second, 5*time.Millisecond)
	svc.Stop()
	svc.Stop()
	svc.ObserveRequest(cdpDeviceA, cdpHeaders("Windows", "x64"), nil, &APIKey{ID: 1, UserID: 1})
	require.Equal(t, int64(1), svc.Stats().Enqueued, "no enqueue after stop")
}

func TestClaudeDevicePlatformService_ListResolvesUsers(t *testing.T) {
	at := time.Unix(1_800_000_000, 0).UTC()
	store := &cdpStoreStub{page: ClaudeDevicePlatformFlaggedPage{
		Items: []ClaudeDevicePlatformDevice{{
			DisplayPrefix: "abababab",
			Reasons:       []ClaudeDevicePlatformReasonMatch{{Reason: "cross_user", LastMatchedAt: at}},
			Users:         []ClaudeDevicePlatformUser{{UserID: 1}, {UserID: 2}},
		}},
		NextCursor: "next",
	}}
	svc := NewClaudeDevicePlatformService(store, cdpUsersStub{names: map[int64]ClaudeDevicePlatformUserName{1: {Username: "alice", Email: "a@example.com"}}})
	res, err := svc.ListFlagged(context.Background(), 1000, "", []string{"cross_user"})
	require.NoError(t, err)
	require.Equal(t, ClaudeDevicePlatformListMaxLimit, store.query.Limit)
	require.Equal(t, "next", res.NextCursor)
	require.Equal(t, "alice", res.Items[0].Users[0].Username)
	require.Empty(t, res.Items[0].Users[1].Username)
}

func TestParseClaudeDevicePlatformReasons(t *testing.T) {
	got, err := ParseClaudeDevicePlatformReasons("")
	require.NoError(t, err)
	require.Equal(t, ClaudeDevicePlatformDefaultReasons, got)
	got, err = ParseClaudeDevicePlatformReasons(" arch_only ,cross_user,arch_only")
	require.NoError(t, err)
	require.Equal(t, []string{"arch_only", "cross_user"}, got)
	for _, bad := range []string{"nope", ",", "cross_user,bogus"} {
		_, err := ParseClaudeDevicePlatformReasons(bad)
		require.ErrorIs(t, err, ErrClaudeDevicePlatformInvalidQuery, bad)
	}
}

// --- gateway hook ---

func cdpGatewayRequest(t *testing.T, path string) (*gin.Context, *http.Request) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.80 (external, cli)")
	req.Header.Set("X-Stainless-Os", "MacOS")
	req.Header.Set("X-Stainless-Arch", "arm64")
	req.Header.Set("X-App", "cli")
	c.Request = req
	c.Set("api_key", &APIKey{ID: 11, UserID: 5})
	return c, req
}

func cdpGatewayParsed(t *testing.T, req *http.Request, deviceID string) (*ParsedRequest, []byte) {
	t.Helper()
	userID := FormatMetadataUserID(deviceID, "original-account-uuid", "00000000-0000-4000-8000-000000000006", "2.1.80")
	env := strings.ReplaceAll(cdpEnvBlock("darwin", "Darwin 25.0.0"), "\n", `\n`)
	raw := []byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":` + strconvQuote(userID) + `},"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.80.000; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"` + env + `"}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	body := expectedOAuthBillingBodyForTest(t, raw, req.Header.Get("User-Agent"))
	parsed, err := ParseGatewayRequest(body, PlatformAnthropic)
	require.NoError(t, err)
	return parsed, body
}

func TestPrepareOAuthRequestIdentity_ObservesDevicePlatformEveryMode(t *testing.T) {
	digest, _, _ := claudeDeviceIdentity(cdpDeviceA)
	for _, mode := range []string{ClaudeOAuthModeCarpool, ClaudeOAuthModeShared, ClaudeOAuthModePinned, ClaudeOAuthModeSingleDevice} {
		for _, countTokens := range []bool{false, true} {
			t.Run(mode, func(t *testing.T) {
				c, req := cdpGatewayRequest(t, "/v1/messages")
				parsed, body := cdpGatewayParsed(t, req, cdpDeviceA)
				observer := NewClaudeDevicePlatformService(&cdpStoreStub{}, nil)
				svc := &GatewayService{identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32))}
				svc.SetClaudeDevicePlatformService(observer)
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{
					"account_uuid": "forced-account-uuid", "claude_oauth_mode": mode,
				}}
				original := append([]byte(nil), body...)
				first, _, _ := svc.prepareOAuthRequestIdentity(req.Context(), c, account, parsed, append([]byte(nil), body...), countTokens)

				// Failover: the same request on another account is not counted again.
				other := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Extra: map[string]any{
					"account_uuid": "forced-account-uuid", "claude_oauth_mode": mode,
				}}
				_, _, _ = svc.prepareOAuthRequestIdentity(req.Context(), c, other, parsed, append([]byte(nil), body...), countTokens)

				got := drainCDPQueue(observer)
				require.Len(t, got, 1)
				require.Equal(t, digest, got[0].Digest)
				require.Equal(t, "macos", got[0].Family)
				require.Equal(t, "arm64", got[0].Arch)
				require.Equal(t, "26", got[0].Major)
				require.Equal(t, int64(5), got[0].UserID)
				require.Equal(t, int64(11), got[0].APIKeyID)
				require.Equal(t, original, body, "request body untouched")

				// Observation does not change the request: same result without it.
				c2, req2 := cdpGatewayRequest(t, "/v1/messages")
				parsed2, body2 := cdpGatewayParsed(t, req2, cdpDeviceA)
				plain := &GatewayService{identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32))}
				second, _, _ := plain.prepareOAuthRequestIdentity(req2.Context(), c2, account, parsed2, body2, countTokens)
				if mode == ClaudeOAuthModeCarpool || mode == ClaudeOAuthModePinned {
					require.Equal(t, second, first)
				}
			})
		}
	}
}

func TestPrepareOAuthRequestIdentity_DevicePlatformSkipsInvalidDeviceAndAPIKeyAccounts(t *testing.T) {
	c, req := cdpGatewayRequest(t, "/v1/messages")
	parsed, body := cdpGatewayParsed(t, req, "user-chosen-id")
	observer := NewClaudeDevicePlatformService(&cdpStoreStub{}, nil)
	svc := &GatewayService{identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32))}
	svc.SetClaudeDevicePlatformService(observer)
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"account_uuid": "u"}}
	_, _, _ = svc.prepareOAuthRequestIdentity(req.Context(), c, account, parsed, body, false)
	require.Empty(t, drainCDPQueue(observer))
	require.Equal(t, int64(1), observer.Stats().InvalidDeviceID)

	// Non-Anthropic OAuth accounts are never observed.
	c2, req2 := cdpGatewayRequest(t, "/v1/messages")
	parsed2, body2 := cdpGatewayParsed(t, req2, cdpDeviceA)
	geminiOAuth := &Account{ID: 2, Platform: PlatformGemini, Type: AccountTypeOAuth, Extra: map[string]any{"account_uuid": "u"}}
	svc.observeClaudeDevicePlatform(c2, geminiOAuth, parsed2, cdpDeviceA)
	_ = body2
	require.Empty(t, drainCDPQueue(observer))
}

func TestPrepareOAuthRequestIdentity_DevicePlatformCountTokensAuxBranch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func() (*gin.Context, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
		req.Header.Set("User-Agent", "claude-cli/2.1.143 (external, cli)")
		req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,token-counting-2024-11-01")
		req.Header.Set("X-Claude-Code-Session-Id", "00000000-0000-4000-8000-000000000009")
		req.Header.Set("x-app", "cli")
		req.Header.Set("X-Stainless-Os", "Windows")
		req.Header.Set("X-Stainless-Arch", "x64")
		c.Request = req
		c.Set("api_key", &APIKey{ID: 4, UserID: 8})
		return c, rec
	}
	account := &Account{ID: 123, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}

	// With a parseable device id in metadata: observed once.
	userID := FormatMetadataUserID(cdpDeviceA, "", "00000000-0000-4000-8000-000000000009", "2.1.143")
	body := []byte(`{"model":"claude-sonnet-4-6","metadata":{"user_id":` + strconvQuote(userID) + `},"messages":[{"role":"user","content":"hello"}],"tools":[]}`)
	parsed, err := ParseGatewayRequest(body, PlatformAnthropic)
	require.NoError(t, err)
	observer := NewClaudeDevicePlatformService(&cdpStoreStub{}, nil)
	svc := &GatewayService{identityService: NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32))}
	svc.SetClaudeDevicePlatformService(observer)
	c, rec := newCtx()
	newBody, identity, err := svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsed, body, true)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, body, newBody)
	require.Empty(t, rec.Body.String())
	got := drainCDPQueue(observer)
	require.Len(t, got, 1)
	require.Equal(t, "windows", got[0].Family)
	require.Equal(t, int64(8), got[0].UserID)

	// Without metadata: allowed as before, not observed (no substitute ids).
	bare := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"tools":[]}`)
	parsedBare, err := ParseGatewayRequest(bare, PlatformAnthropic)
	require.NoError(t, err)
	c, _ = newCtx()
	_, identity, err = svc.prepareOAuthRequestIdentity(c.Request.Context(), c, account, parsedBare, bare, true)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Empty(t, drainCDPQueue(observer))

	// An oversized metadata.user_id is not parsed.
	parsedHuge := &ParsedRequest{Body: bare, Model: "claude-sonnet-4-6", MetadataUserID: strings.Repeat("x", claudeDevicePlatformMaxUserIDBytes+1)}
	c, _ = newCtx()
	svc.observeClaudeDevicePlatformAux(c, account, parsedHuge)
	require.Empty(t, drainCDPQueue(observer))
}
