//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Ops may reclassify an untyped error only when the client received the
// upstream body unchanged; rewritten capacity errors must not be marked.
func TestOpenAIErrorRelayMarksOnlyUnchangedUpstreamBodies(t *testing.T) {
	detail := `{"detail":"Input must be a list"}`
	capacity := `{"error":{"type":"usage_limit_reached","plan_type":"plus","resets_at":123}}`
	for _, tc := range []struct {
		name, payload string
		status        int
		native        bool
		wantMarked    bool
	}{
		{"oauth_detail", detail, 400, false, true},
		{"oauth_capacity_rewrite", capacity, 429, false, false},
		{"native_relay_detail", detail, 400, true, true},
		{"native_relay_capacity_rewrite", capacity, 429, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, recorder := auditOAuthPolicyContext("/v1/responses")
			svc := &OpenAIGatewayService{}
			resp := &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.payload))}
			var err error
			if tc.native {
				_, err = svc.handleNativeRelayErrorResponse(resp, ctx, newOpenAINativeRelayAccount("https://relay.example"), []byte(tc.payload))
			} else {
				_, err = svc.handleErrorResponse(context.Background(), resp, ctx, auditOAuthPolicyAccount(), nil)
			}
			require.Error(t, err)
			_, marked := ctx.Get(OpsUpstreamBodyRelayedKey)
			require.Equal(t, tc.wantMarked, marked)
			if tc.wantMarked {
				require.Equal(t, tc.status, ctx.GetInt(OpsUpstreamBodyRelayedKey))
				require.Equal(t, tc.status, recorder.Code)
				require.Equal(t, tc.payload, recorder.Body.String())
			}
		})
	}
}
