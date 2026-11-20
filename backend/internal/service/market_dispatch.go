package service

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Market selection and final admission (P3a part 2).
//
// Every candidate of a scheduling decision is classified against the group
// the request actually schedules on (after the claude_code_only fallback /
// smart switch) and the authenticated Key:
//
//   - buyer: the group is a market tier and the account is listed there
//     (market membership + listing snapshot of that tier). Uses the buyer
//     concurrency cap C - R (dual slot sets), class-aware device admission and
//     a positive price; a buyer lease is admitted only by the final
//     admission, which writes the immutable market_admissions row.
//     Authenticated user = owner is still the buyer path (self purchase).
//   - owner: a listed account used through the authenticated user's own
//     exclusive hosted group while they are its owner. Total capacity only
//     (it may use the reservations); owner device class.
//   - platform: an admin member of a market tier without an owner and not
//     listed. Original capacity; the final admission re-checks "no owner,
//     still an admin member, not listed" in the database.
//   - excluded: every other combination that involves a market tier or a
//     listed account (a listed account in any other group, ungrouped, Simple
//     mode, an unresolvable group, a pinned-mode account in a tier, a buyer
//     candidate on a path without final admission, ...).
//   - none: neither the group nor the account is on the market — the code
//     path is exactly the pre-market one (no extra Redis or database work).
//
// "Listed" never trusts a single cached copy: an account counts as listed if
// its cached copy says so or the process-wide registry (polled from
// account_market_listings) does, so an old snapshot written back without the
// market fields cannot make a listed account look ordinary for longer than
// one poll interval.

const (
	MarketClassNone     = ""
	MarketClassPlatform = "platform"
	MarketClassOwner    = "owner"
	MarketClassBuyer    = "buyer"
	MarketClassExcluded = "excluded"

	MarketDeviceClassOwner = "owner"
	MarketDeviceClassBuyer = "buyer"

	marketRegistryPollInterval   = 2 * time.Second
	marketAdmissionTimeout       = 3 * time.Second
	marketAdmissionConfirmWindow = 2 * time.Second
)

var (
	// ErrMarketDeviceUnidentified: the request carries no usable device id;
	// the market buyer path never admits an unidentified device.
	ErrMarketDeviceUnidentified = errors.New("market device unidentified")
	// ErrMarketAdmissionRejected is the cause of a market candidate refused at
	// the dispatch boundary (the lease is released and the request reselects).
	ErrMarketAdmissionRejected = errors.New("market admission rejected")
	// ErrMarketCountTokensUnsupported: a market tier without platform
	// accounts has no count_tokens upstream (404 like platforms without it).
	ErrMarketCountTokensUnsupported = errors.New("count_tokens is not available in this market tier")
)

// AccountMarketListing is the listing snapshot carried on a listed account's
// cached copy (status listed only). It drives selection; the final admission
// re-reads the row.
type AccountMarketListing struct {
	GroupID     int64                  `json:"group_id"`
	OwnerUserID int64                  `json:"owner_user_id"`
	Version     int64                  `json:"version"`
	Protection  MarketProtectionParams `json:"protection"`
}

// MarketSelection is the admission expectation of one selected candidate,
// carried from selection (lease class) through waiting to the dispatch
// boundary, where it must still match the database.
type MarketSelection struct {
	Class     string
	AccountID int64
	GroupID   int64
	// OwnerUserID: the owner the candidate was selected with (buyer/owner).
	OwnerUserID int64
	// ListingVersion / Concurrency / reservations: the resource policy the
	// lease was taken under (buyer). A change makes the final admission fail.
	ListingVersion      int64
	Concurrency         int
	BuyerMaxConcurrency int
	ReservedDevices     int
	ReservedAdmissions  int
	SelfPurchase        bool
	// LeaseID is the buyer lease (request id in both slot sets) currently
	// held for this candidate; the final admission checks it is still held.
	LeaseID string
	// Device policy the device admission ran under (buyer): checked against
	// the account row at the final admission.
	DeviceLimit     int
	CycleBudget     bool
	CycleAdmissions int
	CycleGeneration string
}

// IsBuyer reports a buyer-path selection.
func (m *MarketSelection) IsBuyer() bool { return m != nil && m.Class == MarketClassBuyer }

// MarketDispatchState is the polled registry content.
type MarketDispatchState struct {
	Enabled          bool
	ListedAccountIDs []int64
}

// MarketSlotCache is the dual slot set (total + buyer) of listed accounts.
type MarketSlotCache interface {
	AcquireAccountBuyerSlot(ctx context.Context, accountID int64, totalMax, buyerMax int, requestID string) (bool, error)
	ReleaseAccountBuyerSlot(ctx context.Context, accountID int64, requestID string) error
	// RenewAccountBuyerSlot refreshes the lease iff it is a live member of
	// both sets (false = lost; a half lease is never refreshed).
	RenewAccountBuyerSlot(ctx context.Context, accountID int64, requestID string) (bool, error)
	GetAccountBuyerConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error)
	SlotTTLSeconds() int
}

// MarketDeviceAdmission is the class-aware carpool device admission input.
type MarketDeviceAdmission struct {
	Class              string
	ReservedDevices    int
	ReservedAdmissions int
}

// MarketCarpoolAdmissionCache registers devices of listed accounts by class.
// policy is nil when the account has no cycle budget.
type MarketCarpoolAdmissionCache interface {
	GetOrCreateCarpoolDeviceMarket(ctx context.Context, accountID int64, originalDeviceID string, hints ClientHints, limit int, nowUnix int64, policy *CarpoolAdmissionPolicy, adm MarketDeviceAdmission) (*CarpoolDeviceRecord, error)
}

// MarketAdmissionRequest is one candidate attempt's final admission.
type MarketAdmissionRequest struct {
	Class      string
	AttemptID  string
	RequestRef string
	AccountID  int64
	GroupID    int64
	AuthUserID int64
	APIKeyID   int64
}

// MarketAuthoritativeFacts are read in the admission transaction.
type MarketAuthoritativeFacts struct {
	AccountFound           bool
	Account                *Account
	OwnerPaused            bool
	OwnerWithdrawRequested bool
	// MembershipSource of the (account, group) pair: "" = no membership.
	MembershipSource      string
	GroupFound            bool
	GroupMarketTier       bool
	GroupActive           bool
	GroupPlatform         string
	GroupHosted           bool
	GroupExclusive        bool
	OwnerUserStatus       string
	LatestOwnerEventID    int64
	LatestOwnerEventOwner *int64
	// OwnerGroupQualified (owner class only): the scheduled group is an
	// owner group of the authenticated user, evaluated in the same snapshot
	// (live active Anthropic standard hosted exclusive non-tier group with a
	// valid fee rate; the user is its only allowed user; not mapped to
	// another user in hosted_owner_groups; every live member account is
	// owned by the user). Admin-built groups qualify like P2c-mapped ones.
	OwnerGroupQualified bool
	MarketEnabled       bool
	Listing             *MarketListing
	// GroupRate / GroupModelRates: the group's rate_multiplier and
	// model_rate_multipliers in the same snapshot (buyer path only). The
	// final price positivity check reads these, not the group captured at
	// request start, so a rate an admin changed while the request waited
	// is seen.
	GroupRate       float64
	GroupModelRates map[string]float64
	// BuyerRateOverride: the buyer's user rate override on the group in the
	// same snapshot (nil = none); it replaces GroupRate when present.
	BuyerRateOverride *float64
}

