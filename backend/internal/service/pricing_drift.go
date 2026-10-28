package service

import (
	"context"
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
	ID               string               `json:"id"`
	Model            string               `json:"model"`
	OldStatus        string               `json:"old_status"`
	NewStatus        string               `json:"new_status"`
	OldResolvedModel string               `json:"old_resolved_model"`
	NewResolvedModel string               `json:"new_resolved_model"`
	Changes          []PricingDriftChange `json:"changes"`
	OldFeedHash      string               `json:"old_feed_hash"`
	NewFeedHash      string               `json:"new_feed_hash"`
	DetectedAt       time.Time            `json:"detected_at"`
	Occurrences      int                  `json:"occurrences"`
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
	return &PricingDriftService{cfg: cfg, billed: billed, store: store, done: done, now: time.Now, retryDelays: []time.Duration{time.Minute, 5 * time.Minute}}
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
	var added []PricingDriftNotice
	err = s.store.UpdateSettingValue(ctx, SettingKeyPricingDriftNotices, func(current string) (string, error) {
		existing, err := decodePricingDriftNotices(current)
		if err != nil {
			return "", err
		}
		var merged []PricingDriftNotice
		merged, added = mergePricingDriftNotices(existing, found, s.now())
		body, err := json.Marshal(merged)
		return string(body), err
	})
	if err != nil {
		return fmt.Errorf("save notices: %w", err)
	}
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
	var notices []PricingDriftNotice
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
		notices = trimPricingDriftNotices(notices)
		body, err := json.Marshal(notices)
		return string(body), err
	})
	if err != nil {
		return nil, err
	}
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

// mergePricingDriftNotices adds newly found notices. A notice already stored
// is a duplicate when it was detected within the repeat window and is still
// the latest for its model (another Gateway saw the same feed); otherwise the
// change happened again and the notice is reopened.
func mergePricingDriftNotices(existing, found []PricingDriftNotice, now time.Time) (merged, added []PricingDriftNotice) {
	now = now.UTC()
	merged = append([]PricingDriftNotice{}, existing...)
	for _, n := range found {
		index := -1
		for i := range merged {
			if merged[i].ID == n.ID {
				index = i
				break
			}
		}
		if index < 0 {
			n.DetectedAt = now
			n.Occurrences = 1
			merged = append(merged, n)
			added = append(added, n)
			continue
		}
		stored := &merged[index]
		latest := true
		for _, other := range merged {
			if other.Model == stored.Model && other.ID != stored.ID && other.DetectedAt.After(stored.DetectedAt) {
				latest = false
				break
			}
		}
		if latest && now.Sub(stored.DetectedAt) < pricingDriftRepeatWindow {
			continue
		}
		stored.DetectedAt = now
		stored.Occurrences++
		stored.AcknowledgedAt = nil
		stored.AcknowledgedBy = 0
		added = append(added, *stored)
	}
	return trimPricingDriftNotices(merged), added
}

func trimPricingDriftNotices(notices []PricingDriftNotice) []PricingDriftNotice {
	sorted := sortPricingDriftNotices(notices)
	out := make([]PricingDriftNotice, 0, len(sorted))
	open, acknowledged := 0, 0
	for _, n := range sorted {
		if n.AcknowledgedAt == nil {
			if open++; open > pricingDriftMaxOpen {
				logger.LegacyPrintf("service.pricing_drift", "[PricingDrift] WARN dropping unacknowledged notice %s for %s (over %d)", n.ID, n.Model, pricingDriftMaxOpen)
				continue
			}
		} else if acknowledged++; acknowledged > pricingDriftMaxAcknowledged {
			continue
		}
		out = append(out, n)
	}
	return out
}

// diffPricingFeeds compares how each billed model is priced before and after
// a feed change, through the billing calculator itself.
func diffPricingFeeds(cfg *config.Config, change PricingFeedChange, models []string) []PricingDriftNotice {
	if change.Old == nil || change.New == nil {
		return nil
	}
	oldBilling, newBilling := NewBillingService(cfg, change.Old), NewBillingService(cfg, change.New)
	seen := map[string]bool{}
	var out []PricingDriftNotice
	for _, model := range models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		before := describeModelPricing(oldBilling, change.Old, model)
		after := describeModelPricing(newBilling, change.New, model)
		changes := diffModelPrices(before.prices, after.prices)
		if before.status == after.status && len(changes) == 0 {
			continue
		}
		n := PricingDriftNotice{Model: model, OldStatus: before.status, NewStatus: after.status, OldResolvedModel: before.resolved, NewResolvedModel: after.resolved, Changes: changes, OldFeedHash: change.OldHash, NewFeedHash: change.NewHash}
		n.ID = pricingDriftNoticeID(change.NewHash, change.ConfigHash, n)
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
// image tokens, and the feed's default per-image price (group image prices
// take precedence over it and are not compared).
func describeModelPricing(billing *BillingService, pricing *PricingService, model string) modelPricingBehavior {
	b := modelPricingBehavior{status: PricingStatusUnpriced, prices: map[string]float64{}}
	if dynamic := pricing.GetModelPricing(model); dynamic != nil && dynamic.OutputCostPerImage > 0 {
		b.prices["image.per_image"] = dynamic.OutputCostPerImage
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
		if oldOK && newOK && math.Abs(o-n) <= 1e-9*math.Max(math.Abs(o), math.Abs(n)) {
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

func pricingDriftNoticeID(newHash, configHash string, n PricingDriftNotice) string {
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
