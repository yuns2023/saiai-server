package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const driftFeedA = `{
	"gpt-6-sol": {"input_cost_per_token": 0.000002, "output_cost_per_token": 0.000016, "cache_read_input_token_cost": 0.0000002, "litellm_provider": "openai", "mode": "chat"},
	"gpt-6.1-sol": {"input_cost_per_token": 0.000002, "output_cost_per_token": 0.000016, "cache_read_input_token_cost": 0.0000001, "litellm_provider": "openai", "mode": "chat"},
	"acme-1": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "mode": "chat"},
	"acme-long": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "long_context_input_token_threshold": 200000, "long_context_input_cost_multiplier": 2, "mode": "chat"},
	"acme-image": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "mode": "image_generation"}
}`

// Feed B: the alias target gpt-6-sol halves its cache read, gpt-6.1-sol (only
// billed through the intentional alias) changes its own entry, acme-1 is
// removed, acme-long lowers its threshold and acme-image gains a per-image price.
const driftFeedB = `{
	"gpt-6-sol": {"input_cost_per_token": 0.000002, "output_cost_per_token": 0.000016, "cache_read_input_token_cost": 0.0000001, "litellm_provider": "openai", "mode": "chat"},
	"gpt-6.1-sol": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000016, "cache_read_input_token_cost": 0.0000001, "litellm_provider": "openai", "mode": "chat"},
	"acme-long": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "long_context_input_token_threshold": 128000, "long_context_input_cost_multiplier": 2, "mode": "chat"},
	"acme-image": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "output_cost_per_image": 0.04, "mode": "image_generation"}
}`

func driftTestConfig(dir, url string) *config.Config {
	return &config.Config{Pricing: config.PricingConfig{RemoteURL: url, DataDir: dir, UpdateIntervalHours: 24}}
}

func driftChange(t *testing.T, aliases map[string]string, overrides map[string]ModelPriceOverride) PricingFeedChange {
	t.Helper()
	cfg := &config.Config{}
	parse := func(body string) map[string]*LiteLLMModelPricing {
		data, err := (&PricingService{}).parsePricingData([]byte(body))
		require.NoError(t, err)
		return data
	}
	old := newPricingSnapshotService(cfg, parse(driftFeedA), "hash-a", "remote", aliases, overrides)
	return PricingFeedChange{Old: old, New: newPricingSnapshotService(cfg, parse(driftFeedB), "hash-b", "remote", aliases, overrides), OldHash: "hash-a", NewHash: "hash-b", ConfigHash: old.configHash}
}

func driftNoticeFor(notices []PricingDriftNotice, model string) *PricingDriftNotice {
	for i := range notices {
		if notices[i].Model == model {
			return &notices[i]
		}
	}
	return nil
}

func driftChangeFor(n *PricingDriftNotice, key string) *PricingDriftChange {
	for i := range n.Changes {
		if n.Changes[i].Key == key {
			return &n.Changes[i]
		}
	}
	return nil
}

