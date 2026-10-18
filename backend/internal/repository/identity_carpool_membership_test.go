//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Intercept Redis commands before network I/O to verify the exact read-only
// pipeline and distinguish admitted devices from overflow/other accounts.
type carpoolMembershipHook struct {
	redis.Hook
	t      *testing.T
	values map[string]map[string]bool
	calls  int
	fail   bool
}

func (h *carpoolMembershipHook) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (h *carpoolMembershipHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (h *carpoolMembershipHook) ProcessPipelineHook(_ redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(_ context.Context, cmds []redis.Cmder) error {
		h.calls++
		if h.fail {
			return errors.New("cache unavailable")
		}
		for _, cmd := range cmds {
			require.Equal(h.t, "hexists", cmd.Name())
			args := cmd.Args()
			cmd.(*redis.BoolCmd).SetVal(h.values[args[1].(string)][args[2].(string)])
		}
		return nil
	}
}

func TestCarpoolDeviceMembership(t *testing.T) {
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	field := carpoolDeviceField("device-a")
	hook := &carpoolMembershipHook{t: t, values: map[string]map[string]bool{
		carpoolRecordedKey(1): {field: true},
		carpoolRecordedKey(2): {carpoolDeviceField("other-device"): true},
		carpoolOverflowKey(3): {field: true},
	}}
	client.AddHook(hook)
	cache := NewIdentityCache(client).(*identityCache)
	got, err := cache.GetCarpoolDeviceMembership(ctx, []int64{1, 2, 3, 4}, " device-a ")
	require.NoError(t, err)
	require.Equal(t, map[int64]bool{1: true, 2: false, 3: false, 4: false}, got)
	require.Equal(t, 1, hook.calls)
	_, err = cache.GetCarpoolDeviceMembership(ctx, nil, "device-a")
	require.NoError(t, err)
	_, err = cache.GetCarpoolDeviceMembership(ctx, []int64{1}, " ")
	require.NoError(t, err)
	require.Equal(t, 1, hook.calls)
	hook.fail = true
	got, err = cache.GetCarpoolDeviceMembership(ctx, []int64{1}, "device-a")
	require.Error(t, err)
	require.Nil(t, got, "never use partial pipeline results")
}
