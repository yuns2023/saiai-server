package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// codexModelsManifestCacheHeader tells how a manifest answer was produced.
// Codex reads only ETag from this endpoint and ignores unknown headers.
const codexModelsManifestCacheHeader = "X-Saiai-Manifest-Cache"

const (
	codexModelsManifestCacheHit     = "hit"     // fresh group-cache entry
	codexModelsManifestCacheMiss    = "miss"    // real (possibly shared) upstream answer
	codexModelsManifestCacheRefresh = "refresh" // older entry served; background refresh
	codexModelsManifestCacheStale   = "stale"   // stale-on-error fallback
)

// codexModelsManifestMaxRefreshes caps concurrent background refreshes per
// process; when all slots are busy the entry is served without starting one
// (a later request retries).
const codexModelsManifestMaxRefreshes = 4

// codexModelsManifestHasOtherConditions reports request preconditions or
// ranges the group cache does not emulate; such requests bypass it.
func codexModelsManifestHasOtherConditions(header http.Header) bool {
	for _, name := range []string{"If-Match", "If-Unmodified-Since", "If-Range", "Range"} {
		if len(header.Values(name)) > 0 {
			return true
		}
	}
	return false
}

// codexModelsManifestFlightRetries bounds how often a waiter joins a new
// shared fetch after the previous leader's client went away.
const codexModelsManifestFlightRetries = 3

// codexModelsManifestFetch is everything an account loop needs. It holds no
// reference to the gin.Context or the request, so shared and background
// fetches can outlive the request that started them.
type codexModelsManifestFetch struct {
	key      service.CodexModelsManifestFallbackKey
	groupID  *int64
	rawQuery string
	// headers / ifNoneMatch are the request's own (copied) upstream headers,
	// used for relay and API-key accounts as before.
	headers     http.Header
	ifNoneMatch string
	// sharedHeaders are headers without client validators, used for OAuth
	// accounts of a group-cache eligible group.
	sharedHeaders http.Header
	maxSwitches   int
	// Client identity for ObserveCodexClientVersion.
	userAgent, originator, version, clientVersion string
}

func (f codexModelsManifestFetch) flightKey() string {
	return fmt.Sprintf("%d\x00%t\x00%q\x00%q\x00%q", f.key.GroupID, f.key.Official, f.key.RawQuery, f.key.Originator, f.key.Version)
}

type codexModelsManifestFetchResult struct {
	account  *service.Account
	manifest *service.CodexModelsManifest
	err      error
	// fallback: err may be answered from the stale fallback (every attempt
	// was upstream unavailability and the candidates were exhausted).
	fallback   bool
	noAccounts bool
	canceled   bool
	// shared: the answer is an unconditional OAuth answer stored in the
	// group cache, so it can be served to every request of the key.
	shared bool
	// legacy: a relay or API-key account (or a non-shared fetch) took part;
	// waiters must not reuse the outcome.
	legacy bool
}

