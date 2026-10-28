package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// SettingKeyPricingDriftNotices stores the price-feed change notices shown on
// the admin model-pricing page.
const SettingKeyPricingDriftNotices = "model_pricing_drift_notices"

const (
	pricingDriftLookback        = 30 * 24 * time.Hour
	pricingDriftMaxQueued       = 8
	pricingDriftMaxOpen         = 500
	pricingDriftMaxAcknowledged = 100
	// The same change seen again within this window, while it is still the
	// latest notice for the model, is another Gateway reporting the same feed.
	pricingDriftRepeatWindow   = 24 * time.Hour
	pricingDriftAttemptTimeout = 2 * time.Minute
)

// Pricing statuses: priced by the feed (or an override), priced by the
// built-in hardcoded fallback, or not priceable at all.
const (
	PricingStatusPriced   = "priced"
	PricingStatusFallback = "fallback"
	PricingStatusUnpriced = "unpriced"
)

// PricingFeedChange is a replacement of the remote feed content. Old and New
// are frozen pricing services sharing one alias/override configuration, so
// comparing them isolates the feed change from administrator edits.
type PricingFeedChange struct {
	Old        *PricingService
	New        *PricingService
	OldHash    string
	NewHash    string
	ConfigHash string
	At         time.Time
}

func newPricingSnapshotService(cfg *config.Config, data map[string]*LiteLLMModelPricing, hash, source string, aliases map[string]string, overrides map[string]ModelPriceOverride) *PricingService {
	p := &PricingService{cfg: cfg, pricingData: data, localHash: hash, priceSource: source, modelAliases: aliases, modelOverrides: overrides}
	p.updateConfigHashLocked()
	return p
}

// SetFeedChangeHandler registers the receiver of feed changes. Changes seen
// before registration (the startup refresh) are delivered first, in order.
// The handler is called with an internal lock held and must not block.
func (s *PricingService) SetFeedChangeHandler(handler func(PricingFeedChange)) {
	s.feedChangeMu.Lock()
	defer s.feedChangeMu.Unlock()
	s.feedChangeHandler = handler
	for _, change := range s.pendingFeedChanges {
		handler(change)
	}
	s.pendingFeedChanges = nil
}

func (s *PricingService) publishFeedChange(change PricingFeedChange) {
	s.feedChangeMu.Lock()
	defer s.feedChangeMu.Unlock()
	if s.feedChangeHandler != nil {
		s.feedChangeHandler(change)
		return
	}
	s.pendingFeedChanges = append(s.pendingFeedChanges, change)
	if len(s.pendingFeedChanges) > pricingDriftMaxQueued {
		logger.LegacyPrintf("service.pricing", "[Pricing] WARN dropping unevaluated feed change %s", shortFeedHash(s.pendingFeedChanges[0].NewHash))
		s.pendingFeedChanges = s.pendingFeedChanges[1:]
	}
}

// Done is closed when the pricing service stops.
func (s *PricingService) Done() <-chan struct{} { return s.stopCh }

// PricingDriftChange is one price that differs between two feeds. Prices are
// USD per million tokens, except image.per_image (USD per image) and
// long.threshold (input tokens). Nil means the price does not exist.
type PricingDriftChange struct {
	Key string   `json:"key"`
	Old *float64 `json:"old"`
	New *float64 `json:"new"`
}

// PricingDriftNotice records that a feed change altered how a recently billed
// model is priced. Acknowledging it only records that an administrator saw it.
type PricingDriftNotice struct {
	// ID is random. Fingerprint names the change (feed, pricing
	// configuration, model and prices) across Gateways. Reporter is the Gateway
	// process that recorded it; Observed holds when each Gateway process saw
	// this change, so a later recurrence is told apart from a duplicate.
	ID               string               `json:"id"`
	Fingerprint      string               `json:"fingerprint"`
	Reporter         string               `json:"reporter"`
	Observed         map[string]time.Time `json:"observed,omitempty"`
	Model            string               `json:"model"`
	OldStatus        string               `json:"old_status"`
	NewStatus        string               `json:"new_status"`
	OldResolvedModel string               `json:"old_resolved_model"`
	NewResolvedModel string               `json:"new_resolved_model"`
	Changes          []PricingDriftChange `json:"changes"`
	OldFeedHash      string               `json:"old_feed_hash"`
	NewFeedHash      string               `json:"new_feed_hash"`
	DetectedAt       time.Time            `json:"detected_at"`
	AcknowledgedAt   *time.Time           `json:"acknowledged_at"`
	AcknowledgedBy   int64                `json:"acknowledged_by"`
}

