//go:build unit

package repository

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// The shared client keeps ContextTimeoutEnabled off; the nonce store must
// still give up at the caller's short deadline when Redis accepts the
// connection but never answers.
func TestClientProofNonceStoreHonorsContextDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
		}
	}()
	shared := redis.NewClient(&redis.Options{Addr: listener.Addr().String(), ReadTimeout: 10 * time.Second})
	defer func() { _ = shared.Close() }()
	store := NewClientProofNonceStore(shared)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = store.Claim(ctx, "cd_x", "n", time.Minute)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)

	// While commands are stalled, at most clientProofNonceMaxInFlight run in
	// the background; further claims fail fast instead of piling up.
	impl := store.(*clientProofNonceStore)
	for i := 0; i < clientProofNonceMaxInFlight+5; i++ {
		short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, _ = store.Claim(short, "cd_x", "n", time.Minute)
		cancel()
	}
	require.LessOrEqual(t, len(impl.inFlight), clientProofNonceMaxInFlight)
	busyCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started = time.Now()
	_, err = store.Claim(busyCtx, "cd_x", "n", time.Minute)
	require.ErrorIs(t, err, errClientProofNonceBusy)
	require.Less(t, time.Since(started), 50*time.Millisecond)
}
