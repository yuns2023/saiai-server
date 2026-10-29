//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestOpenAINativeTurnStateDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, reason, source string
		values               []string
		bind, other, shared  bool
	}{
		{"unknown", "ownership_unverified", "miss_local_fallback_miss", []string{"MOCK_PRIVATE_STATE"}, false, false, false},
		{"wrong_owner", "owner_mismatch", "local_owner", []string{"MOCK_PRIVATE_STATE"}, true, true, false},
		{"shared_wrong_owner", "owner_mismatch", "shared_owner", []string{"MOCK_PRIVATE_STATE"}, true, true, true},
		{"empty", "empty_value", "not_checked", []string{""}, false, false, false},
		{"multiple", "multiple_values", "not_checked", []string{"MOCK_PRIVATE_STATE", "MOCK_SECOND_STATE"}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "mock_request")))
			svc := auditOAuthPolicyService(nil)
			cache := &stubGatewayCache{}
			if tc.shared {
				svc.cache = cache
			}
			account := auditOAuthPolicyAccount()
			if tc.bind {
				svc.bindOpenAINativeTurnState(account, 55, "MOCK_PRIVATE_SESSION", "MOCK_PRIVATE_STATE")
			}
			if tc.shared {
				svc = &OpenAIGatewayService{cache: cache}
			}
			if tc.other {
				account.ID++
			}
			headers := make(http.Header)
			headers[http.CanonicalHeaderKey(openAIWSTurnStateHeader)] = tc.values
			headers.Set("Authorization", "MOCK_PRIVATE_CREDENTIAL")
			err := svc.validateOpenAINativeTurnState(ctx, account, 55, "MOCK_PRIVATE_SESSION", headers)
			require.ErrorIs(t, err, errOpenAITurnStateAccountMismatch)
			require.Equal(t, errOpenAITurnStateAccountMismatch.Error(), err.Error())
			require.Equal(t, 1, logs.Len())
			entry := logs.All()[0]
			fields := entry.ContextMap()
			require.Equal(t, "openai.turn_state_rejected", entry.Message)
			require.Equal(t, "mock_request", fields["request_id"])
			require.Equal(t, tc.reason, fields["turn_state_reason"])
			require.Equal(t, tc.source, fields["turn_state_cache_source"])
			encoded, e := json.Marshal(fields)
			require.NoError(t, e)
			for _, private := range []string{"MOCK_PRIVATE_STATE", "MOCK_SECOND_STATE", "MOCK_PRIVATE_SESSION", "MOCK_PRIVATE_CREDENTIAL"} {
				require.NotContains(t, string(encoded), private)
			}
		})
	}
	require.NotEqual(t, openAITurnStateAccountKey(55, "MOCK_PRIVATE_STATE"), openAITurnStateAccountKey(56, "MOCK_PRIVATE_STATE"))
}

