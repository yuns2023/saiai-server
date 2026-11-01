package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const (
	chatGPTControlConversation = "TEST_ONLY_CONVERSATION"
	chatGPTControlBody         = `{"conversation_id":"TEST_ONLY_CONVERSATION","gizmo_id":null,"conversation_origin":null}`
	chatGPTControlOtherAccount = "TEST_ONLY_OTHER_ACCOUNT"
)

// The scheduler stores group stickies under an "openai:" prefix.
var chatGPTControlStickyKey = "openai:" + service.ChatGPTConversationSessionHash(chatGPTControlConversation)

var chatGPTControlPaths = []string{
	"/chatgpt/backend-api/conversation/init",
	"/chatgpt/backend-api/f/conversation/prepare",
	"/chatgpt/backend-api/sentinel/chat-requirements/prepare",
}

// newChatGPTControlFixture adds a second healthy account (249) to the group
// and pins the scheduler sticky for the conversation to it, which is the
// production state that sent conversation/init to a non-owner account.
func newChatGPTControlFixture(t *testing.T) *chatGPTBoundFixture {
	f := newChatGPTBoundFixture(t)
	other := f.accounts.accounts[0]
	other.ID = 249
	other.Credentials = map[string]any{"access_token": "TEST_ONLY_OTHER_OAUTH", "chatgpt_account_id": chatGPTControlOtherAccount}
	f.accounts.accounts = append(f.accounts.accounts, other)
	f.cache.bindings[chatGPTControlStickyKey] = 249
	return f
}

func (f *chatGPTBoundFixture) bindConversation(scope service.ChatGPTTurnScope, accountID int64) {
	require.NoError(f.t, f.cache.BindChatGPTDeliveryOwner(context.Background(), scope.ConversationOwnerKey(chatGPTControlConversation), &service.ChatGPTDeliveryOwner{AccountID: accountID}))
}

func TestChatGPTControlRequestsForExistingConversationUseOwner(t *testing.T) {
	paths := append([]string{"/chatgpt/backend-api/f/conversation/TEST_ONLY_FUTURE"}, chatGPTControlPaths...)
	for _, path := range paths {
		for _, mode := range []struct {
			name              string
			updates, noDevice bool
		}{{"default", false, false}, {"updates enabled without device", true, true}} {
			t.Run(path+" "+mode.name, func(t *testing.T) {
				f := newChatGPTControlFixture(t)
				f.h.cfg.Gateway.OpenAIChatUpdatesEnabled = mode.updates
				f.noDevice = mode.noDevice
				f.bindConversation(f.scope, 248)
				w, c := f.run("POST", path, chatGPTControlBody, "")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.Equal(t, []string{"/backend-api" + path[len("/chatgpt/backend-api"):]}, f.calls)
				require.Equal(t, []string{"TEST_ONLY_ACCOUNT"}, f.providerAccounts, "the owner, not the sticky account, serves the request")
				selected, _ := c.Get(opsAccountIDKey)
				require.EqualValues(t, 248, selected)
				require.EqualValues(t, 249, f.cache.bindings[chatGPTControlStickyKey], "owner routing does not rewrite the scheduler sticky")
			})
		}
	}
}

func TestChatGPTControlRequestRecoversOwnerFromLiveResume(t *testing.T) {
	f := newChatGPTControlFixture(t)
	ctx := context.Background()
	turnKey := f.scope.TurnKey(service.ChatGPTTurnBillingIdentity{RequestID: "TEST_ONLY_TURN"})
	_, err := f.cache.PutChatGPTTurnIfAbsent(ctx, turnKey, &service.ChatGPTTurnSnapshot{AccountID: 248, StartedAt: time.Now()}, time.Hour)
	require.NoError(t, err)
	require.NoError(t, f.cache.BindChatGPTUpdates(ctx, f.scope.ResumeKey(chatGPTControlConversation), turnKey, f.scope.UpdatesKey()))
	owner, err := f.cache.GetChatGPTDeliveryOwner(ctx, f.scope.ConversationOwnerKey(chatGPTControlConversation))
	require.NoError(t, err)
	require.Nil(t, owner, "only the live turn knows the account")

	w, _ := f.run("POST", "/chatgpt/backend-api/conversation/init", chatGPTControlBody, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{"TEST_ONLY_ACCOUNT"}, f.providerAccounts)
	owner, err = f.cache.GetChatGPTDeliveryOwner(ctx, f.scope.ConversationOwnerKey(chatGPTControlConversation))
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.EqualValues(t, 248, owner.AccountID, "the live turn becomes the durable owner")
}

