package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const SettingKeyModelPriceOverrides = "model_price_overrides"
const SettingKeyModelPricingHistory = "model_pricing_history"

// Override values are USD per million tokens. Nil inherits; zero is explicit.
// These are unit prices, never token-category discounts.
type ModelPriceOverride struct {
	Input              *float64 `json:"input,omitempty"`
	Output             *float64 `json:"output,omitempty"`
	CacheRead          *float64 `json:"cache_read,omitempty"`
	CacheWrite5m       *float64 `json:"cache_write_5m,omitempty"`
	CacheWrite1h       *float64 `json:"cache_write_1h,omitempty"`
	PriorityInput      *float64 `json:"priority_input,omitempty"`
	PriorityOutput     *float64 `json:"priority_output,omitempty"`
	PriorityCacheRead  *float64 `json:"priority_cache_read,omitempty"`
	PriorityCacheWrite *float64 `json:"priority_cache_write,omitempty"`
}

func (o *ModelPriceOverride) UnmarshalJSON(body []byte) error {
	type rawOverride ModelPriceOverride
	var value rawOverride
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*o = ModelPriceOverride(value)
	return nil
}

func (o ModelPriceOverride) Fields() map[string]*float64 {
	return map[string]*float64{"input": o.Input, "output": o.Output, "cache_read": o.CacheRead, "cache_write_5m": o.CacheWrite5m, "cache_write_1h": o.CacheWrite1h, "priority_input": o.PriorityInput, "priority_output": o.PriorityOutput, "priority_cache_read": o.PriorityCacheRead, "priority_cache_write": o.PriorityCacheWrite}
}
func (o ModelPriceOverride) Validate() error {
	for name, v := range o.Fields() {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 || *v > 1e6) {
			return fmt.Errorf("%s must be a finite price between 0 and 1000000 USD per million tokens", name)
		}
	}
	return nil
}
func (o ModelPriceOverride) Empty() bool {
	for _, v := range o.Fields() {
		if v != nil {
			return false
		}
	}
	return true
}
func cloneOverrides(in map[string]ModelPriceOverride) map[string]ModelPriceOverride {
	// Detach pointers supplied by handlers from the runtime immutable snapshot.
	body, _ := json.Marshal(in)
	out := map[string]ModelPriceOverride{}
	_ = json.Unmarshal(body, &out)
	if out == nil {
		out = map[string]ModelPriceOverride{}
	}
	return out
}
func (s *PricingService) updateConfigHashLocked() {
	body, _ := json.Marshal(struct {
		Aliases   map[string]string
		Overrides map[string]ModelPriceOverride
	}{s.modelAliases, s.modelOverrides})
	sum := sha256.Sum256(body)
	s.configHash = hex.EncodeToString(sum[:])
}
func (s *PricingService) SetModelPricingConfig(aliases map[string]string, overrides map[string]ModelPriceOverride) {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	s.setModelPricingConfig(aliases, overrides)
}
func (s *PricingService) setModelPricingConfig(aliases map[string]string, overrides map[string]ModelPriceOverride) {
	copiedAliases := map[string]string{}
	for k, v := range aliases {
		copiedAliases[strings.ToLower(strings.TrimSpace(k))] = strings.ToLower(strings.TrimSpace(v))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelAliases = copiedAliases
	s.modelOverrides = cloneOverrides(overrides)
	s.updateConfigHashLocked()
}
func (s *PricingService) resolveAliasLocked(model string) string {
	for i := 0; i < len(s.modelAliases); i++ {
		next, ok := s.modelAliases[model]
		if !ok || next == "" {
			break
		}
		model = next
	}
	return model
}
func (s *PricingService) sortedModelNamesLocked() []string {
	names := make([]string, 0, len(s.pricingData))
	for name := range s.pricingData {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type pricingResolution struct {
	Dynamic       *LiteLLMModelPricing
	Override      ModelPriceOverride
	BilledModel   string
	Alias         string
	LookupModel   string
	ResolvedModel string
	Source        string
	Version       string
}

func (s *PricingService) ResolvePricing(model string) pricingResolution {
	s.mu.RLock()
	defer s.mu.RUnlock()
	model = strings.ToLower(strings.TrimSpace(model))
	dynamic := s.lookupModelPricingLocked(model)
	resolved := s.resolveAliasLocked(model)
	if dynamic != nil && dynamic.ResolvedModel != "" {
		resolved = dynamic.ResolvedModel
	}
	sum := sha256.Sum256([]byte(s.localHash + ":" + s.configHash))
	return pricingResolution{Dynamic: dynamic, Override: s.modelOverrides[model], BilledModel: model, Alias: s.modelAliases[model], LookupModel: s.resolveAliasLocked(model), ResolvedModel: resolved, Source: s.priceSource, Version: hex.EncodeToString(sum[:])}
}
func (s *PricingService) ModelPricingConfig() (map[string]string, map[string]ModelPriceOverride) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	aliases := map[string]string{}
	for k, v := range s.modelAliases {
		aliases[k] = v
	}
	return aliases, cloneOverrides(s.modelOverrides)
}
func (s *PricingService) PricingModelNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := map[string]bool{}
	for k := range s.pricingData {
		names[k] = true
	}
	for k := range s.modelAliases {
		names[k] = true
	}
	for k := range s.modelOverrides {
		names[k] = true
	}
	out := make([]string, 0, len(names))
	for k := range names {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func (s *PricingService) PricingMetadata() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	url := ""
	if s.cfg != nil {
		url = s.cfg.Pricing.RemoteURL
	}
	return map[string]any{"source": s.priceSource, "source_url": url, "updated_at": s.lastUpdated, "source_hash": s.localHash, "model_count": len(s.pricingData)}
}

// PricingSnapshot is persisted with each token-billed usage row. It includes
// the reference and effective unit prices at calculation time, independent of
// later feed updates, aliases, overrides or provider service-tier policies.
type PricingSnapshot struct {
	LongContext                *LongContextPricing `json:"long_context,omitempty"`
	Version                    string              `json:"version"`
	Alias                      string              `json:"alias,omitempty"`
	Override                   ModelPriceOverride  `json:"override"`
	Mode                       string              `json:"mode,omitempty"`
	BilledModel                string              `json:"billed_model"`
	ResolvedModel              string              `json:"resolved_model"`
	Source                     string              `json:"source"`
	ServiceTier                string              `json:"service_tier,omitempty"`
	Reference                  *ModelPricing       `json:"reference"`
	Effective                  *ModelPricing       `json:"effective"`
	ReferenceTotalCost         float64             `json:"reference_total_cost"`
	LongContextThreshold       int                 `json:"long_context_threshold,omitempty"`
	LongContextExtraMultiplier float64             `json:"long_context_extra_multiplier,omitempty"`
}

// LongContextPricing records the rule selected by the calculator, rather than
// inferring a historical invoice from today's model catalogue.
type LongContextPricing struct {
	Applied              bool    `json:"applied"`
	Mode                 string  `json:"mode"`
	Threshold            int     `json:"threshold"`
	TotalInputTokens     int     `json:"total_input_tokens"`
	InputMultiplier      float64 `json:"input_multiplier"`
	OutputMultiplier     float64 `json:"output_multiplier"`
	CacheReadMultiplier  float64 `json:"cache_read_multiplier"`
	CacheWriteMultiplier float64 `json:"cache_write_multiplier"`
}

func (s *BillingService) finishModelPricing(p *ModelPricing, r pricingResolution) *ModelPricing {
	base := *s.applyModelSpecificPricingPolicy(r.ResolvedModel, p)
	base.Reference = nil
	base.Snapshot = nil
	effective := base
	set := func(target *float64, value *float64) {
		if value != nil {
			*target = *value / 1e6
		}
	}
	set(&effective.InputPricePerToken, r.Override.Input)
	set(&effective.OutputPricePerToken, r.Override.Output)
	set(&effective.CacheReadPricePerToken, r.Override.CacheRead)
	set(&effective.CacheCreationPricePerToken, r.Override.CacheWrite5m)
	set(&effective.CacheCreation5mPrice, r.Override.CacheWrite5m)
	set(&effective.CacheCreation1hPrice, r.Override.CacheWrite1h)
	effective.PriorityOverrides = map[string]bool{}
	for name, value := range r.Override.Fields() {
		if strings.HasPrefix(name, "priority_") && value != nil {
			effective.PriorityOverrides[name] = true
		}
	}
	if len(effective.PriorityOverrides) > 0 && !usePriorityServiceTierPricing("priority", &base) {
		effective.InputPricePerTokenPriority = base.InputPricePerToken * 2
		effective.OutputPricePerTokenPriority = base.OutputPricePerToken * 2
		effective.CacheReadPricePerTokenPriority = base.CacheReadPricePerToken * 2
		effective.CacheCreationPricePerTokenPriority = base.CacheCreationPricePerToken * 2
	}
	set(&effective.InputPricePerTokenPriority, r.Override.PriorityInput)
	set(&effective.OutputPricePerTokenPriority, r.Override.PriorityOutput)
	set(&effective.CacheReadPricePerTokenPriority, r.Override.PriorityCacheRead)
	set(&effective.CacheCreationPricePerTokenPriority, r.Override.PriorityCacheWrite)
	if r.Override.CacheWrite5m != nil || r.Override.CacheWrite1h != nil {
		if !base.SupportsCacheBreakdown {
			effective.CacheCreation5mPrice = base.CacheCreationPricePerToken
			effective.CacheCreation1hPrice = base.CacheCreationPricePerToken
			set(&effective.CacheCreation5mPrice, r.Override.CacheWrite5m)
			set(&effective.CacheCreation1hPrice, r.Override.CacheWrite1h)
		}
		effective.SupportsCacheBreakdown = true
	}
	effective.Reference = &base
	mode := ""
	if r.Dynamic != nil {
		mode = r.Dynamic.Mode
	}
	effective.Snapshot = &PricingSnapshot{Alias: r.Alias, Override: r.Override, Mode: mode, Version: r.Version, BilledModel: r.BilledModel, ResolvedModel: r.ResolvedModel, Source: r.Source, Reference: &base}
	return &effective
}

// PriceEdit stores non-secret administrator changes in the settings store.
type PriceEdit struct {
	At          time.Time          `json:"at"`
	ActorID     int64              `json:"actor_id"`
	Model       string             `json:"model"`
	BeforeAlias string             `json:"before_alias"`
	AfterAlias  string             `json:"after_alias"`
	Before      ModelPriceOverride `json:"before"`
	After       ModelPriceOverride `json:"after"`
}

func (s *PricingService) PreviewPricing(model string, alias *string, override *ModelPriceOverride) (*BillingService, error) {
	s.mu.RLock()
	p := &PricingService{cfg: s.cfg, pricingData: s.pricingData, modelAliases: map[string]string{}, modelOverrides: cloneOverrides(s.modelOverrides), configHash: s.configHash, localHash: s.localHash, priceSource: s.priceSource}
	for k, v := range s.modelAliases {
		p.modelAliases[k] = v
	}
	s.mu.RUnlock()
	model = strings.ToLower(strings.TrimSpace(model))
	if alias != nil {
		target := strings.ToLower(strings.TrimSpace(*alias))
		if target == "" {
			delete(p.modelAliases, model)
		} else {
			p.modelAliases[model] = target
		}
	}
	if err := validatePricingModelAliases(p.modelAliases); err != nil {
		return nil, err
	}
	if override != nil {
		if err := override.Validate(); err != nil {
			return nil, err
		}
		p.modelOverrides[model] = *override
	}
	p.updateConfigHashLocked()
	if r := p.ResolvePricing(model); r.Dynamic != nil && r.Dynamic.Mode == "image_generation" {
		return nil, fmt.Errorf("image generation uses separate image billing")
	}
	billing := NewBillingService(p.cfg, p)
	if _, err := billing.GetModelPricing(model); err != nil {
		return nil, err
	}
	return billing, nil
}

// UI tier prices are derived by the billing calculator, not duplicated rules.
func (s *BillingService) DisplayUnitPrices(p *ModelPricing, tier string) map[string]float64 {
	return s.displayContextUnitPrices(p, tier, false)
}

// DisplayLongContextUnitPrices follows the same tier selection and whole-request
// rule as actual billing. Unsupported tiers have no separate long-context price.
func (s *BillingService) DisplayLongContextUnitPrices(p *ModelPricing, tier string) map[string]float64 {
	if p == nil || !s.shouldApplySessionLongContextPricing(UsageTokens{InputTokens: p.LongContextInputThreshold + 1}, p) || normalizeBillingServiceTier(tier) == "priority" {
		return nil
	}
	return s.displayContextUnitPrices(p, tier, true)
}

func (s *BillingService) displayContextUnitPrices(p *ModelPricing, tier string, long bool) map[string]float64 {
	if p == nil {
		return nil
	}
	copy := *p
	tokens := UsageTokens{InputTokens: 1, OutputTokens: 1, CacheReadTokens: 1, CacheCreationTokens: 2, CacheCreation5mTokens: 1, CacheCreation1hTokens: 1}
	if long {
		tokens.InputTokens = p.LongContextInputThreshold + 1
	} else {
		copy.LongContextInputThreshold = 0
	}
	c := s.calculateTokenCost(tokens, 1, tier, &copy)
	return map[string]float64{"input": c.InputCost / float64(tokens.InputTokens) * 1e6, "output": c.OutputCost * 1e6, "cache_read": c.CacheReadCost * 1e6, "cache_write_5m": c.CacheCreation5mCost * 1e6, "cache_write_1h": c.CacheCreation1hCost * 1e6}
}

var ErrPricingChanged = errors.New("prices changed; reload and review before saving")

// Serialize pricing writers across the database save without blocking billing
// readers. Recheck after candidate validation so a feed refresh cannot make a
// previously reviewed price version silently stale during an edit.
func (s *PricingService) ApplyPricingConfiguration(expected string, aliases map[string]string, overrides map[string]ModelPriceOverride, persist func() error) error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	s.mu.RLock()
	sum := sha256.Sum256([]byte(s.localHash + ":" + s.configHash))
	version := hex.EncodeToString(sum[:])
	s.mu.RUnlock()
	if version != expected {
		return ErrPricingChanged
	}
	if err := persist(); err != nil {
		return err
	}
	s.setModelPricingConfig(aliases, overrides)
	return nil
}

func (s *BillingService) fallbackPricingResolution(r pricingResolution, p *ModelPricing) pricingResolution {
	r.Source = "hardcoded_fallback"
	if s.fallbackPrices[r.LookupModel] == p {
		r.ResolvedModel = r.LookupModel
		return r
	}
	names := s.ListSupportedModels()
	sort.Strings(names)
	for _, name := range names {
		if s.fallbackPrices[name] == p {
			r.ResolvedModel = name
			break
		}
	}
	return r
}

// Recent models are optional; catalogue inspection remains usable if analytics
// is temporarily unavailable.
type ModelPricingUsageReader interface {
	ListRecentPricingModels(context.Context) ([]string, error)
}