type MarketAdmissionVerdict struct {
	Admitted  bool
	Reason    string
	Admission *MarketAdmission
	Facts     *MarketAuthoritativeFacts
}

// MarketDispatchStore is the database side of market dispatch.
type MarketDispatchStore interface {
	LoadMarketDispatchState(ctx context.Context) (*MarketDispatchState, error)
	// AdmitMarketCandidate locks, reads the facts, asks evaluate ("" = ok)
	// and, for the buyer class, inserts the admission in the same
	// transaction.
	AdmitMarketCandidate(ctx context.Context, req *MarketAdmissionRequest, evaluate func(*MarketAuthoritativeFacts) string) (*MarketAdmissionVerdict, error)
	GetMarketAdmissionByAttempt(ctx context.Context, attemptID string) (*MarketAdmission, error)
	CountMarketTierPlatformMembers(ctx context.Context, groupID int64) (int, error)
}

// MarketRuntime holds the process-wide market dispatch state.
type MarketRuntime struct {
	store MarketDispatchStore

	mu       sync.RWMutex
	enabled  bool
	listed   map[int64]struct{}
	loaded   bool
	lastWarn time.Time
	// pending: listings started in this process and not yet confirmed by a
	// poll that began after they ended (treated as listed meanwhile).
	pending  map[int64]map[uint64]*marketPendingListing
	stopOnce sync.Once
	stop     chan struct{}
}

// NewMarketRuntime creates the runtime; Start begins polling.
func NewMarketRuntime(store MarketDispatchStore) *MarketRuntime {
	return &MarketRuntime{store: store, listed: map[int64]struct{}{}, stop: make(chan struct{}),
		pending: map[int64]map[uint64]*marketPendingListing{}}
}

// marketPendingListing: one entry per listing transaction (token); the
// account counts as listed while any entry remains.
type marketPendingListing struct {
	endedAt time.Time // zero while the transaction runs
}

var marketListingTokens atomic.Uint64

// BeginListing marks an account listed locally once a listing passed its
// checks, right before its writes (MarketListingObserver). The token
// finalizes exactly this listing.
func (r *MarketRuntime) BeginListing(accountID int64) uint64 {
	token := marketListingTokens.Add(1)
	if r == nil {
		return token
	}
	r.mu.Lock()
	if r.pending[accountID] == nil {
		r.pending[accountID] = map[uint64]*marketPendingListing{}
	}
	r.pending[accountID][token] = &marketPendingListing{}
	r.mu.Unlock()
	return token
}

// AbortListing drops this listing's mark at once (rolled back before the
// commit). Other listings of the account are untouched.
func (r *MarketRuntime) AbortListing(accountID int64, token uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if entries := r.pending[accountID]; entries != nil {
		delete(entries, token)
		if len(entries) == 0 {
			delete(r.pending, accountID)
		}
	}
	r.mu.Unlock()
}

// EndListing records the end of this listing's transaction (committed or
// commit result unknown); its mark stays until a registry poll that started
// afterwards. A token that was aborted (or ended) is not touched again.
func (r *MarketRuntime) EndListing(accountID int64, token uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if p := r.pending[accountID][token]; p != nil && p.endedAt.IsZero() {
		p.endedAt = time.Now()
	}
	r.mu.Unlock()
}

// ProvideMarketRuntime loads the registry (retrying briefly) and starts the
// poller.
func ProvideMarketRuntime(store MarketDispatchStore) *MarketRuntime {
	rt := NewMarketRuntime(store)
	for attempt := 0; attempt < 10 && !rt.Refresh(context.Background()); attempt++ {
		time.Sleep(2 * time.Second)
	}
	rt.Start(marketRegistryPollInterval)
	return rt
}

// Refresh reloads the registry once; a failure keeps the last state.
func (r *MarketRuntime) Refresh(ctx context.Context) bool {
	if r == nil || r.store == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	started := time.Now()
	state, err := r.store.LoadMarketDispatchState(ctx)
	if err != nil {
		r.mu.Lock()
		warn := time.Since(r.lastWarn) >= time.Minute
		if warn {
			r.lastWarn = time.Now()
		}
		r.mu.Unlock()
		if warn {
			logger.LegacyPrintf("service.market", "Warning: market registry refresh failed (keeping the last state): %v", err)
		}
		return false
	}
	r.setState(state, started)
	return true
}

// SetState replaces the registry content (tests).
func (r *MarketRuntime) SetState(state *MarketDispatchState) {
	r.setState(state, time.Now())
}

func (r *MarketRuntime) setState(state *MarketDispatchState, readStarted time.Time) {
	if r == nil || state == nil {
		return
	}
	listed := make(map[int64]struct{}, len(state.ListedAccountIDs))
	for _, id := range state.ListedAccountIDs {
		listed[id] = struct{}{}
	}
	r.mu.Lock()
	r.enabled = state.Enabled
	r.listed = listed
	r.loaded = true
	for id, entries := range r.pending {
		for token, p := range entries {
			if !p.endedAt.IsZero() && p.endedAt.Before(readStarted) {
				delete(entries, token)
			}
		}
		if len(entries) == 0 {
			delete(r.pending, id)
		}
	}
	r.mu.Unlock()
}

func (r *MarketRuntime) Start(interval time.Duration) {
	if r == nil || r.store == nil || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				r.Refresh(context.Background())
			}
		}
	}()
}

func (r *MarketRuntime) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
}

// Enabled is the polled market switch (new market admissions only).
func (r *MarketRuntime) Enabled() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled
}

// IsListed reports a listed account per the registry.
func (r *MarketRuntime) IsListed(accountID int64) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	_, ok := r.listed[accountID]
	if !ok {
		_, ok = r.pending[accountID]
	}
	r.mu.RUnlock()
	return ok
}

// marketAccountListed: cached copy or registry.
func (r *MarketRuntime) marketAccountListed(account *Account) bool {
	if account == nil {
		return false
	}
	if len(account.MarketGroupIDs) > 0 || account.MarketListing != nil {
		return true
	}
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.listed[account.ID]; ok {
		return true
	}
	if _, ok := r.pending[account.ID]; ok {
		return true
	}
	// Until the registry has loaded once, any account with an owner may be
	// listed: treat it as listed (conservative; only after a failed start).
	return !r.loaded && account.OwnerUserID != nil
}

