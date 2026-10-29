package service

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	openAIAccountScheduleLayerPreviousResponse = "previous_response_id"
	openAIAccountScheduleLayerSessionSticky    = "session_hash"
	openAIAccountScheduleLayerLoadBalance      = "load_balance"
)

type OpenAIAccountScheduleRequest struct {
	GroupID             *int64
	UserID              int64
	UseUserScope        bool
	SessionHash         string
	StickyAccountID     int64
	PreviousResponseID  string
	RequestedModel      string
	RequiredAccountType string
	RequiredTransport   OpenAIUpstreamTransport
	ExcludedIDs         map[int64]struct{}
}

type OpenAIAccountScheduleDecision struct {
	Layer             string
	StickyPreviousHit bool
	StickySessionHit  bool
	// StickyUserFallbackHit marks a session sticky served by the user-scoped
	// binding after the group binding was missing or ineligible.
	StickyUserFallbackHit bool
	CandidateCount        int
	// TopK is a legacy name: it now reports the number of eligible
	// candidates in the last load-balanced priority tier (no top-K cut).
	TopK                int
	LatencyMs           int64
	LoadSkew            float64
	SelectedAccountID   int64
	SelectedAccountType string
}

type OpenAIAccountSchedulerMetricsSnapshot struct {
	SelectTotal             int64
	StickyPreviousHitTotal  int64
	StickySessionHitTotal   int64
	StickyUserFallbackTotal int64
	LoadBalanceSelectTotal  int64
	AccountSwitchTotal      int64
	SchedulerLatencyMsTotal int64
	SchedulerLatencyMsAvg   float64
	StickyHitRatio          float64
	AccountSwitchRate       float64
	LoadSkewAvg             float64
}

type OpenAIAccountScheduler interface {
	Select(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error)
	ReportSwitch()
	SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot
}

type openAIAccountSchedulerMetrics struct {
	selectTotal            atomic.Int64
	stickyPreviousHitTotal atomic.Int64
	stickySessionHitTotal  atomic.Int64
	stickyUserFallbackHit  atomic.Int64
	loadBalanceSelectTotal atomic.Int64
	accountSwitchTotal     atomic.Int64
	latencyMsTotal         atomic.Int64
	loadSkewMilliTotal     atomic.Int64
}

func (m *openAIAccountSchedulerMetrics) recordSelect(decision OpenAIAccountScheduleDecision) {
	if m == nil {
		return
	}
	m.selectTotal.Add(1)
	m.latencyMsTotal.Add(decision.LatencyMs)
	m.loadSkewMilliTotal.Add(int64(math.Round(decision.LoadSkew * 1000)))
	if decision.StickyPreviousHit {
		m.stickyPreviousHitTotal.Add(1)
	}
	if decision.StickySessionHit {
		m.stickySessionHitTotal.Add(1)
	}
	if decision.StickyUserFallbackHit {
		m.stickyUserFallbackHit.Add(1)
	}
	if decision.Layer == openAIAccountScheduleLayerLoadBalance {
		m.loadBalanceSelectTotal.Add(1)
	}
}

func (m *openAIAccountSchedulerMetrics) recordSwitch() {
	if m == nil {
		return
	}
	m.accountSwitchTotal.Add(1)
}

type defaultOpenAIAccountScheduler struct {
	service *OpenAIGatewayService
	metrics openAIAccountSchedulerMetrics
}

func newDefaultOpenAIAccountScheduler(service *OpenAIGatewayService) OpenAIAccountScheduler {
	return &defaultOpenAIAccountScheduler{service: service}
}

