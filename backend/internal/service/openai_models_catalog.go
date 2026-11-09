package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// codexModelsCatalogBaselineVersion is the client_version used when the
// gateway itself fetches the Codex manifest for non-Codex clients. The
// manifest only lists models a client version supports, so the newest
// version seen from official Codex clients replaces it at runtime.
const codexModelsCatalogBaselineVersion = "0.167.0"

const openAIModelsCatalogMaxAttempts = 3

var codexModelsCatalogVersion struct {
	mu      sync.Mutex
	version openai.CodexSemver
	set     bool
}

// ObserveCodexClientVersion records the engine version of an official Codex
// client so later catalog fetches ask the manifest for current models.
// Prerelease versions are ignored.
func ObserveCodexClientVersion(userAgent, originator, versionHeader string) {
	_, version, isCodex, known := openai.CodexEngineVersion(userAgent, originator, versionHeader)
	if !isCodex || !known || strings.Contains(version.String(), "-") {
		return
	}
	codexModelsCatalogVersion.mu.Lock()
	defer codexModelsCatalogVersion.mu.Unlock()
	if !codexModelsCatalogVersion.set || version.Compare(codexModelsCatalogVersion.version) > 0 {
		codexModelsCatalogVersion.version = version
		codexModelsCatalogVersion.set = true
	}
}

func currentCodexModelsCatalogVersion() string {
	baseline, _ := openai.ParseCodexSemver(codexModelsCatalogBaselineVersion)
	codexModelsCatalogVersion.mu.Lock()
	defer codexModelsCatalogVersion.mu.Unlock()
	if codexModelsCatalogVersion.set && codexModelsCatalogVersion.version.Compare(baseline) > 0 {
		return codexModelsCatalogVersion.version.String()
	}
	return baseline.String()
}

// ListOpenAIModels returns an OpenAI-style model list for the group, built
// from the same upstream Codex manifest official Codex clients receive.
// Image models are served by gateway endpoints outside the Codex manifest and
// are appended from the built-in list.
func (s *OpenAIGatewayService) ListOpenAIModels(ctx context.Context, groupID *int64) ([]ModelsCatalogEntry, error) {
	key := "openai:" + strconv.FormatInt(derefGroupID(groupID), 10)
	return s.openAIModelsCatalog.get(ctx, key, func(fetchCtx context.Context) ([]ModelsCatalogEntry, error) {
		return s.fetchOpenAIModelsCatalog(fetchCtx, groupID)
	})
}

func (s *OpenAIGatewayService) fetchOpenAIModelsCatalog(ctx context.Context, groupID *int64) ([]ModelsCatalogEntry, error) {
	version := currentCodexModelsCatalogVersion()
	rawQuery := url.Values{"client_version": {version}}.Encode()
	excluded := make(map[int64]struct{})
	var lastErr error
	for attempt := 0; attempt < openAIModelsCatalogMaxAttempts; attempt++ {
		account, err := s.SelectAccountForModelWithExclusions(ctx, groupID, "", "", excluded)
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		headers := make(http.Header)
		ApplyCodexModelsDiscoveryIdentity(headers, version)
		manifest, err := s.FetchNativeCodexModelsManifest(ctx, account, rawQuery, "", headers)
		if err == nil {
			if manifest.NotModified {
				err = errors.New("Codex models manifest returned 304 to an unconditional request")
			} else {
				return openAIModelsFromCodexManifest(manifest.Body)
			}
		}
		lastErr = err
		if !IsRetryableCodexModelsManifestError(err) || ctx.Err() != nil {
			return nil, err
		}
		excluded[account.ID] = struct{}{}
	}
	return nil, lastErr
}

// openAIModelsFromCodexManifest converts manifest models[].slug into OpenAI
// list objects. The manifest carries no release dates, so created is 0
// instead of an invented timestamp.
func openAIModelsFromCodexManifest(body []byte) ([]ModelsCatalogEntry, error) {
	var manifest struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("invalid Codex models manifest: %w", err)
	}
	models := make([]ModelsCatalogEntry, 0, len(manifest.Models)+2)
	seen := make(map[string]struct{})
	for _, item := range manifest.Models {
		slug := strings.TrimSpace(item.Slug)
		if slug == "" {
			continue
		}
		displayName := strings.TrimSpace(item.DisplayName)
		if displayName == "" {
			displayName = slug
		}
		raw, err := json.Marshal(openai.Model{ID: slug, Object: "model", OwnedBy: "openai", Type: "model", DisplayName: displayName})
		if err != nil {
			return nil, err
		}
		models = appendUniqueModels(models, seen, []ModelsCatalogEntry{{ID: slug, Raw: raw}})
	}
	if len(models) == 0 {
		return nil, errors.New("Codex models manifest lists no models")
	}
	if len(models) > modelsCatalogMaxModels {
		return nil, errors.New("Codex models manifest is too large")
	}
	for _, model := range openai.DefaultModels {
		if !strings.HasPrefix(model.ID, "gpt-image-") {
			continue
		}
		raw, err := json.Marshal(model)
		if err != nil {
			return nil, err
		}
		models = appendUniqueModels(models, seen, []ModelsCatalogEntry{{ID: model.ID, Raw: raw}})
	}
	return models, nil
}
