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
}