type PricingDriftList struct {
	Notices        []PricingDriftNotice `json:"notices"`
	Unacknowledged int                  `json:"unacknowledged"`
}

// BilledPricingModelReader lists the model names billing priced since a time.
type BilledPricingModelReader interface {
	ListBilledPricingModels(ctx context.Context, since time.Time) ([]string, error)
}

// SettingValueUpdater atomically replaces one setting value. update receives
// the current value ("" when unset) and may run more than once.
type SettingValueUpdater interface {
	UpdateSettingValue(ctx context.Context, key string, update func(current string) (string, error)) error
}

type pricingDriftStore interface {
	SettingValueUpdater
	GetValue(ctx context.Context, key string) (string, error)
}

// PricingDriftService evaluates feed changes for models billed in the last
// 30 days and keeps notices for the admin model-pricing page. It is
// best-effort: evaluation never blocks a feed replacement, failures are
// retried a few times, and a change is lost if the process exits first.
type PricingDriftService struct {
	instance    string
	cfg         *config.Config
	billed      BilledPricingModelReader
	store       pricingDriftStore
	done        <-chan struct{}
	now         func() time.Time
	retryDelays []time.Duration

	mu      sync.Mutex
	queue   []PricingFeedChange
	running bool
	idle    chan struct{}
}

func NewPricingDriftService(cfg *config.Config, billed BilledPricingModelReader, store pricingDriftStore, done <-chan struct{}) *PricingDriftService {
	return &PricingDriftService{instance: randomPricingDriftID(), cfg: cfg, billed: billed, store: store, done: done, now: time.Now, retryDelays: []time.Duration{time.Minute, 5 * time.Minute}}
}

// Enqueue accepts a feed change without blocking.
func (s *PricingDriftService) Enqueue(change PricingFeedChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, change)
	if len(s.queue) > pricingDriftMaxQueued {
		logger.LegacyPrintf("service.pricing_drift", "[PricingDrift] WARN queue full, dropping feed change %s", shortFeedHash(s.queue[0].NewHash))
		s.queue = s.queue[1:]
	}
	if !s.running {
		s.running = true
		s.idle = make(chan struct{})
		go s.run(s.idle)
	}
}

func (s *PricingDriftService) run(idle chan struct{}) {
	defer close(idle)
	for {
		s.mu.Lock()
		batch := s.queue
		s.queue = nil
		if len(batch) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		s.processBatch(batch)
	}
}

// wait blocks until the worker is idle; for tests.
func (s *PricingDriftService) wait() {
	s.mu.Lock()
	idle := s.idle
	s.mu.Unlock()
	if idle != nil {
		<-idle
	}
}

func (s *PricingDriftService) processBatch(batch []PricingFeedChange) {
	for attempt := 0; ; attempt++ {
		err := s.evaluateAndStore(batch)
		if err == nil {
			return
		}
		if attempt >= len(s.retryDelays) {
			logger.LegacyPrintf("service.pricing_drift", "[PricingDrift] ERROR giving up on %d feed change(s) ending at %s: %v", len(batch), shortFeedHash(batch[len(batch)-1].NewHash), err)
			return
		}
		logger.LegacyPrintf("service.pricing_drift", "[PricingDrift] evaluation failed, retrying in %v: %v", s.retryDelays[attempt], err)
		select {
		case <-time.After(s.retryDelays[attempt]):
		case <-s.done:
			return
		}
	}
}