// Advance stored deadlines deterministically: no sleeps or provider traffic.
func TestOpenAINativeTurnStateContinuousUseDoesNotRenewOwnership(t *testing.T) {
	svc := auditOAuthPolicyService(nil)
	account := auditOAuthPolicyAccount()
	const state = "MOCK_LONG_LIVED_STATE"
	const session = "MOCK_LONG_SESSION"
	svc.bindOpenAINativeTurnState(account, 55, session, state)
	store := svc.getOpenAIWSStateStore().(*defaultOpenAIWSStateStore)
	key := openAITurnStateAccountKey(55, state)
	initial := store.responseToAccount[key].expiresAt
	require.InDelta(t, (24 * time.Hour).Seconds(), time.Until(initial).Seconds(), 2)
	headers := make(http.Header)
	headers.Set(openAIWSTurnStateHeader, state)
	for range 3 {
		require.NoError(t, svc.validateOpenAINativeTurnState(context.Background(), account, 55, session, headers))
		require.Equal(t, initial, store.responseToAccount[key].expiresAt)
	}
	expired := time.Now().Add(-time.Second)
	binding := store.responseToAccount[key]
	binding.expiresAt = expired
	store.responseToAccount[key] = binding
	for k, b := range store.sessionToTurnState {
		b.expiresAt = expired
		store.sessionToTurnState[k] = b
	}
	core, logs := observer.New(zap.WarnLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	require.ErrorIs(t, svc.validateOpenAINativeTurnState(ctx, account, 55, session, headers), errOpenAITurnStateAccountMismatch)
	require.Equal(t, "local_expired_local_fallback_miss", logs.All()[0].ContextMap()["turn_state_cache_source"])
	// A new provider observation restores ownership; incoming use alone does not.
	svc.bindOpenAINativeTurnState(account, 55, session, state)
	require.NoError(t, svc.validateOpenAINativeTurnState(ctx, account, 55, session, headers))
}

// The repository classifies absent keys independently of its backend; distinguish that from
// an outage without exposing potentially sensitive backend error messages.
type turnStateDiagnosticCache struct {
	stubGatewayCache
	lookupErr error
}

func (c *turnStateDiagnosticCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, c.lookupErr
}

func TestOpenAINativeTurnStateSharedMissAndOutage(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		err          error
	}{
		{"miss", "miss_local_fallback_miss", ErrGatewaySessionCacheMiss},
		{"outage", "shared_lookup_error_local_fallback_miss", errors.New("MOCK_PRIVATE_BACKEND_ERROR")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			ctx := logger.IntoContext(context.Background(), zap.New(core))
			svc := &OpenAIGatewayService{cache: &turnStateDiagnosticCache{lookupErr: tc.err}}
			account := auditOAuthPolicyAccount()
			headers := make(http.Header)
			headers.Set(openAIWSTurnStateHeader, "MOCK_STATE")
			require.ErrorIs(t, svc.validateOpenAINativeTurnState(ctx, account, 55, "MOCK_SESSION", headers), errOpenAITurnStateAccountMismatch)
			fields := logs.All()[0].ContextMap()
			require.Equal(t, tc.source, fields["turn_state_cache_source"])
			encoded, err := json.Marshal(fields)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "MOCK_PRIVATE_BACKEND_ERROR")
			// A verified local fallback still works when shared ownership is absent.
			svc.getOpenAIWSStateStore().BindSessionTurnState(0, openAINativeTurnStateKey(55, account.ID, "MOCK_SESSION", "MOCK_STATE"), "MOCK_STATE", time.Minute)
			require.NoError(t, svc.validateOpenAINativeTurnState(ctx, account, 55, "MOCK_SESSION", headers))
			require.Equal(t, 1, logs.Len())
		})
	}
}

func TestOpenAINativeTurnStateMalformedMetadataDiagnostics(t *testing.T) {
	for _, transport := range []string{"http", "frame"} {
		t.Run(transport, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			c, rec := auditOAuthPolicyContext("/v1/responses")
			c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core)))
			payload := []byte(`{"model":"gpt-6.1-sol","client_metadata":{"x-codex-turn-state":{"private":"MOCK_PRIVATE_BODY"}}}`)
			upstream := &httpUpstreamSequenceRecorder{}
			svc := auditOAuthPolicyService(upstream)
			var err error
			if transport == "http" {
				_, err = svc.Forward(c.Request.Context(), c, auditOAuthPolicyAccount(), payload)
				require.Equal(t, http.StatusConflict, rec.Code)
			} else {
				err = svc.validateOpenAINativeFrameTurnState(c, nil, payload)
			}
			require.ErrorIs(t, err, errOpenAITurnStateAccountMismatch)
			require.Zero(t, upstream.callCount)
			require.Equal(t, 1, logs.Len())
			fields := logs.All()[0].ContextMap()
			require.Equal(t, "non_string_"+transport+"_metadata", fields["turn_state_reason"])
			encoded, e := json.Marshal(fields)
			require.NoError(t, e)
			require.NotContains(t, string(encoded), "MOCK_PRIVATE_BODY")
		})
	}
}