// ---------------------------------------------------------------------------
// Request context

type marketDispatchKey struct{}
type marketAttemptKey struct{}

// MarketDispatch is the per-request market context set by the Messages
// handler (the only path that runs the final admission). Without it, buyer
// candidates are never selected.
type MarketDispatch struct {
	APIKey *APIKey

	mu        sync.Mutex
	leases    map[int64]string
	priceKey  string
	priceSnap *MarketPriceSnapshot
	priceDone bool
}

// WithMarketDispatch marks a request as market-capable for apiKey (the Key
// as billed, after smart switch).
func WithMarketDispatch(ctx context.Context, apiKey *APIKey) context.Context {
	if ctx == nil || apiKey == nil {
		return ctx
	}
	return context.WithValue(ctx, marketDispatchKey{}, &MarketDispatch{APIKey: apiKey})
}

func marketDispatchFromContext(ctx context.Context) *MarketDispatch {
	if ctx == nil {
		return nil
	}
	d, _ := ctx.Value(marketDispatchKey{}).(*MarketDispatch)
	if d == nil || d.APIKey == nil {
		return nil
	}
	return d
}

// MarketAttempt is one dispatch attempt: the selection expectation in, the
// admission proof out (only a successfully served attempt's proof reaches
// RecordUsage).
type MarketAttempt struct {
	Selection  *MarketSelection
	Proof      *MarketSettlementProof
	MarketPath bool
	Price      *MarketPriceSnapshot
	// deviceAdmitted: the class-aware device admission of this attempt
	// succeeded (required before a buyer admission).
	deviceAdmitted bool
}

// MarketPriceSnapshot is the price the buyer admission was evaluated with.
type MarketPriceSnapshot struct {
	Model          string
	PriceVersion   string
	GroupRate      float64
	ModelRate      float64
	UserDiscount   float64
	InputPerToken  float64
	OutputPerToken float64
}

// WithMarketAttempt attaches a fresh attempt for one Forward call.
func WithMarketAttempt(ctx context.Context, selection *MarketSelection) (context.Context, *MarketAttempt) {
	attempt := &MarketAttempt{Selection: selection}
	return context.WithValue(ctx, marketAttemptKey{}, attempt), attempt
}

func marketAttemptFromContext(ctx context.Context) *MarketAttempt {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(marketAttemptKey{}).(*MarketAttempt)
	return a
}

// PriceSnapshot is the price the served buyer admission was evaluated
// with (nil for every other attempt).
func (a *MarketAttempt) PriceSnapshot() *MarketPriceSnapshot {
	if a == nil || a.Proof == nil || a.Price == nil {
		return nil
	}
	p := *a.Price
	return &p
}

// SettlementFields returns what RecordUsage must carry for this attempt.
func (a *MarketAttempt) SettlementFields() (bool, *MarketSettlementProof) {
	if a == nil {
		return false, nil
	}
	if a.Proof == nil {
		return a.MarketPath, nil
	}
	proof := *a.Proof
	return a.MarketPath, &proof
}

func authUserIDFromContext(ctx context.Context) int64 {
	id, _ := ctx.Value(accountUserIDContextKey{}).(int64)
	return id
}

// ---------------------------------------------------------------------------
// Classification

// SetMarketRuntime enables market dispatch on the gateway.
func (s *GatewayService) SetMarketRuntime(rt *MarketRuntime) {
	if s != nil {
		s.market = rt
	}
}

func (s *GatewayService) marketAccountListed(account *Account) bool {
	return s.market.marketAccountListed(account)
}

// MarketAccountListed reports a listed account (cached copy or registry).
func (s *GatewayService) MarketAccountListed(account *Account) bool {
	return s != nil && s.marketAccountListed(account)
}

func (s *GatewayService) groupForMarket(ctx context.Context, groupID *int64) *Group {
	if groupID == nil || *groupID <= 0 {
		return nil
	}
	return s.groupFromContext(ctx, *groupID)
}

// marketSelectionGroupKey carries the scheduling group id of the current
// selection (set once the claude_code_only fallback is resolved).
type marketSelectionGroupKey struct{}

func withMarketSelectionGroup(ctx context.Context, groupID *int64) context.Context {
	var id int64
	if groupID != nil {
		id = *groupID
	}
	return context.WithValue(ctx, marketSelectionGroupKey{}, id)
}

func marketSelectionGroupFromContext(ctx context.Context) (*int64, bool) {
	id, ok := ctx.Value(marketSelectionGroupKey{}).(int64)
	if !ok {
		return nil, false
	}
	if id <= 0 {
		return nil, true
	}
	return &id, true
}

// classifyMarketCandidate classifies a candidate for the current selection
// context (scheduling group from context). The sellable check (price) is
// included for buyers.
func (s *GatewayService) classifyMarketCandidate(ctx context.Context, account *Account) (string, *MarketSelection) {
	groupID, _ := marketSelectionGroupFromContext(ctx)
	return s.classifyMarketCandidateInGroup(ctx, account, groupID, true)
}

func (s *GatewayService) classifyMarketCandidateInGroup(ctx context.Context, account *Account, groupID *int64, sellable bool) (string, *MarketSelection) {
	if account == nil {
		return MarketClassExcluded, nil
	}
	listed := s.marketAccountListed(account)
	if s.isRunModeSimple() {
		if listed {
			return MarketClassExcluded, nil
		}
		return MarketClassNone, nil
	}
	group := s.groupForMarket(ctx, groupID)
	if group == nil || !group.MarketTier {
		if !listed {
			return MarketClassNone, nil
		}
		if group == nil {
			return MarketClassExcluded, nil
		}
		authUser := authUserIDFromContext(ctx)
		// The owner path exists only where the final admission runs (Messages):
		// count_tokens, Ops retries and other paths never use a listed account.
		if marketDispatchFromContext(ctx) != nil && group.UsesHostedUsage() && group.IsExclusive && group.IsActive() &&
			account.OwnerUserID != nil && authUser > 0 && *account.OwnerUserID == authUser &&
			slices.Contains(account.GroupIDs, group.ID) {
			return MarketClassOwner, &MarketSelection{
				Class:       MarketClassOwner,
				AccountID:   account.ID,
				GroupID:     group.ID,
				OwnerUserID: authUser,
				Concurrency: account.Concurrency,
			}
		}
		return MarketClassExcluded, nil
	}

	// Market tier (anthropic only; pinned mode is not supported in tiers).
	if account.Platform != PlatformAnthropic ||
		(account.IsAnthropicOAuthOrSetupToken() && account.GetClaudeOAuthMode() == ClaudeOAuthModePinned) {
		return MarketClassExcluded, nil
	}
	if slices.Contains(account.MarketGroupIDs, group.ID) {
		sel, reason := s.marketBuyerSelection(ctx, account, group, sellable)
		if reason != "" {
			return MarketClassExcluded, nil
		}
		return MarketClassBuyer, sel
	}
	if slices.Contains(account.GroupIDs, group.ID) && !listed && account.OwnerUserID == nil {
		return MarketClassPlatform, &MarketSelection{
			Class:       MarketClassPlatform,
			AccountID:   account.ID,
			GroupID:     group.ID,
			Concurrency: account.Concurrency,
		}
	}
	return MarketClassExcluded, nil
}

