package service

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clientproof"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	gocache "github.com/patrickmn/go-cache"
)

// SAIAI client proof v1 (docs/CLIENT_PROOF_V1.md). Observe mode only: the
// status is recorded on usage rows and never rejects a request.

// Group client_attestation_policy values. "enforce" is reserved: it is
// accepted from the database (CHECK constraint) and treated like observe,
// but the v1 admin API does not allow selecting it.
const (
	ClientAttestationPolicyOff     = "off"
	ClientAttestationPolicyObserve = "observe"
	ClientAttestationPolicyEnforce = "enforce"
)

const (
	// ClientDeviceLimit is the maximum number of non-revoked devices per user.
	ClientDeviceLimit = 50

	clientDeviceCacheTTL         = 60 * time.Second
	clientDeviceNegativeCacheTTL = 10 * time.Second
	clientDeviceCacheMaxEntries  = 10000
	clientDeviceTouchInterval    = 5 * time.Minute
	clientDeviceTouchTimeout     = 2 * time.Second
	clientAttestationWindow      = 7 * 24 * time.Hour
	codexDeviceActiveWindow      = 30 * 24 * time.Hour
	clientDeviceSummaryTimeout   = 2 * time.Second

	// Request-path budgets: worst-case added latency is the device lookup
	// plus the nonce claim (150 ms + 100 ms); everything else is CPU-only or
	// asynchronous. See docs/CLIENT_PROOF_V1.md.
	clientDeviceLookupTimeout = 150 * time.Millisecond
	clientProofNonceTimeout   = 100 * time.Millisecond
	// Concurrency caps (non-blocking; when full the work is skipped).
	clientDeviceLookupConcurrency = 16 // full → unverified without I/O
	clientDeviceTouchConcurrency  = 8  // full → the last_* update is dropped

	// Enrollment protection: process-wide concurrency taken before any DB
	// connection, a per-user fixed-window rate limit and a whole-transaction
	// timeout. Busy/rate-limited → 503 + Retry-After (clients retry on 5xx).
	clientDeviceEnrollConcurrency = 4
	clientDeviceEnrollPerUser     = 10
	clientDeviceEnrollPeriod      = time.Minute
	clientDeviceEnrollTimeout     = 3 * time.Second
	// ClientDeviceEnrollRetryAfter is the Retry-After (seconds) sent with 503.
	ClientDeviceEnrollRetryAfter = 60
)

// Client surfaces for last_claude_at / last_codex_at, chosen by route.
const (
	ClientSurfaceClaude = "claude"
	ClientSurfaceCodex  = "codex"
)

// ClientSurfaceForPath maps a gateway request path to the device activity
// field it updates: Claude Messages → claude; Responses, Codex backend, Codex
// images and native ChatGPT → codex; anything else updates last_seen only.
func ClientSurfaceForPath(path string) string {
	switch {
	case path == "/v1/messages" || path == "/v1/messages/count_tokens":
		return ClientSurfaceClaude
	case path == "/v1/responses" || strings.HasPrefix(path, "/v1/responses/"),
		path == "/responses" || strings.HasPrefix(path, "/responses/"),
		strings.HasPrefix(path, "/backend-api/codex/"),
		strings.HasPrefix(path, "/v1/codex/"),
		strings.HasPrefix(path, "/chatgpt/backend-api/"):
		return ClientSurfaceCodex
	default:
		return ""
	}
}

var (
	ErrClientDeviceRevoked  = infraerrors.Forbidden("client_device_revoked", "client device key was revoked")
	ErrClientDeviceLimit    = infraerrors.TooManyRequests("client_device_limit", "client device limit reached")
	ErrClientDeviceNotFound = infraerrors.NotFound("client_device_not_found", "client device not found")
	ErrClientDeviceInvalid  = infraerrors.BadRequest("client_device_invalid", "invalid client device enrollment")
	// ErrClientDeviceBusy covers both the enrollment concurrency cap and the
	// per-user rate limit; the handler answers 503 with Retry-After.
	ErrClientDeviceBusy = infraerrors.ServiceUnavailable("client_device_busy", "client device enrollment busy")
)