func (s *defaultOpenAIAccountScheduler) Select(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (_ *AccountSelectionResult, decision OpenAIAccountScheduleDecision, _ error) {
	start := time.Now()
	defer func() {
		decision.LatencyMs = time.Since(start).Milliseconds()
		s.metrics.recordSelect(decision)
	}()

	previousResponseID := strings.TrimSpace(req.PreviousResponseID)
	if previousResponseID != "" {
		var selection *AccountSelectionResult
		var err error
		if req.UseUserScope {
			selection, err = s.service.SelectAccountByPreviousResponseIDForUser(
				ctx, req.GroupID, req.UserID, previousResponseID, req.RequestedModel, req.ExcludedIDs,
			)
		} else {
			selection, err = s.service.SelectAccountByPreviousResponseID(
				ctx, req.GroupID, previousResponseID, req.RequestedModel, req.ExcludedIDs,
			)
		}
		if err != nil {
			return nil, decision, err
		}
		if selection != nil && selection.Account != nil {
			if !s.isAccountRequestCompatible(selection.Account, req) {
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				selection = nil
			}
		}
		if selection != nil && selection.Account != nil {
			decision.Layer = openAIAccountScheduleLayerPreviousResponse
			decision.StickyPreviousHit = true
			decision.SelectedAccountID = selection.Account.ID
			decision.SelectedAccountType = selection.Account.Type
			if req.SessionHash != "" {
				_ = s.bindStickySession(ctx, req, selection.Account.ID)
			}
			return selection, decision, nil
		}
	}

	selection, userFallback := s.selectBySessionHash(ctx, req)
	if selection != nil && selection.Account != nil {
		decision.Layer = openAIAccountScheduleLayerSessionSticky
		decision.StickySessionHit = true
		decision.StickyUserFallbackHit = userFallback
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
		return selection, decision, nil
	}

	selection, candidateCount, topK, loadSkew, err := s.selectByLoadBalance(ctx, req)
	decision.Layer = openAIAccountScheduleLayerLoadBalance
	decision.CandidateCount = candidateCount
	decision.TopK = topK
	decision.LoadSkew = loadSkew
	if err != nil {
		return nil, decision, err
	}
	if selection != nil && selection.Account != nil {
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
	}
	return selection, decision, nil
}

// selectBySessionHash tries the group binding first. When it is missing or no
// longer eligible, a Responses request (user scope) tries the account the
// user's session last used in any group: a Key moved to another group keeps its
// upstream account and prompt cache when that account is also in the new
// group. The second result reports a user-scoped hit.
func (s *defaultOpenAIAccountScheduler) selectBySessionHash(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, bool) {
	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" || s == nil || s.service == nil || s.service.cache == nil {
		return nil, false
	}

	accountID := req.StickyAccountID
	if accountID <= 0 {
		if id, err := s.service.getStickySessionAccountID(ctx, req.GroupID, sessionHash); err == nil && id > 0 {
			accountID = id
		}
	}
	if accountID > 0 {
		if selection := s.trySessionStickyAccount(ctx, req, sessionHash, accountID, false); selection != nil {
			return selection, false
		}
	}
	if !req.UseUserScope {
		return nil, false
	}
	userAccountID := s.service.getUserStickySessionAccountID(ctx, req.UserID, sessionHash)
	if userAccountID <= 0 || userAccountID == accountID {
		return nil, false
	}
	selection := s.trySessionStickyAccount(ctx, req, sessionHash, userAccountID, true)
	if selection != nil {
		logger.FromContext(ctx).Info("openai.sticky_user_fallback_hit",
			zap.Int64("group_id", derefGroupID(req.GroupID)),
			zap.Int64("account_id", userAccountID),
			zap.Int64("group_sticky_account_id", accountID),
			zap.Bool("acquired", selection.Acquired))
	}
	return selection, selection != nil
}

