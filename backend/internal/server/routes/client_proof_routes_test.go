package routes

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/clientproof"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type routeProofRepo struct {
	service.ClientDeviceRepository
	device *service.ClientDevice
}

func (r *routeProofRepo) GetByDeviceID(_ context.Context, id string) (*service.ClientDevice, error) {
	if r.device.DeviceID != id {
		return nil, nil
	}
	copied := *r.device
	return &copied, nil
}

func (r *routeProofRepo) Touch(context.Context, int64, service.ClientDeviceTouch) error { return nil }

type routeProofNonces struct{ claims []string }

func (n *routeProofNonces) Claim(_ context.Context, _ string, nonce string, _ time.Duration) (bool, error) {
	n.claims = append(n.claims, nonce)
	return true, nil
}

// Every authenticated gateway route the local proxy uses must run the proof
// middleware after API-key auth and verify against the raw request target.
// A nonce is claimed only when the signature over c.Request.RequestURI
// verified, so each claim proves both placement and target fidelity. The
// group platform is retired so rejectRetiredGroupPlatform (which runs right
// after the proof middleware) ends the request before any handler.
func TestClientProofMiddlewareCoversGatewayRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	raw, err := priv.PublicKey.Bytes()
	require.NoError(t, err)
	deviceID, err := clientproof.NewDeviceID()
	require.NoError(t, err)
	nonces := &routeProofNonces{}
	devices := service.NewClientDeviceService(&routeProofRepo{device: &service.ClientDevice{ID: 1, DeviceID: deviceID, UserID: 7, PublicKey: raw}}, nonces)

	groupID := int64(5)
	var proofSeenByAuth string
	router := gin.New()
	RegisterGatewayRoutes(router,
		&handler.Handlers{
			Client:        handler.NewClientHandler(handler.BuildInfo{}, devices),
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			proofSeenByAuth = c.GetHeader(clientproof.HeaderName)
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 3, UserID: 7, GroupID: &groupID,
				Group: &service.Group{ID: groupID, Platform: service.PlatformSora, Status: service.StatusActive, Hydrated: true,
					ClientAttestationPolicy: service.ClientAttestationPolicyObserve}})
			c.Next()
		}),
		nil, nil, nil, nil, &config.Config{},
	)

	cases := []struct{ method, target string }{
		{http.MethodPost, "/v1/messages?beta=true"},
		{http.MethodPost, "/v1/messages/count_tokens?beta=true"},
		{http.MethodGet, "/v1/models?client_version=0.164.0"},
		{http.MethodPost, "/v1/responses"},
		{http.MethodPost, "/v1/responses/compact"},
		{http.MethodGet, "/v1/responses"},
		{http.MethodPost, "/responses"},
		{http.MethodPost, "/responses/compact"},
		{http.MethodGet, "/responses"},
		{http.MethodGet, "/backend-api/codex/models?client_version=0.164.0&q=%2Fx%20y"},
		{http.MethodGet, "/chatgpt/backend-api/models"},
		{http.MethodPost, "/chatgpt/backend-api/f/conversation"},
		{http.MethodPost, "/chatgpt/api/estuary/upload_content_bytes"},
	}
	for i, tc := range cases {
		nonce, err := clientproof.NewNonce()
		require.NoError(t, err)
		header, err := clientproof.Sign(priv, deviceID, tc.method, tc.target, time.Now().Unix(), nonce, "sk-route")
		require.NoError(t, err)
		req := httptest.NewRequest(tc.method, tc.target, nil)
		req.Header.Set("Authorization", "Bearer sk-route")
		req.Header.Set(clientproof.HeaderName, header)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusGone, w.Code, "%s %s", tc.method, tc.target)
		require.Equal(t, header, proofSeenByAuth, "auth runs before the proof middleware")
		require.Len(t, nonces.claims, i+1, "proof verified for %s %s", tc.method, tc.target)
	}
}