// marketBuyerSelection checks the cached buyer conditions; reason "" = ok.
func (s *GatewayService) marketBuyerSelection(ctx context.Context, account *Account, group *Group, sellable bool) (*MarketSelection, string) {
	dispatch := marketDispatchFromContext(ctx)
	if dispatch == nil {
		return nil, "no_final_admission"
	}
	if !s.market.Enabled() {
		return nil, "market_disabled"
	}
	listing := account.MarketListing
	if listing == nil || listing.GroupID != group.ID || account.OwnerUserID == nil || *account.OwnerUserID != listing.OwnerUserID {
		return nil, "listing_snapshot"
	}
	if MarketAccountIneligibleReason(account) != "" || !account.IsSchedulable() {
		return nil, "ineligible"
	}
	buyerMax := account.Concurrency - listing.Protection.ReservedConcurrency
	if account.Concurrency <= 0 || buyerMax < 1 {
		return nil, "buyer_capacity"
	}
	sel := &MarketSelection{
		Class:               MarketClassBuyer,
		AccountID:           account.ID,
		GroupID:             group.ID,
		OwnerUserID:         listing.OwnerUserID,
		ListingVersion:      listing.Version,
		Concurrency:         account.Concurrency,
		BuyerMaxConcurrency: buyerMax,
		ReservedDevices:     listing.Protection.ReservedDevices,
		ReservedAdmissions:  listing.Protection.ReservedAdmissions,
		SelfPurchase:        dispatch.APIKey.UserID == listing.OwnerUserID,
	}
	sel.DeviceLimit, sel.CycleBudget, sel.CycleAdmissions, sel.CycleGeneration = marketDevicePolicy(account)
	if !sellable {
		return sel, ""
	}
	if _, ok := s.marketPrice(ctx, dispatch, group, marketRequestedModelFromContext(ctx)); !ok {
		return nil, "not_priced"
	}
	return sel, ""
}

// marketDevicePolicy is the carpool device policy of an account copy.
func marketDevicePolicy(a *Account) (limit int, budget bool, admissions int, generation string) {
	limit = a.GetClaudeOAuthCarpoolDeviceLimit()
	if a.IsClaudeOAuthCarpoolAutoExpandEnabled() {
		p := a.CarpoolAdmissionPolicy()
		return limit, true, p.Admissions, p.Generation
	}
	return limit, false, 0, ""
}

// marketFilterCandidates drops excluded candidates. Accounts with class none
// pass untouched; when no candidate is market-related the input slice is
// returned as is.
func (s *GatewayService) marketFilterCandidates(ctx context.Context, accounts []Account, groupID *int64) []Account {
	if len(accounts) == 0 {
		return accounts
	}
	group := s.groupForMarket(ctx, groupID)
	tier := group != nil && group.MarketTier
	anyListed := false
	if !tier {
		for i := range accounts {
			if s.marketAccountListed(&accounts[i]) {
				anyListed = true
				break
			}
		}
		if !anyListed {
			return accounts
		}
	}
	countTokens := tier && marketCountTokensSelection(ctx)
	out := make([]Account, 0, len(accounts))
	for i := range accounts {
		class, _ := s.classifyMarketCandidateInGroup(ctx, &accounts[i], groupID, true)
		if class == MarketClassExcluded {
			continue
		}
		if countTokens && !MarketCountTokensCapable(&accounts[i]) {
			// Filtered before ranking (never an attempt on a 404 account).
			continue
		}
		out = append(out, accounts[i])
	}
	return out
}

// isAccountInGroupForSelection: admin membership of the group, or a buyer
// candidate of the tier, and never an excluded market candidate.
func (s *GatewayService) isAccountInGroupForSelection(ctx context.Context, account *Account, groupID *int64) bool {
	if account == nil {
		return false
	}
	class, _ := s.classifyMarketCandidate(ctx, account)
	switch class {
	case MarketClassExcluded:
		return false
	case MarketClassBuyer:
		return true
	}
	return s.isAccountInGroup(account, groupID)
}

// marketSelectionChanged: the fresh copy of a candidate is excluded now, or
// would be classified (or, for a buyer, resource-versioned) differently from
// the copy it was selected with (list -> use window).
func (s *GatewayService) marketSelectionChanged(ctx context.Context, selected, fresh *Account) bool {
	if selected == nil || fresh == nil {
		return false
	}
	groupID := mustMarketGroup(ctx)
	b, selB := s.classifyMarketCandidateInGroup(ctx, fresh, groupID, true)
	if b == MarketClassExcluded {
		return true
	}
	a, selA := s.classifyMarketCandidateInGroup(ctx, selected, groupID, false)
	if a != b {
		return true
	}
	if a == MarketClassBuyer && (selA.ListingVersion != selB.ListingVersion || selA.Concurrency != selB.Concurrency) {
		return true
	}
	return false
}

func mustMarketGroup(ctx context.Context) *int64 {
	id, _ := marketSelectionGroupFromContext(ctx)
	return id
}

// ---------------------------------------------------------------------------
// Price

type marketRequestedModelKey struct{}

func withMarketRequestedModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, marketRequestedModelKey{}, model)
}

func marketRequestedModelFromContext(ctx context.Context) string {
	m, _ := ctx.Value(marketRequestedModelKey{}).(string)
	return m
}

// marketPrice evaluates the sale price inputs before dispatch: the model as
// a setup-token account sends it, a positive priced model, the effective
// group rate (user override or group rate), the model factor and the user
// discount. Zero or missing anywhere = not sellable. Cached per request.
func (s *GatewayService) marketPrice(ctx context.Context, d *MarketDispatch, group *Group, requestedModel string) (*MarketPriceSnapshot, bool) {
	if d == nil || group == nil {
		return nil, false
	}
	model := claude.NormalizeModelID(strings.TrimSpace(requestedModel))
	key := model + "|" + strconv.FormatInt(group.ID, 10)
	d.mu.Lock()
	if d.priceDone && d.priceKey == key {
		snap := d.priceSnap
		d.mu.Unlock()
		return snap, snap != nil
	}
	d.mu.Unlock()
	snap, ok := s.computeMarketPrice(ctx, d, group, model)
	if !ok {
		snap = nil
	}
	d.mu.Lock()
	d.priceKey, d.priceSnap, d.priceDone = key, snap, true
	d.mu.Unlock()
	return snap, ok
}