// trySessionStickyAccount applies the sticky eligibility checks to accountID.
// An ineligible group binding is deleted as before; the user-scoped binding is
// only skipped, since it may still be valid in the user's other group. Like a
// group sticky, a user-scoped hit is historical affinity for this session hash
// and skips the soft new-session 5h admission of load balancing; rate limits,
// status and every other check above still apply.
func (s *defaultOpenAIAccountScheduler) trySessionStickyAccount(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	sessionHash string,
	accountID int64,
	userScoped bool,
) *AccountSelectionResult {
	drop := func() {
		if !userScoped {
			_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		}
	}
	if req.ExcludedIDs != nil {
		if _, excluded := req.ExcludedIDs[accountID]; excluded {
			return nil
		}
	}

	account, err := s.service.getSchedulableAccount(ctx, accountID)
	if err != nil || account == nil {
		drop()
		return nil
	}
	if !s.service.isOpenAIAccountCurrentlyInGroup(ctx, accountID, req.GroupID) {
		drop()
		return nil
	}
	if req.RequiredAccountType != "" && account.Type != req.RequiredAccountType {
		// A sticky from a different protocol is not an eligible reservation.
		// Leave the other protocol's binding intact and continue selection.
		return nil
	}
	if shouldClearStickySession(account, req.RequestedModel) || !account.IsOpenAI() || !account.IsSchedulable() {
		drop()
		return nil
	}
	if !s.isAccountRequestCompatible(account, req) {
		drop()
		return nil
	}

	result, acquireErr := s.service.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
	if acquireErr == nil && result.Acquired {
		if userScoped {
			_ = s.bindStickySession(ctx, req, accountID)
		} else {
			_ = s.service.refreshStickySessionTTL(ctx, req.GroupID, sessionHash, s.service.openAIWSSessionStickyTTL())
			if req.UseUserScope {
				_ = s.service.setUserStickySessionAccountID(ctx, req.UserID, sessionHash, accountID, s.service.openAIWSSessionStickyTTL())
			}
		}
		return &AccountSelectionResult{
			Account:     account,
			Acquired:    true,
			ReleaseFunc: result.ReleaseFunc,
		}
	}

	cfg := s.service.schedulingConfig()
	// WaitPlan.MaxConcurrency 使用 Concurrency（非 EffectiveLoadFactor），因为 WaitPlan 控制的是 Redis 实际并发槽位等待。
	if s.service.concurrencyService != nil {
		return &AccountSelectionResult{
			Account: account,
			WaitPlan: &AccountWaitPlan{
				AccountID:      accountID,
				MaxConcurrency: account.Concurrency,
				Timeout:        cfg.StickySessionWaitTimeout,
				MaxWaiting:     cfg.StickySessionMaxWaiting,
			},
		}
	}
	return nil
}

// bindStickySession records the selected account for the group session and,
// for Responses requests, as the user's session preference.
func (s *defaultOpenAIAccountScheduler) bindStickySession(ctx context.Context, req OpenAIAccountScheduleRequest, accountID int64) error {
	if req.UseUserScope {
		return s.service.BindStickySessionForUser(ctx, req.GroupID, req.UserID, req.SessionHash, accountID)
	}
	return s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, accountID)
}

func (s *defaultOpenAIAccountScheduler) selectByLoadBalance(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, int, int, float64, error) {
	accounts, err := s.service.listSchedulableAccounts(ctx, req.GroupID)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if len(accounts) == 0 {
		return nil, 0, 0, 0, errNoAvailableOpenAIAccounts
	}

	filtered := make([]*Account, 0, len(accounts))
	loadReq := make([]AccountWithConcurrency, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[account.ID]; excluded {
				continue
			}
		}
		if !account.IsSchedulable() || !account.IsOpenAI() {
			continue
		}
		if !s.isAccountRequestCompatible(account, req) {
			continue
		}
		filtered = append(filtered, account)
		loadReq = append(loadReq, AccountWithConcurrency{
			ID:             account.ID,
			MaxConcurrency: account.EffectiveLoadFactor(),
		})
	}
	if len(filtered) == 0 {
		return nil, 0, 0, 0, errNoAvailableOpenAIAccounts
	}

	loadMap := s.loadOpenAIAccountLoadMap(ctx, loadReq)
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Priority != filtered[j].Priority {
			return filtered[i].Priority < filtered[j].Priority
		}
		return filtered[i].ID < filtered[j].ID
	})

	candidateCount := len(filtered)
	policy := s.service.newSessionQuotaGuardPolicy()
	allQuotaGuarded := true
	var earliestRetryAfter time.Duration
	for _, account := range filtered {
		fresh := s.service.resolveFreshSchedulableOpenAIAccount(ctx, account, req.RequestedModel)
		if fresh == nil || !s.service.shouldRejectNewSessionForHighFiveHourUsage(fresh, time.Now()) {
			allQuotaGuarded = false
			break
		}
		if retryAfter := openAINewSessionQuotaGuardRetryAfter(fresh, time.Now(), policy); retryAfter > 0 &&
			(earliestRetryAfter == 0 || retryAfter < earliestRetryAfter) {
			earliestRetryAfter = retryAfter
		}
	}
	lastTopK := 0
	lastLoadSkew := 0.0
	for start := 0; start < len(filtered); {
		priority := filtered[start].Priority
		end := start + 1
		for end < len(filtered) && filtered[end].Priority == priority {
			end++
		}
		selection, topK, loadSkew, hadFresh, err := s.selectByLoadBalancePriorityLayer(ctx, req, filtered[start:end], loadMap, true)
		lastTopK = topK
		lastLoadSkew = loadSkew
		if err != nil {
			return nil, candidateCount, topK, loadSkew, err
		}
		if selection != nil {
			return selection, candidateCount, topK, loadSkew, nil
		}
		if hadFresh {
			break
		}
		start = end
	}

	if allQuotaGuarded {
		return nil, candidateCount, lastTopK, lastLoadSkew, &OpenAINewSessionQuotaGuardError{RetryAfter: earliestRetryAfter}
	}
	return nil, candidateCount, lastTopK, lastLoadSkew, ErrNoAvailableAccounts
}

