package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type driftNoticeStoreStub struct{ value string }

func (s *driftNoticeStoreStub) GetValue(context.Context, string) (string, error) {
	if s.value == "" {
		return "", service.ErrSettingNotFound
	}
	return s.value, nil
}

func (s *driftNoticeStoreStub) UpdateSettingValue(_ context.Context, _ string, update func(string) (string, error)) error {
	next, err := update(s.value)
	if err == nil {
		s.value = next
	}
	return err
}

type driftBilledModelsStub struct{}

func (driftBilledModelsStub) ListBilledPricingModels(context.Context, time.Time) ([]string, error) {
	return nil, nil
}

func TestPricingDriftNoticesListAndAcknowledge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	acked := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	seed, err := json.Marshal([]service.PricingDriftNotice{
		{ID: "old-open", Model: "a", DetectedAt: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), Changes: []service.PricingDriftChange{}},
		{ID: "seen", Model: "b", DetectedAt: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), AcknowledgedAt: &acked, AcknowledgedBy: 3},
		{ID: "new-open", Model: "c", DetectedAt: time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)},
	})
	require.NoError(t, err)
	store := &driftNoticeStoreStub{value: string(seed)}
	h := &SettingHandler{}
	h.SetPricingDriftService(service.NewPricingDriftService(&config.Config{}, driftBilledModelsStub{}, store, nil))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
		c.Next()
	})
	router.GET("/drift", h.ListPricingDriftNotices)
	router.POST("/drift/ack", h.AcknowledgePricingDriftNotices)

	type body struct {
		Data service.PricingDriftList `json:"data"`
	}
	w := priceHandlerRequest(router, http.MethodGet, "/drift", nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var got body
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, 2, got.Data.Unacknowledged)
	ids := []string{}
	for _, n := range got.Data.Notices {
		ids = append(ids, n.ID)
	}
	require.Equal(t, []string{"new-open", "old-open", "seen"}, ids)

	w = priceHandlerRequest(router, http.MethodPost, "/drift/ack", map[string]any{"ids": []string{"new-open", "seen"}})
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, 1, got.Data.Unacknowledged)
	for _, n := range got.Data.Notices {
		switch n.ID {
		case "new-open":
			require.EqualValues(t, 42, n.AcknowledgedBy)
			require.NotNil(t, n.AcknowledgedAt)
		case "seen":
			require.EqualValues(t, 3, n.AcknowledgedBy, "an earlier acknowledgement is kept")
		case "old-open":
			require.Nil(t, n.AcknowledgedAt)
		}
	}

	for _, invalid := range []any{map[string]any{}, map[string]any{"ids": []string{}}, map[string]any{"ids": []string{""}}, map[string]any{"all": true}} {
		w = priceHandlerRequest(router, http.MethodPost, "/drift/ack", invalid)
		require.Equal(t, http.StatusBadRequest, w.Code, invalid)
	}

	unavailable := gin.New()
	unavailable.GET("/drift", (&SettingHandler{}).ListPricingDriftNotices)
	require.Equal(t, http.StatusServiceUnavailable, priceHandlerRequest(unavailable, http.MethodGet, "/drift", nil).Code)
}
