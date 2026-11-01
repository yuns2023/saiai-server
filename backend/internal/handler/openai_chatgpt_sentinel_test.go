package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const (
	chatGPTSentinelBody = `{"p":"TEST_ONLY_PROOF"}`
	chatGPTNewChatBody  = `{"model":"auto","messages":[{"id":"TEST_ONLY_NEW","author":{"role":"user"},"content":{"parts":["TEST_ONLY_TEXT"]}}]}`
	chatGPTFollowUpBody = `{"model":"auto","conversation_id":"TEST_ONLY_CONVERSATION","messages":[{"id":"TEST_ONLY_FOLLOW_UP","author":{"role":"user"},"content":{"parts":["TEST_ONLY_TEXT"]}}]}`
	chatGPTOwnerToken   = "TEST_ONLY_PREPARE_TEST_ONLY_ACCOUNT"
	chatGPTOtherToken   = "TEST_ONLY_PREPARE_" + chatGPTControlOtherAccount
)

func chatGPTSentinelHeaders(tokens ...string) http.Header {
	return http.Header{service.ChatGPTSentinelPrepareTokenHeader: tokens}
}

func (f *chatGPTBoundFixture) deviceAffinity() int64 {
	owner, err := f.cache.GetChatGPTUploadOwner(context.Background(), f.scope.UploadSessionKey(chatGPTBoundDeviceID))
	require.NoError(f.t, err)
	return owner
}

func (f *chatGPTBoundFixture) bindSentinel(token string, accountID int64) {
	require.NoError(f.t, f.cache.BindChatGPTUploadOwner(context.Background(), []string{f.scope.SentinelKey(token)}, accountID, time.Minute))
}