// NormalizeClientAttestationPolicy maps stored values; unknown → off.
func NormalizeClientAttestationPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case ClientAttestationPolicyObserve:
		return ClientAttestationPolicyObserve
	case ClientAttestationPolicyEnforce:
		return ClientAttestationPolicyEnforce
	default:
		return ClientAttestationPolicyOff
	}
}

// CollectsClientAttestation reports whether usage rows in this group record
// the client proof status (observe, or the reserved enforce).
func (g *Group) CollectsClientAttestation() bool {
	if g == nil {
		return false
	}
	return NormalizeClientAttestationPolicy(g.ClientAttestationPolicy) != ClientAttestationPolicyOff
}

// ClientAttestationForUsage returns the value to persist in
// usage_logs.client_attestation for the final (post smart-switch) key: the
// request status when that key's group collects attestation, else NULL.
func ClientAttestationForUsage(status *int16, apiKey *APIKey) *int16 {
	if status == nil || apiKey == nil || !apiKey.Group.CollectsClientAttestation() {
		return nil
	}
	value := *status
	return &value
}

// ClientDevice is an enrolled local-proxy device key.
type ClientDevice struct {
	ID              int64      `json:"id"`
	DeviceID        string     `json:"device_id"`
	UserID          int64      `json:"user_id"`
	FirstAPIKeyID   *int64     `json:"first_api_key_id"`
	PublicKey       []byte     `json:"-"`
	PublicKeySHA256 string     `json:"public_key_sha256"`
	ClientVersion   string     `json:"client_version"`
	OS              string     `json:"os"`
	Arch            string     `json:"arch"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSeenAt      *time.Time `json:"last_seen_at"`
	LastIP          string     `json:"last_ip"`
	LastClaudeAt    *time.Time `json:"last_claude_at"`
	LastCodexAt     *time.Time `json:"last_codex_at"`
	RevokedAt       *time.Time `json:"revoked_at"`
	RevokedBy       *int64     `json:"revoked_by"`
}

// ClientDeviceEnrollInput is a validated enrollment request.
type ClientDeviceEnrollInput struct {
	UserID          int64
	APIKeyID        int64
	DeviceID        string // candidate id for a new row
	PublicKey       []byte
	PublicKeySHA256 string
	ClientVersion   string
	OS              string
	Arch            string
}

// ClientDeviceEnrollOutcome tells the service what the repository did.
type ClientDeviceEnrollOutcome int

const (
	ClientDeviceEnrollCreated ClientDeviceEnrollOutcome = iota + 1
	ClientDeviceEnrollExisting
	ClientDeviceEnrollRevoked
	ClientDeviceEnrollLimit
)

// ClientDeviceTouch selects which last_* columns a verified request updates.
type ClientDeviceTouch struct {
	IP     string
	Seen   bool
	Claude bool
	Codex  bool
}

// ClientDeviceUserSummary is the per-user admin list aggregate. Nil fields
// mean the aggregate could not be loaded (never a misleading zero).
type ClientDeviceUserSummary struct {
	CodexDeviceCount    *int             `json:"codex_device_count"`
	ClientAttestation7d map[string]int64 `json:"client_attestation_7d"`
}

type ClientDeviceRepository interface {
	// Enroll is idempotent on (user_id, public_key_sha256) and enforces the
	// per-user limit atomically.
	Enroll(ctx context.Context, input ClientDeviceEnrollInput, limit int) (*ClientDevice, ClientDeviceEnrollOutcome, error)
	// GetByDeviceID returns (nil, nil) when the device does not exist.
	GetByDeviceID(ctx context.Context, deviceID string) (*ClientDevice, error)
	ListByUser(ctx context.Context, userID int64) ([]ClientDevice, error)
	// Revoke returns the revoked device's device_id.
	Revoke(ctx context.Context, userID, id, revokedBy int64) (string, error)
	Touch(ctx context.Context, id int64, touch ClientDeviceTouch) error
	CountActiveCodexDevices(ctx context.Context, userIDs []int64, since time.Time) (map[int64]int, error)
	CountClientAttestations(ctx context.Context, userIDs []int64, since time.Time) (map[int64]map[int16]int64, error)
}

// ClientProofNonceStore remembers nonces for replay detection.
type ClientProofNonceStore interface {
	// Claim returns true when the nonce was not seen before (SET NX).
	Claim(ctx context.Context, deviceID, nonce string, ttl time.Duration) (bool, error)
}

// ClientProofRequest is everything the verifier needs from one request.
type ClientProofRequest struct {
	Header   string
	Method   string
	Target   string
	APIKey   string
	UserID   int64
	ClientIP string
	// Surface selects last_claude_at / last_codex_at (ClientSurfaceForPath).
	Surface string
}

type cachedClientDevice struct {
	device *ClientDevice
	key    *ecdsa.PublicKey
}

// ClientDeviceService owns enrollment, admin views and proof verification.
type ClientDeviceService struct {
	repo    ClientDeviceRepository
	nonces  ClientProofNonceStore
	devices *clientDeviceCache
	touches *gocache.Cache
	now     func() time.Time
	// touchAsync runs throttled last_* updates off the request path.
	touchAsync func(func())

	lookupSlots chan struct{}
	touchSlots  chan struct{}
	enrollSlots chan struct{}

	enrollMu     sync.Mutex
	enrollWindow map[int64]clientDeviceEnrollCounter
}

type clientDeviceEnrollCounter struct {
	start time.Time
	count int
}

func NewClientDeviceService(repo ClientDeviceRepository, nonces ClientProofNonceStore) *ClientDeviceService {
	return &ClientDeviceService{
		repo:         repo,
		nonces:       nonces,
		devices:      newClientDeviceCache(clientDeviceCacheMaxEntries),
		touches:      gocache.New(clientDeviceTouchInterval, 2*clientDeviceTouchInterval),
		now:          time.Now,
		touchAsync:   func(fn func()) { go fn() },
		lookupSlots:  make(chan struct{}, clientDeviceLookupConcurrency),
		touchSlots:   make(chan struct{}, clientDeviceTouchConcurrency),
		enrollSlots:  make(chan struct{}, clientDeviceEnrollConcurrency),
		enrollWindow: make(map[int64]clientDeviceEnrollCounter),
	}
}

func tryAcquire(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// allowEnroll applies the per-user fixed-window enrollment rate limit.
func (s *ClientDeviceService) allowEnroll(userID int64) bool {
	s.enrollMu.Lock()
	defer s.enrollMu.Unlock()
	now := s.now()
	for id, w := range s.enrollWindow {
		if now.Sub(w.start) >= clientDeviceEnrollPeriod {
			delete(s.enrollWindow, id)
		}
	}
	w := s.enrollWindow[userID]
	if w.count == 0 {
		w.start = now
	}
	if w.count >= clientDeviceEnrollPerUser {
		return false
	}
	w.count++
	s.enrollWindow[userID] = w
	return true
}

// Enroll registers (or returns) the device for a public key.
func (s *ClientDeviceService) Enroll(ctx context.Context, userID, apiKeyID int64, publicKey, clientVersion, osName, arch string) (*ClientDevice, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("client_device_unavailable", "client device service unavailable")
	}
	_, raw, err := clientproof.DecodePublicKey(publicKey)
	if err != nil {
		return nil, ErrClientDeviceInvalid
	}
	deviceID, err := clientproof.NewDeviceID()
	if err != nil {
		return nil, err
	}
	if !s.allowEnroll(userID) {
		return nil, ErrClientDeviceBusy
	}
	// Taken before the repository acquires a DB connection.
	if !tryAcquire(s.enrollSlots) {
		return nil, ErrClientDeviceBusy
	}
	defer func() { <-s.enrollSlots }()
	ctx, cancel := context.WithTimeout(ctx, clientDeviceEnrollTimeout)
	defer cancel()
	device, outcome, err := s.repo.Enroll(ctx, ClientDeviceEnrollInput{
		UserID:          userID,
		APIKeyID:        apiKeyID,
		DeviceID:        deviceID,
		PublicKey:       raw,
		PublicKeySHA256: clientproof.PublicKeySHA256(raw),
		ClientVersion:   clientVersion,
		OS:              osName,
		Arch:            arch,
	}, ClientDeviceLimit)
	if err != nil {
		return nil, err
	}
	switch outcome {
	case ClientDeviceEnrollRevoked:
		return nil, ErrClientDeviceRevoked
	case ClientDeviceEnrollLimit:
		return nil, ErrClientDeviceLimit
	}
	if device == nil {
		return nil, errors.New("client device enrollment returned no device")
	}
	if outcome == ClientDeviceEnrollCreated {
		slog.Log(ctx, slog.LevelInfo, "client_device_enrolled", "user_id", userID, "client_device_id", device.ID)
	}
	// A previous negative lookup for this id must not hide the new device.
	s.devices.delete(device.DeviceID)
	return device, nil
}

func (s *ClientDeviceService) ListUserDevices(ctx context.Context, userID int64) ([]ClientDevice, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("client_device_unavailable", "client device service unavailable")
	}
	return s.repo.ListByUser(ctx, userID)
}

// RevokeUserDevice revokes by row id and drops the local cache entry so this
// instance stops reporting the device as valid immediately. Other instances
// converge within clientDeviceCacheTTL.
func (s *ClientDeviceService) RevokeUserDevice(ctx context.Context, userID, id, revokedBy int64) error {
	if s == nil || s.repo == nil {
		return infraerrors.ServiceUnavailable("client_device_unavailable", "client device service unavailable")
	}
	deviceID, err := s.repo.Revoke(ctx, userID, id, revokedBy)
	if err != nil {
		return err
	}
	s.devices.revoke(deviceID)
	slog.Log(ctx, slog.LevelInfo, "client_device_revoked", "user_id", userID, "client_device_id", id, "revoked_by", revokedBy)
	return nil
}

// ListUserSummaries returns the admin user-list aggregates for one page of
// users. Each aggregate fails independently and is left nil on error.
func (s *ClientDeviceService) ListUserSummaries(ctx context.Context, userIDs []int64) map[int64]ClientDeviceUserSummary {
	out := make(map[int64]ClientDeviceUserSummary, len(userIDs))
	if s == nil || s.repo == nil || len(userIDs) == 0 {
		return out
	}
	lookupCtx, cancel := context.WithTimeout(ctx, clientDeviceSummaryTimeout)
	defer cancel()
	now := s.now()
	counts, countErr := s.repo.CountActiveCodexDevices(lookupCtx, userIDs, now.Add(-codexDeviceActiveWindow))
	if countErr != nil {
		slog.Warn("admin_user_codex_device_summary_failed", "error", countErr)
	}
	attestations, attErr := s.repo.CountClientAttestations(lookupCtx, userIDs, now.Add(-clientAttestationWindow))
	if attErr != nil {
		slog.Warn("admin_user_client_attestation_summary_failed", "error", attErr)
	}
	for _, id := range userIDs {
		var summary ClientDeviceUserSummary
		if countErr == nil {
			count := counts[id]
			summary.CodexDeviceCount = &count
		}
		if attErr == nil {
			named := make(map[string]int64)
			for status, n := range attestations[id] {
				if clientproof.Status(status).Known() && n > 0 {
					named[clientproof.Status(status).String()] = n
				}
			}
			summary.ClientAttestation7d = named
		}
		out[id] = summary
	}
	return out
}

// VerifyProof computes the observe-mode status for one request. It never
// returns an error: infrastructure failures map to StatusUnverified.
//
// Order: missing → parse (invalid) → device lookup (unverified on error;
// unknown_device when absent or owned by another user) → signature
// (invalid) → revoked → freshness (stale) → nonce claim (replay /
// unverified) → valid. The nonce is consumed only by otherwise-valid proofs.
func (s *ClientDeviceService) VerifyProof(ctx context.Context, req ClientProofRequest) clientproof.Status {
	if req.Header == "" {
		return clientproof.StatusMissing
	}
	proof, err := clientproof.ParseHeader(req.Header)
	if err != nil {
		return clientproof.StatusInvalid
	}
	// The proof binds the Authorization Bearer value; without one (e.g.
	// x-api-key only) no proof can be valid, including one over sha256("").
	if req.APIKey == "" {
		return clientproof.StatusInvalid
	}
	if s == nil || s.repo == nil {
		return clientproof.StatusUnverified
	}
	entry, err := s.lookupDevice(ctx, proof.DeviceID)
	if err != nil {
		return clientproof.StatusUnverified
	}
	if entry.device == nil || entry.device.UserID != req.UserID {
		return clientproof.StatusUnknownDevice
	}
	if entry.key == nil {
		return clientproof.StatusInvalid
	}
	now := s.now()
	status := clientproof.Verify(proof, entry.key, req.Method, req.Target, req.APIKey, now)
	if status == clientproof.StatusInvalid {
		return status
	}
	if entry.device.RevokedAt != nil {
		return clientproof.StatusRevoked
	}
	if status != clientproof.StatusValid {
		return status
	}
	if s.nonces == nil {
		return clientproof.StatusUnverified
	}
	nonceCtx, cancel := context.WithTimeout(ctx, clientProofNonceTimeout)
	fresh, err := s.nonces.Claim(nonceCtx, proof.DeviceID, proof.Nonce, clientproof.NonceTTL)
	cancel()
	if err != nil {
		return clientproof.StatusUnverified
	}
	if !fresh {
		return clientproof.StatusReplay
	}
	s.touch(entry.device, req.Surface, req.ClientIP)
	return clientproof.StatusValid
}

var errClientDeviceLookupBusy = errors.New("client device lookup busy")

func (s *ClientDeviceService) lookupDevice(ctx context.Context, deviceID string) (cachedClientDevice, error) {
	if entry, ok := s.devices.get(deviceID); ok {
		return entry, nil
	}
	if !tryAcquire(s.lookupSlots) {
		return cachedClientDevice{}, errClientDeviceLookupBusy
	}
	defer func() { <-s.lookupSlots }()
	generation := s.devices.generation()
	lookupCtx, cancel := context.WithTimeout(ctx, clientDeviceLookupTimeout)
	defer cancel()
	device, err := s.repo.GetByDeviceID(lookupCtx, deviceID)
	if err != nil {
		return cachedClientDevice{}, err
	}
	entry := cachedClientDevice{device: device}
	ttl := clientDeviceCacheTTL
	if device == nil {
		ttl = clientDeviceNegativeCacheTTL
	} else if key, keyErr := clientproof.ParsePublicKey(device.PublicKey); keyErr == nil {
		entry.key = key
	}
	s.devices.setIfCurrent(deviceID, entry, ttl, generation)
	return entry, nil
}

// touch updates last_* at most once per (device, field) per interval. The
// update runs off the request path with its own short timeout; at most
// clientDeviceTouchConcurrency run at once, otherwise the update is dropped.
func (s *ClientDeviceService) touch(device *ClientDevice, surface, clientIP string) {
	if device == nil {
		return
	}
	id := strconv.FormatInt(device.ID, 10)
	due := func(field string) bool {
		return s.touches.Add(field+":"+id, struct{}{}, clientDeviceTouchInterval) == nil
	}
	touch := ClientDeviceTouch{IP: clientIP, Seen: due("seen")}
	switch surface {
	case ClientSurfaceCodex:
		touch.Codex = due(ClientSurfaceCodex)
	case ClientSurfaceClaude:
		touch.Claude = due(ClientSurfaceClaude)
	}
	if !touch.Seen && !touch.Claude && !touch.Codex {
		return
	}
	if !tryAcquire(s.touchSlots) {
		// Dropped: release the throttle so a later valid request retries.
		for field, set := range map[string]bool{"seen": touch.Seen, ClientSurfaceCodex: touch.Codex, ClientSurfaceClaude: touch.Claude} {
			if set {
				s.touches.Delete(field + ":" + id)
			}
		}
		return
	}
	repo := s.repo
	deviceRowID := device.ID
	s.touchAsync(func() {
		defer func() { <-s.touchSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), clientDeviceTouchTimeout)
		defer cancel()
		if err := repo.Touch(ctx, deviceRowID, touch); err != nil {
			slog.Warn("client_device_touch_failed", "client_device_id", deviceRowID, "error", err)
		}
	})
}
