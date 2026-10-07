package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type chatGPTAdminSettingsRepo struct {
	service.SettingRepository
	values   map[string]string
	writeErr error
}

func (r *chatGPTAdminSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *chatGPTAdminSettingsRepo) Set(_ context.Context, key, value string) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	r.values[key] = value
	return nil
}

func TestChatGPTBillingSettingsAdminReadSaveValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &chatGPTAdminSettingsRepo{values: map[string]string{"site_name": "unchanged"}}
	h := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil)
	router := gin.New()
	router.GET("/settings/chatgpt-billing", h.GetChatGPTBillingSettings)
	router.PUT("/settings/chatgpt-billing", h.UpdateChatGPTBillingSettings)
	run := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/settings/chatgpt-billing", bytes.NewBufferString(body)))
		return w
	}
	readPrice := func() float64 {
		w := run(http.MethodGet, "")
		require.Equal(t, http.StatusOK, w.Code)
		var reply struct {
			Data service.OpenAIChatGPTBillingSettings `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reply))
		return reply.Data.SuccessTurnPriceUSD
	}
	require.Zero(t, readPrice())
	require.Equal(t, http.StatusOK, run(http.MethodPut, `{"success_turn_price_usd":0.02}`).Code)
	require.Equal(t, 0.02, readPrice())
	for _, body := range []string{`{}`, `{"success_turn_price_usd":null}`, `{"success_turn_price_usd":-1}`, `{"success_turn_price_usd":"0.02"}`, `{"success_turn_price_usd":1e999}`} {
		require.Equal(t, http.StatusBadRequest, run(http.MethodPut, body).Code, body)
		require.Equal(t, 0.02, readPrice())
	}
	require.Equal(t, "unchanged", repo.values["site_name"])
	repo.writeErr = errors.New("write failed")
	require.Equal(t, http.StatusInternalServerError, run(http.MethodPut, `{"success_turn_price_usd":0.04}`).Code)
	require.Equal(t, 0.02, readPrice())
	repo.writeErr = nil
	require.Equal(t, http.StatusOK, run(http.MethodPut, `{"success_turn_price_usd":0}`).Code)
	require.Zero(t, readPrice())
}
