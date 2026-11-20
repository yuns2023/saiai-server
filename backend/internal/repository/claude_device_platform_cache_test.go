package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// The Redis tests need a task-owned Redis on a Unix socket (same variable as
// the carpool tests); they use DB 7 and flush it.
func cdpTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	socket := os.Getenv("SAIAI_TEST_CARPOOL_REDIS_SOCKET")
	if socket == "" {
		t.Skip("task-owned Redis Unix socket not supplied")
	}
	require.True(t, filepath.IsAbs(socket))
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, DB: 7})
	t.Cleanup(func() { _ = rdb.Close() })
	require.NoError(t, rdb.FlushDB(context.Background()).Err())
	return rdb
}

const cdpT0 = int64(1_800_000_000)

func cdpDigest(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

func cdpObs(device, family, arch, major string, user int64, at int64) service.ClaudeDevicePlatformObservation {
	return service.ClaudeDevicePlatformObservation{
		Digest: cdpDigest(device), DisplayPrefix: cdpDigest(device)[:8], Family: family, Arch: arch, Major: major,
		UserID: user, APIKeyID: user * 10, ObservedAt: at,
	}
}

type cdpHarness struct {
	t      *testing.T
	rdb    *redis.Client
	store  *claudeDevicePlatformCache
	limits service.ClaudeDevicePlatformLimits
}

func newCDPHarness(t *testing.T) *cdpHarness {
	rdb := cdpTestRedis(t)
	return &cdpHarness{t: t, rdb: rdb, store: &claudeDevicePlatformCache{rdb: rdb}, limits: service.DefaultClaudeDevicePlatformLimits()}
}

func (h *cdpHarness) record(obs service.ClaudeDevicePlatformObservation) service.ClaudeDevicePlatformWriteStatus {
	h.t.Helper()
	status, err := h.store.RecordClaudeDevicePlatform(context.Background(), obs, h.limits)
	require.NoError(h.t, err)
	return status
}

func (h *cdpHarness) device(name string, at int64) (service.ClaudeDevicePlatformDevice, bool) {
	h.t.Helper()
	fields, err := h.rdb.HGetAll(context.Background(), claudeDevicePlatformDevKey(cdpDigest(name))).Result()
	require.NoError(h.t, err)
	return decodeClaudeDevicePlatformDevice(fields, at-int64(service.ClaudeDevicePlatformRetention/time.Second))
}

func reasonSet(d service.ClaudeDevicePlatformDevice) map[string]bool {
	out := map[string]bool{}
	for _, r := range d.Reasons {
		out[r.Reason] = true
	}
	return out
}

func TestClaudeDevicePlatformRedis_CrossFamily(t *testing.T) {
	h := newCDPHarness(t)
	require.Equal(t, service.ClaudeDevicePlatformWriteOK, h.record(cdpObs("a", "macos", "arm64", "15", 1, cdpT0)))
	_, flagged := h.device("a", cdpT0)
	require.False(t, flagged)
	// Unknown values never create a difference.
	h.record(cdpObs("a", "unknown", "unknown", "", 1, cdpT0+10))
	h.record(cdpObs("a", "unknown", "x64", "", 1, cdpT0+20))
	_, flagged = h.device("a", cdpT0)
	require.False(t, flagged)

	h.record(cdpObs("a", "windows", "x64", "11", 1, cdpT0+60))
	d, flagged := h.device("a", cdpT0+60)
	require.True(t, flagged)
	require.Equal(t, map[string]bool{"cross_family": true}, reasonSet(d))
	require.Equal(t, "macos+windows", d.Reasons[0].Evidence)
	score, err := h.rdb.ZScore(context.Background(), claudeDevicePlatformFlaggedKey, cdpDigest("a")).Result()
	require.NoError(t, err)
	require.Equal(t, float64(cdpT0+60), score)

	// A late, much older event is not paired with a newer family.
	h.record(cdpObs("late", "linux", "x64", "", 1, cdpT0+25*3600))
	h.record(cdpObs("late", "macos", "x64", "", 1, cdpT0))
	_, flagged = h.device("late", cdpT0+25*3600)
	require.False(t, flagged)

	// Two families more than 24 h apart are not cross_family.
	h.record(cdpObs("b", "macos", "arm64", "", 1, cdpT0))
	h.record(cdpObs("b", "linux", "arm64", "", 1, cdpT0+25*3600))
	_, flagged = h.device("b", cdpT0+25*3600)
	require.False(t, flagged)
}

func TestClaudeDevicePlatformRedis_VersionAlternationVsUpgrade(t *testing.T) {
	h := newCDPHarness(t)
	// 15 -> 26 -> 15 again: alternation.
	h.record(cdpObs("alt", "macos", "arm64", "15", 1, cdpT0))
	h.record(cdpObs("alt", "macos", "arm64", "26", 1, cdpT0+100))
	d, flagged := h.device("alt", cdpT0+100)
	require.True(t, flagged)
	require.Equal(t, map[string]bool{"sequential_upgrade": true}, reasonSet(d))
	h.record(cdpObs("alt", "macos", "arm64", "15", 1, cdpT0+200))
	d, _ = h.device("alt", cdpT0+200)
	require.True(t, reasonSet(d)["version_alternation"])
	for _, r := range d.Reasons {
		if r.Reason == "version_alternation" {
			require.Equal(t, "macos:15/26", r.Evidence)
		}
	}

	// 10.15 -> 15 once, never back: sequential only (numeric ordering).
	h.record(cdpObs("up", "macos", "x64", "10.15", 1, cdpT0))
	h.record(cdpObs("up", "macos", "x64", "10.15", 1, cdpT0+50))
	h.record(cdpObs("up", "macos", "x64", "15", 1, cdpT0+100))
	h.record(cdpObs("up", "macos", "x64", "15", 1, cdpT0+300))
	d, flagged = h.device("up", cdpT0+300)
	require.True(t, flagged)
	require.Equal(t, map[string]bool{"sequential_upgrade": true}, reasonSet(d))

	// Out-of-order arrival: an older 10 event recorded after 11 still
	// keeps per-major first/last as min/max.
	h.record(cdpObs("ooo", "windows", "x64", "11", 1, cdpT0+100))
	h.record(cdpObs("ooo", "windows", "x64", "10", 1, cdpT0))
	d, _ = h.device("ooo", cdpT0+100)
	require.Equal(t, map[string]bool{"sequential_upgrade": true}, reasonSet(d))
	require.Equal(t, cdpT0, d.Platforms[0].FirstSeenAt.Unix())
	require.Equal(t, cdpT0+100, d.Platforms[0].LastSeenAt.Unix())
	require.Equal(t, cdpT0, d.Reasons[0].LastMatchedAt.Unix(), "matched by the event at cdpT0")
	h.record(cdpObs("ooo", "windows", "x64", "11", 1, cdpT0+150))
	h.record(cdpObs("ooo", "windows", "x64", "10", 1, cdpT0+120))
	d, _ = h.device("ooo", cdpT0+150)
	require.True(t, reasonSet(d)["version_alternation"], "10 seen again after 11 first seen")
	for _, r := range d.Reasons {
		want := map[string]int64{"sequential_upgrade": cdpT0 + 150, "version_alternation": cdpT0 + 120}[r.Reason]
		require.Equal(t, want, r.LastMatchedAt.Unix(), "last_matched never moves backwards: %s", r.Reason)
	}
}

func TestClaudeDevicePlatformRedis_ArchOnlyAndCrossUser(t *testing.T) {
	h := newCDPHarness(t)
	h.record(cdpObs("arch", "macos", "x64", "", 1, cdpT0))
	h.record(cdpObs("arch", "macos", "arm64", "", 1, cdpT0+10))
	d, _ := h.device("arch", cdpT0+10)
	require.Equal(t, map[string]bool{"arch_only": true}, reasonSet(d))

	h.record(cdpObs("cu", "linux", "x64", "", 1, cdpT0))
	_, flagged := h.device("cu", cdpT0)
	require.False(t, flagged)
	h.record(cdpObs("cu", "linux", "x64", "", 2, cdpT0+10))
	d, flagged = h.device("cu", cdpT0+10)
	require.True(t, flagged)
	require.Equal(t, map[string]bool{"cross_user": true}, reasonSet(d))
	require.Len(t, d.Users, 2)
	require.Equal(t, int64(2), d.Users[0].UserID)
	require.Equal(t, int64(20), d.Users[0].APIKeyID)
}

func TestClaudeDevicePlatformRedis_UserTruncationKeepsFlag(t *testing.T) {
	h := newCDPHarness(t)
	for u := int64(1); u <= 21; u++ {
		h.record(cdpObs("many", "linux", "x64", "", u, cdpT0+u))
	}
	d, flagged := h.device("many", cdpT0+21)
	require.True(t, flagged)
	require.True(t, reasonSet(d)["cross_user"])
	require.True(t, d.UsersTruncated)
	require.Len(t, d.Users, service.ClaudeDevicePlatformMaxUsers)
	ids := map[int64]bool{}
	for _, u := range d.Users {
		ids[u.UserID] = true
	}
	require.False(t, ids[1], "oldest user replaced")
	require.True(t, ids[21])

	// A 22nd user in the same second as the oldest kept user replaces it.
	h.record(cdpObs("many", "linux", "x64", "", 23, cdpT0+2))
	d, _ = h.device("many", cdpT0+21)
	ids = map[int64]bool{}
	for _, u := range d.Users {
		ids[u.UserID] = true
	}
	require.True(t, ids[23])
	require.False(t, ids[2])

	// An older out-of-order event for another user is dropped, the flag stays.
	h.record(cdpObs("many", "linux", "x64", "", 22, cdpT0))
	d, _ = h.device("many", cdpT0+21)
	require.Len(t, d.Users, service.ClaudeDevicePlatformMaxUsers)
	require.True(t, reasonSet(d)["cross_user"])
}

func TestClaudeDevicePlatformRedis_GlobalAndUserCaps(t *testing.T) {
	h := newCDPHarness(t)
	h.limits.MaxDevices = 3
	h.limits.MaxNewPerUserDay = 2
	ctx := context.Background()

	require.Equal(t, service.ClaudeDevicePlatformWriteOK, h.record(cdpObs("u1a", "linux", "x64", "", 1, cdpT0)))
	require.Equal(t, service.ClaudeDevicePlatformWriteOK, h.record(cdpObs("u1b", "linux", "x64", "", 1, cdpT0+1)))
	require.Equal(t, service.ClaudeDevicePlatformWriteUserCap, h.record(cdpObs("u1c", "linux", "x64", "", 1, cdpT0+2)))
	require.Zero(t, h.rdb.Exists(ctx, claudeDevicePlatformDevKey(cdpDigest("u1c"))).Val())
	// Existing devices of a capped user still update.
	require.Equal(t, service.ClaudeDevicePlatformWriteOK, h.record(cdpObs("u1a", "windows", "x64", "", 1, cdpT0+3)))
	// Another user is not affected by user 1's cap; a new user on an existing device is recorded.
	require.Equal(t, service.ClaudeDevicePlatformWriteOK, h.record(cdpObs("u2a", "linux", "x64", "", 2, cdpT0+4)))
	require.Equal(t, service.ClaudeDevicePlatformWriteGlobalCap, h.record(cdpObs("u3a", "linux", "x64", "", 3, cdpT0+5)))
	require.Equal(t, service.ClaudeDevicePlatformWriteOK, h.record(cdpObs("u1b", "linux", "x64", "", 3, cdpT0+6)))
	d, _ := h.device("u1b", cdpT0+6)
	require.True(t, reasonSet(d)["cross_user"])
	// No owner: new devices dropped.
	h.limits.MaxDevices = 100
	require.Equal(t, service.ClaudeDevicePlatformWriteNoOwner, h.record(cdpObs("anon", "linux", "x64", "", 0, cdpT0+7)))
	// The next UTC day the user may add devices again.
	require.Equal(t, service.ClaudeDevicePlatformWriteOK, h.record(cdpObs("u1c", "linux", "x64", "", 1, cdpT0+86400)))
	// A device older than the retention leaves the tracked set (frees cap).
	require.Equal(t, int64(4), h.rdb.ZCard(ctx, claudeDevicePlatformTrackedKey).Val())
	h.record(cdpObs("late", "linux", "x64", "", 9, cdpT0+31*86400))
	require.Equal(t, int64(2), h.rdb.ZCard(ctx, claudeDevicePlatformTrackedKey).Val(), "late + u1c (recorded on day 1)")
	require.Zero(t, h.rdb.ZCard(ctx, claudeDevicePlatformFlaggedKey).Val())
}

func TestClaudeDevicePlatformRedis_ReadDropsExpiredDetails(t *testing.T) {
	h := newCDPHarness(t)
	h.record(cdpObs("old", "macos", "arm64", "", 1, cdpT0))
	h.record(cdpObs("old", "linux", "x64", "", 2, cdpT0+29*86400))
	// Read on day 31: user 1 and the macOS entry left the window, the
	// cross_user flag (matched on day 29) is still shown.
	d, flagged := h.device("old", cdpT0+31*86400)
	require.True(t, flagged)
	require.True(t, reasonSet(d)["cross_user"])
	require.Len(t, d.Users, 1)
	require.Equal(t, int64(2), d.Users[0].UserID)
	require.Len(t, d.Platforms, 1)
	require.Equal(t, "linux", d.Platforms[0].Family)
}

func TestClaudeDevicePlatformRedis_TTLs(t *testing.T) {
	h := newCDPHarness(t)
	ctx := context.Background()
	h.record(cdpObs("ttl", "linux", "x64", "", 1, cdpT0))
	// The hash expires when its tracked entry would be pruned (event clock).
	expireAt := h.rdb.ExpireTime(ctx, claudeDevicePlatformDevKey(cdpDigest("ttl"))).Val()
	require.Equal(t, time.Duration(cdpT0)*time.Second+service.ClaudeDevicePlatformRetention, expireAt)
	h.record(cdpObs("ttl", "linux", "x64", "", 1, cdpT0+100))
	h.record(cdpObs("ttl", "linux", "x64", "", 1, cdpT0+50)) // late event never shortens it
	expireAt = h.rdb.ExpireTime(ctx, claudeDevicePlatformDevKey(cdpDigest("ttl"))).Val()
	require.Equal(t, time.Duration(cdpT0+100)*time.Second+service.ClaudeDevicePlatformRetention, expireAt)
	dailyTTL := h.rdb.TTL(ctx, claudeDevicePlatformNewKey(1, cdpT0)).Val()
	require.InDelta(t, (48 * time.Hour).Seconds(), dailyTTL.Seconds(), 5)
}

func TestClaudeDevicePlatformRedis_ConcurrentUsers(t *testing.T) {
	h := newCDPHarness(t)
	var wg sync.WaitGroup
	for u := int64(1); u <= 2; u++ {
		wg.Add(1)
		go func(u int64) {
			defer wg.Done()
			other := &claudeDevicePlatformCache{rdb: h.rdb}
			for i := int64(0); i < 20; i++ {
				_, err := other.RecordClaudeDevicePlatform(context.Background(), cdpObs("race", "linux", "x64", "", u, cdpT0+i), h.limits)
				if err != nil {
					t.Errorf("record: %v", err)
				}
			}
		}(u)
	}
	wg.Wait()
	d, flagged := h.device("race", cdpT0+20)
	require.True(t, flagged)
	require.Len(t, d.Users, 2)
	// Exactly one of the two users created the device (one daily count).
	c1, _ := h.rdb.Get(context.Background(), claudeDevicePlatformNewKey(1, cdpT0)).Int()
	c2, _ := h.rdb.Get(context.Background(), claudeDevicePlatformNewKey(2, cdpT0)).Int()
	require.Equal(t, 1, c1+c2)
}

func TestClaudeDevicePlatformRedis_ListPagination(t *testing.T) {
	h := newCDPHarness(t)
	ctx := context.Background()
	// 5 cross_user devices at the same second (tie-break by digest), 2 later
	// arch_only devices, one unflagged device.
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("tie-%d", i)
		h.record(cdpObs(name, "linux", "x64", "", 1, cdpT0))
		h.record(cdpObs(name, "linux", "x64", "", 2, cdpT0))
	}
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("arch-%d", i)
		h.record(cdpObs(name, "macos", "x64", "", 3, cdpT0+100+int64(i)))
		h.record(cdpObs(name, "macos", "arm64", "", 3, cdpT0+100+int64(i)))
	}
	h.record(cdpObs("plain", "linux", "x64", "", 4, cdpT0))
	now := time.Unix(cdpT0+200, 0)

	var all []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, err := h.store.ListFlaggedClaudeDevicePlatforms(ctx, service.ClaudeDevicePlatformFlaggedQuery{Limit: 2, Cursor: cursor, Reasons: []string{"cross_user"}, Now: now})
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Items), 2)
		for _, item := range page.Items {
			all = append(all, item.DisplayPrefix)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	require.Len(t, all, 5)
	seen := map[string]bool{}
	for i, p := range all {
		require.False(t, seen[p], "no duplicates")
		seen[p] = true
		if i > 0 {
			require.Greater(t, cdpDigestByPrefix(t, all[i-1]), cdpDigestByPrefix(t, p), "digest desc within a tie")
		}
	}

	page, err := h.store.ListFlaggedClaudeDevicePlatforms(ctx, service.ClaudeDevicePlatformFlaggedQuery{Limit: 10, Reasons: []string{"arch_only", "cross_user"}, Now: now})
	require.NoError(t, err)
	require.Len(t, page.Items, 7)
	require.Empty(t, page.NextCursor)
	require.Equal(t, cdpDigest("arch-1")[:8], page.Items[0].DisplayPrefix, "score desc")

	// A missing detail hash is skipped.
	require.NoError(t, h.rdb.Del(ctx, claudeDevicePlatformDevKey(cdpDigest("arch-1"))).Err())
	page, err = h.store.ListFlaggedClaudeDevicePlatforms(ctx, service.ClaudeDevicePlatformFlaggedQuery{Limit: 10, Reasons: []string{"arch_only"}, Now: now})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)

	// The cursor member disappears between pages (slow path): no
	// duplicates, the rest of the tie group still follows.
	first, err := h.store.ListFlaggedClaudeDevicePlatforms(ctx, service.ClaudeDevicePlatformFlaggedQuery{Limit: 2, Reasons: []string{"cross_user"}, Now: now})
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	cursorDigest := first.NextCursor[len(first.NextCursor)-64:]
	require.NoError(t, h.rdb.ZRem(ctx, claudeDevicePlatformFlaggedKey, cursorDigest).Err())
	rest, err := h.store.ListFlaggedClaudeDevicePlatforms(ctx, service.ClaudeDevicePlatformFlaggedQuery{Limit: 10, Cursor: first.NextCursor, Reasons: []string{"cross_user"}, Now: now})
	require.NoError(t, err)
	require.Len(t, rest.Items, 3)
	for _, item := range rest.Items {
		for _, prev := range first.Items {
			require.NotEqual(t, prev.DisplayPrefix, item.DisplayPrefix)
		}
	}

	_, err = h.store.ListFlaggedClaudeDevicePlatforms(ctx, service.ClaudeDevicePlatformFlaggedQuery{Limit: 10, Cursor: "garbage", Now: now})
	require.ErrorIs(t, err, service.ErrClaudeDevicePlatformInvalidQuery)
}