func TestDiffPricingFeedsFollowsAliasesAndOverrides(t *testing.T) {
	change := driftChange(t,
		map[string]string{"gpt-6.1-sol": "gpt-6-sol", "gpt-6-sol-pinned": "gpt-6-sol"},
		map[string]ModelPriceOverride{"gpt-6-sol-pinned": {CacheRead: priceTestNumber(0.2)}},
	)
	notices := diffPricingFeeds(&config.Config{}, change, []string{"GPT-6.1-sol", "gpt-6.1-sol", "gpt-6-sol-pinned", "acme-1", "acme-long", "acme-image", "acme-unchanged"})

	// The intentional alias is never reported; its target's price change is,
	// under the billed name, and its own (unused) feed entry change is not.
	aliased := driftNoticeFor(notices, "gpt-6.1-sol")
	require.NotNil(t, aliased)
	require.Equal(t, []string{"default.cache_read", "flex.cache_read", "priority.cache_read"}, driftChangeKeys(aliased))
	read := driftChangeFor(aliased, "default.cache_read")
	require.InDelta(t, 0.2, *read.Old, 1e-9)
	require.InDelta(t, 0.1, *read.New, 1e-9)
	require.Equal(t, "gpt-6-sol", aliased.NewResolvedModel)

	// A price pinned by an override (and the tier prices billing derives
	// from it) does not change, so the model is not reported.
	require.Nil(t, driftNoticeFor(notices, "gpt-6-sol-pinned"))

	removed := driftNoticeFor(notices, "acme-1")
	require.NotNil(t, removed)
	require.Equal(t, PricingStatusPriced, removed.OldStatus)
	require.Equal(t, PricingStatusUnpriced, removed.NewStatus)
	require.NotNil(t, driftChangeFor(removed, "default.input").Old)
	require.Nil(t, driftChangeFor(removed, "default.input").New)

	long := driftNoticeFor(notices, "acme-long")
	require.NotNil(t, long)
	require.Equal(t, []string{"long.threshold"}, driftChangeKeys(long))
	require.Equal(t, 200000.0, *long.Changes[0].Old)
	require.Equal(t, 128000.0, *long.Changes[0].New)

	image := driftNoticeFor(notices, "acme-image")
	require.NotNil(t, image)
	require.Equal(t, []string{"image.per_image"}, driftChangeKeys(image))
	require.Nil(t, image.Changes[0].Old)

	require.Nil(t, driftNoticeFor(notices, "acme-unchanged"))
	require.Len(t, notices, 4)
	for _, n := range notices {
		require.Len(t, n.ID, 16)
		require.Equal(t, "hash-a", n.OldFeedHash)
		require.Equal(t, "hash-b", n.NewFeedHash)
	}
}

func TestDiffPricingFeedsIgnoresSourceOnlyDifferences(t *testing.T) {
	change := driftChange(t, map[string]string{}, map[string]ModelPriceOverride{})
	change.New = newPricingSnapshotService(&config.Config{}, change.Old.pricingData, "hash-b", "local_cache", map[string]string{}, map[string]ModelPriceOverride{})
	require.Empty(t, diffPricingFeeds(&config.Config{}, change, []string{"gpt-6-sol", "acme-1"}))
}

func driftChangeKeys(n *PricingDriftNotice) []string {
	keys := []string{}
	for _, c := range n.Changes {
		keys = append(keys, c.Key)
	}
	return keys
}

func TestMergePricingDriftNotices(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	found := []PricingDriftNotice{{ID: "a", Model: "m"}}
	merged, added := mergePricingDriftNotices(nil, found, now)
	require.Len(t, merged, 1)
	require.Len(t, added, 1)
	require.Equal(t, 1, merged[0].Occurrences)

	// Another Gateway reporting the same feed change shortly after.
	acked := now.Add(time.Minute)
	merged[0].AcknowledgedAt = &acked
	merged2, added := mergePricingDriftNotices(merged, found, now.Add(10*time.Minute))
	require.Empty(t, added)
	require.NotNil(t, merged2[0].AcknowledgedAt)

	// The same change after a newer notice for the model (it flipped back and
	// forth) reopens the acknowledged notice.
	merged3, _ := mergePricingDriftNotices(merged2, []PricingDriftNotice{{ID: "b", Model: "m"}}, now.Add(20*time.Minute))
	merged4, added := mergePricingDriftNotices(merged3, found, now.Add(30*time.Minute))
	require.Len(t, added, 1)
	reopened := driftNoticeByID(merged4, "a")
	require.Nil(t, reopened.AcknowledgedAt)
	require.Equal(t, 2, reopened.Occurrences)
	require.Equal(t, now.Add(30*time.Minute), reopened.DetectedAt)

	// Past the repeat window the same change is a new occurrence.
	_, added = mergePricingDriftNotices(merged4, []PricingDriftNotice{{ID: "a", Model: "m"}}, now.Add(30*time.Minute+pricingDriftRepeatWindow))
	require.Len(t, added, 1)
}

func driftNoticeByID(notices []PricingDriftNotice, id string) *PricingDriftNotice {
	for i := range notices {
		if notices[i].ID == id {
			return &notices[i]
		}
	}
	return nil
}