func (s *defaultOpenAIAccountScheduler) loadOpenAIAccountLoadMap(ctx context.Context, loadReq []AccountWithConcurrency) map[int64]*AccountLoadInfo {
	loadMap := map[int64]*AccountLoadInfo{}
	if s == nil || s.service == nil || s.service.concurrencyService == nil || len(loadReq) == 0 {
		return loadMap
	}
	if batchLoad, loadErr := s.service.concurrencyService.GetAccountsLoadBatch(ctx, loadReq); loadErr == nil {
		loadMap = batchLoad
	}
	return loadMap
}

// selectByLoadBalancePriorityLayer picks a new-session account inside one
// account-priority tier the way the Claude scheduler does: the lowest global
// load rate first, uniformly random among accounts with the same load rate,
// and every candidate of the tier is tried before waiting. Runtime quality
// signals (error rate, TTFT) are intentionally not used: operators manage
// unhealthy accounts. LoadRate already includes waiting requests.
func (s *defaultOpenAIAccountScheduler) selectByLoadBalancePriorityLayer(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	accounts []*Account,
	loadMap map[int64]*AccountLoadInfo,
	allowWaitPlan bool,
) (*AccountSelectionResult, int, float64, bool, error) {
	if len(accounts) == 0 {
		return nil, 0, 0, false, nil
	}

	loadRateSum := 0.0
	loadRateSumSquares := 0.0
	candidates := make([]accountWithLoad, 0, len(accounts))
	now := time.Now()
	for _, snapshotAccount := range accounts {
		account := s.service.resolveFreshSchedulableOpenAIAccount(ctx, snapshotAccount, req.RequestedModel)
		if account == nil || !s.isAccountRequestCompatible(account, req) {
			continue
		}
		if s.service.shouldRejectNewSessionForHighFiveHourUsage(account, now) {
			continue
		}
		loadInfo := loadMap[account.ID]
		if loadInfo == nil {
			loadInfo = &AccountLoadInfo{AccountID: account.ID}
		}
		loadRate := float64(loadInfo.LoadRate)
		loadRateSum += loadRate
		loadRateSumSquares += loadRate * loadRate
		candidates = append(candidates, accountWithLoad{account: account, loadInfo: loadInfo})
	}
	if len(candidates) == 0 {
		return nil, 0, 0, false, nil
	}
	loadSkew := calcLoadSkewByMoments(loadRateSum, loadRateSumSquares, len(candidates))

	// Unlike the Claude layer, accounts at LoadRate >= 100 stay candidates:
	// the rate counts waiting requests and may use LoadFactor, so the real
	// slot (Concurrency) can still be free. The slot acquisition decides.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].account.Priority != candidates[j].account.Priority {
			return candidates[i].account.Priority < candidates[j].account.Priority
		}
		return candidates[i].loadInfo.LoadRate < candidates[j].loadInfo.LoadRate
	})
	shuffleWithinPriorityAndLoadGroups(candidates)

	// hadFresh latches once a candidate passed the pre-acquire checks: a busy
	// tier waits instead of falling through to a lower priority.
	hadFresh := false
	for _, candidate := range candidates {
		fresh := s.service.resolveFreshSchedulableOpenAIAccount(ctx, candidate.account, req.RequestedModel)
		if fresh == nil ||
			s.service.shouldRejectNewSessionForHighFiveHourUsage(fresh, time.Now()) ||
			!s.isAccountRequestCompatible(fresh, req) {
			continue
		}
		hadFresh = true
		result, acquireErr := s.service.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
		if acquireErr != nil {
			return nil, len(candidates), loadSkew, hadFresh, acquireErr
		}
		if result != nil && result.Acquired {
			if req.SessionHash != "" {
				_ = s.bindStickySession(ctx, req, fresh.ID)
			}
			return &AccountSelectionResult{
				Account:     fresh,
				Acquired:    true,
				ReleaseFunc: result.ReleaseFunc,
			}, len(candidates), loadSkew, hadFresh, nil
		}
	}

	if !allowWaitPlan {
		return nil, len(candidates), loadSkew, hadFresh, nil
	}

	cfg := s.service.schedulingConfig()
	// WaitPlan.MaxConcurrency 使用 Concurrency（非 EffectiveLoadFactor），因为 WaitPlan 控制的是 Redis 实际并发槽位等待。
	for _, candidate := range candidates {
		fresh := s.service.resolveFreshSchedulableOpenAIAccount(ctx, candidate.account, req.RequestedModel)
		if fresh == nil ||
			s.service.shouldRejectNewSessionForHighFiveHourUsage(fresh, time.Now()) ||
			!s.isAccountRequestCompatible(fresh, req) {
			continue
		}
		hadFresh = true
		return &AccountSelectionResult{
			Account: fresh,
			WaitPlan: &AccountWaitPlan{
				AccountID:      fresh.ID,
				MaxConcurrency: fresh.Concurrency,
				Timeout:        cfg.FallbackWaitTimeout,
				MaxWaiting:     cfg.FallbackMaxWaiting,
			},
		}, len(candidates), loadSkew, hadFresh, nil
	}

	return nil, len(candidates), loadSkew, hadFresh, nil
}