func cdpDigestByPrefix(t *testing.T, prefix string) string {
	t.Helper()
	for i := 0; i < 5; i++ {
		if d := cdpDigest(fmt.Sprintf("tie-%d", i)); d[:8] == prefix {
			return d
		}
	}
	t.Fatalf("unknown prefix %s", prefix)
	return ""
}

func TestClaudeDevicePlatformRedis_MemoryUsage(t *testing.T) {
	h := newCDPHarness(t)
	ctx := context.Background()
	measure := func(name string) int64 {
		n, err := h.rdb.MemoryUsage(ctx, claudeDevicePlatformDevKey(cdpDigest(name))).Result()
		require.NoError(t, err)
		return n
	}
	h.record(cdpObs("single", "macos", "arm64", "15", 1, cdpT0))
	for u := int64(1); u <= 20; u++ {
		h.record(cdpObs("twenty", "linux", "x64", "", 1000000+u, cdpT0+u))
	}
	at := cdpT0
	for _, fam := range []string{"macos", "windows", "linux"} {
		for _, arch := range []string{"x64", "arm64", "unknown"} {
			at++
			h.record(cdpObs("max", fam, arch, "", 1000001, at))
		}
	}
	for _, major := range []string{"10.15", "11", "12", "13", "14", "15", "26", "15"} {
		at++
		h.record(cdpObs("max", "macos", "arm64", major, 1000001, at))
	}
	for _, major := range []string{"10", "11", "10"} {
		at++
		h.record(cdpObs("max", "windows", "x64", major, 1000001, at))
	}
	for u := int64(2); u <= 21; u++ {
		at++
		h.record(cdpObs("max", "linux", "x64", "", 1000000+u, at))
	}
	d, _ := h.device("max", at)
	require.Len(t, d.Reasons, 5)
	enc := h.rdb.ObjectEncoding(ctx, claudeDevicePlatformDevKey(cdpDigest("max"))).Val()
	t.Logf("MEMORY USAGE bytes: single-user=%d 20-user=%d max-platform=%d (encoding %s)", measure("single"), measure("twenty"), measure("max"), enc)
	require.Equal(t, "listpack", enc)
}
