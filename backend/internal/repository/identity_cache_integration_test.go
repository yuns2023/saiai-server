//go:build integration

package repository

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type IdentityCacheSuite struct {
	IntegrationRedisSuite
	cache *identityCache
}

func (s *IdentityCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewIdentityCache(s.rdb).(*identityCache)
}

func (s *IdentityCacheSuite) TestGetFingerprint_Missing() {
	_, err := s.cache.GetFingerprint(s.ctx, 1)
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil for missing fingerprint")
}

func (s *IdentityCacheSuite) TestSetAndGetFingerprint() {
	fp := &service.Fingerprint{ClientID: "c1", UserAgent: "ua"}
	require.NoError(s.T(), s.cache.SetFingerprint(s.ctx, 1, fp), "SetFingerprint")
	gotFP, err := s.cache.GetFingerprint(s.ctx, 1)
	require.NoError(s.T(), err, "GetFingerprint")
	require.Equal(s.T(), "c1", gotFP.ClientID)
	require.Equal(s.T(), "ua", gotFP.UserAgent)
}

func (s *IdentityCacheSuite) TestFingerprint_TTL() {
	fp := &service.Fingerprint{ClientID: "c1", UserAgent: "ua"}
	require.NoError(s.T(), s.cache.SetFingerprint(s.ctx, 2, fp))

	fpKey := fmt.Sprintf("%s%d", fingerprintKeyPrefix, 2)
	ttl, err := s.rdb.TTL(s.ctx, fpKey).Result()
	require.NoError(s.T(), err, "TTL fpKey")
	s.AssertTTLWithin(ttl, 1*time.Second, fingerprintTTL)
}

func (s *IdentityCacheSuite) TestGetFingerprint_JSONCorruption() {
	fpKey := fmt.Sprintf("%s%d", fingerprintKeyPrefix, 999)
	require.NoError(s.T(), s.rdb.Set(s.ctx, fpKey, "invalid-json-data", 1*time.Minute).Err(), "Set invalid JSON")

	_, err := s.cache.GetFingerprint(s.ctx, 999)
	require.Error(s.T(), err, "expected error for corrupted JSON")
	require.False(s.T(), errors.Is(err, redis.Nil), "expected decoding error, not redis.Nil")
}

func (s *IdentityCacheSuite) TestSetFingerprint_Nil() {
	err := s.cache.SetFingerprint(s.ctx, 100, nil)
	require.NoError(s.T(), err, "SetFingerprint(nil) should succeed")
}

func (s *IdentityCacheSuite) TestGetOrCreateCarpoolDevice_ConcurrentDifferentDevicesHonorsLimit() {
	const accountID int64 = 42

	start := make(chan struct{})
	var wg sync.WaitGroup
	type result struct {
		record *service.CarpoolDeviceRecord
		err    error
	}
	results := make(chan result, 2)

	for _, deviceID := range []string{"device-a", "device-b"} {
		wg.Add(1)
		go func(deviceID string) {
			defer wg.Done()
			<-start
			record, err := s.cache.GetOrCreateCarpoolDevice(s.ctx, accountID, deviceID, service.ClientHints{UserAgent: "ua"}, 1, time.Now().Unix())
			results <- result{record: record, err: err}
		}(deviceID)
	}

	close(start)
	wg.Wait()
	close(results)

	successes := 0
	failures := 0
	for result := range results {
		if errors.Is(result.err, service.ErrClaudeOAuthCarpoolDevicesFull) {
			failures++
			continue
		}
		require.NoError(s.T(), result.err)
		require.NotNil(s.T(), result.record)
		successes++
	}

	require.Equal(s.T(), 1, successes)
	require.Equal(s.T(), 1, failures)

	recorded, err := s.cache.ListCarpoolDevices(s.ctx, accountID)
	require.NoError(s.T(), err)
	require.Len(s.T(), recorded, 1)

	overflow, err := s.cache.ListCarpoolOverflowDevices(s.ctx, accountID)
	require.NoError(s.T(), err)
	require.Len(s.T(), overflow, 1)
}