func (s *defaultOpenAIAccountScheduler) isAccountRequestCompatible(account *Account, req OpenAIAccountScheduleRequest) bool {
	if account == nil || (req.RequiredAccountType != "" && account.Type != req.RequiredAccountType) {
		return false
	}
	return s.isAccountTransportCompatible(account, req.RequiredTransport)
}

func (s *defaultOpenAIAccountScheduler) isAccountTransportCompatible(account *Account, requiredTransport OpenAIUpstreamTransport) bool {
	// HTTP 入站可回退到 HTTP 线路，不需要在账号选择阶段做传输协议强过滤。
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || s.service == nil || account == nil {
		return false
	}
	return s.service.getOpenAIWSProtocolResolver().Resolve(account).Transport == requiredTransport
}

func (s *defaultOpenAIAccountScheduler) ReportSwitch() {
	if s == nil {
		return
	}
	s.metrics.recordSwitch()
}

func (s *defaultOpenAIAccountScheduler) SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	if s == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}

	selectTotal := s.metrics.selectTotal.Load()
	prevHit := s.metrics.stickyPreviousHitTotal.Load()
	sessionHit := s.metrics.stickySessionHitTotal.Load()
	switchTotal := s.metrics.accountSwitchTotal.Load()
	latencyTotal := s.metrics.latencyMsTotal.Load()
	loadSkewTotal := s.metrics.loadSkewMilliTotal.Load()

	snapshot := OpenAIAccountSchedulerMetricsSnapshot{
		SelectTotal:             selectTotal,
		StickyPreviousHitTotal:  prevHit,
		StickySessionHitTotal:   sessionHit,
		StickyUserFallbackTotal: s.metrics.stickyUserFallbackHit.Load(),
		LoadBalanceSelectTotal:  s.metrics.loadBalanceSelectTotal.Load(),
		AccountSwitchTotal:      switchTotal,
		SchedulerLatencyMsTotal: latencyTotal,
	}
	if selectTotal > 0 {
		snapshot.SchedulerLatencyMsAvg = float64(latencyTotal) / float64(selectTotal)
		snapshot.StickyHitRatio = float64(prevHit+sessionHit) / float64(selectTotal)
		snapshot.AccountSwitchRate = float64(switchTotal) / float64(selectTotal)
		snapshot.LoadSkewAvg = float64(loadSkewTotal) / 1000 / float64(selectTotal)
	}
	return snapshot
}

