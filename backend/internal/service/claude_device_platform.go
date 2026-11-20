package service

import (
	"container/list"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// Claude device platform observation (observe-only abuse detection).
//
// For every original Claude Code device_id seen on an Anthropic OAuth /
// setup-token account, the site records which platforms (family + arch,
// family + major) and which SAIAI users used it, and flags suspicious
// combinations for the admin list. It never blocks, alerts or changes a
// request: the request path only normalizes bounded values and does a
// non-blocking enqueue; failures are counted and dropped.

const (
	ClaudeDevicePlatformReasonCrossFamily        = "cross_family"
	ClaudeDevicePlatformReasonVersionAlternation = "version_alternation"
	ClaudeDevicePlatformReasonSequentialUpgrade  = "sequential_upgrade"
	ClaudeDevicePlatformReasonArchOnly           = "arch_only"
	ClaudeDevicePlatformReasonCrossUser          = "cross_user"

	// ClaudeDevicePlatformRetention is how long a device, its entries and
	// its flags are kept after their last write.
	ClaudeDevicePlatformRetention = 30 * 24 * time.Hour
	// ClaudeDevicePlatformCrossFamilyWindow: two families seen within it.
	ClaudeDevicePlatformCrossFamilyWindow = 24 * time.Hour
	// ClaudeDevicePlatformMaxDevices is the per-site cap of tracked devices.
	ClaudeDevicePlatformMaxDevices = 20000
	// ClaudeDevicePlatformMaxNewPerUserDay caps new devices per user per UTC day.
	ClaudeDevicePlatformMaxNewPerUserDay = 50
	// ClaudeDevicePlatformMaxUsers is the number of users kept per device.
	ClaudeDevicePlatformMaxUsers = 20
	// ClaudeDevicePlatformListMaxLimit caps the admin page size.
	ClaudeDevicePlatformListMaxLimit = 100

	claudeDevicePlatformQueueSize      = 2048
	claudeDevicePlatformWorkers        = 2
	claudeDevicePlatformWriteTimeout   = 2 * time.Second
	claudeDevicePlatformDedupeSize     = 10000
	claudeDevicePlatformDedupeInterval = 5 * time.Minute
	claudeDevicePlatformListTimeout    = 10 * time.Second

	// claudeDevicePlatformObservedKey marks a gin request as observed so an
	// account failover (prepareOAuthRequestIdentity runs again) does not
	// record the same request twice.
	claudeDevicePlatformObservedKey = "claude_device_platform_observed"
)

// ClaudeDevicePlatformReasons lists every flag reason.
var ClaudeDevicePlatformReasons = []string{
	ClaudeDevicePlatformReasonCrossFamily,
	ClaudeDevicePlatformReasonVersionAlternation,
	ClaudeDevicePlatformReasonCrossUser,
	ClaudeDevicePlatformReasonArchOnly,
	ClaudeDevicePlatformReasonSequentialUpgrade,
}

// ClaudeDevicePlatformDefaultReasons is the admin list default filter.
var ClaudeDevicePlatformDefaultReasons = []string{
	ClaudeDevicePlatformReasonCrossFamily,
	ClaudeDevicePlatformReasonVersionAlternation,
	ClaudeDevicePlatformReasonCrossUser,
}

// ErrClaudeDevicePlatformInvalidQuery rejects a bad cursor/reason/limit.
var ErrClaudeDevicePlatformInvalidQuery = errors.New("invalid claude device platform query")

// ClaudeDevicePlatformObservation is the small value queued per request. It
// never carries the raw device id, headers, the request body or the gin
// context.
type ClaudeDevicePlatformObservation struct {
	Digest        string
	DisplayPrefix string
	Family        string
	Arch          string
	Major         string
	UserID        int64
	APIKeyID      int64
	ObservedAt    int64 // unix milliseconds (orders same-second switches)
}

// ClaudeDevicePlatformLimits are passed to the store on every write.
type ClaudeDevicePlatformLimits struct {
	Retention          time.Duration
	CrossFamilyWindow  time.Duration
	MaxDevices         int
	MaxNewPerUserDay   int
	MaxUsers           int
	MaxFlaggedDevices  int
	NewDeviceDayWindow time.Duration
}

// DefaultClaudeDevicePlatformLimits returns the production limits.
func DefaultClaudeDevicePlatformLimits() ClaudeDevicePlatformLimits {
	return ClaudeDevicePlatformLimits{
		Retention:          ClaudeDevicePlatformRetention,
		CrossFamilyWindow:  ClaudeDevicePlatformCrossFamilyWindow,
		MaxDevices:         ClaudeDevicePlatformMaxDevices,
		MaxNewPerUserDay:   ClaudeDevicePlatformMaxNewPerUserDay,
		MaxUsers:           ClaudeDevicePlatformMaxUsers,
		MaxFlaggedDevices:  ClaudeDevicePlatformMaxDevices,
		NewDeviceDayWindow: 48 * time.Hour,
	}
}

// ClaudeDevicePlatformWriteStatus is the store result of one observation.
type ClaudeDevicePlatformWriteStatus string

const (
	ClaudeDevicePlatformWriteOK        ClaudeDevicePlatformWriteStatus = "ok"
	ClaudeDevicePlatformWriteGlobalCap ClaudeDevicePlatformWriteStatus = "global_cap"
	ClaudeDevicePlatformWriteUserCap   ClaudeDevicePlatformWriteStatus = "user_cap"
	ClaudeDevicePlatformWriteNoOwner   ClaudeDevicePlatformWriteStatus = "no_owner"
)

// ClaudeDevicePlatformFlaggedQuery is one admin page request.
type ClaudeDevicePlatformFlaggedQuery struct {
	Limit   int
	Cursor  string
	Reasons []string
	Now     time.Time
}

// ClaudeDevicePlatformReasonMatch is one flag of a device.
type ClaudeDevicePlatformReasonMatch struct {
	Reason        string    `json:"reason"`
	LastMatchedAt time.Time `json:"last_matched_at"`
	Evidence      string    `json:"evidence"`
}

// ClaudeDevicePlatformArchEntry is one family + arch seen on a device.
type ClaudeDevicePlatformArchEntry struct {
	Family      string    `json:"family"`
	Arch        string    `json:"arch"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// ClaudeDevicePlatformVersionEntry is one family + major seen on a device.
type ClaudeDevicePlatformVersionEntry struct {
	Family      string    `json:"family"`
	Major       string    `json:"major"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// ClaudeDevicePlatformUser is one SAIAI user seen on a device.
type ClaudeDevicePlatformUser struct {
	UserID     int64     `json:"user_id"`
	Username   string    `json:"username"`
	Email      string    `json:"email"`
	APIKeyID   int64     `json:"api_key_id"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// ClaudeDevicePlatformDevice is one flagged device for the admin list.
type ClaudeDevicePlatformDevice struct {
	DisplayPrefix  string                             `json:"display_prefix"`
	LastMatchedAt  time.Time                          `json:"last_matched_at"`
	Reasons        []ClaudeDevicePlatformReasonMatch  `json:"reasons"`
	Platforms      []ClaudeDevicePlatformArchEntry    `json:"platforms"`
	Versions       []ClaudeDevicePlatformVersionEntry `json:"versions"`
	Users          []ClaudeDevicePlatformUser         `json:"users"`
	UsersTruncated bool                               `json:"users_truncated"`
}

// ClaudeDevicePlatformFlaggedPage is the store result of one admin page.
type ClaudeDevicePlatformFlaggedPage struct {
	Items      []ClaudeDevicePlatformDevice
	NextCursor string
}

// ClaudeDevicePlatformStore persists observations (Redis, per site).
type ClaudeDevicePlatformStore interface {
	RecordClaudeDevicePlatform(ctx context.Context, obs ClaudeDevicePlatformObservation, limits ClaudeDevicePlatformLimits) (ClaudeDevicePlatformWriteStatus, error)
	ListFlaggedClaudeDevicePlatforms(ctx context.Context, query ClaudeDevicePlatformFlaggedQuery) (ClaudeDevicePlatformFlaggedPage, error)
}

// ClaudeDevicePlatformUserName is the read-time identity of a user id.
type ClaudeDevicePlatformUserName struct {
	Username string
	Email    string
}

// ClaudeDevicePlatformUserLookup resolves user ids at read time (batch).
type ClaudeDevicePlatformUserLookup interface {
	LookupClaudeDevicePlatformUsers(ctx context.Context, ids []int64) (map[int64]ClaudeDevicePlatformUserName, error)
}

// ClaudeDevicePlatformStats are process-local counters since start.
type ClaudeDevicePlatformStats struct {
	Enqueued        int64 `json:"enqueued"`
	Written         int64 `json:"written"`
	DedupeSkipped   int64 `json:"dedupe_skipped"`
	QueueFull       int64 `json:"queue_full"`
	InvalidDeviceID int64 `json:"invalid_device_id"`
	ParseOverflow   int64 `json:"parse_overflow"`
	GlobalCap       int64 `json:"global_cap"`
	UserCap         int64 `json:"user_cap"`
	NoOwner         int64 `json:"no_owner"`
	WriteErrors     int64 `json:"write_errors"`
}

type claudeDevicePlatformCounters struct {
	enqueued, written, dedupeSkipped, queueFull, invalidDeviceID atomic.Int64
	parseOverflow, globalCap, userCap, noOwner, writeErrors      atomic.Int64
}

// ClaudeDevicePlatformService queues observations and serves the admin list.
type ClaudeDevicePlatformService struct {
	store  ClaudeDevicePlatformStore
	users  ClaudeDevicePlatformUserLookup
	limits ClaudeDevicePlatformLimits
	now    func() time.Time

	queue    chan ClaudeDevicePlatformObservation
	counters claudeDevicePlatformCounters
	dedupe   *claudeDevicePlatformDedupe

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// NewClaudeDevicePlatformService creates the service (workers not started).
func NewClaudeDevicePlatformService(store ClaudeDevicePlatformStore, users ClaudeDevicePlatformUserLookup) *ClaudeDevicePlatformService {
	return &ClaudeDevicePlatformService{
		store:  store,
		users:  users,
		limits: DefaultClaudeDevicePlatformLimits(),
		now:    time.Now,
		queue:  make(chan ClaudeDevicePlatformObservation, claudeDevicePlatformQueueSize),
		dedupe: newClaudeDevicePlatformDedupe(claudeDevicePlatformDedupeSize),
		stopCh: make(chan struct{}),
	}
}

// ProvideClaudeDevicePlatformService creates and starts the service.
func ProvideClaudeDevicePlatformService(store ClaudeDevicePlatformStore, users ClaudeDevicePlatformUserLookup) *ClaudeDevicePlatformService {
	svc := NewClaudeDevicePlatformService(store, users)
	svc.Start()
	return svc
}

// Start launches the fixed worker pool once.
func (s *ClaudeDevicePlatformService) Start() {
	if s == nil || s.store == nil {
		return
	}
	s.startOnce.Do(func() {
		for i := 0; i < claudeDevicePlatformWorkers; i++ {
			s.wg.Add(1)
			go s.worker()
		}
	})
}

// Stop stops the workers; queued observations not yet written are dropped.
func (s *ClaudeDevicePlatformService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.wg.Wait()
	})
}

// Stats returns the process-local counters.
func (s *ClaudeDevicePlatformService) Stats() ClaudeDevicePlatformStats {
	if s == nil {
		return ClaudeDevicePlatformStats{}
	}
	c := &s.counters
	return ClaudeDevicePlatformStats{
		Enqueued: c.enqueued.Load(), Written: c.written.Load(), DedupeSkipped: c.dedupeSkipped.Load(),
		QueueFull: c.queueFull.Load(), InvalidDeviceID: c.invalidDeviceID.Load(), ParseOverflow: c.parseOverflow.Load(),
		GlobalCap: c.globalCap.Load(), UserCap: c.userCap.Load(), NoOwner: c.noOwner.Load(), WriteErrors: c.writeErrors.Load(),
	}
}

// ObserveRequest normalizes one inbound request and queues it. It never
// blocks and never fails: every problem is a counter. key may be nil.
func (s *ClaudeDevicePlatformService) ObserveRequest(deviceID string, headers http.Header, system any, key *APIKey) {
	if s == nil {
		return
	}
	digest, prefix, ok := claudeDeviceIdentity(deviceID)
	if !ok {
		s.counters.invalidDeviceID.Add(1)
		return
	}
	sample, overflow := normalizeClaudeDevicePlatform(headers, system)
	if overflow {
		s.counters.parseOverflow.Add(1)
	}
	obs := ClaudeDevicePlatformObservation{
		Digest:        digest,
		DisplayPrefix: prefix,
		Family:        sample.Family,
		Arch:          sample.Arch,
		Major:         sample.Major,
		ObservedAt:    s.now().UnixMilli(),
	}
	if key != nil && key.UserID > 0 {
		obs.UserID = key.UserID
		if key.ID > 0 {
			obs.APIKeyID = key.ID
		}
	}
	select {
	case <-s.stopCh:
		return
	default:
	}
	switch s.dedupe.admit(obs, s.now(), func() bool {
		select {
		case s.queue <- obs:
			return true
		default:
			return false
		}
	}) {
	case claudeDevicePlatformAdmitQueued:
		s.counters.enqueued.Add(1)
	case claudeDevicePlatformAdmitSkipped:
		s.counters.dedupeSkipped.Add(1)
	default:
		s.counters.queueFull.Add(1)
	}
}

func (s *ClaudeDevicePlatformService) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case obs := <-s.queue:
			s.write(obs)
		}
	}
}

func (s *ClaudeDevicePlatformService) write(obs ClaudeDevicePlatformObservation) {
	ctx, cancel := context.WithTimeout(context.Background(), claudeDevicePlatformWriteTimeout)
	defer cancel()
	status, err := s.store.RecordClaudeDevicePlatform(ctx, obs, s.limits)
	if err != nil {
		s.dedupe.forget(obs)
		if s.counters.writeErrors.Add(1)%100 == 1 {
			logger.L().Debug("gateway.claude_device_platform_write_failed", zap.Error(err))
		}
		return
	}
	switch status {
	case ClaudeDevicePlatformWriteOK:
		s.counters.written.Add(1)
	case ClaudeDevicePlatformWriteGlobalCap:
		s.counters.globalCap.Add(1)
	case ClaudeDevicePlatformWriteUserCap:
		s.counters.userCap.Add(1)
	case ClaudeDevicePlatformWriteNoOwner:
		s.counters.noOwner.Add(1)
	}
}

// ClaudeDevicePlatformListResult is the admin API payload.
type ClaudeDevicePlatformListResult struct {
	Items      []ClaudeDevicePlatformDevice `json:"items"`
	NextCursor string                       `json:"next_cursor"`
	Reasons    []string                     `json:"reasons"`
	Stats      ClaudeDevicePlatformStats    `json:"stats"`
}

// ParseClaudeDevicePlatformReasons parses a comma-separated reason filter;
// empty = the default filter.
func ParseClaudeDevicePlatformReasons(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return append([]string(nil), ClaudeDevicePlatformDefaultReasons...), nil
	}
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		known := false
		for _, reason := range ClaudeDevicePlatformReasons {
			if reason == part {
				known = true
				break
			}
		}
		if !known || len(out) >= len(ClaudeDevicePlatformReasons) {
			return nil, ErrClaudeDevicePlatformInvalidQuery
		}
		if !seen[part] {
			seen[part] = true
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil, ErrClaudeDevicePlatformInvalidQuery
	}
	return out, nil
}

// ListFlagged returns one page of flagged devices with usernames resolved.
func (s *ClaudeDevicePlatformService) ListFlagged(ctx context.Context, limit int, cursor string, reasons []string) (ClaudeDevicePlatformListResult, error) {
	result := ClaudeDevicePlatformListResult{Items: []ClaudeDevicePlatformDevice{}, Reasons: reasons}
	if s == nil || s.store == nil {
		return result, nil
	}
	result.Stats = s.Stats()
	if limit <= 0 {
		limit = 50
	}
	if limit > ClaudeDevicePlatformListMaxLimit {
		limit = ClaudeDevicePlatformListMaxLimit
	}
	ctx, cancel := context.WithTimeout(ctx, claudeDevicePlatformListTimeout)
	defer cancel()
	page, err := s.store.ListFlaggedClaudeDevicePlatforms(ctx, ClaudeDevicePlatformFlaggedQuery{
		Limit: limit, Cursor: cursor, Reasons: reasons, Now: s.now(),
	})
	if err != nil {
		return result, err
	}
	result.NextCursor = page.NextCursor
	if page.Items != nil {
		result.Items = page.Items
	}
	s.resolveUsers(ctx, result.Items)
	return result, nil
}

// resolveUsers fills usernames; a lookup failure leaves them empty (the
// user ids are still shown).
func (s *ClaudeDevicePlatformService) resolveUsers(ctx context.Context, items []ClaudeDevicePlatformDevice) {
	if s.users == nil {
		return
	}
	seen := map[int64]bool{}
	var ids []int64
	for _, item := range items {
		for _, u := range item.Users {
			if u.UserID > 0 && !seen[u.UserID] {
				seen[u.UserID] = true
				ids = append(ids, u.UserID)
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	names, err := s.users.LookupClaudeDevicePlatformUsers(ctx, ids)
	if err != nil {
		logger.L().Warn("admin.claude_device_platform_user_lookup_failed", zap.Error(err))
		return
	}
	for i := range items {
		for j := range items[i].Users {
			if name, ok := names[items[i].Users[j].UserID]; ok {
				items[i].Users[j].Username = name.Username
				items[i].Users[j].Email = name.Email
			}
		}
	}
}

// claudeDevicePlatformDedupe skips repeats of a device's current state.
// One bounded LRU (shared capacity) holds two kinds of keys: per device the
// last queued platform state (family, arch, major), and per device + user
// the last queue time. An
// observation is skipped only when the device's last platform state is the
// same and both entries are younger than the interval (and only for
// observations without a known major), so a platform
// switch (15 -> 26 -> 15, also across users) is always queued while a
// shared device used alternately by two users at the same platform is
// written at most once per interval per user. Check, enqueue and update run
// under one lock so concurrent producers cannot reorder the state. A failed
// write forgets the device's entries; forgetting only ever causes an extra
// write, never a skipped one, so it is safe in any order.
type claudeDevicePlatformDedupe struct {
	mu    sync.Mutex
	max   int
	order *list.List
	items map[string]*list.Element
}

type claudeDevicePlatformDedupeEntry struct {
	key   string
	state string
	at    time.Time
}

type claudeDevicePlatformAdmit int

const (
	claudeDevicePlatformAdmitQueued claudeDevicePlatformAdmit = iota
	claudeDevicePlatformAdmitSkipped
	claudeDevicePlatformAdmitFull
)

func newClaudeDevicePlatformDedupe(size int) *claudeDevicePlatformDedupe {
	return &claudeDevicePlatformDedupe{max: size, order: list.New(), items: make(map[string]*list.Element, size)}
}

func (d *claudeDevicePlatformDedupe) freshLocked(key string, now time.Time) (*claudeDevicePlatformDedupeEntry, bool) {
	el, ok := d.items[key]
	if !ok {
		return nil, false
	}
	entry, _ := el.Value.(*claudeDevicePlatformDedupeEntry)
	age := now.Sub(entry.at)
	return entry, age >= 0 && age < claudeDevicePlatformDedupeInterval
}

func (d *claudeDevicePlatformDedupe) putLocked(key, state string, now time.Time) {
	if el, ok := d.items[key]; ok {
		entry, _ := el.Value.(*claudeDevicePlatformDedupeEntry)
		entry.state, entry.at = state, now
		d.order.MoveToFront(el)
		return
	}
	d.items[key] = d.order.PushFront(&claudeDevicePlatformDedupeEntry{key: key, state: state, at: now})
	for d.order.Len() > d.max {
		oldest := d.order.Back()
		if oldest == nil {
			break
		}
		entry, _ := oldest.Value.(*claudeDevicePlatformDedupeEntry)
		delete(d.items, entry.key)
		d.order.Remove(oldest)
	}
}

// admit skips a repeat or calls enqueue (non-blocking) and records the
// state only when it was queued. It never waits on the lock: under
// contention the observation is simply enqueued without dedupe. An
// observation with a known major version is never skipped (another replica
// may have recorded a different major meanwhile), only recorded.
func (d *claudeDevicePlatformDedupe) admit(obs ClaudeDevicePlatformObservation, now time.Time, enqueue func() bool) claudeDevicePlatformAdmit {
	if !d.mu.TryLock() {
		if enqueue() {
			return claudeDevicePlatformAdmitQueued
		}
		return claudeDevicePlatformAdmitFull
	}
	defer d.mu.Unlock()
	deviceKey := "d|" + obs.Digest
	userKey := "u|" + obs.Digest + "|" + strconv.FormatInt(obs.UserID, 10)
	state := obs.Family + "|" + obs.Arch + "|" + obs.Major
	if obs.Major == "" {
		device, deviceFresh := d.freshLocked(deviceKey, now)
		_, userFresh := d.freshLocked(userKey, now)
		if deviceFresh && userFresh && device.state == state {
			return claudeDevicePlatformAdmitSkipped
		}
	}
	if !enqueue() {
		return claudeDevicePlatformAdmitFull
	}
	d.putLocked(deviceKey, state, now)
	d.putLocked(userKey, "", now)
	return claudeDevicePlatformAdmitQueued
}

// forget drops the device's and the device + user entries. It never waits:
// under contention the entries stay (at most one interval of dedupe).
func (d *claudeDevicePlatformDedupe) forget(obs ClaudeDevicePlatformObservation) {
	if !d.mu.TryLock() {
		return
	}
	defer d.mu.Unlock()
	for _, key := range []string{"d|" + obs.Digest, "u|" + obs.Digest + "|" + strconv.FormatInt(obs.UserID, 10)} {
		if el, ok := d.items[key]; ok {
			delete(d.items, key)
			d.order.Remove(el)
		}
	}
}
