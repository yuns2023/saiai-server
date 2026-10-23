//go:build unit

package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

type smartAuthKeyRepo struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r smartAuthKeyRepo) GetByID(context.Context, int64) (*service.APIKey, error) { return r.key, nil }
func (r smartAuthKeyRepo) GetByKeyForAuth(context.Context, string) (*service.APIKey, error) {
	return r.key, nil
}
func (r smartAuthKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type smartAuthUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r smartAuthUserRepo) GetByID(context.Context, int64) (*service.User, error) { return r.user, nil }

type smartAuthGroupRepo struct {
	service.GroupRepository
	groups map[int64]*service.Group
}

func (r smartAuthGroupRepo) GetByID(_ context.Context, id int64) (*service.Group, error) {
	return r.groups[id], nil
}

func (r smartAuthGroupRepo) GetByIDLite(ctx context.Context, id int64) (*service.Group, error) {
	return r.GetByID(ctx, id)
}

func TestSmartSwitchAuthUsesActualBillingContextAndPreservesWireBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "zstd"}[compressed], func(t *testing.T) {
			now := time.Now()
			limit := 1.0
			original := &service.Group{ID: 1, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeSubscription, DailyLimitUSD: &limit}
			paid := &service.Group{ID: 2, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true}
			user := &service.User{ID: 1, Status: service.StatusActive, Balance: 10}
			key := &service.APIKey{ID: 1, UserID: 1, Key: "TEST_ONLY", Status: service.StatusActive, User: user, Group: original, GroupID: &original.ID, SmartSwitch: &domain.KeySmartSwitchPolicy{Enabled: true, GroupIDs: []int64{1, 2}, PaygConfirmed: true}}
			sub := &service.UserSubscription{ID: 1, UserID: 1, GroupID: 1, Status: service.SubscriptionStatusActive, ExpiresAt: now.Add(time.Hour), DailyUsageUSD: 1, DailyWindowStart: &now, WeeklyWindowStart: &now, MonthlyWindowStart: &now, FiveHourWindowStart: &now}
			subs := &stubUserSubscriptionRepo{getActive: func(context.Context, int64, int64) (*service.UserSubscription, error) {
				copy := *sub
				return &copy, nil
			}}
			cfg := &config.Config{RunMode: config.RunModeStandard}
			cfg.Gateway.MaxBodySize = 1 << 20
			keys := service.NewAPIKeyService(smartAuthKeyRepo{key: key}, smartAuthUserRepo{user: user}, smartAuthGroupRepo{groups: map[int64]*service.Group{1: original, 2: paid}}, subs, nil, nil, cfg)
			subsvc := service.NewSubscriptionService(nil, subs, nil, nil, cfg)
			t.Cleanup(subsvc.Stop)
			wire := []byte(`{"model":"claude-sonnet","messages":[]}`)
			if compressed {
				encoder, err := zstd.NewWriter(nil)
				require.NoError(t, err)
				wire = encoder.EncodeAll(wire, nil)
				encoder.Close()
			}
			router := gin.New()
			router.POST("/v1/messages", apiKeyAuthWithSubscription(keys, subsvc, nil, cfg), func(c *gin.Context) {
				effective, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				require.Equal(t, int64(2), *effective.GroupID)
				_, hasSubscription := GetSubscriptionFromContext(c)
				require.False(t, hasSubscription)
				require.Equal(t, int64(2), c.Request.Context().Value(ctxkey.Group).(*service.Group).ID)
				actual, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Equal(t, wire, actual)
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(wire))
			request.Header.Set("x-api-key", "TEST_ONLY")
			if compressed {
				request.Header.Set("Content-Encoding", "zstd")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Equal(t, int64(1), *key.GroupID)
		})
	}
}

func TestSmartSwitchRejectsUnsafeBodyBeforeSelection(t *testing.T) {
	for _, body := range []string{`{`, `{}`, `{"model":"gpt","previous_response_id":"resp_example"}`, `{"model":"gpt","background":true}`} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))
		_, ok := applySmartSwitch(c, &service.APIKey{}, nil, nil, &config.Config{})
		require.False(t, ok)
		require.True(t, c.IsAborted())
	}
}

func TestSmartSwitchDecodedBodyLimitMatchesRequestBodyLimit(t *testing.T) {
	encoder, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	// Decodes past gateway.max_body_size but stays within server.max_request_body_size.
	wire := encoder.EncodeAll([]byte(`{"pad":"`+strings.Repeat("x", 4096)+`"}`), nil)
	encoder.Close()
	for _, tc := range []struct {
		serverLimit int64
		want        string
	}{{1 << 20, "model is required"}, {2048, "oversized compressed body"}} {
		cfg := &config.Config{}
		cfg.Gateway.MaxBodySize = 1024
		cfg.Server.MaxRequestBodySize = tc.serverLimit
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(wire))
		c.Request.Header.Set("Content-Encoding", "zstd")
		_, ok := applySmartSwitch(c, &service.APIKey{}, nil, nil, cfg)
		require.False(t, ok)
		require.Contains(t, recorder.Body.String(), tc.want)
	}
}