func (s *OpenAIGatewayService) getOpenAIAccountScheduler() OpenAIAccountScheduler {
	if s == nil {
		return nil
	}
	s.openaiSchedulerOnce.Do(func() {
		if s.openaiScheduler == nil {
			s.openaiScheduler = newDefaultOpenAIAccountScheduler(s)
		}
	})
	return s.openaiScheduler
}

func (s *OpenAIGatewayService) SelectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return s.selectAccountWithScheduler(ctx, groupID, 0, false, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport)
}

// SelectChatGPTOAuthAccount filters native Chat and Codex Images account type
// before scheduling acquires a slot or confirms a sticky. Responses continues
// to use the generic selector and may use either OAuth or API-key accounts.
func (s *OpenAIGatewayService) SelectChatGPTOAuthAccount(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	scheduler := s.getOpenAIAccountScheduler()
	if scheduler == nil {
		return nil, OpenAIAccountScheduleDecision{}, ErrNoAvailableAccounts
	}
	selection, decision, err := scheduler.Select(ctx, OpenAIAccountScheduleRequest{
		GroupID: groupID, SessionHash: sessionHash, RequestedModel: requestedModel,
		RequiredAccountType: AccountTypeOAuth, RequiredTransport: OpenAIUpstreamTransportHTTPSSE,
	})
	if err != nil || selection == nil || selection.Account == nil || !selection.Account.IsOpenAIOAuth() {
		// Retain a defensive ownership boundary for custom schedulers and
		// selections returned alongside an error; rejected reservations are ours.
		if selection != nil && selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		if err == nil {
			err = ErrNoAvailableAccounts
		}
		return nil, decision, err
	}
	return selection, decision, nil
}

func (s *OpenAIGatewayService) SelectAccountWithSchedulerForUser(
	ctx context.Context,
	groupID *int64,
	userID int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return s.selectAccountWithScheduler(ctx, groupID, userID, true, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport)
}

func (s *OpenAIGatewayService) selectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	userID int64,
	useUserScope bool,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{}
	scheduler := s.getOpenAIAccountScheduler()
	if scheduler == nil {
		selection, err := s.SelectAccountWithLoadAwareness(ctx, groupID, sessionHash, requestedModel, excludedIDs)
		decision.Layer = openAIAccountScheduleLayerLoadBalance
		return selection, decision, err
	}

	var stickyAccountID int64
	if sessionHash != "" && s.cache != nil {
		if accountID, err := s.getStickySessionAccountID(ctx, groupID, sessionHash); err == nil && accountID > 0 {
			stickyAccountID = accountID
		}
	}

	return scheduler.Select(ctx, OpenAIAccountScheduleRequest{
		GroupID:            groupID,
		UserID:             userID,
		UseUserScope:       useUserScope,
		SessionHash:        sessionHash,
		StickyAccountID:    stickyAccountID,
		PreviousResponseID: previousResponseID,
		RequestedModel:     requestedModel,
		RequiredTransport:  requiredTransport,
		ExcludedIDs:        excludedIDs,
	})
}

func (s *OpenAIGatewayService) RecordOpenAIAccountSwitch() {
	scheduler := s.getOpenAIAccountScheduler()
	if scheduler == nil {
		return
	}
	scheduler.ReportSwitch()
}

func (s *OpenAIGatewayService) SnapshotOpenAIAccountSchedulerMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	scheduler := s.getOpenAIAccountScheduler()
	if scheduler == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}
	return scheduler.SnapshotMetrics()
}

func (s *OpenAIGatewayService) openAIWSSessionStickyTTL() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds > 0 {
		ttl := time.Duration(s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds) * time.Second
		if ttl > openaiStickySessionTTL {
			return openaiStickySessionTTL
		}
		return ttl
	}
	return openaiStickySessionTTL
}

func calcLoadSkewByMoments(sum float64, sumSquares float64, count int) float64 {
	if count <= 1 {
		return 0
	}
	mean := sum / float64(count)
	variance := sumSquares/float64(count) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}
