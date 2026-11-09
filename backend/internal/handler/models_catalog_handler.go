package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	pkgopenai "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	anthropicModelsDefaultLimit = 20
	anthropicModelsMaxLimit     = 1000
)

// visibleCatalogModels drops the group's blocked models. The cached catalog
// is shared, so it is never filtered in place.
func visibleCatalogModels(models []service.ModelsCatalogEntry, group *service.Group) []service.ModelsCatalogEntry {
	if group == nil || len(group.BlockedModelPatterns) == 0 {
		return models
	}
	visible := make([]service.ModelsCatalogEntry, 0, len(models))
	for _, model := range models {
		if !group.IsModelBlocked(model.ID) {
			visible = append(visible, model)
		}
	}
	return visible
}

func rawCatalogData(models []service.ModelsCatalogEntry) []json.RawMessage {
	data := make([]json.RawMessage, len(models))
	for i, model := range models {
		data[i] = model.Raw
	}
	return data
}

func anthropicModelsError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": errType, "message": message}})
}

type anthropicModelsPageQuery struct {
	limit    int
	afterID  string
	beforeID string
}

// parseAnthropicModelsPageQuery validates limit / after_id / before_id before
// any upstream work so a bad request is rejected the same way whether or not
// the catalog is available.
func parseAnthropicModelsPageQuery(c *gin.Context) (anthropicModelsPageQuery, bool) {
	query := anthropicModelsPageQuery{limit: anthropicModelsDefaultLimit}
	values := c.Request.URL.Query()
	for _, name := range []string{"limit", "after_id", "before_id"} {
		if len(values[name]) > 1 {
			anthropicModelsError(c, http.StatusBadRequest, "invalid_request_error", name+": must be given at most once")
			return query, false
		}
	}
	if raw, ok := c.GetQuery("limit"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || parsed < 1 || parsed > anthropicModelsMaxLimit {
			anthropicModelsError(c, http.StatusBadRequest, "invalid_request_error", "limit: must be an integer between 1 and 1000")
			return query, false
		}
		query.limit = parsed
	}
	query.afterID = c.Query("after_id")
	query.beforeID = c.Query("before_id")
	if query.afterID != "" && query.beforeID != "" {
		anthropicModelsError(c, http.StatusBadRequest, "invalid_request_error", "after_id and before_id cannot both be set")
		return query, false
	}
	return query, true
}

// writeAnthropicModelsPage pages the visible catalog locally with the
// Anthropic list semantics (newest first), so has_more and the cursors always
// refer to models the client can see. Unknown cursors yield an empty page.
func writeAnthropicModelsPage(c *gin.Context, query anthropicModelsPageQuery, models []service.ModelsCatalogEntry) {
	cursor := func(id string) int {
		for i, model := range models {
			if model.ID == id {
				return i
			}
		}
		return -1
	}
	start, end := 0, len(models)
	hasMore := false
	switch {
	case query.afterID != "":
		start = end
		if i := cursor(query.afterID); i >= 0 {
			start = i + 1
		}
		end = min(end, start+query.limit)
		hasMore = end < len(models)
	case query.beforeID != "":
		end = 0
		if i := cursor(query.beforeID); i >= 0 {
			end = i
		}
		start = max(0, end-query.limit)
		hasMore = start > 0
	default:
		end = min(end, query.limit)
		hasMore = end < len(models)
	}
	page := models[start:end]
	var firstID, lastID any
	if len(page) > 0 {
		firstID, lastID = page[0].ID, page[len(page)-1].ID
	}
	c.JSON(http.StatusOK, gin.H{
		"data":     rawCatalogData(page),
		"has_more": hasMore,
		"first_id": firstID,
		"last_id":  lastID,
	})
}

