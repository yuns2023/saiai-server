package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func modelsCatalogFixture(ids ...string) []service.ModelsCatalogEntry {
	models := make([]service.ModelsCatalogEntry, len(ids))
	for i, id := range ids {
		models[i] = service.ModelsCatalogEntry{ID: id, Raw: json.RawMessage(`{"type":"model","id":"` + id + `"}`)}
	}
	return models
}

type anthropicModelsPageBody struct {
	Data    []map[string]any `json:"data"`
	HasMore bool             `json:"has_more"`
	FirstID *string          `json:"first_id"`
	LastID  *string          `json:"last_id"`
}

func runAnthropicModelsPage(t *testing.T, query string, models []service.ModelsCatalogEntry) (int, anthropicModelsPageBody) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models"+query, nil)
	if query, ok := parseAnthropicModelsPageQuery(c); ok {
		writeAnthropicModelsPage(c, query, models)
	}
	var body anthropicModelsPageBody
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	}
	return rec.Code, body
}

func pageIDs(body anthropicModelsPageBody) []string {
	ids := make([]string, len(body.Data))
	for i, item := range body.Data {
		ids[i], _ = item["id"].(string)
	}
	return ids
}

func TestWriteAnthropicModelsPagePaginatesVisibleCatalog(t *testing.T) {
	group := &service.Group{BlockedModelPatterns: []string{"claude-opus-5"}}
	visible := visibleCatalogModels(modelsCatalogFixture("claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5", "claude-haiku-5-5"), group)

	code, body := runAnthropicModelsPage(t, "?limit=1000", visible)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}, pageIDs(body))
	require.False(t, body.HasMore)
	require.Equal(t, "claude-opus-5-5", *body.FirstID)
	require.Equal(t, "claude-haiku-5-5", *body.LastID)

	code, body = runAnthropicModelsPage(t, "?limit=2", visible)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"claude-opus-5-5", "claude-sonnet-5-5"}, pageIDs(body))
	require.True(t, body.HasMore)

	code, body = runAnthropicModelsPage(t, "?limit=2&after_id=claude-sonnet-5-5", visible)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"claude-haiku-5-5"}, pageIDs(body))
	require.False(t, body.HasMore)

	code, body = runAnthropicModelsPage(t, "?limit=1&before_id=claude-haiku-5-5", visible)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"claude-sonnet-5-5"}, pageIDs(body))
	require.True(t, body.HasMore)

	// A blocked or unknown cursor yields an empty, terminal page.
	code, body = runAnthropicModelsPage(t, "?after_id=claude-opus-5", visible)
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, body.Data)
	require.False(t, body.HasMore)
	require.Nil(t, body.FirstID)

	code, body = runAnthropicModelsPage(t, "?before_id=claude-opus-5-5", visible)
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, body.Data)
	require.False(t, body.HasMore)

	// Every model blocked: an empty, well-formed list.
	code, body = runAnthropicModelsPage(t, "", visibleCatalogModels(visible, &service.Group{BlockedModelPatterns: []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}}))
	require.Equal(t, http.StatusOK, code)
	require.NotNil(t, body.Data)
	require.Empty(t, body.Data)
	require.False(t, body.HasMore)
}

func TestWriteAnthropicModelsPageDefaultsAndRejectsBadInput(t *testing.T) {
	ids := make([]string, 25)
	for i := range ids {
		ids[i] = "claude-m-" + string(rune('a'+i))
	}
	code, body := runAnthropicModelsPage(t, "", modelsCatalogFixture(ids...))
	require.Equal(t, http.StatusOK, code)
	require.Len(t, body.Data, 20, "default limit matches the Anthropic API")
	require.True(t, body.HasMore)

	for _, query := range []string{"?limit=0", "?limit=-1", "?limit=1001", "?limit=abc", "?limit=99999999999999999999", "?limit=", "?limit=1&limit=2", "?after_id=a&after_id=b", "?after_id=a&before_id=b"} {
		code, _ := runAnthropicModelsPage(t, query, modelsCatalogFixture("a"))
		require.Equal(t, http.StatusBadRequest, code, query)
	}
}

func TestVisibleCatalogModelsDoesNotMutateSharedCatalog(t *testing.T) {
	catalog := modelsCatalogFixture("a", "b")
	visible := visibleCatalogModels(catalog, &service.Group{BlockedModelPatterns: []string{"a"}})
	require.Len(t, visible, 1)
	require.Equal(t, "a", catalog[0].ID)
	require.Equal(t, "b", catalog[1].ID)
}