func (s *GatewayService) computeMarketPrice(ctx context.Context, d *MarketDispatch, group *Group, model string) (*MarketPriceSnapshot, bool) {
	if model == "" || s.billingService == nil || group == nil {
		return nil, false
	}
	// Price with the group object RecordUsage bills with (the Key's group);
	// a Key without it, or for another group, is not a sale.
	if d.APIKey.Group == nil || d.APIKey.Group.ID != group.ID {
		return nil, false
	}
	group = d.APIKey.Group
	pricing, err := s.billingService.GetModelPricing(model)
	if err != nil || pricing == nil || !(pricing.InputPricePerToken > 0) || !(pricing.OutputPricePerToken > 0) {
		return nil, false
	}
	userID := d.APIKey.UserID
	if d.APIKey.User != nil {
		userID = d.APIKey.User.ID
	}
	rate := s.getUserGroupRateMultiplier(ctx, userID, group.ID, group.RateMultiplier)
	modelRate := group.ModelRateFor(model)
	discount := d.APIKey.User.PaygDiscountRate()
	for _, v := range []float64{rate, modelRate, discount} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return nil, false
		}
	}
	snap := &MarketPriceSnapshot{
		Model: model, GroupRate: rate, ModelRate: modelRate, UserDiscount: discount,
		InputPerToken: pricing.InputPricePerToken, OutputPerToken: pricing.OutputPricePerToken,
	}
	if pricing.Snapshot != nil {
		snap.PriceVersion = pricing.Snapshot.Version
	}
	return snap, true
}

// MarketFinalAdmissionReason is the judgement the final admission runs on
// the authoritative snapshot (inside the admission transaction): "" admits.
// priceModel is the model the buyer is priced with (buyer class only).
func MarketFinalAdmissionReason(ctx context.Context, sel *MarketSelection, req *MarketAdmissionRequest, f *MarketAuthoritativeFacts, priceModel string) string {
	if reason := EvaluateMarketAdmission(sel, req, f); reason != "" {
		return reason
	}
	if sel.Class == MarketClassBuyer && !marketSnapshotGroupPriced(f, priceModel) {
		return "not_priced"
	}
	// Trusted-only / user / Key allowlists as stored now (tightened while
	// the request waited).
	if !accountAllowsUser(ctx, f.Account) {
		return "not_allowed"
	}
	return ""
}