func TestTrimPricingDriftNoticesKeepsOpenNoticesFirst(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var notices []PricingDriftNotice
	for i := 0; i < pricingDriftMaxOpen+5; i++ {
		notices = append(notices, PricingDriftNotice{ID: fmt.Sprintf("open-%d", i), DetectedAt: base.Add(time.Duration(i) * time.Minute)})
	}
	for i := 0; i < pricingDriftMaxAcknowledged+5; i++ {
		at := base
		notices = append(notices, PricingDriftNotice{ID: fmt.Sprintf("acked-%d", i), DetectedAt: base.Add(time.Duration(i) * time.Minute), AcknowledgedAt: &at})
	}
	out := trimPricingDriftNotices(notices)
	require.Len(t, out, pricingDriftMaxOpen+pricingDriftMaxAcknowledged)
	require.Equal(t, fmt.Sprintf("open-%d", pricingDriftMaxOpen+4), out[0].ID)
	require.Nil(t, driftNoticeByID(out, "open-0"))
	require.Nil(t, driftNoticeByID(out, "acked-0"))
}

type driftBilledStub struct {
	mu     sync.Mutex
	models []string
	errs   []error
	since  time.Time
	calls  int
}

func (s *driftBilledStub) ListBilledPricingModels(_ context.Context, since time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.since = since
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		return nil, err
	}
	return s.models, nil
}

type driftStoreStub struct {
	mu    sync.Mutex
	value string
	set   bool
}

func (s *driftStoreStub) GetValue(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set {
		return "", ErrSettingNotFound
	}
	return s.value, nil
}

func (s *driftStoreStub) UpdateSettingValue(_ context.Context, _ string, update func(string) (string, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := update(s.value)
	if err != nil {
		return err
	}
	s.value, s.set = next, true
	return nil
}

func TestPricingDriftServiceStoresListsAndAcknowledges(t *testing.T) {
	billed := &driftBilledStub{models: []string{"acme-1", "gpt-6-sol"}, errs: []error{errors.New("db down")}}
	store := &driftStoreStub{}
	drift := NewPricingDriftService(&config.Config{}, billed, store, nil)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	drift.now = func() time.Time { return now }
	drift.retryDelays = []time.Duration{time.Millisecond}

	list, err := drift.List(context.Background())
	require.NoError(t, err)
	require.Empty(t, list.Notices)

	drift.Enqueue(driftChange(t, map[string]string{}, map[string]ModelPriceOverride{}))
	drift.wait()
	require.Equal(t, 2, billed.calls, "a failed attempt is retried")
	require.Equal(t, now.Add(-pricingDriftLookback), billed.since)

	list, err = drift.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, list.Unacknowledged)
	require.Equal(t, now, list.Notices[0].DetectedAt)

	first := list.Notices[0].ID
	list, err = drift.Acknowledge(context.Background(), []string{first, "unknown"}, 7)
	require.NoError(t, err)
	require.Equal(t, 1, list.Unacknowledged)
	require.Equal(t, first, list.Notices[1].ID, "acknowledged notices sort after open ones")
	require.EqualValues(t, 7, list.Notices[1].AcknowledgedBy)

	now = now.Add(time.Hour)
	list, err = drift.Acknowledge(context.Background(), []string{first}, 9)
	require.NoError(t, err)
	require.EqualValues(t, 7, list.Notices[1].AcknowledgedBy, "the first acknowledgement is kept")
	require.Equal(t, now.Add(-time.Hour), *list.Notices[1].AcknowledgedAt)

	// The same feed change from another Gateway adds nothing.
	drift.Enqueue(driftChange(t, map[string]string{}, map[string]ModelPriceOverride{}))
	drift.wait()
	list, err = drift.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list.Notices, 2)
	require.Equal(t, 1, list.Unacknowledged)
}

func TestPricingDriftServiceKeepsCorruptStoreIntact(t *testing.T) {
	store := &driftStoreStub{value: "{not json", set: true}
	drift := NewPricingDriftService(&config.Config{}, &driftBilledStub{models: []string{"acme-1"}}, store, nil)
	drift.retryDelays = nil
	drift.Enqueue(driftChange(t, map[string]string{}, map[string]ModelPriceOverride{}))
	drift.wait()
	require.Equal(t, "{not json", store.value)
	_, err := drift.List(context.Background())
	require.Error(t, err)
	_, err = drift.Acknowledge(context.Background(), []string{"x"}, 1)
	require.Error(t, err)
	require.Equal(t, "{not json", store.value)
}