// fetchCodexModelsManifestWithFailover runs the account selection / switch
// loop. shared marks a fetch for a group-cache eligible group: OAuth accounts
// are then asked unconditionally and their answer is stored as a hit.
func (h *OpenAIGatewayHandler) fetchCodexModelsManifestWithFailover(ctx context.Context, f codexModelsManifestFetch, shared bool, reason string) (result codexModelsManifestFetchResult) {
	result.legacy = !shared
	excluded := make(map[int64]struct{})
	var lastErr error
	// The stale fallback applies only when every attempt of this request
	// failed with upstream unavailability; one account-level decision (for
	// example a 401) makes the request surface its error as before.
	staleEligible := true
	for switchCount := 0; ; switchCount++ {
		account, err := h.gatewayService.SelectAccountForModelWithExclusions(ctx, f.groupID, "", "", excluded)
		if err != nil {
			if ctx.Err() != nil {
				result.canceled = true
				return result
			}
			if lastErr != nil {
				// Only an exhausted candidate list ends the retries as an
				// upstream outage; a selection failure keeps its old answer.
				result.err = lastErr
				result.fallback = staleEligible && errors.Is(err, service.ErrNoAvailableAccounts)
				return result
			}
			result.noAccounts = true
			return result
		}

		groupCacheable := shared && account.IsOpenAIOAuth() && !account.IsOpenAICodexNativeRelay()
		headers, ifNoneMatch := f.headers, f.ifNoneMatch
		if groupCacheable {
			headers, ifNoneMatch = f.sharedHeaders, ""
		} else {
			result.legacy = true
		}
		started := time.Now()
		manifest, err := h.gatewayService.FetchNativeCodexModelsManifest(ctx, account, f.rawQuery, ifNoneMatch, headers)
		if shared {
			status := http.StatusOK
			if err != nil {
				status = service.CodexModelsManifestUpstreamStatus(err)
			} else if manifest.NotModified {
				status = http.StatusNotModified
			}
			logger.FromContext(ctx).Info("openai.codex_models_manifest_upstream_fetch",
				zap.Int64("group_id", f.key.GroupID),
				zap.Int64("account_id", account.ID),
				zap.String("reason", reason),
				zap.Duration("duration", time.Since(started)),
				zap.Int("status", status),
				zap.Bool("ok", err == nil))
		}
		if err != nil {
			if ctx.Err() != nil {
				result.canceled = true
				return result
			}
			if !service.IsCodexModelsManifestStaleFallbackError(err) {
				staleEligible = false
			}
			if service.IsRetryableCodexModelsManifestError(err) && switchCount < f.maxSwitches {
				excluded[account.ID] = struct{}{}
				lastErr = err
				continue
			}
			result.err = err
			result.fallback = staleEligible
			return result
		}

		if groupCacheable && manifest.NotModified {
			// The shared request carried no validator; a 304 is not an
			// answer that can be served or stored.
			result.err = infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST",
				"Codex models manifest returned 304 to an unconditional request")
			return result
		}

		// Only a version the official upstream just answered for (query and
		// headers agree) may raise the gateway's own catalog version.
		if account.IsOpenAIOAuth() && !manifest.NotModified && f.clientVersion == f.version {
			service.ObserveCodexClientVersion(f.userAgent, f.originator, f.version)
		}
		// A body the blocked-model filter rejects must not replace a usable
		// entry (it is still answered to this request as before).
		storable := manifest.NotModified || codexModelsManifestFilterable(manifest.Body)
		switch {
		case !storable:
			result.legacy = true
		case groupCacheable && manifest.GroupCacheable():
			h.gatewayService.RememberCodexModelsManifestGroupCache(f.key, account.ID, manifest)
			result.shared = true
		case !account.IsOpenAICodexNativeRelay():
			h.gatewayService.RememberCodexModelsManifestFallback(f.key, account.ID, manifest)
		}
		result.account, result.manifest = account, manifest
		return result
	}
}

func codexModelsManifestFilterable(body []byte) bool {
	_, err := service.FilterCodexModelsManifest(body, func(string) bool { return false })
	return err == nil
}

// codexModelsManifestFreshResult returns the fresh group-cache entry as a
// shared result, so a fetch that won the flight after another one stored the
// answer does not ask upstream again.
func (h *OpenAIGatewayHandler) codexModelsManifestFreshResult(f codexModelsManifestFetch) (codexModelsManifestFetchResult, bool) {
	entry, state, _ := h.gatewayService.LookupCodexModelsManifestGroupCache(f.key)
	if state != service.CodexModelsManifestGroupCacheFresh {
		return codexModelsManifestFetchResult{}, false
	}
	return codexModelsManifestFetchResult{shared: true,
		manifest: &service.CodexModelsManifest{Body: entry.Body, ETag: entry.ETag, Headers: entry.Headers}}, true
}

// runCodexModelsManifestFlight is the body of a shared fetch. A panic in a
// singleflight.DoChan function would crash the process, so it is recovered.
func (h *OpenAIGatewayHandler) runCodexModelsManifestFlight(ctx context.Context, f codexModelsManifestFetch, reason string) (result codexModelsManifestFetchResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.FromContext(ctx).Error("openai.codex_models_manifest_flight_panic",
				zap.Int64("group_id", f.key.GroupID), zap.Any("panic", recovered))
			result = codexModelsManifestFetchResult{legacy: true,
				err: infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_REQUEST_FAILED", "Codex models manifest fetch failed")}
		}
	}()
	return h.fetchCodexModelsManifestWithFailover(ctx, f, true, reason)
}

