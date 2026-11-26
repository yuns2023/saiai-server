package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
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
	official := pkgopenai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator"))
	fallbackKey := service.CodexModelsManifestFallbackKey{GroupID: apiKey.Group.ID, RawQuery: c.Request.URL.RawQuery, Official: official}
	if official {
		fallbackKey.Originator = strings.ToLower(strings.TrimSpace(c.GetHeader("originator")))
	}
	if !official || filterBlocked {
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
	// The stale fallback applies only when every attempt of this request
	// failed with upstream unavailability; one account-level decision (for
	// example a 401) makes the request surface its error as before.
	staleEligible := true
	fail := func(err error) {
		if staleEligible && h.serveCodexModelsManifestFallback(c, fallbackKey, apiKey.Group, filterBlocked, ifNoneMatch, err) {
			return
		}
		h.errorResponse(c, infraerrors.Code(err), "upstream_error", infraerrors.Message(err))
	}
	for switchCount := 0; ; switchCount++ {
		account, err := h.gatewayService.SelectAccountForModelWithExclusions(c.Request.Context(), apiKey.GroupID, "", "", excluded)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			if lastErr != nil {
				fail(lastErr)
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
			if !service.IsCodexModelsManifestStaleFallbackError(err) {
				staleEligible = false
			}
			if service.IsRetryableCodexModelsManifestError(err) && switchCount < maxSwitches {
				excluded[account.ID] = struct{}{}
				lastErr = err
				continue
			}
			fail(err)
			return
		}

		// Only a version the official upstream just answered for (query and
		// headers agree) may raise the gateway's own catalog version.
		if account.IsOpenAIOAuth() && !manifest.NotModified && c.Query("client_version") == c.GetHeader("version") {
			service.ObserveCodexClientVersion(c.GetHeader("User-Agent"), c.GetHeader("originator"), c.GetHeader("version"))
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
		nativeHeaders := account.IsOpenAIOAuth() || account.IsOpenAICodexNativeRelay()
		h.gatewayService.RememberCodexModelsManifestFallback(fallbackKey, account.ID, manifest, nativeHeaders)
		if nativeHeaders {
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

// codexModelsManifestStaleHeader marks a manifest served from the stale
// fallback. Codex reads only ETag from this response; unknown headers are
// ignored, so the marker is for diagnostics only.
const codexModelsManifestStaleHeader = "X-Saiai-Manifest-Stale"

// serveCodexModelsManifestFallback answers with the group's last good
// manifest after upstream unavailability. It reproduces what the success path
// would write for that answer (native headers, ETag/304, blocked filter with
// no-store) and records the failed attempt as a recovered upstream error.
func (h *OpenAIGatewayHandler) serveCodexModelsManifestFallback(c *gin.Context, key service.CodexModelsManifestFallbackKey, group *service.Group, filterBlocked bool, ifNoneMatch string, cause error) bool {
	if !service.IsCodexModelsManifestStaleFallbackError(cause) {
		return false
	}
	fallback, ok := h.gatewayService.LookupCodexModelsManifestFallback(key)
	if !ok {
		return false
	}
	body := fallback.Body
	if filterBlocked {
		filtered, err := service.FilterCodexModelsManifest(body, group.IsModelBlocked)
		if err != nil {
			return false
		}
		body = filtered
	}

	age := time.Since(fallback.StoredAt)
	logger.FromContext(c.Request.Context()).Warn("openai.codex_models_manifest_stale_served",
		zap.Int64("group_id", key.GroupID),
		zap.Int64("stored_account_id", fallback.AccountID),
		zap.Duration("age", age),
		zap.Int("upstream_status", service.CodexModelsManifestUpstreamStatus(cause)),
		zap.String("cause", infraerrors.Message(cause)))
	service.SetOpsUpstreamError(c, service.CodexModelsManifestUpstreamStatus(cause),
		"Codex models manifest served from stale fallback: "+infraerrors.Message(cause), "")

	header := c.Writer.Header()
	if fallback.NativeHeaders {
		h.gatewayService.WriteNativeCodexResponseHeaders(header, fallback.Headers)
		// The stored Date belongs to the original answer; let net/http stamp
		// this response.
		header.Del("Date")
	}
	header.Set(codexModelsManifestStaleHeader, "1")
	if filterBlocked {
		header.Del("ETag")
		header.Del("Last-Modified")
		c.Header("Cache-Control", "private, no-store")
		c.Data(http.StatusOK, "application/json", body)
		return true
	}
	if fallback.ETag != "" && !fallback.NativeHeaders {
		c.Header("ETag", fallback.ETag)
	}
	if service.CodexModelsManifestETagMatches(ifNoneMatch, fallback.ETag) {
		c.Status(http.StatusNotModified)
		return true
	}
	c.Data(http.StatusOK, "application/json", body)
	return true
}
