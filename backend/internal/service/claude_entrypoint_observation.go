package service

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	claudeEntrypointObservationFlushInterval = 30 * time.Second
	claudeEntrypointObservationMaxKeys       = 5000
	// One group cannot take more than this many buffered keys per flush
	// window, so a noisy group cannot starve the others.
	claudeEntrypointObservationMaxKeysPerGroup = 1000
	claudeEntrypointObservationRetentionDays   = 90
	claudeEntrypointObservationRetentionTick   = 24 * time.Hour
	claudeEntrypointObservationWriteTimeout    = 10 * time.Second
	claudeEntrypointObservationMaxUserAgent    = 256
	claudeEntrypointObservationMaxVersion      = 32
	claudeEntrypointObservationListLimit       = 500
	// WARN limiter: at most once per group+entrypoint pair and at most
	// claudeEntrypointViolationLogMaxPerWindow lines per one-minute window;
	// the pair map holds at most claudeEntrypointViolationLogMaxKeys pairs
	// per window and is only reset when the window rolls over.
	claudeEntrypointViolationLogInterval     = time.Minute
	claudeEntrypointViolationLogMaxPerWindow = 60
	claudeEntrypointViolationLogMaxKeys      = 1000
	// ClaudeEntrypointObservationMaxDays bounds the admin read window.
	ClaudeEntrypointObservationMaxDays = 90
)

// ClaudeEntrypointObservationKey is the primary key of
// group_claude_entrypoint_observations. Day is the UTC date ("2006-01-02").
type ClaudeEntrypointObservationKey struct {
	GroupID        int64
	Day            string
	UAEntrypoint   string
	BodyEntrypoint string
	BodyStatus     string
	CLIVersion     string
	Policy         string
	Result         string
	Reason         string
}

// ClaudeEntrypointObservation is one aggregated row to upsert.
type ClaudeEntrypointObservation struct {
	ClaudeEntrypointObservationKey
	Requests        int64
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	SampleUserAgent string
}