// marketSnapshotGroupPriced: the buyer's effective group rate (the user rate
// override if one exists, else the tier's rate) and the model's rate factor
// as stored in the admission snapshot are both positive. Sellability only:
// the request-start price check (computeMarketPrice, the group the charge
// uses) must also pass, and the charge itself follows ordinary billing. A
// user rate override of 0 on a tier is allowed (free self-use, migration
// 136); it is re-read here so a cached positive rate at request start never
// admits a sale.
func marketSnapshotGroupPriced(f *MarketAuthoritativeFacts, model string) bool {
	if f == nil {
		return false
	}
	rate := f.GroupRate
	if f.BuyerRateOverride != nil {
		rate = *f.BuyerRateOverride
	}
	modelRate := (&Group{ModelRateMultipliers: f.GroupModelRates}).ModelRateFor(model)
	for _, v := range []float64{rate, modelRate} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Leases

// tryAcquireAccountSlot takes the lease of the candidate's class: buyers in
// both slot sets under C - R, everyone else in the total set as before.
func (s *GatewayService) tryAcquireAccountSlot(ctx context.Context, account *Account) (*AcquireResult, error) {
	if account == nil {
		return &AcquireResult{}, nil
	}
	class, sel := s.classifyMarketCandidate(ctx, account)
	switch class {
	case MarketClassExcluded:
		return &AcquireResult{}, nil
	case MarketClassBuyer:
		if s.concurrencyService == nil {
			return &AcquireResult{}, nil
		}
		res, err := s.concurrencyService.AcquireAccountBuyerSlot(ctx, account.ID, sel.Concurrency, sel.BuyerMaxConcurrency)
		if err == nil && res != nil && res.Acquired {
			if d := marketDispatchFromContext(ctx); d != nil {
				d.mu.Lock()
				if d.leases == nil {
					d.leases = map[int64]string{}
				}
				d.leases[account.ID] = res.LeaseID
				d.mu.Unlock()
			}
		}
		return res, err
	}
	if s.concurrencyService == nil {
		return &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	return s.concurrencyService.AcquireAccountSlot(ctx, account.ID, account.Concurrency)
}

// adjustMarketBuyerLoads makes a buyer candidate's load reflect the tighter
// of its two limits: free = min(C - total, C - R - buyer).
func (s *GatewayService) adjustMarketBuyerLoads(ctx context.Context, accounts []*Account, loads map[int64]*AccountLoadInfo) {
	if s.market == nil || s.concurrencyService == nil || len(accounts) == 0 || loads == nil {
		return
	}
	groupID, _ := marketSelectionGroupFromContext(ctx)
	group := s.groupForMarket(ctx, groupID)
	if group == nil || !group.MarketTier {
		return
	}
	buyers := make(map[int64]*MarketSelection)
	ids := make([]int64, 0)
	for _, acc := range accounts {
		if class, sel := s.classifyMarketCandidateInGroup(ctx, acc, groupID, false); class == MarketClassBuyer {
			buyers[acc.ID] = sel
			ids = append(ids, acc.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	counts, err := s.concurrencyService.GetAccountBuyerConcurrencyBatch(ctx, ids)
	if err != nil {
		// Unknown buyer load: treat as full so it is only tried last; the
		// atomic acquire still decides.
		counts = nil
	}
	for id, sel := range buyers {
		info := loads[id]
		if info == nil {
			info = &AccountLoadInfo{AccountID: id}
			loads[id] = info
		}
		if counts == nil {
			if info.LoadRate < 100 {
				info.LoadRate = 100
			}
			continue
		}
		buyerRate := (counts[id] + info.WaitingCount) * 100 / sel.BuyerMaxConcurrency
		totalRate := (info.CurrentConcurrency + info.WaitingCount) * 100 / sel.Concurrency
		for _, rate := range []int{buyerRate, totalRate} {
			if rate > info.LoadRate {
				info.LoadRate = rate
			}
		}
	}
}

// attachMarketSelection records the class of a returned selection on the
// result and its wait plan (the handler's waiting and the initial-serial
// release/reacquire keep it). A result whose account turned excluded is
// dropped (its lease released).
func (s *GatewayService) attachMarketSelection(ctx context.Context, result *AccountSelectionResult) (*AccountSelectionResult, error) {
	if result == nil || result.Account == nil {
		return result, nil
	}
	class, sel := s.classifyMarketCandidateInGroup(ctx, result.Account, mustMarketGroup(ctx), false)
	if class == MarketClassExcluded {
		if result.Acquired && result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
		return nil, &marketSelectionChangedError{accountID: result.Account.ID}
	}
	if sel.IsBuyer() && result.Acquired {
		if d := marketDispatchFromContext(ctx); d != nil {
			d.mu.Lock()
			sel.LeaseID = d.leases[result.Account.ID]
			d.mu.Unlock()
		}
	}
	result.Market = sel
	if result.WaitPlan != nil {
		result.WaitPlan.Market = sel
		if sel != nil {
			result.WaitPlan.MaxConcurrency = sel.Concurrency
		}
	}
	return result, nil
}

// marketSelectionChangedError: the selected account's market class changed
// at the end of a selection; the caller retries the group without it.
type marketSelectionChangedError struct{ accountID int64 }

func (e *marketSelectionChangedError) Error() string { return ErrNoAvailableAccounts.Error() }
func (e *marketSelectionChangedError) Unwrap() error { return ErrNoAvailableAccounts }

// selectAccountWithMarketRetry reselects the same group without an account
// whose market class changed under the selection (bounded).
func (s *GatewayService) selectAccountWithMarketRetry(ctx context.Context, groupID *int64, sessionHash, model string, excluded map[int64]struct{}, metadata string) (*AccountSelectionResult, error) {
	local := excluded
	for attempt := 0; ; attempt++ {
		result, err := s.selectAccountWithLoadAwareness(ctx, groupID, sessionHash, model, local, metadata)
		var changed *marketSelectionChangedError
		if !errors.As(err, &changed) || attempt >= 3 {
			return result, err
		}
		if local == nil || len(local) == len(excluded) {
			copied := make(map[int64]struct{}, len(excluded)+1)
			for id := range excluded {
				copied[id] = struct{}{}
			}
			local = copied
		}
		local[changed.accountID] = struct{}{}
	}
}

// NewFallbackWaitPlan builds the wait plan of a selection that was released
// for the initial-serial wait, inheriting its market class.
func NewFallbackWaitPlan(selection *AccountSelectionResult, timeout time.Duration, maxWaiting int) *AccountWaitPlan {
	if selection == nil || selection.Account == nil {
		return nil
	}
	plan := &AccountWaitPlan{
		AccountID:      selection.Account.ID,
		MaxConcurrency: selection.Account.Concurrency,
		Timeout:        timeout,
		MaxWaiting:     maxWaiting,
		Market:         selection.Market,
	}
	if selection.Market != nil {
		plan.MaxConcurrency = selection.Market.Concurrency
	}
	return plan
}

// ---------------------------------------------------------------------------
// Final admission (dispatch boundary)

// marketGate runs at the start of Forward: a listed account or a market
// selection must come with a matching attempt; anything else (a listed
// account reached without a market selection, a selection for another
// account) is refused before any upstream work.
func (s *GatewayService) marketGate(ctx context.Context, account *Account) (*MarketAttempt, error) {
	attempt := marketAttemptFromContext(ctx)
	var sel *MarketSelection
	if attempt != nil {
		sel = attempt.Selection
	}
	if sel == nil {
		if account != nil && s.marketAccountListed(account) {
			return nil, ErrMarketAdmissionRejected
		}
		return nil, nil
	}
	if account == nil || sel.AccountID != account.ID {
		return nil, ErrMarketAdmissionRejected
	}
	if s.isRunModeSimple() {
		return nil, ErrMarketAdmissionRejected
	}
	return attempt, nil
}

// marketDeviceAdmissionFor returns the class-aware device admission of the
// current attempt (nil = the old path).
func marketDeviceAdmissionFor(ctx context.Context, account *Account) *MarketDeviceAdmission {
	attempt := marketAttemptFromContext(ctx)
	if attempt == nil || attempt.Selection == nil || account == nil || attempt.Selection.AccountID != account.ID {
		return nil
	}
	switch attempt.Selection.Class {
	case MarketClassBuyer:
		return &MarketDeviceAdmission{
			Class:              MarketDeviceClassBuyer,
			ReservedDevices:    attempt.Selection.ReservedDevices,
			ReservedAdmissions: attempt.Selection.ReservedAdmissions,
		}
	case MarketClassOwner:
		return &MarketDeviceAdmission{Class: MarketDeviceClassOwner}
	}
	return nil
}

// marketListedWithoutAdmission: right before a first send, an attempt
// without a buyer/owner market admission whose account is now listed (local
// registry or cached copy only; no DB/Redis round trip).
func (s *GatewayService) marketListedWithoutAdmission(ctx context.Context, account *Account) bool {
	return account != nil && marketDeviceAdmissionFor(ctx, account) == nil && s.marketAccountListed(account)
}

// markMarketDeviceAdmitted records a successful class-aware device
// admission on the current attempt.
func markMarketDeviceAdmitted(ctx context.Context, account *Account) {
	if attempt := marketAttemptFromContext(ctx); attempt != nil && attempt.Selection != nil && account != nil && attempt.Selection.AccountID == account.ID {
		attempt.deviceAdmitted = true
	}
}

// finalizeMarketAdmission is the authoritative admission of a market
// candidate, after the device check and before the first upstream send.
// Returns an error (never dispatch) when anything no longer matches.
func (s *GatewayService) finalizeMarketAdmission(ctx context.Context, account *Account, attempt *MarketAttempt, mappedModel string) error {
	if attempt == nil || attempt.Selection == nil {
		return nil
	}
	sel := attempt.Selection
	if s.market == nil || s.market.store == nil {
		return ErrMarketAdmissionRejected
	}
	authUser := authUserIDFromContext(ctx)
	dispatch := marketDispatchFromContext(ctx)
	var priceModel string
	req := &MarketAdmissionRequest{
		Class:      sel.Class,
		AccountID:  account.ID,
		GroupID:    sel.GroupID,
		AuthUserID: authUser,
	}
	if sel.Class == MarketClassBuyer {
		if !attempt.deviceAdmitted {
			logMarketAdmission(ctx, sel, "device_not_admitted", nil)
			return ErrMarketAdmissionRejected
		}
		if !marketKeyBilledTo(dispatch, authUser, sel.GroupID) {
			// The Key as billed must be the tier the candidate was admitted to.
			logMarketAdmission(ctx, sel, "billing_group_mismatch", nil)
			return ErrMarketAdmissionRejected
		}
		requestRef, _ := ctx.Value(ctxkey.ClientRequestID).(string)
		if strings.TrimSpace(requestRef) == "" {
			return ErrMarketAdmissionRejected
		}
		priceModel = claude.NormalizeModelID(strings.TrimSpace(mappedModel))
		group := s.groupForMarket(ctx, &sel.GroupID)
		if group == nil {
			if g, err := s.resolveGroupByID(ctx, sel.GroupID); err == nil {
				group = g
			}
		}
		// Re-evaluated now (not the selection-time cache): a price or rate
		// that changed while waiting is caught here. The group rates are
		// re-checked once more against the admission snapshot below.
		var price *MarketPriceSnapshot
		ok := group != nil
		if ok {
			price, ok = s.computeMarketPrice(ctx, dispatch, group, priceModel)
		}
		if !ok {
			logMarketAdmission(ctx, sel, "not_priced", nil)
			return ErrMarketAdmissionRejected
		}
		attempt.Price = price
		req.AttemptID = uuid.NewString()
		req.RequestRef = requestRef
		req.APIKeyID = dispatch.APIKey.ID
	}
	if sel.Class == MarketClassOwner && !marketKeyBilledTo(dispatch, authUser, sel.GroupID) {
		// The owner's own Key of the scheduled owner group pays the request.
		logMarketAdmission(ctx, sel, "billing_group_mismatch", nil)
		return ErrMarketAdmissionRejected
	}

	admitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), marketAdmissionTimeout)
	verdict, err := s.market.store.AdmitMarketCandidate(admitCtx, req, func(f *MarketAuthoritativeFacts) string {
		return MarketFinalAdmissionReason(ctx, sel, req, f, priceModel)
	})
	cancel()
	if err != nil {
		if sel.Class != MarketClassBuyer {
			logMarketAdmission(ctx, sel, "db_error", err)
			return ErrMarketAdmissionRejected
		}
		// The commit result may be unknown: confirm by attempt id. Only a
		// confirmed row (with the expected identity) may dispatch.
		confirmCtx, confirmCancel := context.WithTimeout(context.WithoutCancel(ctx), marketAdmissionConfirmWindow)
		row, confirmErr := s.market.store.GetMarketAdmissionByAttempt(confirmCtx, req.AttemptID)
		confirmCancel()
		if confirmErr != nil || row == nil || row.AccountID != req.AccountID || row.GroupID != req.GroupID ||
			row.BuyerUserID != req.AuthUserID || row.APIKeyID != req.APIKeyID || row.RequestRef != req.RequestRef {
			logMarketAdmission(ctx, sel, "unknown_commit", err)
			return ErrMarketAdmissionRejected
		}
		verdict = &MarketAdmissionVerdict{Admitted: true, Admission: row}
	}
	if verdict == nil || !verdict.Admitted {
		reason := "rejected"
		if verdict != nil {
			reason = verdict.Reason
		}
		logMarketAdmission(ctx, sel, reason, nil)
		return ErrMarketAdmissionRejected
	}
	if ctx.Err() != nil {
		// The client went away while the admission ran: its lease was
		// released on cancellation, so the first upstream send must not
		// happen (an admission without a sale settles nothing).
		logMarketAdmission(ctx, sel, "request_cancelled", ctx.Err())
		return ctx.Err()
	}
	if sel.Class == MarketClassBuyer {
		a := verdict.Admission
		if a == nil {
			return ErrMarketAdmissionRejected
		}
		// Right before the first send: the buyer lease must still be a live
		// member of both slot sets; the same atomic step refreshes it (a lost
		// renewal, or an expired member taken by someone else = no dispatch;
		// the admission row then settles nothing).
		if held, err := s.concurrencyService.RenewBuyerLease(ctx, account.ID, sel.LeaseID); err != nil || !held {
			logMarketAdmission(ctx, sel, "lease_lost", err)
			return ErrMarketAdmissionRejected
		}
		attempt.MarketPath = true
		attempt.Proof = &MarketSettlementProof{
			AdmissionID: a.ID,
			AttemptID:   a.AttemptID,
			RequestRef:  a.RequestRef,
			AccountID:   a.AccountID,
			GroupID:     a.GroupID,
			BuyerUserID: a.BuyerUserID,
			APIKeyID:    a.APIKeyID,
		}
	}
	return nil
}

// marketKeyBilledTo: the Key that bills this request belongs to the
// authenticated user and to the scheduled group.
func marketKeyBilledTo(dispatch *MarketDispatch, authUser, groupID int64) bool {
	return dispatch != nil && dispatch.APIKey != nil && authUser > 0 && dispatch.APIKey.UserID == authUser &&
		dispatch.APIKey.GroupID != nil && *dispatch.APIKey.GroupID == groupID
}

// precheckMarketOwnerAdmission runs the owner final admission (the same
// authoritative database check) BEFORE the owner device registration, so a
// candidate refused by this precheck never registers an owner-class device,
// reclassifies a device or consumes a cycle admission. A refusal or an
// unknown result (database error) fails over like the final admission; the
// attempt is never downgraded to a buyer or plain dispatch. The pre-send
// final admission still runs afterwards.
func (s *GatewayService) precheckMarketOwnerAdmission(ctx context.Context, account *Account) error {
	attempt := marketAttemptFromContext(ctx)
	if attempt == nil || attempt.Selection == nil || attempt.Selection.Class != MarketClassOwner {
		return ErrMarketAdmissionRejected
	}
	return s.finalizeMarketAdmission(ctx, account, attempt, "")
}

func logMarketAdmission(ctx context.Context, sel *MarketSelection, reason string, err error) {
	fields := []zap.Field{
		zap.String("class", sel.Class),
		zap.Int64("account_id", sel.AccountID),
		zap.Int64("group_id", sel.GroupID),
		zap.String("reason", reason),
	}
	if err != nil {
		fields = append(fields, zap.Error(err))
	}
	logger.FromContext(ctx).Warn("gateway.market_admission_rejected", fields...)
}

// EvaluateMarketAdmission decides on the authoritative facts ("" = admit).
func EvaluateMarketAdmission(sel *MarketSelection, req *MarketAdmissionRequest, f *MarketAuthoritativeFacts) string {
	if sel == nil || req == nil || f == nil {
		return "invalid"
	}
	if !f.AccountFound || f.Account == nil {
		return "account_missing"
	}
	acc := f.Account
	// Same predicate as scheduling (status, switch, expiry, rate limit,
	// overload, temp-unschedulable, token, owner pause/withdraw, proxy).
	if !acc.IsSchedulable() || f.OwnerPaused || f.OwnerWithdrawRequested {
		return "account_inactive"
	}
	if !f.GroupFound || !f.GroupActive || f.GroupPlatform != acc.Platform {
		return "group_inactive"
	}
	listed := f.Listing != nil && f.Listing.Status == MarketListingStatusListed
	switch sel.Class {
	case MarketClassPlatform:
		if !f.GroupMarketTier || f.MembershipSource != AccountGroupSourceAdmin {
			return "not_admin_member"
		}
		if acc.OwnerUserID != nil {
			return "has_owner"
		}
		if listed {
			return "listed"
		}
		return ""
	case MarketClassOwner:
		if f.GroupMarketTier || !f.GroupHosted || !f.GroupExclusive || f.MembershipSource != AccountGroupSourceAdmin {
			return "not_owner_group"
		}
		if acc.OwnerUserID == nil || req.AuthUserID <= 0 || *acc.OwnerUserID != req.AuthUserID {
			return "not_owner"
		}
		if !f.OwnerGroupQualified {
			return "not_owner_group"
		}
		if f.OwnerUserStatus != StatusActive {
			return "owner_inactive"
		}
		return ""
	case MarketClassBuyer:
		if !f.MarketEnabled {
			return "market_disabled"
		}
		if !f.GroupMarketTier || f.MembershipSource != AccountGroupSourceMarket {
			return "not_market_member"
		}
		if !listed || f.Listing.SelectedGroupID == nil || *f.Listing.SelectedGroupID != req.GroupID {
			return "not_listed"
		}
		if f.Listing.Version != sel.ListingVersion {
			return "listing_changed"
		}
		if acc.OwnerUserID == nil || *acc.OwnerUserID != sel.OwnerUserID || f.Listing.OwnerUserID != sel.OwnerUserID {
			return "owner_changed"
		}
		if f.OwnerUserStatus != StatusActive {
			return "owner_inactive"
		}
		if f.LatestOwnerEventID <= 0 || f.LatestOwnerEventOwner == nil || *f.LatestOwnerEventOwner != sel.OwnerUserID {
			return "owner_version"
		}
		if reason := MarketAccountIneligibleReason(acc); reason != "" {
			return "ineligible_" + reason
		}
		if acc.Concurrency != sel.Concurrency || acc.Concurrency-f.Listing.Protection.ReservedConcurrency != sel.BuyerMaxConcurrency {
			return "capacity_changed"
		}
		if f.Listing.Protection.ReservedDevices != sel.ReservedDevices || f.Listing.Protection.ReservedAdmissions != sel.ReservedAdmissions {
			return "reservation_changed"
		}
		limit, budget, admissions, generation := marketDevicePolicy(acc)
		if limit != sel.DeviceLimit || budget != sel.CycleBudget || admissions != sel.CycleAdmissions || generation != sel.CycleGeneration {
			return "device_policy_changed"
		}
		return ""
	}
	return "class"
}

// marketDispatchCancelled is checked right before every send of a market
// attempt (main and passthrough paths): a request cancelled after its
// admission (lease possibly released on cancellation) never sends.
func marketDispatchCancelled(ctx context.Context) error {
	if a := marketAttemptFromContext(ctx); a != nil && a.Selection != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// marketAdmissionError maps a final-admission failure: a cancelled request
// ends (no reselection); anything else fails over.
func marketAdmissionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return marketAdmissionFailoverError(err)
}

// marketAdmissionFailoverError refuses a candidate at the dispatch boundary
// without touching the account's health: the handler releases the lease,
// excludes the account for this request and reselects.
func marketAdmissionFailoverError(cause error) error {
	if cause == nil {
		cause = ErrMarketAdmissionRejected
	}
	return &UpstreamFailoverError{
		StatusCode:   503,
		ResponseBody: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"No available accounts"}}`),
		Cause:        cause,
	}
}

// SelectMarketTierCountTokensAccount selects the count_tokens account of a
// market tier: platform accounts only (buyer candidates need the Messages
// final admission and are never selected here), each re-checked in the
// database (no owner, still an admin member, not listed) before use; no
// sticky session read, write, renewal or clear. ErrMarketCountTokensUnsupported
// when the tier has no platform account at all (404 like platforms without
// count_tokens); otherwise selection errors mean "temporarily unavailable"
// (503).
type marketCountTokensKey struct{}

func marketCountTokensSelection(ctx context.Context) bool {
	v, _ := ctx.Value(marketCountTokensKey{}).(bool)
	return v
}

func (s *GatewayService) SelectMarketTierCountTokensAccount(ctx context.Context, groupID *int64, model string) (*Account, error) {
	ctx = context.WithValue(ctx, marketDispatchKey{}, (*MarketDispatch)(nil))
	ctx = context.WithValue(ctx, marketCountTokensKey{}, true)
	excluded := map[int64]struct{}{}
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		var account *Account
		account, err = s.SelectAccountForModelWithExclusions(ctx, groupID, "", model, excluded)
		if err != nil {
			break
		}
		// The copy actually returned (the scheduler may re-read the account
		// snapshot after ranking) must itself be capable, plus the DB check.
		if MarketCountTokensCapable(account) && s.verifyMarketTierPlatform(ctx, account, groupID) {
			return account, nil
		}
		excluded[account.ID] = struct{}{}
		err = ErrNoAvailableAccounts
	}
	if groupID != nil && s.market != nil && s.market.store != nil && errors.Is(err, ErrNoAvailableAccounts) {
		countCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		n, countErr := s.market.store.CountMarketTierPlatformMembers(countCtx, *groupID)
		cancel()
		if countErr == nil && n == 0 {
			return nil, ErrMarketCountTokensUnsupported
		}
	}
	return nil, err
}

// MarketCountTokensCapable: an account that can serve count_tokens in a tier
// (same condition as the repository's "configured" count): anthropic, not
// Bedrock (404), not pinned (never in tiers).
func MarketCountTokensCapable(a *Account) bool {
	if a == nil || a.Platform != PlatformAnthropic || a.IsBedrock() {
		return false
	}
	return !a.IsAnthropicOAuthOrSetupToken() || a.GetClaudeOAuthMode() != ClaudeOAuthModePinned
}

// verifyMarketTierPlatform is the read-only authoritative platform check.
func (s *GatewayService) verifyMarketTierPlatform(ctx context.Context, account *Account, groupID *int64) bool {
	if account == nil || groupID == nil || s.market == nil || s.market.store == nil {
		return false
	}
	sel := &MarketSelection{Class: MarketClassPlatform, AccountID: account.ID, GroupID: *groupID, Concurrency: account.Concurrency}
	req := &MarketAdmissionRequest{Class: MarketClassPlatform, AccountID: account.ID, GroupID: *groupID, AuthUserID: authUserIDFromContext(ctx)}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), marketAdmissionTimeout)
	defer cancel()
	verdict, err := s.market.store.AdmitMarketCandidate(checkCtx, req, func(f *MarketAuthoritativeFacts) string {
		if reason := EvaluateMarketAdmission(sel, req, f); reason != "" {
			return reason
		}
		// The fresh row: still count_tokens capable and allowed for this user.
		if !MarketCountTokensCapable(f.Account) {
			return "count_tokens_incapable"
		}
		if !accountAllowsUser(ctx, f.Account) {
			return "not_allowed"
		}
		return ""
	})
	if err != nil || verdict == nil || !verdict.Admitted {
		reason := "db_error"
		if verdict != nil {
			reason = verdict.Reason
		}
		logMarketAdmission(ctx, sel, "count_tokens_"+reason, err)
		return false
	}
	return true
}