// awaitCodexModelsManifestFlight coalesces concurrent misses of one key into
// one upstream fetch. The leader's fetch runs with the leader's context; every
// caller waits only as long as its own context allows. own reports whether the
// result was fetched for this request. ok is false when the client went away.
func (h *OpenAIGatewayHandler) awaitCodexModelsManifestFlight(ctx context.Context, f codexModelsManifestFetch) (result codexModelsManifestFetchResult, own bool, ok bool) {
	for attempt := 0; attempt < codexModelsManifestFlightRetries; attempt++ {
		leader := false
		ch := h.codexModelsManifestFlight.DoChan(f.flightKey(), func() (any, error) {
			leader = true
			if fresh, ok := h.codexModelsManifestFreshResult(f); ok {
				return fresh, nil
			}
			return h.runCodexModelsManifestFlight(ctx, f, "miss"), nil
		})
		var shared codexModelsManifestFetchResult
		select {
		case <-ctx.Done():
			return codexModelsManifestFetchResult{}, false, false
		case r := <-ch:
			shared, _ = r.Val.(codexModelsManifestFetchResult)
		}
		if leader {
			return shared, true, !shared.canceled
		}
		if shared.canceled {
			// The leader's client went away (or a background refresh ran
			// out of time); join or start the next fetch.
			continue
		}
		if shared.legacy {
			// A relay or API-key account answered: fetch for this request as
			// before instead of reusing another client's answer.
			own := h.fetchCodexModelsManifestWithFailover(ctx, f, true, "miss")
			return own, true, !own.canceled
		}
		return shared, false, true
	}
	result = h.runCodexModelsManifestFlight(ctx, f, "miss")
	return result, true, !result.canceled
}

// serveCodexModelsManifestGroupCache answers from the group cache: a fresh
// entry without contacting upstream; an older one (within the fallback max
// age) immediately while one background refresh is started.
func (h *OpenAIGatewayHandler) serveCodexModelsManifestGroupCache(c *gin.Context, f codexModelsManifestFetch, group *service.Group, filterBlocked bool) bool {
	entry, state, refresh := h.gatewayService.LookupCodexModelsManifestGroupCache(f.key)
	if state == service.CodexModelsManifestGroupCacheMiss {
		return false
	}
	revalidating := state == service.CodexModelsManifestGroupCacheRevalidate
	label := codexModelsManifestCacheHit
	if revalidating {
		label = codexModelsManifestCacheRefresh
	}
	if !h.serveCodexModelsManifestBody(c, entry.Body, entry.Headers, group, filterBlocked, label, revalidating) {
		return false
	}
	if revalidating && refresh {
		h.startCodexModelsManifestRefresh(f, entry.StoredAt)
	}
	return true
}

// startCodexModelsManifestRefresh starts one detached refresh for the key
// unless a fetch for it is already running. It must not use the request: it
// only captures f (copied headers and identity strings).
func (h *OpenAIGatewayHandler) startCodexModelsManifestRefresh(f codexModelsManifestFetch, storedAt time.Time) {
	// A refresh has no client: relay/API-key attempts use the shared
	// (validator-free) headers too.
	f.headers, f.ifNoneMatch = f.sharedHeaders, ""
	// The result channel is buffered; not reading it does not leak.
	_ = h.codexModelsManifestFlight.DoChan(f.flightKey(), func() (result any, err error) {
		if done := h.codexModelsManifestRefreshDone; done != nil {
			defer done()
		}
		// A panic escaping a DoChan function would end the process.
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.L().Error("openai.codex_models_manifest_refresh_panic",
					zap.Int64("group_id", f.key.GroupID), zap.Any("panic", recovered))
				result, err = codexModelsManifestFetchResult{legacy: true}, nil
			}
		}()
		return h.refreshCodexModelsManifest(f, storedAt), nil
	})
}

