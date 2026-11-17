package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newBindContext(t *testing.T, method, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(method, "/api/v1/admin/subscriptions/1", strings.NewReader(body))
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	return c
}

func TestBindOptionalJSON_RevokeBody(t *testing.T) {
	t.Run("no body keeps old clients working", func(t *testing.T) {
		var req RevokeSubscriptionRequest
		require.NoError(t, bindOptionalJSON(newBindContext(t, http.MethodDelete, ""), &req))
		require.Empty(t, req.Notes)
	})
	t.Run("notes", func(t *testing.T) {
		var req RevokeSubscriptionRequest
		require.NoError(t, bindOptionalJSON(newBindContext(t, http.MethodDelete, `{"notes":"退款"}`), &req))
		require.Equal(t, "退款", req.Notes)
	})
	t.Run("500 characters allowed, 501 rejected", func(t *testing.T) {
		var req RevokeSubscriptionRequest
		ok := `{"notes":"` + strings.Repeat("备", 500) + `"}`
		require.NoError(t, bindOptionalJSON(newBindContext(t, http.MethodDelete, ok), &req))
		tooLong := `{"notes":"` + strings.Repeat("备", 501) + `"}`
		require.Error(t, bindOptionalJSON(newBindContext(t, http.MethodDelete, tooLong), &req))
	})
	t.Run("malformed JSON", func(t *testing.T) {
		var req RevokeSubscriptionRequest
		require.Error(t, bindOptionalJSON(newBindContext(t, http.MethodDelete, `{"notes":`), &req))
	})
}