func (s *PricingDriftService) evaluateAndStore(batch []PricingFeedChange) error {
	ctx, cancel := context.WithTimeout(context.Background(), pricingDriftAttemptTimeout)
	defer cancel()
	started := time.Now()
	models, err := s.billed.ListBilledPricingModels(ctx, s.now().Add(-pricingDriftLookback))
	if err != nil {
		return fmt.Errorf("list billed models: %w", err)
	}
	logger.LegacyPrintf("service.pricing_drift", "[PricingDrift] checking %d feed change(s) against %d billed models (listed in %v)", len(batch), len(models), time.Since(started).Round(time.Millisecond))
	var found []PricingDriftNotice
	for _, change := range batch {
		found = append(found, diffPricingFeeds(s.cfg, change, models)...)
	}
	if len(found) == 0 {
		return nil
	}
	var added, dropped []PricingDriftNotice
	err = s.store.UpdateSettingValue(ctx, SettingKeyPricingDriftNotices, func(current string) (string, error) {
		existing, err := decodePricingDriftNotices(current)
		if err != nil {
			return "", err
		}
		var merged []PricingDriftNotice
		merged, added = mergePricingDriftNotices(existing, found, s.instance)
		merged, dropped = trimPricingDriftNotices(merged)
		body, err := json.Marshal(merged)
		return string(body), err
	})
	if err != nil {
		return fmt.Errorf("save notices: %w", err)
	}
	logDroppedPricingDriftNotices(dropped)
	for _, n := range added {
		body, _ := json.Marshal(n.Changes)
		logger.LegacyPrintf("service.pricing_drift", "[PricingDrift] WARN price feed %s changed billed model %s: status %s -> %s, resolved %s -> %s, changes %s", shortFeedHash(n.NewFeedHash), n.Model, n.OldStatus, n.NewStatus, n.OldResolvedModel, n.NewResolvedModel, body)
	}
	return nil
}

// List returns all notices, unacknowledged first, newest first.
func (s *PricingDriftService) List(ctx context.Context) (*PricingDriftList, error) {
	raw, err := s.store.GetValue(ctx, SettingKeyPricingDriftNotices)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, err
	}
	notices, err := decodePricingDriftNotices(raw)
	if err != nil {
		return nil, err
	}
	return newPricingDriftList(notices), nil
}

// Acknowledge marks the given notices as seen. The first acknowledgement's
// time and actor are kept.
func (s *PricingDriftService) Acknowledge(ctx context.Context, ids []string, actorID int64) (*PricingDriftList, error) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var notices, dropped []PricingDriftNotice
	err := s.store.UpdateSettingValue(ctx, SettingKeyPricingDriftNotices, func(current string) (string, error) {
		var err error
		notices, err = decodePricingDriftNotices(current)
		if err != nil {
			return "", err
		}
		now := s.now().UTC()
		for i := range notices {
			if want[notices[i].ID] && notices[i].AcknowledgedAt == nil {
				notices[i].AcknowledgedAt = &now
				notices[i].AcknowledgedBy = actorID
			}
		}
		notices, dropped = trimPricingDriftNotices(notices)
		body, err := json.Marshal(notices)
		return string(body), err
	})
	if err != nil {
		return nil, err
	}
	logDroppedPricingDriftNotices(dropped)
	return newPricingDriftList(notices), nil
}

func decodePricingDriftNotices(raw string) ([]PricingDriftNotice, error) {
	notices := []PricingDriftNotice{}
	if strings.TrimSpace(raw) == "" {
		return notices, nil
	}
	if err := json.Unmarshal([]byte(raw), &notices); err != nil {
		return nil, fmt.Errorf("decode pricing drift notices: %w", err)
	}
	return notices, nil
}

func newPricingDriftList(notices []PricingDriftNotice) *PricingDriftList {
	list := &PricingDriftList{Notices: sortPricingDriftNotices(notices)}
	for _, n := range list.Notices {
		if n.AcknowledgedAt == nil {
			list.Unacknowledged++
		}
	}
	return list
}