func (s *IdentityCacheSuite) TestGetOrCreateCarpoolDevice_ConcurrentSameDeviceReusesRecord() {
	const accountID int64 = 43

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan *service.CarpoolDeviceRecord, 8)
	errs := make(chan error, 8)

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			assignment, err := s.cache.GetOrCreateCarpoolDevice(s.ctx, accountID, "same-device", service.ClientHints{UserAgent: "ua"}, 2, time.Now().Unix())
			if err != nil {
				errs <- err
				return
			}
			results <- assignment
		}()
	}

	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(s.T(), err)
	}

	for record := range results {
		require.NotNil(s.T(), record)
		require.Equal(s.T(), "same-device", record.OriginalDeviceID)
	}

	recorded, err := s.cache.ListCarpoolDevices(s.ctx, accountID)
	require.NoError(s.T(), err)
	require.Len(s.T(), recorded, 1)
	require.Equal(s.T(), "same-device", recorded[0].OriginalDeviceID)
}

func (s *IdentityCacheSuite) TestGetOrCreateCarpoolDevice_OverflowIsCapped() {
	const accountID int64 = 430

	_, err := s.cache.GetOrCreateCarpoolDevice(s.ctx, accountID, "recorded-device", service.ClientHints{UserAgent: "ua"}, 1, time.Now().Unix())
	require.NoError(s.T(), err)

	for i := 0; i < carpoolOverflowMaxItems+5; i++ {
		_, err := s.cache.GetOrCreateCarpoolDevice(s.ctx, accountID, fmt.Sprintf("overflow-%03d", i), service.ClientHints{UserAgent: "ua"}, 1, time.Now().Add(time.Duration(i)*time.Second).Unix())
		require.ErrorIs(s.T(), err, service.ErrClaudeOAuthCarpoolDevicesFull)
	}

	overflow, err := s.cache.ListCarpoolOverflowDevices(s.ctx, accountID)
	require.NoError(s.T(), err)
	require.Len(s.T(), overflow, carpoolOverflowMaxItems)
	for _, item := range overflow {
		require.NotEqual(s.T(), "overflow-000", item.OriginalDeviceID)
	}
}

func (s *IdentityCacheSuite) TestSharedBucketState_CRUD() {
	const accountID int64 = 44
	state := &service.SharedBucketState{
		Bucket:        2,
		LastSeenAt:    time.Now().Unix(),
		LastUserAgent: "ua",
	}

	require.NoError(s.T(), s.cache.SetSharedBucketState(s.ctx, accountID, 2, state))

	got, err := s.cache.GetSharedBucketState(s.ctx, accountID, 2)
	require.NoError(s.T(), err)
	require.NotNil(s.T(), got)
	require.Equal(s.T(), 2, got.Bucket)
	require.Equal(s.T(), "ua", got.LastUserAgent)

	listed, err := s.cache.ListSharedBucketStates(s.ctx, accountID, 4)
	require.NoError(s.T(), err)
	require.Len(s.T(), listed, 1)
	require.Equal(s.T(), 2, listed[0].Bucket)

	require.NoError(s.T(), s.cache.DeleteSharedBucketState(s.ctx, accountID, 2))
	got, err = s.cache.GetSharedBucketState(s.ctx, accountID, 2)
	require.NoError(s.T(), err)
	require.Nil(s.T(), got)
}

func (s *IdentityCacheSuite) TestGetOrAssignSharedBucket_PersistsBindingAndPrunesOnShrink() {
	const accountID int64 = 45

	firstBucket, err := s.cache.GetOrAssignSharedBucket(s.ctx, accountID, "device-a", 4, 3)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 3, firstBucket)

	reusedBucket, err := s.cache.GetOrAssignSharedBucket(s.ctx, accountID, "device-a", 4, 1)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 3, reusedBucket)

	require.NoError(s.T(), s.cache.EnsureSharedBucketTopology(s.ctx, accountID, 2))

	reboundBucket, err := s.cache.GetOrAssignSharedBucket(s.ctx, accountID, "device-a", 2, 1)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 1, reboundBucket)
}

func (s *IdentityCacheSuite) TestDeleteSharedBucketState_DoesNotRemoveDeviceBinding() {
	const accountID int64 = 46

	bucket, err := s.cache.GetOrAssignSharedBucket(s.ctx, accountID, "device-a", 4, 2)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 2, bucket)

	require.NoError(s.T(), s.cache.DeleteSharedBucketState(s.ctx, accountID, 2))

	reusedBucket, err := s.cache.GetOrAssignSharedBucket(s.ctx, accountID, "device-a", 4, 1)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 2, reusedBucket)
}

func TestIdentityCacheSuite(t *testing.T) {
	suite.Run(t, new(IdentityCacheSuite))
}
