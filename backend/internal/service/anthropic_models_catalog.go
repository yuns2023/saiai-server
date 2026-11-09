package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	claudeAPIModelsURL = "https://api.anthropic.com/v1/models"
	// anthropicModelsCatalogMaxPages bounds pagination; one page of 1000 has
	// always been enough for the official catalog.
	anthropicModelsCatalogMaxPages    = 5
	anthropicModelsCatalogMaxAccounts = 3
	anthropicModelsCatalogBodyLimit   = 4 << 20
	// A source that failed discovery is skipped for this long so that the
	// next refresh reaches other accounts of the group.
	anthropicModelsSourceCooldown = 10 * time.Minute
)

// anthropicModelsSourceTimeout bounds one source account's catalog fetch.
var anthropicModelsSourceTimeout = 6 * time.Second

// ListAnthropicModels returns the official Anthropic model catalog as seen by
// the group's OAuth/setup-token accounts. Only accounts that talk to
// api.anthropic.com directly are used as a source: API-key accounts may point
// at another gateway (both SAIAI sites relay to each other), which would turn
// discovery into a loop and would not be the official list anyway.
func (s *GatewayService) ListAnthropicModels(ctx context.Context, groupID *int64) ([]ModelsCatalogEntry, error) {
	key := "anthropic:" + strconv.FormatInt(derefGroupID(groupID), 10)
	return s.anthropicModels.get(ctx, key, func(fetchCtx context.Context) ([]ModelsCatalogEntry, error) {
		return s.fetchAnthropicModelsCatalog(fetchCtx, groupID)
	})
}

func (s *GatewayService) fetchAnthropicModelsCatalog(ctx context.Context, groupID *int64) ([]ModelsCatalogEntry, error) {
	accounts, _, err := s.listSchedulableAccounts(ctx, groupID, PlatformAnthropic, false)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sources := make([]*Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if account.Platform != PlatformAnthropic || !account.IsAnthropicOAuthOrSetupToken() || !account.IsSchedulable() {
			continue
		}
		if failedAt, ok := s.anthropicModels.sourceFailures.Load(account.ID); ok {
			if now.Sub(failedAt.(time.Time)) < anthropicModelsSourceCooldown {
				continue
			}
			s.anthropicModels.sourceFailures.CompareAndDelete(account.ID, failedAt)
		}
		sources = append(sources, account)
	}
	if len(sources) == 0 {
		return nil, ErrModelsCatalogUnavailable
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].Priority != sources[j].Priority {
			return sources[i].Priority < sources[j].Priority
		}
		return sources[i].ID < sources[j].ID
	})
	if len(sources) > anthropicModelsCatalogMaxAccounts {
		sources = sources[:anthropicModelsCatalogMaxAccounts]
	}
	var lastErr error
	for _, account := range sources {
		// Each source gets its own deadline so one hanging account cannot use
		// up the whole refresh and keep being picked first.
		sourceCtx, cancel := context.WithTimeout(ctx, anthropicModelsSourceTimeout)
		models, fetchErr := s.fetchAnthropicModelsFromAccount(sourceCtx, account)
		cancel()
		if fetchErr == nil && modelsCatalogBytes(models) > modelsCatalogMaxBytes {
			fetchErr = errors.New("upstream models list exceeds the size limit")
		}
		if fetchErr == nil {
			s.anthropicModels.sourceFailures.Delete(account.ID)
			return models, nil
		}
		if ctx.Err() == nil {
			s.anthropicModels.sourceFailures.Store(account.ID, time.Now())
		}
		lastErr = fetchErr
		logger.LegacyPrintf("service.gateway", "Anthropic models catalog fetch failed (account=%d group=%d): %v", account.ID, derefGroupID(groupID), fetchErr)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

// fetchAnthropicModelsFromAccount pages through GET /v1/models the way Claude
// Code's own capability lookup does (Bearer + OAuth beta), reusing the
// account's cached client fingerprint, proxy and TLS profile like the OAuth
// usage poller. Discovery never creates fingerprints, slots or device
// bindings, and upstream errors are not fed into account rate-limit state.
func (s *GatewayService) fetchAnthropicModelsFromAccount(ctx context.Context, account *Account) ([]ModelsCatalogEntry, error) {
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}
	var fingerprint *Fingerprint
	if s.identityService != nil && s.identityService.cache != nil {
		if fp, fpErr := s.identityService.cache.GetFingerprint(ctx, account.ID); fpErr == nil {
			fingerprint = fp
		}
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	models := make([]ModelsCatalogEntry, 0, 32)
	seen := make(map[string]struct{})
	afterID := ""
	usedCursors := make(map[string]struct{})
	for page := 0; page < anthropicModelsCatalogMaxPages; page++ {
		query := url.Values{}
		query.Set("beta", "true")
		query.Set("limit", "1000")
		if afterID != "" {
			query.Set("after_id", afterID)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeAPIModelsURL+"?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		for name, value := range claude.DefaultHeaders {
			req.Header.Set(name, value)
		}
		if fingerprint != nil {
			(&IdentityService{}).ApplyFingerprint(req, fingerprint)
		}
		req.Header.Set("authorization", "Bearer "+token)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", claude.BetaOAuth)
		req.Header.Set("accept", "application/json")
		req.Header.Del("content-type")

		resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, account.IsTLSFingerprintEnabled())
		if err != nil {
			return nil, fmt.Errorf("upstream request: %s", sanitizeUpstreamErrorMessage(err.Error()))
		}
		body, readErr := readUpstreamResponseBodyLimited(resp.Body, anthropicModelsCatalogBodyLimit)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read upstream body: %w", readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(body)))
		}
		pageModels, meta, parseErr := parseModelsCatalogData(body)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid upstream models list: %w", parseErr)
		}
		models = appendUniqueModels(models, seen, pageModels)
		if len(models) > modelsCatalogMaxModels {
			return nil, errors.New("upstream models list is too large")
		}
		if !meta.hasMore {
			return models, nil
		}
		if _, used := usedCursors[meta.lastID]; meta.lastID == "" || used {
			return nil, errors.New("upstream models list has_more without a new last_id")
		}
		usedCursors[meta.lastID] = struct{}{}
		afterID = meta.lastID
	}
	return nil, errors.New("upstream models list exceeds the page limit")
}
