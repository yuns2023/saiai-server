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
//
// Groups whose schedulable accounts are all OpenAI OAuth (no native relay, no
// API-key upstream) share one cached answer per fallback key: fresh for
// 20 minutes from the upstream fetch, then served while one background
// refresh runs, up to the stale fallback's maximum age.
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
	// The headers are a copy: shared and background fetches may outlive the
	// request.
	headers := c.Request.Header.Clone()
	ifNoneMatch := c.GetHeader("If-None-Match")
	filterBlocked := len(apiKey.Group.BlockedModelPatterns) > 0
	official := pkgopenai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator"))
	// Version is forwarded for every client (the discovery identity rewrite
	// replaces only User-Agent and Originator). The full User-Agent is not
	// part of the key: it carries per-machine OS/terminal strings.
	fallbackKey := service.CodexModelsManifestFallbackKey{GroupID: apiKey.Group.ID, RawQuery: c.Request.URL.RawQuery,
		Official: official, Version: strings.TrimSpace(c.GetHeader("version"))}
	if official {
		fallbackKey.Originator = strings.ToLower(strings.TrimSpace(c.GetHeader("originator")))
	}
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
	// Shared answers are fetched unconditionally; the client's validator is
	// compared locally.
	sharedHeaders := headers.Clone()
	sharedHeaders.Del("If-None-Match")
	sharedHeaders.Del("If-Modified-Since")

	maxSwitches := h.maxAccountSwitches
	if maxSwitches <= 0 {
		maxSwitches = 3
	}
	fetch := codexModelsManifestFetch{
		key:           fallbackKey,
		groupID:       apiKey.GroupID,
		rawQuery:      c.Request.URL.RawQuery,
		headers:       headers,
		ifNoneMatch:   ifNoneMatch,
		sharedHeaders: sharedHeaders,
		maxSwitches:   maxSwitches,
		userAgent:     c.GetHeader("User-Agent"),
		originator:    c.GetHeader("originator"),
		version:       c.GetHeader("version"),
		clientVersion: c.Query("client_version"),
	}
	ctx := c.Request.Context()
	// Eligibility is checked on every request (hits included), so a group
	// that gains a relay or API-key account leaves the cache at once; when it
	// cannot be decided the request takes the uncached path. Requests with
	// other preconditions or ranges keep their original semantics.
	if !codexModelsManifestHasOtherConditions(c.Request.Header) &&
		h.gatewayService.CodexModelsManifestGroupCacheEligibility(ctx, apiKey.GroupID) == service.CodexModelsManifestGroupCacheEligible {
		if h.serveCodexModelsManifestGroupCache(c, fetch, apiKey.Group, filterBlocked) {
			return
		}
		result, own, ok := h.awaitCodexModelsManifestFlight(ctx, fetch)
		if !ok {
			return
		}
		h.writeCodexModelsManifestResult(c, fallbackKey, apiKey.Group, filterBlocked, result, own)
		return
	}
	result := h.fetchCodexModelsManifestWithFailover(ctx, fetch, false, "")
	h.writeCodexModelsManifestResult(c, fallbackKey, apiKey.Group, filterBlocked, result, true)
}

// writeCodexModelsManifestResult answers with the outcome of an account loop.
// own is false when the answer was fetched by another request (singleflight);
// then only the entry-level headers of the upstream answer are replayed.
func (h *OpenAIGatewayHandler) writeCodexModelsManifestResult(c *gin.Context, key service.CodexModelsManifestFallbackKey, group *service.Group, filterBlocked bool, result codexModelsManifestFetchResult, own bool) {
	switch {
	case result.canceled:
		return
	case result.noAccounts:
		h.errorResponse(c, http.StatusServiceUnavailable, "upstream_error", "No available OpenAI accounts")
		return
	case result.err != nil:
		// The stale fallback applies only when every attempt failed with
		// upstream unavailability and the candidates were exhausted.
		if result.fallback && h.serveCodexModelsManifestFallback(c, key, group, filterBlocked, result.err) {
			return
		}
		h.errorResponse(c, infraerrors.Code(result.err), "upstream_error", infraerrors.Message(result.err))
		return
	}
	account, manifest := result.account, result.manifest
	if result.shared {
		replay := service.CodexModelsManifestEntryHeaders(manifest)
		if own {
			replay = manifest.Headers
		}
		if !h.serveCodexModelsManifestBody(c, manifest.Body, replay, group, filterBlocked, codexModelsManifestCacheMiss, false) {
			h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Codex models manifest has an invalid models array")
		}
		return
	}

	body := manifest.Body
	if filterBlocked {
		if manifest.NotModified {
			h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Codex models manifest returned 304 to an unconditional request")
			return
		}
		var err error
		if body, err = service.FilterCodexModelsManifest(body, group.IsModelBlocked); err != nil {
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
}

// codexModelsManifestStaleHeader marks a manifest served from the stale
// fallback. Codex reads only ETag from this response; unknown headers are
// ignored, so the marker is for diagnostics only.
const codexModelsManifestStaleHeader = "X-Saiai-Manifest-Stale"

// serveCodexModelsManifestFallback answers with the group's last good
// manifest after upstream unavailability. It keeps the success path's
// validator semantics (ETag/304; blocked filter with no-store and no ETag) and
// records the failed attempt as a recovered upstream error.
func (h *OpenAIGatewayHandler) serveCodexModelsManifestFallback(c *gin.Context, key service.CodexModelsManifestFallbackKey, group *service.Group, filterBlocked bool, cause error) bool {
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

	// Only the validator is replayed; request IDs and other upstream headers
	// of the original answer are not. The stale copy must not be persisted by
	// clients or intermediaries (upstream itself answers private, no-store).
	c.Header(codexModelsManifestStaleHeader, "1")
	c.Header(codexModelsManifestCacheHeader, codexModelsManifestCacheStale)
	c.Header("Cache-Control", "private, no-store")
	if filterBlocked {
		c.Data(http.StatusOK, "application/json", body)
		return true
	}
	if fallback.ETag != "" {
		c.Header("ETag", fallback.ETag)
	}
	// The stale answer has no upstream to forward conditions to, so every
	// If-None-Match line is considered here.
	if service.CodexModelsManifestETagMatches(strings.Join(c.Request.Header.Values("If-None-Match"), ","), fallback.ETag) {
		c.Status(http.StatusNotModified)
		return true
	}
	c.Data(http.StatusOK, "application/json", body)
	return true
}