func sortPricingDriftNotices(notices []PricingDriftNotice) []PricingDriftNotice {
	out := append([]PricingDriftNotice{}, notices...)
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].AcknowledgedAt == nil) != (out[j].AcknowledgedAt == nil) {
			return out[i].AcknowledgedAt == nil
		}
		return out[i].DetectedAt.After(out[j].DetectedAt)
	})
	return out
}

// mergePricingDriftNotices adds the notices this Gateway process found. A
// process sees each feed replacement once. A stored notice with the same
// fingerprint that this process already observed at the same time is a
// retried save; one it observed at another time is an earlier occurrence (the
// feed flipped back and forth). One it has not observed, recorded by another
// Gateway within the repeat window (even reported late), is the same feed
// change: this process's observation is added to it. Anything else is a new
// notice with a new ID, so a page showing an earlier notice can never
// acknowledge it.
func mergePricingDriftNotices(existing, found []PricingDriftNotice, reporter string) (merged, added []PricingDriftNotice) {
	merged = append([]PricingDriftNotice{}, existing...)
	for _, n := range found {
		retried := false
		for _, stored := range merged {
			if seen, ok := stored.Observed[reporter]; ok && stored.Fingerprint == n.Fingerprint && seen.Equal(n.DetectedAt) {
				retried = true
				break
			}
		}
		// Otherwise join the closest notice of another Gateway in the window.
		match := -1
		var matchGap time.Duration
		for i, stored := range merged {
			if _, ok := stored.Observed[reporter]; ok || stored.Fingerprint != n.Fingerprint {
				continue
			}
			gap := n.DetectedAt.Sub(stored.DetectedAt)
			if gap < 0 {
				gap = -gap
			}
			if gap < pricingDriftRepeatWindow && (match < 0 || gap < matchGap) {
				match, matchGap = i, gap
			}
		}
		switch {
		case retried:
		case match >= 0:
			observed := map[string]time.Time{reporter: n.DetectedAt}
			for k, v := range merged[match].Observed {
				observed[k] = v
			}
			merged[match].Observed = observed
		default:
			n.ID, n.Reporter, n.Observed = randomPricingDriftID(), reporter, map[string]time.Time{reporter: n.DetectedAt}
			merged = append(merged, n)
			added = append(added, n)
		}
	}
	return merged, added
}

func randomPricingDriftID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// trimPricingDriftNotices keeps the newest 500 unacknowledged and 100
// acknowledged notices.
func trimPricingDriftNotices(notices []PricingDriftNotice) (kept, dropped []PricingDriftNotice) {
	sorted := sortPricingDriftNotices(notices)
	kept = make([]PricingDriftNotice, 0, len(sorted))
	open, acknowledged := 0, 0
	for _, n := range sorted {
		if n.AcknowledgedAt == nil {
			if open++; open > pricingDriftMaxOpen {
				dropped = append(dropped, n)
				continue
			}
		} else if acknowledged++; acknowledged > pricingDriftMaxAcknowledged {
			continue
		}
		kept = append(kept, n)
	}
	return kept, dropped
}

func logDroppedPricingDriftNotices(dropped []PricingDriftNotice) {
	for _, n := range dropped {
		logger.LegacyPrintf("service.pricing_drift", "[PricingDrift] WARN dropped unacknowledged notice %s for %s (over %d)", n.ID, n.Model, pricingDriftMaxOpen)
	}
}

