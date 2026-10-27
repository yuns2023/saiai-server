package middleware

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clientproof"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ClientProofVerifier computes the observe-mode client proof status.
type ClientProofVerifier interface {
	VerifyProof(ctx context.Context, req service.ClientProofRequest) clientproof.Status
}

const contextKeyClientAttestation = "client_attestation_status"

// ClientProof evaluates X-Saiai-Client-Proof (docs/CLIENT_PROOF_V1.md). It
// must run after API-key authentication. It never rejects:
//
//   - The header is always removed from the inbound request, so no upstream
//     path can copy it (each upstream header filter also denies it).
//   - A present header is always verified, whatever the authenticated
//     group's policy (smart switching or fallbacks may move the request to
//     an observe group later). A request without the header does no I/O and
//     is "missing". Usage rows persist the status only when the final group
//     collects attestation (ClientAttestationForUsage), else NULL.
//
// The signed target is c.Request.RequestURI, the raw request-line target set
// by net/http. No gateway middleware rewrites it (InboundEndpointMiddleware
// only stores a normalized copy in the context).
func ClientProof(verifier ClientProofVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		values := c.Request.Header.Values(clientproof.HeaderName)
		c.Request.Header.Del(clientproof.HeaderName)

		apiKey, ok := GetAPIKeyFromContext(c)
		if !ok || apiKey == nil {
			c.Next()
			return
		}
		var status clientproof.Status
		switch {
		case len(values) == 0:
			status = clientproof.StatusMissing
		case len(values) > 1:
			status = clientproof.StatusInvalid
		case verifier == nil:
			status = clientproof.StatusUnverified
		default:
			status = verifier.VerifyProof(c.Request.Context(), service.ClientProofRequest{
				Header:   values[0],
				Method:   c.Request.Method,
				Target:   clientProofTarget(c),
				APIKey:   bearerToken(c),
				UserID:   apiKey.UserID,
				ClientIP: ip.GetTrustedClientIP(c),
				Surface:  service.ClientSurfaceForPath(c.Request.URL.Path),
			})
		}
		SetClientAttestation(c, status)
		c.Next()
	}
}

func clientProofTarget(c *gin.Context) string {
	if c.Request.RequestURI != "" {
		return c.Request.RequestURI
	}
	// Only synthetic requests (never net/http server requests) lack it.
	return c.Request.URL.RequestURI()
}

// bearerToken returns the Authorization Bearer value exactly as API-key
// authentication parses it (scheme case-insensitive, value trimmed). The
// contract signs this raw header value, never x-api-key / x-goog-api-key or
// a key resolved by authentication; without a Bearer token the hash input
// is empty, so any proof sent alongside x-api-key alone is invalid.
func bearerToken(c *gin.Context) string {
	parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

// SetClientAttestation stores the request's client proof status.
func SetClientAttestation(c *gin.Context, status clientproof.Status) {
	c.Set(contextKeyClientAttestation, status)
}

// GetClientAttestation returns the request's status for usage recording, or
// nil when the status was not collected.
func GetClientAttestation(c *gin.Context) *int16 {
	if c == nil {
		return nil
	}
	value, ok := c.Get(contextKeyClientAttestation)
	if !ok {
		return nil
	}
	status, ok := value.(clientproof.Status)
	if !ok || !status.Known() {
		return nil
	}
	out := int16(status)
	return &out
}