func (h *OpenAIGatewayHandler) refreshCodexModelsManifest(f codexModelsManifestFetch, storedAt time.Time) codexModelsManifestFetchResult {
	select {
	case h.codexModelsManifestRefreshSlots <- struct{}{}:
		defer func() { <-h.codexModelsManifestRefreshSlots }()
	default:
		// All refresh slots are busy: skip without throttling the entry.
		return codexModelsManifestFetchResult{legacy: true}
	}
	// Re-check after winning the flight: another refresh may have replaced
	// the entry or failed (throttle) while this request was being answered.
	entry, state, due := h.gatewayService.LookupCodexModelsManifestGroupCache(f.key)
	if state != service.CodexModelsManifestGroupCacheRevalidate || !due || !entry.StoredAt.Equal(storedAt) {
		if fresh, ok := h.codexModelsManifestFreshResult(f); ok {
			return fresh
		}
		return codexModelsManifestFetchResult{legacy: true}
	}
	ctx, cancel := context.WithTimeout(context.Background(), service.CodexModelsManifestRefreshBudget(f.maxSwitches))
	defer cancel()
	switch h.gatewayService.CodexModelsManifestGroupCacheEligibility(ctx, f.groupID) {
	case service.CodexModelsManifestGroupCacheIneligible:
		// The group gained a relay/API-key account: stop serving the entry
		// as a hit; it stays a stale fallback.
		h.gatewayService.ForgetCodexModelsManifestGroupCache(f.key, storedAt)
		return codexModelsManifestFetchResult{legacy: true}
	case service.CodexModelsManifestGroupCacheUnknown:
		// Listing failed or no account is schedulable: keep the entry.
		h.gatewayService.MarkCodexModelsManifestRefreshFailed(f.key, storedAt)
		return codexModelsManifestFetchResult{legacy: true}
	}
	result := h.runCodexModelsManifestFlight(ctx, f, "refresh")
	if !result.shared {
		switch service.CodexModelsManifestUpstreamStatus(result.err) {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			// Upstream rejects the request after the switch loop: stop
			// serving a list it now refuses.
			h.gatewayService.DeleteCodexModelsManifestGroupCache(f.key, storedAt)
		default:
			// The old entry is kept with its age; retries are throttled.
			h.gatewayService.MarkCodexModelsManifestRefreshFailed(f.key, storedAt)
		}
		if result.err != nil {
			logger.FromContext(ctx).Warn("openai.codex_models_manifest_refresh_failed",
				zap.Int64("group_id", f.key.GroupID),
				zap.Int("upstream_status", service.CodexModelsManifestUpstreamStatus(result.err)),
				zap.String("cause", infraerrors.Message(result.err)))
		}
	}
	return result
}

// serveCodexModelsManifestBody writes a stored or shared manifest. replay are
// upstream response headers (passed through the native response policy);
// the client's If-None-Match (every line, weak compare) is answered locally.
// It returns false, writing nothing, when the blocked-model filter fails.
func (h *OpenAIGatewayHandler) serveCodexModelsManifestBody(c *gin.Context, body []byte, replay http.Header, group *service.Group, filterBlocked bool, label string, revalidating bool) bool {
	if filterBlocked {
		filtered, err := service.FilterCodexModelsManifest(body, group.IsModelBlocked)
		if err != nil {
			return false
		}
		body = filtered
	}
	dst := c.Writer.Header()
	h.gatewayService.WriteNativeCodexResponseHeaders(dst, replay)
	dst.Set(codexModelsManifestCacheHeader, label)
	if filterBlocked {
		dst.Del("ETag")
		dst.Del("Last-Modified")
		dst.Set("Cache-Control", "private, no-store")
		c.Data(http.StatusOK, "application/json", body)
		return true
	}
	if revalidating || dst.Get("Cache-Control") == "" {
		// Never let clients or intermediaries persist the copy (upstream
		// itself answers private, no-store).
		dst.Set("Cache-Control", "private, no-store")
	}
	// Validate against the ETag actually sent: the response header policy
	// may have removed it.
	etag := strings.TrimSpace(dst.Get("ETag"))
	if service.CodexModelsManifestETagMatches(strings.Join(c.Request.Header.Values("If-None-Match"), ","), etag) {
		c.Status(http.StatusNotModified)
		return true
	}
	c.Data(http.StatusOK, "application/json", body)
	return true
}
