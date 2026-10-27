package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	pkgopenai "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// CodexModels serves the live Codex model manifest. Model discovery is kept
// separate from request admission: the selected upstream account remains the
// authority for what it advertises (minus the group's blocked models), while
// unknown model IDs can still be sent to /v1/responses and decided there.
func (h *OpenAIGatewayHandler) CodexModels(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		h.errorResponse(c, http.StatusUnauthorized, "invalid_request_error", "API key group is required")
		return
	}
	if apiKey.Group.Platform != service.PlatformOpenAI {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Codex models manifest is only available for OpenAI groups")
		return
	}
	// Discovery is open to every valid key of the group: it is read-only and
	// unbilled, and model turns keep their own client policy. Non-official
	// clients are fetched with the gateway's Codex identity instead of theirs.
	headers := c.Request.Header
	ifNoneMatch := c.GetHeader("If-None-Match")
	filterBlocked := len(apiKey.Group.BlockedModelPatterns) > 0
	if official := pkgopenai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")); !official || filterBlocked {
		headers = headers.Clone()
		if !official {
			service.ApplyCodexModelsDiscoveryIdentity(headers, c.Query("client_version"))
		}
		if filterBlocked {
			// A filtered catalog must not be revalidated against the upstream
			// validator, otherwise a blacklist change would keep a stale list.
			headers.Del("If-None-Match")
			headers.Del("If-Modified-Since")
			ifNoneMatch = ""
		}
	}

	maxSwitches := h.maxAccountSwitches
	if maxSwitches <= 0 {
		maxSwitches = 3
	}
	excluded := make(map[int64]struct{})
	var lastErr error
	for switchCount := 0; ; switchCount++ {
		account, err := h.gatewayService.SelectAccountForModelWithExclusions(c.Request.Context(), apiKey.GroupID, "", "", excluded)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			if lastErr != nil {
				h.errorResponse(c, infraerrors.Code(lastErr), "upstream_error", infraerrors.Message(lastErr))
				return
			}
			h.errorResponse(c, http.StatusServiceUnavailable, "upstream_error", "No available OpenAI accounts")
			return
		}

		manifest, err := h.gatewayService.FetchNativeCodexModelsManifest(c.Request.Context(), account, c.Request.URL.RawQuery, ifNoneMatch, headers)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			if service.IsRetryableCodexModelsManifestError(err) && switchCount < maxSwitches {
				excluded[account.ID] = struct{}{}
				lastErr = err
				continue
			}
			h.errorResponse(c, infraerrors.Code(err), "upstream_error", infraerrors.Message(err))
			return
		}

		body := manifest.Body
		if filterBlocked {
			if manifest.NotModified {
				h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Codex models manifest returned 304 to an unconditional request")
				return
			}
			if body, err = service.FilterCodexModelsManifest(body, apiKey.Group.IsModelBlocked); err != nil {
				h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Codex models manifest has an invalid models array")
				return
			}
		}
		if account.IsOpenAIOAuth() || account.IsOpenAICodexNativeRelay() {
			h.gatewayService.WriteNativeCodexResponseHeaders(c.Writer.Header(), manifest.Headers)
		}
		if filterBlocked {
			c.Writer.Header().Del("ETag")
			c.Writer.Header().Del("Last-Modified")
			c.Header("Cache-Control", "private, no-store")
			c.Data(http.StatusOK, "application/json", body)
			return
		}
		if manifest.ETag != "" && !account.IsOpenAIOAuth() && !account.IsOpenAICodexNativeRelay() {
			c.Header("ETag", manifest.ETag)
		}
		if manifest.NotModified {
			c.Status(http.StatusNotModified)
			return
		}
		c.Data(http.StatusOK, "application/json", body)
		return
	}
}