// diffPricingFeeds compares how each billed model is priced before and after
// a feed change, through the billing calculator itself.
func diffPricingFeeds(cfg *config.Config, change PricingFeedChange, models []string) []PricingDriftNotice {
	if change.Old == nil || change.New == nil {
		return nil
	}
	oldBilling, newBilling := NewBillingService(cfg, change.Old), NewBillingService(cfg, change.New)
	detectedAt := change.At.UTC()
	if change.At.IsZero() {
		detectedAt = time.Now().UTC()
	}
	seen := map[string]bool{}
	var out []PricingDriftNotice
	for _, model := range models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		image := isImagePricingModel(change.Old, model) || isImagePricingModel(change.New, model)
		before := describeModelPricing(oldBilling, change.Old, model, image)
		after := describeModelPricing(newBilling, change.New, model, image)
		changes := diffModelPrices(before.prices, after.prices)
		if before.status == after.status && len(changes) == 0 {
			continue
		}
		n := PricingDriftNotice{Model: model, OldStatus: before.status, NewStatus: after.status, OldResolvedModel: before.resolved, NewResolvedModel: after.resolved, Changes: changes, OldFeedHash: change.OldHash, NewFeedHash: change.NewHash, DetectedAt: detectedAt}
		n.Fingerprint = pricingDriftFingerprint(change.NewHash, change.ConfigHash, n)
		out = append(out, n)
	}
	return out
}

type modelPricingBehavior struct {
	status   string
	resolved string
	prices   map[string]float64
}

// describeModelPricing lists every unit price billing would use for model:
// each service tier, the whole-session long-context tier and its threshold,
// image tokens, and the default per-image price of image models (group image
// prices take precedence over it and are not compared).
func describeModelPricing(billing *BillingService, pricing *PricingService, model string, image bool) modelPricingBehavior {
	b := modelPricingBehavior{status: PricingStatusUnpriced, prices: map[string]float64{}}
	// The default per-image price (feed, else the hardcoded default) only
	// matters for image models (on either side of the change).
	if image {
		b.prices["image.per_image"] = billing.getDefaultImagePrice(model, "1K")
	}
	p, err := billing.GetModelPricing(model)
	if err != nil {
		return b
	}
	b.status = PricingStatusPriced
	if p.Snapshot != nil {
		b.resolved = p.Snapshot.ResolvedModel
		if p.Snapshot.Source == "hardcoded_fallback" {
			b.status = PricingStatusFallback
		}
	}
	for _, tier := range []string{"default", "priority", "flex"} {
		for field, price := range billing.DisplayUnitPrices(p, tier) {
			b.prices[tier+"."+field] = price
		}
		for field, price := range billing.DisplayLongContextUnitPrices(p, tier) {
			b.prices["long."+tier+"."+field] = price
		}
	}
	if billing.shouldApplySessionLongContextPricing(UsageTokens{InputTokens: p.LongContextInputThreshold + 1}, p) {
		b.prices["long.threshold"] = float64(p.LongContextInputThreshold)
	}
	// Image token billing needs all three prices (CalculateImageTokenCost).
	if p.InputPricePerToken > 0 && p.InputImagePricePerToken > 0 && p.OutputImagePricePerToken > 0 {
		b.prices["image.input"] = p.InputImagePricePerToken * 1e6
		b.prices["image.output"] = p.OutputImagePricePerToken * 1e6
	}
	return b
}

func isImagePricingModel(pricing *PricingService, model string) bool {
	dynamic := pricing.GetModelPricing(model)
	return dynamic != nil && (dynamic.Mode == "image_generation" || dynamic.OutputCostPerImage > 0)
}

func diffModelPrices(before, after map[string]float64) []PricingDriftChange {
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	changes := []PricingDriftChange{}
	for k := range keys {
		o, oldOK := before[k]
		n, newOK := after[k]
		if oldOK && newOK && (o == n || k != "long.threshold" && math.Abs(o-n) <= 1e-9*math.Max(math.Abs(o), math.Abs(n))) {
			continue
		}
		c := PricingDriftChange{Key: k}
		if oldOK {
			c.Old = &o
		}
		if newOK {
			c.New = &n
		}
		changes = append(changes, c)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Key < changes[j].Key })
	return changes
}

func pricingDriftFingerprint(newHash, configHash string, n PricingDriftNotice) string {
	body, _ := json.Marshal(struct {
		NewHash, ConfigHash, Model, OldStatus, NewStatus string
		Changes                                          []PricingDriftChange
	}{newHash, configHash, n.Model, n.OldStatus, n.NewStatus, n.Changes})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:8])
}

func shortFeedHash(hash string) string {
	return hash[:min(12, len(hash))]
}