// The production incident: Sentinel prepare and f/conversation were scheduled
// independently, so the token reached an account that did not issue it.
func TestChatGPTSentinelTokenPinsNewChatToIssuingAccount(t *testing.T) {
	t.Run("without a token the scheduler picks 248", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.accounts.accounts[1].Priority = 5
		w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTNewChatBody, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, []string{"TEST_ONLY_ACCOUNT"}, f.providerAccounts)
	})

	f := newChatGPTControlFixture(t)
	f.accounts.accounts[1].Priority = 5
	ctx := context.Background()
	affinity := f.scope.UploadSessionKey(chatGPTBoundDeviceID)
	_, err := f.cache.ClaimChatGPTUploadOwner(ctx, affinity, 249, time.Minute)
	require.NoError(t, err)

	w, c := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"prepare_token":"`+chatGPTOtherToken+`"`, "the provider response is relayed unchanged")
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, []string{chatGPTControlOtherAccount}, f.providerAccounts)
	selected, _ := c.Get(opsAccountIDKey)
	require.EqualValues(t, 249, selected)

	// The device affinity expires before the user sends.
	require.NoError(t, f.cache.ReleaseChatGPTUploadOwner(ctx, affinity, 249))
	f.headers = chatGPTSentinelHeaders(chatGPTOtherToken)
	w, _ = f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTNewChatBody, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{chatGPTControlOtherAccount, chatGPTControlOtherAccount}, f.providerAccounts)
	require.EqualValues(t, 249, f.deviceAffinity(), "the turn's account becomes the device affinity")
}

func TestChatGPTSentinelForExistingConversationFollowsOwner(t *testing.T) {
	t.Run("without an owner request the scheduler decides", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.setOwner(func(a *service.Account) { a.Priority = 5 })
		w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, []string{chatGPTControlOtherAccount}, f.providerAccounts)
		require.EqualValues(t, 249, f.deviceAffinity())
	})

	for _, mode := range []struct {
		name, control string
		fromModel     bool
	}{
		{"after conversation init", "/chatgpt/backend-api/conversation/init", false},
		{"after conversation prepare", "/chatgpt/backend-api/f/conversation/prepare", false},
		{"after the previous turn", "", true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			f := newChatGPTControlFixture(t)
			f.setOwner(func(a *service.Account) { a.Priority = 5 })
			f.bindConversation(f.scope, 248)
			// A stale affinity from an earlier new chat on 249.
			_, err := f.cache.ClaimChatGPTUploadOwner(context.Background(), f.scope.UploadSessionKey(chatGPTBoundDeviceID), 249, time.Minute)
			require.NoError(t, err)
			if mode.fromModel {
				f.bindSentinel("TEST_ONLY_PREVIOUS", 248)
				f.headers = chatGPTSentinelHeaders("TEST_ONLY_PREVIOUS")
				w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", strings.Replace(chatGPTFollowUpBody, "TEST_ONLY_FOLLOW_UP", "TEST_ONLY_PREVIOUS_TURN", 1), "")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				f.headers = nil
			} else {
				w, _ := f.run("POST", mode.control, chatGPTControlBody, "")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			}
			require.EqualValues(t, 248, f.deviceAffinity())

			w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			f.headers = chatGPTSentinelHeaders(chatGPTOwnerToken)
			w, _ = f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTFollowUpBody, "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			for _, account := range f.providerAccounts {
				require.Equal(t, "TEST_ONLY_ACCOUNT", account)
			}
		})
	}
}

func TestChatGPTSentinelTokenNeverReachesAnotherAccount(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		headers    http.Header
		bind       func(*chatGPTBoundFixture)
		status     int
		errType    string
	}{
		{"issued by another account than the conversation", chatGPTFollowUpBody, chatGPTSentinelHeaders(chatGPTOtherToken),
			func(f *chatGPTBoundFixture) { f.bindConversation(f.scope, 248); f.bindSentinel(chatGPTOtherToken, 249) }, http.StatusConflict, "integrity_context_unavailable"},
		{"unknown token", chatGPTNewChatBody, chatGPTSentinelHeaders("TEST_ONLY_UNKNOWN"),
			func(*chatGPTBoundFixture) {}, http.StatusConflict, "integrity_context_unavailable"},
		{"token issued for another key", chatGPTNewChatBody, chatGPTSentinelHeaders(chatGPTOtherToken),
			func(f *chatGPTBoundFixture) {
				other := service.ChatGPTTurnScope{UserID: f.scope.UserID, APIKeyID: f.scope.APIKeyID + 1, GroupID: f.gid}
				require.NoError(t, f.cache.BindChatGPTUploadOwner(context.Background(), []string{other.SentinelKey(chatGPTOtherToken)}, 249, time.Minute))
			}, http.StatusConflict, "integrity_context_unavailable"},
		{"repeated headers disagree", chatGPTNewChatBody, chatGPTSentinelHeaders(chatGPTOwnerToken, chatGPTOtherToken),
			func(f *chatGPTBoundFixture) {
				f.bindSentinel(chatGPTOwnerToken, 248)
				f.bindSentinel(chatGPTOtherToken, 249)
			}, http.StatusBadRequest, "invalid_request_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newChatGPTControlFixture(t)
			tc.bind(f)
			f.headers = tc.headers
			w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", tc.body, "")
			require.Equal(t, tc.status, w.Code, w.Body.String())
			errType, _, _ := chatGPTBoundErrorBody(t, w)
			require.Equal(t, tc.errType, errType)
			require.Empty(t, f.calls, "no provider request is sent")
		})
	}
}

func TestChatGPTSentinelConcurrentTurnOnAnotherAccountIsRejected(t *testing.T) {
	f := newChatGPTControlFixture(t)
	f.bindSentinel(chatGPTOwnerToken, 248)
	f.cache.ChatGPTTurnCache = &chatGPTSentinelCacheFault{ChatGPTTurnCache: f.cache.ChatGPTTurnCache,
		raceTurn: &service.ChatGPTTurnSnapshot{AccountID: 249, RequestedModel: "auto", StartedAt: time.Now()}}
	f.headers = chatGPTSentinelHeaders(chatGPTOwnerToken)
	w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTNewChatBody, "")
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Empty(t, f.calls)
}

func TestChatGPTSentinelStoreAndProviderFailures(t *testing.T) {
	t.Run("token lookup fails", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.bindSentinel(chatGPTOwnerToken, 248)
		f.cache.ChatGPTTurnCache = &chatGPTSentinelCacheFault{ChatGPTTurnCache: f.cache.ChatGPTTurnCache, failGet: true}
		f.headers = chatGPTSentinelHeaders(chatGPTOwnerToken)
		w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTNewChatBody, "")
		require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		errType, _, _ := chatGPTBoundErrorBody(t, w)
		require.Equal(t, "integrity_context_unavailable", errType)
		require.Empty(t, f.calls)
	})

	t.Run("token binding fails", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.cache.ChatGPTTurnCache = &chatGPTSentinelCacheFault{ChatGPTTurnCache: f.cache.ChatGPTTurnCache, failBind: true}
		w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
		require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		require.NotContains(t, w.Body.String(), "TEST_ONLY_PREPARE", "an unbound token never reaches the client")
	})

	for _, body := range []string{`{"persona":"TEST_ONLY"}`, `{"prepare_token":"A","prepare_token":"B"}`, `{"prepare_token":""}`, `not json`} {
		t.Run("unrecognized provider response "+body, func(t *testing.T) {
			f := newChatGPTControlFixture(t)
			f.sentinelBody = body
			w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
			require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
		})
	}

	t.Run("provider error is relayed unbound", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.sentinelStatus = http.StatusForbidden
		f.sentinelBody = `{"detail":"TEST_ONLY_DENIED"}`
		w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
		require.Equal(t, http.StatusForbidden, w.Code)
		require.Equal(t, `{"detail":"TEST_ONLY_DENIED"}`, w.Body.String())
	})
}

func TestChatGPTSentinelConflictPointsRetryAtConversationAccount(t *testing.T) {
	f := newChatGPTControlFixture(t)
	f.bindConversation(f.scope, 248)
	f.bindSentinel(chatGPTOtherToken, 249)
	_, err := f.cache.ClaimChatGPTUploadOwner(context.Background(), f.scope.UploadSessionKey(chatGPTBoundDeviceID), 249, time.Minute)
	require.NoError(t, err)
	f.headers = chatGPTSentinelHeaders(chatGPTOtherToken)
	w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTFollowUpBody, "")
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Empty(t, f.calls)
	require.EqualValues(t, 248, f.deviceAffinity(), "the refreshed token must come from the conversation account")

	f.headers = nil
	w, _ = f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	f.headers = chatGPTSentinelHeaders(chatGPTOwnerToken)
	w, _ = f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTFollowUpBody, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{"TEST_ONLY_ACCOUNT", "TEST_ONLY_ACCOUNT"}, f.providerAccounts)
}

func TestChatGPTSentinelCombinesWithOtherOwners(t *testing.T) {
	t.Run("device proof of another account", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.bindSentinel(chatGPTOtherToken, 249)
		f.headers = chatGPTSentinelHeaders(chatGPTOtherToken)
		w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTNewChatBody, chatGPTBoundCookie)
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		require.Empty(t, f.calls)
	})

	t.Run("device proof of the same account", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, chatGPTBoundCookie)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		f.headers = chatGPTSentinelHeaders(chatGPTOwnerToken)
		w, _ = f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTNewChatBody, chatGPTBoundCookie)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, []string{"TEST_ONLY_ACCOUNT", "TEST_ONLY_ACCOUNT"}, f.providerAccounts)
	})

	t.Run("issuing account cooling down", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.bindSentinel(chatGPTOtherToken, 249)
		reset := time.Now().Add(10 * time.Minute)
		f.accounts.accounts[1].RateLimitResetAt = &reset
		f.headers = chatGPTSentinelHeaders(chatGPTOtherToken)
		w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTNewChatBody, "")
		require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
		require.Empty(t, f.calls, "the token is never moved to another account")
	})
}

func TestChatGPTSentinelCoolingAffinity(t *testing.T) {
	t.Run("a new chat moves to an available account", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		_, err := f.cache.ClaimChatGPTUploadOwner(context.Background(), f.scope.UploadSessionKey(chatGPTBoundDeviceID), 249, time.Minute)
		require.NoError(t, err)
		reset := time.Now().Add(10 * time.Minute)
		f.accounts.accounts[1].RateLimitResetAt = &reset
		w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, []string{"TEST_ONLY_ACCOUNT"}, f.providerAccounts)
		require.EqualValues(t, 248, f.deviceAffinity())
		f.headers = chatGPTSentinelHeaders(chatGPTOwnerToken)
		w, _ = f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTNewChatBody, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})

	t.Run("the only live conversation account cooling down is skipped", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		memory := f.cache.ChatGPTTurnCache
		uploads, ok := memory.(service.ChatGPTUploadCache)
		require.True(t, ok)
		f.cache.ChatGPTTurnCache = &chatGPTBoundUpdatesCache{ChatGPTTurnCache: memory, ChatGPTUploadCache: uploads, accounts: []int64{249}}
		for _, cooling := range []bool{false, true} {
			if cooling {
				reset := time.Now().Add(10 * time.Minute)
				f.accounts.accounts[1].RateLimitResetAt = &reset
			}
			f.providerAccounts = nil
			w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			if cooling {
				require.Equal(t, []string{"TEST_ONLY_ACCOUNT"}, f.providerAccounts)
				require.EqualValues(t, 248, f.deviceAffinity())
			} else {
				require.Equal(t, []string{chatGPTControlOtherAccount}, f.providerAccounts, "an available live conversation account is kept")
				require.EqualValues(t, 249, f.deviceAffinity())
			}
		}
	})

	t.Run("a first claim skips the cooling live conversation account", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		memory := f.cache.ChatGPTTurnCache
		uploads, ok := memory.(service.ChatGPTUploadCache)
		require.True(t, ok)
		f.cache.ChatGPTTurnCache = &chatGPTBoundUpdatesCache{ChatGPTTurnCache: memory, ChatGPTUploadCache: uploads, accounts: []int64{249}}
		reset := time.Now().Add(10 * time.Minute)
		f.accounts.accounts[1].RateLimitResetAt = &reset
		w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, []string{"TEST_ONLY_ACCOUNT"}, f.providerAccounts)
		require.EqualValues(t, 248, f.deviceAffinity())
	})

	t.Run("a conversation on a cooling account reports the cooldown", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.bindConversation(f.scope, 248)
		f.bindSentinel(chatGPTOtherToken, 249)
		reset := time.Now().Add(10 * time.Minute)
		f.setOwner(func(a *service.Account) { a.RateLimitResetAt = &reset })
		f.headers = chatGPTSentinelHeaders(chatGPTOtherToken)
		w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTFollowUpBody, "")
		require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
		require.NotEmpty(t, w.Header().Get("Retry-After"))
		require.Empty(t, f.calls)
	})

	t.Run("a cooling device proof owner is never replaced", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		reset := time.Now().Add(10 * time.Minute)
		f.setOwner(func(a *service.Account) { a.RateLimitResetAt = &reset })
		w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, chatGPTBoundCookie)
		require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
		require.Empty(t, f.calls)
		require.EqualValues(t, 248, f.binding())
	})

	t.Run("a conversation on a removed account must start over", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.bindConversation(f.scope, 248)
		f.bindSentinel(chatGPTOtherToken, 249)
		f.setOwner(func(a *service.Account) { a.Status = service.StatusError })
		f.headers = chatGPTSentinelHeaders(chatGPTOtherToken)
		w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", chatGPTFollowUpBody, "")
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		errType, _, _ := chatGPTBoundErrorBody(t, w)
		require.Equal(t, "conversation_context_unavailable", errType)
		require.Empty(t, f.calls)
	})
}

func TestChatGPTSentinelPrepareShapes(t *testing.T) {
	t.Run("compressed 100 KiB response", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		padding := strings.Repeat("x", 100*1024)
		plain := `{"persona":"TEST_ONLY","prepare_token":"TEST_ONLY_PREPARE_GZIP","turnstile":{"required":true,"dx":"` + padding + `"}}`
		var compressed bytes.Buffer
		zw := gzip.NewWriter(&compressed)
		_, err := zw.Write([]byte(plain))
		require.NoError(t, err)
		require.NoError(t, zw.Close())
		f.sentinelBody, f.sentinelEncoding = compressed.String(), "gzip"
		w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, "gzip", w.Header().Get("Content-Encoding"))
		require.Equal(t, compressed.Bytes(), w.Body.Bytes(), "the original bytes are relayed")
		owner, err := f.cache.GetChatGPTUploadOwner(context.Background(), f.scope.SentinelKey("TEST_ONLY_PREPARE_GZIP"))
		require.NoError(t, err)
		require.NotZero(t, owner)
	})

	t.Run("controls never hold model slots or need model billing", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.h.chatGPTBillingSettings = nil
		for _, tc := range []struct {
			body   string
			status int
		}{{"", http.StatusOK}, {`{"persona":"TEST_ONLY"}`, http.StatusBadGateway}, {`{"detail":"TEST_ONLY"}`, http.StatusForbidden}} {
			f.sentinelBody = tc.body
			if tc.status == http.StatusForbidden {
				f.sentinelStatus = tc.status
			}
			w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Empty(t, f.slots.active)
		}
		require.Len(t, f.calls, 3)
	})

	for _, tc := range []struct{ name, body, encoding, message string }{
		{"corrupt gzip", "TEST_ONLY_NOT_GZIP", "gzip", "Chat integrity response is unreadable"},
		{"missing token", `{"persona":"TEST_ONLY"}`, "", "Chat integrity response has no usable prepare token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newChatGPTControlFixture(t)
			f.sentinelBody, f.sentinelEncoding = tc.body, tc.encoding
			w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
			require.Equal(t, http.StatusBadGateway, w.Code)
			_, _, message := chatGPTBoundErrorBody(t, w)
			require.Equal(t, tc.message, message)
			require.Empty(t, w.Header().Get("Content-Encoding"), "provider headers are not relayed on failure")
		})
	}

	t.Run("missing user context", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.noSubject = true
		w, _ := f.run("POST", service.ChatGPTSentinelPreparePath, chatGPTSentinelBody, "")
		require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
		require.Empty(t, f.calls)
	})
}

// chatGPTSentinelCacheFault fails Sentinel ownership reads or writes, or
// simulates a concurrent request that created the turn on another account.
type chatGPTSentinelCacheFault struct {
	service.ChatGPTTurnCache
	failGet, failBind bool
	raceTurn          *service.ChatGPTTurnSnapshot
}

func (c *chatGPTSentinelCacheFault) upload() service.ChatGPTUploadCache {
	cache, ok := c.ChatGPTTurnCache.(service.ChatGPTUploadCache)
	if !ok {
		panic("TEST_ONLY upload cache fixture is missing")
	}
	return cache
}

func isChatGPTSentinelKey(key string) bool { return strings.Contains(key, ":sentinel:") }

func (c *chatGPTSentinelCacheFault) GetChatGPTUploadOwner(ctx context.Context, key string) (int64, error) {
	if c.failGet && isChatGPTSentinelKey(key) {
		return 0, errors.New("TEST_ONLY sentinel store unavailable")
	}
	return c.upload().GetChatGPTUploadOwner(ctx, key)
}

func (c *chatGPTSentinelCacheFault) ClaimChatGPTUploadOwner(ctx context.Context, key string, owner int64, ttl time.Duration) (int64, error) {
	return c.upload().ClaimChatGPTUploadOwner(ctx, key, owner, ttl)
}

func (c *chatGPTSentinelCacheFault) BindChatGPTUploadOwner(ctx context.Context, keys []string, owner int64, ttl time.Duration) error {
	if c.failBind && isChatGPTSentinelKey(keys[0]) {
		return errors.New("TEST_ONLY sentinel store unavailable")
	}
	return c.upload().BindChatGPTUploadOwner(ctx, keys, owner, ttl)
}

func (c *chatGPTSentinelCacheFault) ReleaseChatGPTUploadOwner(ctx context.Context, key string, owner int64) error {
	return c.upload().ReleaseChatGPTUploadOwner(ctx, key, owner)
}

func (c *chatGPTSentinelCacheFault) SetChatGPTUploadOwner(ctx context.Context, key string, owner int64, ttl time.Duration) error {
	return c.upload().SetChatGPTUploadOwner(ctx, key, owner, ttl)
}

func (c *chatGPTSentinelCacheFault) GetChatGPTTurn(ctx context.Context, key string) (*service.ChatGPTTurnSnapshot, error) {
	if c.raceTurn != nil {
		return nil, nil
	}
	return c.ChatGPTTurnCache.GetChatGPTTurn(ctx, key)
}

func (c *chatGPTSentinelCacheFault) PutChatGPTTurnIfAbsent(ctx context.Context, key string, turn *service.ChatGPTTurnSnapshot, ttl time.Duration) (*service.ChatGPTTurnSnapshot, error) {
	if c.raceTurn != nil {
		return c.raceTurn, nil
	}
	return c.ChatGPTTurnCache.PutChatGPTTurnIfAbsent(ctx, key, turn, ttl)
}