// anthropicModelsCatalog returns the upstream Anthropic catalog, or the
// built-in list when no catalog is available.
func (h *GatewayHandler) anthropicModelsCatalog(c *gin.Context, group *service.Group) []service.ModelsCatalogEntry {
	var groupID *int64
	if group != nil {
		groupID = &group.ID
	}
	if models, err := h.gatewayService.ListAnthropicModels(c.Request.Context(), groupID); err == nil {
		return models
	}
	models := make([]service.ModelsCatalogEntry, 0, len(claude.DefaultModels))
	for _, model := range claude.DefaultModels {
		if raw, err := json.Marshal(model); err == nil {
			models = append(models, service.ModelsCatalogEntry{ID: model.ID, Raw: raw})
		}
	}
	return models
}

// serveAnthropicModels answers /v1/models for Anthropic groups.
func (h *GatewayHandler) serveAnthropicModels(c *gin.Context, apiKey *service.APIKey) {
	c.Header("Cache-Control", "private, no-store")
	query, ok := parseAnthropicModelsPageQuery(c)
	if !ok {
		return
	}
	var group *service.Group
	if apiKey != nil {
		group = apiKey.Group
	}
	writeAnthropicModelsPage(c, query, visibleCatalogModels(h.anthropicModelsCatalog(c, group), group))
}

// ModelDetail serves GET /v1/models/:model for Anthropic groups.
func (h *GatewayHandler) ModelDetail(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	apiKey, _ := middleware2.GetAPIKeyFromContext(c)
	modelID := c.Param("model")
	var group *service.Group
	if apiKey != nil {
		group = apiKey.Group
	}
	if !group.IsModelBlocked(modelID) {
		for _, model := range h.anthropicModelsCatalog(c, group) {
			if model.ID == modelID {
				c.Data(http.StatusOK, "application/json", model.Raw)
				return
			}
		}
	}
	anthropicModelsError(c, http.StatusNotFound, "not_found_error", "model: "+modelID)
}

// OpenAIModels serves /v1/models to non-Codex clients of an OpenAI group in
// the OpenAI list format, built from the upstream Codex manifest. Without an
// upstream catalog it falls back to the built-in list.
func (h *OpenAIGatewayHandler) OpenAIModels(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	group := openAIModelsGroup(c)
	models, err := h.openAIModelsCatalog(c, group)
	if err != nil {
		models = builtinOpenAIModels()
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": rawCatalogData(visibleCatalogModels(models, group))})
}

// OpenAIModelDetail serves GET /v1/models/:model for OpenAI groups.
func (h *OpenAIGatewayHandler) OpenAIModelDetail(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	group := openAIModelsGroup(c)
	modelID := c.Param("model")
	notFound := func() {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
			"message": "The model '" + modelID + "' does not exist",
			"type":    "invalid_request_error",
			"param":   "model",
			"code":    "model_not_found",
		}})
	}
	if group != nil && group.IsModelBlocked(modelID) {
		notFound()
		return
	}
	models, err := h.openAIModelsCatalog(c, group)
	if err != nil {
		models = builtinOpenAIModels()
	}
	for _, model := range models {
		if model.ID == modelID {
			c.Data(http.StatusOK, "application/json", model.Raw)
			return
		}
	}
	notFound()
}

func openAIModelsGroup(c *gin.Context) *service.Group {
	if apiKey, ok := middleware2.GetAPIKeyFromContext(c); ok && apiKey != nil {
		return apiKey.Group
	}
	return nil
}

func (h *OpenAIGatewayHandler) openAIModelsCatalog(c *gin.Context, group *service.Group) ([]service.ModelsCatalogEntry, error) {
	if group == nil {
		return nil, errors.New("API key group is required")
	}
	return h.gatewayService.ListOpenAIModels(c.Request.Context(), &group.ID)
}

func builtinOpenAIModels() []service.ModelsCatalogEntry {
	models := make([]service.ModelsCatalogEntry, 0, len(pkgopenai.DefaultModels))
	for _, model := range pkgopenai.DefaultModels {
		raw, err := json.Marshal(model)
		if err != nil {
			continue
		}
		models = append(models, service.ModelsCatalogEntry{ID: model.ID, Raw: raw})
	}
	return models
}