func TestChatGPTControlRequestsNeverScheduleAnUnownedConversation(t *testing.T) {
	t.Run("missing owner", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		w, _ := f.run("POST", "/chatgpt/backend-api/conversation/init", chatGPTControlBody, "")
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		errType, _, message := chatGPTBoundErrorBody(t, w)
		require.Equal(t, "conversation_context_unavailable", errType)
		require.Contains(t, message, "start a new conversation")
		require.Empty(t, f.calls)
	})

	t.Run("owner bound to another key", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.bindConversation(service.ChatGPTTurnScope{UserID: f.scope.UserID, APIKeyID: f.scope.APIKeyID + 1, GroupID: f.gid}, 248)
		w, _ := f.run("POST", "/chatgpt/backend-api/conversation/init", chatGPTControlBody, "")
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		require.Empty(t, f.calls)
	})

	t.Run("ownership store failure", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.bindConversation(f.scope, 248)
		f.cache.ChatGPTTurnCache = chatGPTFailingResumeCache{f.cache.ChatGPTTurnCache}
		w, _ := f.run("POST", "/chatgpt/backend-api/conversation/init", chatGPTControlBody, "")
		require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		errType, _, _ := chatGPTBoundErrorBody(t, w)
		require.Equal(t, "accounting_unavailable", errType)
		require.Empty(t, f.calls)
	})
}

func TestChatGPTControlRequestOwnerUnavailableKeepsConversation(t *testing.T) {
	t.Run("temporary", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.bindConversation(f.scope, 248)
		reset := time.Now().Add(10 * time.Minute)
		f.setOwner(func(a *service.Account) { a.RateLimitResetAt = &reset })
		w, c := f.run("POST", "/chatgpt/backend-api/conversation/init", chatGPTControlBody, chatGPTBoundCookie)
		require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
		require.NotEmpty(t, w.Header().Get("Retry-After"))
		require.Empty(t, f.calls)
		require.Contains(t, chatGPTBoundOpsDetail(c), "bound context conversation")
		require.Empty(t, w.Header().Values("Set-Cookie"))
		require.EqualValues(t, 248, f.binding(), "a cooldown keeps the device proof")
	})

	for _, tc := range []struct {
		name, reason string
		mutate       func(*service.Account)
	}{
		{"disabled", service.ChatGPTBoundReasonInactive, func(a *service.Account) { a.Status = service.StatusError }},
		{"moved out of group", service.ChatGPTBoundReasonNotInGroup, func(a *service.Account) { a.GroupIDs = []int64{a.GroupIDs[0] + 1} }},
	} {
		t.Run("permanent: "+tc.name, func(t *testing.T) {
			f := newChatGPTControlFixture(t)
			f.bindConversation(f.scope, 248)
			f.setOwner(tc.mutate)
			w, c := f.run("POST", "/chatgpt/backend-api/conversation/init", chatGPTControlBody, "")
			require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
			errType, _, _ := chatGPTBoundErrorBody(t, w)
			require.Equal(t, "conversation_context_unavailable", errType)
			require.Contains(t, chatGPTBoundOpsDetail(c), tc.reason)
			require.Empty(t, f.calls, "the healthy sticky account is never used instead")
		})
	}

	t.Run("permanent with the owner's device proof", func(t *testing.T) {
		f := newChatGPTControlFixture(t)
		f.bindConversation(f.scope, 248)
		f.setOwner(func(a *service.Account) { a.Schedulable = false })
		w, _ := f.run("POST", "/chatgpt/backend-api/conversation/init", chatGPTControlBody, chatGPTBoundCookie)
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		require.Equal(t, []string{clearChatGPTDeviceCookieHeader}, w.Header().Values("Set-Cookie"))
		require.Zero(t, f.binding())
		require.Empty(t, f.calls)
	})

	for _, tc := range []struct{ name, body string }{
		{"no conversation id", `{"gizmo_id":null}`},
		{"null conversation id", `{"conversation_id":null}`},
	} {
		t.Run("new conversation is scheduled: "+tc.name, func(t *testing.T) {
			f := newChatGPTControlFixture(t)
			w, _ := f.run("POST", "/chatgpt/backend-api/conversation/init", tc.body, "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Len(t, f.calls, 1)
		})
	}
}

func TestChatGPTControlRequestDeviceAndConversationOwnersMustAgree(t *testing.T) {
	f := newChatGPTControlFixture(t)
	f.bindConversation(f.scope, 249) // the device proof belongs to 248
	w, _ := f.run("POST", "/chatgpt/backend-api/conversation/init", chatGPTControlBody, chatGPTBoundCookie)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	errType, _, _ := chatGPTBoundErrorBody(t, w)
	require.Equal(t, "integrity_context_unavailable", errType)
	require.Empty(t, f.calls)
	require.EqualValues(t, 248, f.binding(), "a conflict does not release the device proof")

	agreeing := newChatGPTControlFixture(t)
	agreeing.bindConversation(agreeing.scope, 248)
	w, _ = agreeing.run("POST", "/chatgpt/backend-api/conversation/init", chatGPTControlBody, chatGPTBoundCookie)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{"TEST_ONLY_ACCOUNT"}, agreeing.providerAccounts)
}

type chatGPTFailingResumeCache struct{ service.ChatGPTTurnCache }

func (chatGPTFailingResumeCache) GetChatGPTResume(context.Context, string) (*service.ChatGPTTurnSnapshot, error) {
	return nil, errors.New("turn store unavailable")
}