// ClaudeEntrypointObservationSummary is one row of the admin observation list
// (aggregated over the requested day range).
type ClaudeEntrypointObservationSummary struct {
	UAEntrypoint    string    `json:"ua_entrypoint"`
	BodyEntrypoint  string    `json:"body_entrypoint"`
	BodyStatus      string    `json:"body_status"`
	CLIVersion      string    `json:"cli_version"`
	Result          string    `json:"result"`
	Reason          string    `json:"reason"`
	Requests        int64     `json:"requests"`
	FirstSeenAt     time.Time `json:"first_seen_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
	SampleUserAgent string    `json:"sample_user_agent"`
}

// ClaudeEntrypointObservationRepository persists the daily aggregates.
type ClaudeEntrypointObservationRepository interface {
	// UpsertClaudeEntrypointObservations adds requests to existing rows and
	// extends their seen range; rows have unique keys.
	UpsertClaudeEntrypointObservations(ctx context.Context, rows []ClaudeEntrypointObservation) error
	// DeleteClaudeEntrypointObservationsBefore deletes rows with day < beforeDay.
	DeleteClaudeEntrypointObservationsBefore(ctx context.Context, beforeDay string) (int64, error)
	// ListClaudeEntrypointObservations aggregates rows with day >= sinceDay.
	ListClaudeEntrypointObservations(ctx context.Context, groupID int64, sinceDay string, limit int) ([]ClaudeEntrypointObservationSummary, error)
}

// ClaudeEntrypointObservationService buffers per-request observations in
// memory and flushes them every 30 s. It never blocks the request path: the
// buffer is bounded and a failed batch is logged and dropped.
type ClaudeEntrypointObservationService struct {
	repo ClaudeEntrypointObservationRepository
	now  func() time.Time

	mu         sync.Mutex
	pending    map[ClaudeEntrypointObservationKey]*ClaudeEntrypointObservation
	groupKeys  map[int64]int
	dropped    int64
	lastStatus ClaudeEntrypointCollectorStatus

	logMu         sync.Mutex
	logWindow     time.Time
	logLogged     map[string]struct{}
	logCount      int
	logSuppressed int64

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// ClaudeEntrypointCollectorStatus describes the last flush, for the admin
// observation list.
type ClaudeEntrypointCollectorStatus struct {
	// DroppedLastFlush is the number of requests not recorded in the last
	// flush window because the buffer (global or per-group) was full.
	DroppedLastFlush int64 `json:"dropped_last_flush"`
	// LastErrorAt is when a flush last failed to write (its batch was
	// dropped); nil if no flush failed since start.
	LastErrorAt *time.Time `json:"last_error_at"`
}

// NewClaudeEntrypointObservationService creates the recorder (not started).
func NewClaudeEntrypointObservationService(repo ClaudeEntrypointObservationRepository) *ClaudeEntrypointObservationService {
	return &ClaudeEntrypointObservationService{
		repo:      repo,
		now:       time.Now,
		pending:   make(map[ClaudeEntrypointObservationKey]*ClaudeEntrypointObservation),
		groupKeys: make(map[int64]int),
		logLogged: make(map[string]struct{}),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// ClaudeEntrypointObservationInput is what the gateway records per
// (request, group).
type ClaudeEntrypointObservationInput struct {
	GroupID   int64
	Facts     claudebilling.EntrypointFacts
	Policy    string
	Result    string
	Reason    string
	UserAgent string
}

// Record adds one request to the in-memory aggregate. Safe on a nil receiver.
func (s *ClaudeEntrypointObservationService) Record(in ClaudeEntrypointObservationInput) {
	if s == nil {
		return
	}
	key := ClaudeEntrypointObservationKey{
		GroupID:        in.GroupID,
		UAEntrypoint:   in.Facts.RecordedUAEntrypoint(),
		BodyEntrypoint: in.Facts.RecordedBodyEntrypoint(),
		BodyStatus:     in.Facts.BodyStatus,
		CLIVersion:     truncateBytes(in.Facts.UAVersion, claudeEntrypointObservationMaxVersion),
		Policy:         in.Policy,
		Result:         in.Result,
		Reason:         in.Reason,
	}
	sample := sanitizeObservationUserAgent(in.UserAgent)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	key.Day = now.Format("2006-01-02")
	if row, ok := s.pending[key]; ok {
		row.Requests++
		// The clock may step backwards: keep first = min, last = max and
		// only let a request at or after the current last replace the sample.
		if now.Before(row.FirstSeenAt) {
			row.FirstSeenAt = now
		}
		if !now.Before(row.LastSeenAt) {
			row.LastSeenAt = now
			row.SampleUserAgent = sample
		}
		return
	}
	if len(s.pending) >= claudeEntrypointObservationMaxKeys || s.groupKeys[in.GroupID] >= claudeEntrypointObservationMaxKeysPerGroup {
		s.dropped++
		return
	}
	s.groupKeys[in.GroupID]++
	s.pending[key] = &ClaudeEntrypointObservation{
		ClaudeEntrypointObservationKey: key,
		Requests:                       1,
		FirstSeenAt:                    now,
		LastSeenAt:                     now,
		SampleUserAgent:                sample,
	}
}

// ShouldLogViolation rate-limits the gateway WARN log: once per group +
// entrypoint pair per one-minute window, at most 60 lines per window, and at
// most 1000 tracked pairs per window (further pairs are suppressed, not
// reset). Suppressed lines are counted and summarized when the window rolls
// over. A nil receiver always logs.
func (s *ClaudeEntrypointObservationService) ShouldLogViolation(groupID int64, uaEntrypoint, bodyEntrypoint string) bool {
	if s == nil {
		return true
	}
	key := strings.Join([]string{strconv.FormatInt(groupID, 10), uaEntrypoint, bodyEntrypoint}, "|")
	s.logMu.Lock()
	defer s.logMu.Unlock()
	// Read the clock under the lock; a time before the current window (clock
	// step back) stays in the current window instead of resetting it.
	now := s.now()
	if s.logWindow.IsZero() || now.Sub(s.logWindow) >= claudeEntrypointViolationLogInterval {
		if s.logSuppressed > 0 {
			logger.L().Warn("gateway.claude_entrypoint_policy_logs_suppressed",
				zap.Int64("suppressed", s.logSuppressed))
		}
		s.logWindow = now
		s.logLogged = make(map[string]struct{})
		s.logCount = 0
		s.logSuppressed = 0
	}
	if _, seen := s.logLogged[key]; seen {
		s.logSuppressed++
		return false
	}
	if len(s.logLogged) >= claudeEntrypointViolationLogMaxKeys || s.logCount >= claudeEntrypointViolationLogMaxPerWindow {
		s.logSuppressed++
		return false
	}
	s.logLogged[key] = struct{}{}
	s.logCount++
	return true
}

// CollectorStatus returns the last flush's drop count and last write error time.
func (s *ClaudeEntrypointObservationService) CollectorStatus() ClaudeEntrypointCollectorStatus {
	if s == nil {
		return ClaudeEntrypointCollectorStatus{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.lastStatus
	if status.LastErrorAt != nil {
		at := *status.LastErrorAt
		status.LastErrorAt = &at
	}
	return status
}

// Start launches the flush/retention loop once.
func (s *ClaudeEntrypointObservationService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.startOnce.Do(func() {
		go s.run()
	})
}

// Stop flushes buffered observations and stops the loop. Idempotent.
// Callers stop serving requests first; a Record after the final flush is
// not written.
func (s *ClaudeEntrypointObservationService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		started := true
		s.startOnce.Do(func() { started = false })
		if started && s.repo != nil {
			<-s.doneCh
		}
	})
}

func (s *ClaudeEntrypointObservationService) run() {
	defer close(s.doneCh)
	ticker := time.NewTicker(claudeEntrypointObservationFlushInterval)
	defer ticker.Stop()
	var lastRetention time.Time
	for {
		select {
		case <-ticker.C:
			s.Flush(context.Background())
			if now := s.now(); now.Sub(lastRetention) >= claudeEntrypointObservationRetentionTick || now.Before(lastRetention) {
				lastRetention = now
				s.DeleteExpired(context.Background())
			}
		case <-s.stopCh:
			s.Flush(context.Background())
			return
		}
	}
}

// Flush writes the buffered aggregate in one batch. A DB error drops the
// batch (no retry) so a database outage cannot grow memory.
func (s *ClaudeEntrypointObservationService) Flush(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.mu.Lock()
	pending := s.pending
	dropped := s.dropped
	s.pending = make(map[ClaudeEntrypointObservationKey]*ClaudeEntrypointObservation)
	s.groupKeys = make(map[int64]int)
	s.dropped = 0
	s.lastStatus.DroppedLastFlush = dropped
	s.mu.Unlock()

	if dropped > 0 {
		logger.L().Warn("gateway.claude_entrypoint_observations_dropped",
			zap.Int64("dropped_requests", dropped),
			zap.Int("max_keys", claudeEntrypointObservationMaxKeys),
			zap.Int("max_keys_per_group", claudeEntrypointObservationMaxKeysPerGroup))
	}
	if len(pending) == 0 {
		return
	}
	rows := make([]ClaudeEntrypointObservation, 0, len(pending))
	for _, row := range pending {
		rows = append(rows, *row)
	}
	// A stable key order keeps concurrent writers from deadlocking.
	sort.Slice(rows, func(i, j int) bool {
		return lessObservationKey(rows[i].ClaudeEntrypointObservationKey, rows[j].ClaudeEntrypointObservationKey)
	})
	writeCtx, cancel := context.WithTimeout(ctx, claudeEntrypointObservationWriteTimeout)
	defer cancel()
	if err := s.repo.UpsertClaudeEntrypointObservations(writeCtx, rows); err != nil {
		at := s.now().UTC()
		s.mu.Lock()
		s.lastStatus.LastErrorAt = &at
		s.mu.Unlock()
		logger.L().Warn("gateway.claude_entrypoint_observations_flush_failed",
			zap.Int("rows", len(rows)), zap.Error(err))
	}
}

// DeleteExpired removes rows older than the 90-day retention window.
func (s *ClaudeEntrypointObservationService) DeleteExpired(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	// Keeps exactly the 90 UTC days (today included) the admin list can show.
	before := s.now().UTC().AddDate(0, 0, -(claudeEntrypointObservationRetentionDays - 1)).Format("2006-01-02")
	writeCtx, cancel := context.WithTimeout(ctx, claudeEntrypointObservationWriteTimeout)
	defer cancel()
	if _, err := s.repo.DeleteClaudeEntrypointObservationsBefore(writeCtx, before); err != nil {
		logger.L().Warn("gateway.claude_entrypoint_observations_retention_failed", zap.Error(err))
	}
}

// List returns the group's observations of the last `days` UTC days
// (today included), at most 500 rows; truncated reports that more rows
// exist. Rows still buffered in memory are not included.
func (s *ClaudeEntrypointObservationService) List(ctx context.Context, groupID int64, days int) (rows []ClaudeEntrypointObservationSummary, truncated bool, err error) {
	if s == nil || s.repo == nil {
		return []ClaudeEntrypointObservationSummary{}, false, nil
	}
	if days < 1 {
		days = 1
	}
	if days > ClaudeEntrypointObservationMaxDays {
		days = ClaudeEntrypointObservationMaxDays
	}
	since := s.now().UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	rows, err = s.repo.ListClaudeEntrypointObservations(ctx, groupID, since, claudeEntrypointObservationListLimit+1)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > claudeEntrypointObservationListLimit {
		rows, truncated = rows[:claudeEntrypointObservationListLimit], true
	}
	if rows == nil {
		rows = []ClaudeEntrypointObservationSummary{}
	}
	return rows, truncated, nil
}

func lessObservationKey(a, b ClaudeEntrypointObservationKey) bool {
	if a.GroupID != b.GroupID {
		return a.GroupID < b.GroupID
	}
	for _, pair := range [][2]string{
		{a.Day, b.Day}, {a.UAEntrypoint, b.UAEntrypoint}, {a.BodyEntrypoint, b.BodyEntrypoint},
		{a.BodyStatus, b.BodyStatus}, {a.CLIVersion, b.CLIVersion}, {a.Policy, b.Policy},
		{a.Result, b.Result}, {a.Reason, b.Reason},
	} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	return false
}

func sanitizeObservationUserAgent(userAgent string) string {
	userAgent = strings.ToValidUTF8(userAgent, "")
	userAgent = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, userAgent)
	if utf8.RuneCountInString(userAgent) > claudeEntrypointObservationMaxUserAgent {
		runes := []rune(userAgent)
		userAgent = string(runes[:claudeEntrypointObservationMaxUserAgent])
	}
	return userAgent
}

func truncateBytes(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
