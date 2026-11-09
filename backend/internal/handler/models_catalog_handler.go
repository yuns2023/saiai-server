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

// writeAnthropicModelsPage pages the visible catalog locally with the
// Anthropic list semantics (limit / after_id / before_id, newest first), so
// has_more and the cursors always refer to models the client can see.
func writeAnthropicModelsPage(c *gin.Context, models []service.ModelsCatalogEntry) {
	limit := anthropicModelsDefaultLimit
	if raw, ok := c.GetQuery("limit"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || parsed < 1 || parsed > anthropicModelsMaxLimit {
			anthropicModelsError(c, http.StatusBadRequest, "invalid_request_error", "limit: must be an integer between 1 and 1000")
			return
		}
		limit = parsed
	}
	afterID := c.Query("after_id")
	beforeID := c.Query("before_id")
	if afterID != "" && beforeID != "" {
		anthropicModelsError(c, http.StatusBadRequest, "invalid_request_error", "after_id and before_id cannot both be set")
		return
	}
	cursor := func(id string) int {
		for i, model := range models {
			if model.ID == id {
				return i
			}
		}
		return -1
	}
	start, end := 0, len(models)
	switch {
	case afterID != "":
		if i := cursor(afterID); i >= 0 {
			start = i + 1
		} else {
			start = end
		}
		if start+limit < end {
			end = start + limit
		}
	case beforeID != "":
		if i := cursor(beforeID); i >= 0 {
			end = i
		} else {
			end = 0
		}
		if end-limit > 0 {
			start = end - limit
		}
	default:
		if limit < end {
			end = limit
		}
	}
	page := models[start:end]
	hasMore := end < len(models)
	if beforeID != "" {
		hasMore = start > 0
	}
	var firstID, lastID any
	if len(page) > 0 {
		firstID, lastID = page[0].ID, page[len(page)-1].ID
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{
		"data":     rawCatalogData(page),
		"has_more": hasMore,
		"first_id": firstID,
		"last_id":  lastID,
	})
}

// serveAnthropicModelsCatalog answers /v1/models from the upstream Anthropic
// catalog. It returns false when no catalog is available, leaving the caller
// to serve the built-in list.
func (h *GatewayHandler) serveAnthropicModelsCatalog(c *gin.Context, apiKey *service.APIKey) bool {
	var groupID *int64
	var group *service.Group
	if apiKey != nil && apiKey.Group != nil {
		groupID, group = &apiKey.Group.ID, apiKey.Group
	}
	models, err := h.gatewayService.ListAnthropicModels(c.Request.Context(), groupID)
	if err != nil {
		return false
	}
	writeAnthropicModelsPage(c, visibleCatalogModels(models, group))
	return true
}

// ModelDetail serves GET /v1/models/:model for Anthropic groups.
func (h *GatewayHandler) ModelDetail(c *gin.Context) {
	apiKey, _ := middleware2.GetAPIKeyFromContext(c)
	modelID := c.Param("model")
	var groupID *int64
	var group *service.Group
	if apiKey != nil && apiKey.Group != nil {
		groupID, group = &apiKey.Group.ID, apiKey.Group
	}
	if group != nil && group.IsModelBlocked(modelID) {
		anthropicModelsError(c, http.StatusNotFound, "not_found_error", "model: "+modelID)
		return
	}
	models, err := h.gatewayService.ListAnthropicModels(c.Request.Context(), groupID)
	if err == nil {
		for _, model := range models {
			if model.ID == modelID {
				c.Header("Cache-Control", "private, no-store")
				c.Data(http.StatusOK, "application/json", model.Raw)
				return
			}
		}
		anthropicModelsError(c, http.StatusNotFound, "not_found_error", "model: "+modelID)
		return
	}
	for _, model := range claude.DefaultModels {
		if model.ID == modelID {
			c.JSON(http.StatusOK, model)
			return
		}
	}
	anthropicModelsError(c, http.StatusNotFound, "not_found_error", "model: "+modelID)
}

// OpenAIModels serves /v1/models to non-Codex clients of an OpenAI group in
// the OpenAI list format, built from the upstream Codex manifest. Without an
// upstream catalog it falls back to the built-in list.
func (h *OpenAIGatewayHandler) OpenAIModels(c *gin.Context) {
	group := openAIModelsGroup(c)
	models, err := h.openAIModelsCatalog(c, group)
	if err != nil {
		models = builtinOpenAIModels()
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": rawCatalogData(visibleCatalogModels(models, group))})
}

// OpenAIModelDetail serves GET /v1/models/:model for OpenAI groups.
func (h *OpenAIGatewayHandler) OpenAIModelDetail(c *gin.Context) {
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
			c.Header("Cache-Control", "private, no-store")
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
