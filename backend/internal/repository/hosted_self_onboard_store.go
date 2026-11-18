package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	// The session id itself never appears in a Redis key (sha256).
	hostedOnboardSessionKeyPrefix = "hosted_onboard:session:"
	hostedOnboardAuthURLKeyPrefix = "hosted_onboard:auth_url:"
	hostedOnboardSubmitKeyPrefix  = "hosted_onboard:submit:"
)

// hostedOnboardConsumeScript checks owner, purpose, state and expiry of the
// session hash and deletes it in one step. On any mismatch the session is
// kept and nil is returned. Returns the code verifier on success.
//
// KEYS[1] session key; ARGV: user_id, purpose, state, now_ms.
var hostedOnboardConsumeScript = redis.NewScript(`
local v = redis.call('HMGET', KEYS[1], 'user_id', 'purpose', 'state', 'expires_at_ms', 'verifier')
if not v[1] or not v[5] then
  return false
end
if v[1] ~= ARGV[1] or v[2] ~= ARGV[2] or v[3] ~= ARGV[3] then
  return false
end
if tonumber(v[4]) == nil or tonumber(v[4]) <= tonumber(ARGV[4]) then
  redis.call('DEL', KEYS[1])
  return false
end
redis.call('DEL', KEYS[1])
return v[5]
`)

type hostedOnboardStore struct {
	rdb *redis.Client
}

// NewHostedOnboardStore stores per-user self-onboarding sessions and rate
// counters in Redis.
func NewHostedOnboardStore(rdb *redis.Client) service.HostedOnboardStore {
	return &hostedOnboardStore{rdb: rdb}
}

func hostedOnboardSessionKey(sessionID string) string {
	return hashedKey(hostedOnboardSessionKeyPrefix, sessionID)
}

func (s *hostedOnboardStore) SaveSession(ctx context.Context, sessionID string, session *service.HostedOnboardSession, ttl time.Duration) error {
	if session == nil || sessionID == "" {
		return errors.New("invalid hosted onboard session")
	}
	key := hostedOnboardSessionKey(sessionID)
	ok, err := s.rdb.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("check hosted onboard session: %w", err)
	}
	if ok != 0 {
		return errors.New("hosted onboard session collision")
	}
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, key,
		"user_id", strconv.FormatInt(session.UserID, 10),
		"purpose", session.Purpose,
		"state", session.State,
		"verifier", session.CodeVerifier,
		"expires_at_ms", strconv.FormatInt(session.ExpiresAt.UnixMilli(), 10),
	)
	pipe.PExpire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("store hosted onboard session: %w", err)
	}
	return nil
}

func (s *hostedOnboardStore) ConsumeSession(ctx context.Context, sessionID string, userID int64, purpose, state string, now time.Time) (*service.HostedOnboardSession, error) {
	raw, err := hostedOnboardConsumeScript.Run(ctx, s.rdb, []string{hostedOnboardSessionKey(sessionID)},
		strconv.FormatInt(userID, 10), purpose, state, strconv.FormatInt(now.UnixMilli(), 10)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("consume hosted onboard session: %w", err)
	}
	verifier, ok := raw.(string)
	if !ok || verifier == "" {
		return nil, nil
	}
	return &service.HostedOnboardSession{UserID: userID, Purpose: purpose, State: state, CodeVerifier: verifier}, nil
}

func (s *hostedOnboardStore) CountAuthURL(ctx context.Context, userID int64, window time.Duration) (int64, error) {
	return s.count(ctx, hostedOnboardAuthURLKeyPrefix+strconv.FormatInt(userID, 10), window)
}

func (s *hostedOnboardStore) CountSubmit(ctx context.Context, userID int64, window time.Duration) (int64, error) {
	return s.count(ctx, hostedOnboardSubmitKeyPrefix+strconv.FormatInt(userID, 10), window)
}

func (s *hostedOnboardStore) count(ctx context.Context, key string, window time.Duration) (int64, error) {
	ms := window.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	n, err := aitpInstallCounterScript.Run(ctx, s.rdb, []string{key}, ms).Int64()
	if err != nil {
		return 0, fmt.Errorf("hosted onboard counter: %w", err)
	}
	return n, nil
}
