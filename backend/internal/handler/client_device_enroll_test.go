package handler

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// enrollRepoFake models the repository contract: idempotent per
// (user, key hash), revoked keys stay revoked, per-user non-revoked limit.
type enrollRepoFake struct {
	service.ClientDeviceRepository
	mu      sync.Mutex
	devices []*service.ClientDevice
}

func (f *enrollRepoFake) Enroll(_ context.Context, in service.ClientDeviceEnrollInput, limit int) (*service.ClientDevice, service.ClientDeviceEnrollOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	active := 0
	for _, d := range f.devices {
		if d.UserID != in.UserID {
			continue
		}
		if d.PublicKeySHA256 == in.PublicKeySHA256 {
			if d.RevokedAt != nil {
				return d, service.ClientDeviceEnrollRevoked, nil
			}
			return d, service.ClientDeviceEnrollExisting, nil
		}
		if d.RevokedAt == nil {
			active++
		}
	}
	if active >= limit {
		return nil, service.ClientDeviceEnrollLimit, nil
	}
	d := &service.ClientDevice{ID: int64(len(f.devices) + 1), DeviceID: in.DeviceID, UserID: in.UserID, PublicKey: in.PublicKey, PublicKeySHA256: in.PublicKeySHA256}
	f.devices = append(f.devices, d)
	return d, service.ClientDeviceEnrollCreated, nil
}

func newEnrollRouter(repo *enrollRepoFake, userID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewClientHandler(BuildInfo{}, service.NewClientDeviceService(repo, nil))
	router.POST("/enroll", func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 3, UserID: userID, User: &service.User{ID: userID}})
		c.Next()
	}, h.EnrollDevice)
	return router
}

func newEnrollPublicKey(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	raw, err := priv.PublicKey.Bytes()
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func postEnroll(router *gin.Engine, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/enroll", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func enrollBody(publicKey string) string {
	return `{"public_key":"` + publicKey + `","client_version":"1.1.54","os":"windows","arch":"x86_64"}`
}

func TestClientDeviceEnrollIsIdempotent(t *testing.T) {
	repo := &enrollRepoFake{}
	router := newEnrollRouter(repo, 7)
	key := newEnrollPublicKey(t)

	first := postEnroll(router, enrollBody(key))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var resp ClientDeviceEnrollResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &resp))
	require.Equal(t, "active", resp.Status)
	require.Regexp(t, `^cd_[A-Za-z0-9_-]{22}$`, resp.DeviceID)
	require.JSONEq(t, `{"device_id":"`+resp.DeviceID+`","status":"active"}`, first.Body.String(), "flat contract body, no envelope")

	second := postEnroll(router, enrollBody(key))
	require.Equal(t, http.StatusOK, second.Code)
	require.JSONEq(t, first.Body.String(), second.Body.String(), "same key → same device")
	require.Len(t, repo.devices, 1)

	// The same key enrolled by another user is a different device.
	other := postEnroll(newEnrollRouter(repo, 8), enrollBody(key))
	require.Equal(t, http.StatusOK, other.Code)
	require.NotEqual(t, first.Body.String(), other.Body.String())
}

func TestClientDeviceEnrollRevokedKey(t *testing.T) {
	repo := &enrollRepoFake{}
	router := newEnrollRouter(repo, 7)
	key := newEnrollPublicKey(t)
	require.Equal(t, http.StatusOK, postEnroll(router, enrollBody(key)).Code)
	now := time.Now()
	repo.devices[0].RevokedAt = &now

	w := postEnroll(router, enrollBody(key))
	require.Equal(t, http.StatusForbidden, w.Code)
	require.JSONEq(t, `{"code":"client_device_revoked"}`, w.Body.String())
}

func TestClientDeviceEnrollLimit(t *testing.T) {
	repo := &enrollRepoFake{}
	router := newEnrollRouter(repo, 7)
	for i := 0; i < service.ClientDeviceLimit; i++ {
		require.Equal(t, http.StatusOK, postEnroll(router, enrollBody(newEnrollPublicKey(t))).Code)
	}
	w := postEnroll(router, enrollBody(newEnrollPublicKey(t)))
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.JSONEq(t, `{"code":"client_device_limit"}`, w.Body.String())

	// Revoked devices do not count toward the limit.
	now := time.Now()
	repo.devices[0].RevokedAt = &now
	require.Equal(t, http.StatusOK, postEnroll(router, enrollBody(newEnrollPublicKey(t))).Code)
}

func TestClientDeviceEnrollValidation(t *testing.T) {
	router := newEnrollRouter(&enrollRepoFake{}, 7)
	key := newEnrollPublicKey(t)
	for name, body := range map[string]string{
		"not json":         `{`,
		"missing key":      `{"client_version":"1"}`,
		"bad key":          enrollBody("AAAA"),
		"long version":     `{"public_key":"` + key + `","client_version":"` + strings.Repeat("v", 65) + `"}`,
		"long os":          `{"public_key":"` + key + `","os":"` + strings.Repeat("o", 33) + `"}`,
		"long arch":        `{"public_key":"` + key + `","arch":"` + strings.Repeat("a", 33) + `"}`,
		"body over 4 KiB":  `{"public_key":"` + key + `","pad":"` + strings.Repeat("p", 4096) + `"}`,
		"std base64 key":   enrollBody(strings.NewReplacer("-", "+", "_", "/").Replace(key) + "="),
		"compressed point": enrollBody(base64.RawURLEncoding.EncodeToString(append([]byte{0x02}, make([]byte, 32)...))),
	} {
		w := postEnroll(router, body)
		require.Equal(t, http.StatusBadRequest, w.Code, name)
		require.Contains(t, w.Body.String(), `"code":"invalid_request"`, name)
	}
	ok := `{"public_key":"` + key + `","client_version":"` + strings.Repeat("v", 64) + `","os":"` + strings.Repeat("o", 32) + `","arch":"` + strings.Repeat("a", 32) + `"}`
	require.Equal(t, http.StatusOK, postEnroll(router, ok).Code, "limits are inclusive")
}
