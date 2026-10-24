package middleware

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPTraceOmitsModerationEvidenceBodies(t *testing.T) {
	require.True(t, traceOmitsResponseBody("/api/v1/admin/usage/input-moderation-events/5/evidence"))
	require.False(t, traceOmitsResponseBody("/api/v1/admin/usage/input-risk-alerts"))
}