func TestPricingSyncPublishesFeedChangesInOrder(t *testing.T) {
	dir := t.TempDir()
	remote := &fakePricingRemoteClient{pricingBody: []byte(driftFeedA)}
	svc := NewPricingService(driftTestConfig(dir, "https://example.com/model_pricing.json"), remote)
	var changes []PricingFeedChange
	svc.SetFeedChangeHandler(func(c PricingFeedChange) { changes = append(changes, c) })

	// The first load has nothing to compare with.
	require.NoError(t, svc.downloadPricingData())
	require.Empty(t, changes)
	// Unchanged content publishes nothing.
	require.NoError(t, svc.syncWithRemote())
	require.Empty(t, changes)

	remote.pricingBody = []byte(driftFeedB)
	require.NoError(t, svc.syncWithRemote())
	require.Len(t, changes, 1)
	c := changes[0]
	require.NotEqual(t, c.OldHash, c.NewHash)
	require.Equal(t, c.NewHash, svc.currentPricingHash())
	require.NotNil(t, c.Old.GetModelPricing("acme-1"))
	require.Nil(t, c.New.GetModelPricing("acme-1"))

	// The snapshots keep the configuration of the moment of the change.
	svc.SetModelPricingConfig(map[string]string{"x": "gpt-6-sol"}, nil)
	require.NotContains(t, c.New.modelAliases, "x")
}

func TestPricingStartupRefreshComparesWithStaleLocalCopy(t *testing.T) {
	dir := t.TempDir()
	pricingFile := filepath.Join(dir, "model_pricing.json")
	require.NoError(t, os.WriteFile(pricingFile, []byte(driftFeedA), 0644))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(pricingFile, old, old))

	remote := &fakePricingRemoteClient{pricingBody: []byte(driftFeedB)}
	svc := NewPricingService(driftTestConfig(dir, "https://example.com/model_pricing.json"), remote)
	require.NoError(t, svc.checkAndUpdatePricing())
	require.Equal(t, 1, remote.fetches)
	require.Nil(t, svc.GetModelPricing("acme-1"), "the downloaded feed is kept, not the stale file")

	// The change seen before a handler exists is delivered on registration.
	var changes []PricingFeedChange
	svc.SetFeedChangeHandler(func(c PricingFeedChange) { changes = append(changes, c) })
	require.Len(t, changes, 1)
	require.NotNil(t, changes[0].Old.GetModelPricing("acme-1"))
}

type failingPricingRemoteClient struct{ fakePricingRemoteClient }

func (f *failingPricingRemoteClient) FetchPricingJSON(context.Context, string) ([]byte, error) {
	return nil, errors.New("offline")
}

func TestPricingStartupKeepsOrRepairsLocalCopy(t *testing.T) {
	dir := t.TempDir()
	pricingFile := filepath.Join(dir, "model_pricing.json")
	require.NoError(t, os.WriteFile(pricingFile, []byte(driftFeedA), 0644))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(pricingFile, old, old))
	cfg := driftTestConfig(dir, "https://example.com/model_pricing.json")

	// A stale but valid copy stays in use when the download fails.
	svc := NewPricingService(cfg, &failingPricingRemoteClient{})
	require.NoError(t, svc.checkAndUpdatePricing())
	require.NotNil(t, svc.GetModelPricing("acme-1"))

	// A corrupt fresh copy is repaired from the remote feed.
	require.NoError(t, os.WriteFile(pricingFile, []byte("{"), 0644))
	repaired := NewPricingService(cfg, &fakePricingRemoteClient{pricingBody: []byte(driftFeedB)})
	require.NoError(t, repaired.checkAndUpdatePricing())
	require.NotNil(t, repaired.GetModelPricing("acme-long"))

	// Corrupt and offline fails, so Initialize falls back to the bundled file.
	require.NoError(t, os.WriteFile(pricingFile, []byte("{"), 0644))
	require.Error(t, NewPricingService(cfg, &failingPricingRemoteClient{}).checkAndUpdatePricing())
}
