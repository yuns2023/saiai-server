package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clientproof"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The Gateway middleware already deletes X-Saiai-Client-Proof from the
// inbound request; every upstream header builder must also refuse it.
func TestClientProofHeaderIsNeverCopiedUpstream(t *testing.T) {
	for _, name := range []string{clientproof.HeaderName, "x-saiai-client-proof", "X-SAIAI-CLIENT-PROOF"} {
		require.False(t, shouldCopyOpenAIRequestHeader(name), name)
		require.False(t, shouldCopyOpenAIWSRequestHeader(name), name)
		require.False(t, shouldCopyOpenAINativeRelayWSHeader(name), name)
	}
	require.False(t, allowedHeaders["x-saiai-client-proof"], "Claude OAuth/API-key allowlist")

	src := http.Header{}
	src.Set(clientproof.HeaderName, "v1.x")
	src.Set("X-Keep", "1")
	for _, websocket := range []bool{false, true} {
		dst := http.Header{}
		copyOpenAINativeRequestHeaders(dst, src, websocket)
		require.Empty(t, dst.Get(clientproof.HeaderName))
		require.Equal(t, "1", dst.Get("X-Keep"))
	}

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	c.Request.Header.Set(clientproof.HeaderName, "v1.x")
	c.Request.Header.Set("X-Keep", "1")
	req := httptest.NewRequest(http.MethodPost, "https://upstream.invalid/v1/messages", nil)
	copyAnthropicPassthroughRequest(req, c)
	require.Empty(t, req.Header.Get(clientproof.HeaderName), "Anthropic API-key passthrough")
	require.Equal(t, "1", req.Header.Get("X-Keep"))

	relay := (&OpenAIGatewayService{nativeRelayInstanceID: "relay-a"}).buildOpenAINativeRelayWSHeaders(c, "next-hop-key")
	require.Empty(t, relay.Get(clientproof.HeaderName), "native relay WS dial")
}

func TestOpenAIRecordUsage_ClientAttestationFollowsGroupPolicy(t *testing.T) {
	record := func(policy string) *UsageLog {
		usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
		svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
		status := int16(clientproof.StatusReplay)
		groupID := int64(12)
		err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
			Result: &OpenAIForwardResult{RequestID: "resp_attestation_" + policy, Model: "gpt-5.4", Duration: time.Second,
				Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 5}},
			APIKey:            &APIKey{ID: 1101, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI, ClientAttestationPolicy: policy, RateMultiplier: 1}},
			User:              &User{ID: 2101},
			Account:           &Account{ID: 3101},
			ClientAttestation: &status,
		})
		require.NoError(t, err)
		require.NotNil(t, usageRepo.lastLog)
		return usageRepo.lastLog
	}
	require.Nil(t, record(ClientAttestationPolicyOff).ClientAttestation)
	got := record(ClientAttestationPolicyObserve).ClientAttestation
	require.NotNil(t, got)
	require.Equal(t, int16(clientproof.StatusReplay), *got)
}
