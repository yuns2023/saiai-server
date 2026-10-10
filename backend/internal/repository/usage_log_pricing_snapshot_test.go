//go:build unit

package repository

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogPricingSnapshotInsertPersistsImmutablePrices(t *testing.T) {
	log := &service.UsageLog{RequestID: "price-snapshot", APIKeyID: 2, PricingSnapshot: &service.PricingSnapshot{Version: "v1", BilledModel: "alias-a", ResolvedModel: "model-base", Effective: &service.ModelPricing{CacheReadPricePerToken: 0}, Reference: &service.ModelPricing{CacheReadPricePerToken: 1e-6}}}
	prepared := prepareUsageLogInsert(log)
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Equal(t, "jsonb", usageLogInsertArgTypes[len(usageLogInsertArgTypes)-1])
	body, ok := prepared.args[len(prepared.args)-1].(string)
	require.True(t, ok)
	var snapshot service.PricingSnapshot
	require.NoError(t, json.Unmarshal([]byte(body), &snapshot))
	require.Equal(t, "v1", snapshot.Version)
	require.Zero(t, snapshot.Effective.CacheReadPricePerToken)
	require.Equal(t, 1e-6, snapshot.Reference.CacheReadPricePerToken)
	query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
	require.Contains(t, query, "pricing_snapshot")
	require.Contains(t, query, "::jsonb")
	require.Len(t, args, len(prepared.args))
	require.Equal(t, 1, strings.Count(usageLogSelectColumns, "pricing_snapshot"))
	require.Nil(t, prepareUsageLogInsert(&service.UsageLog{}).args[len(prepared.args)-1])
}

func TestUsageLogPricingSnapshotScannerRestoresSavedReferenceAndEffectivePrices(t *testing.T) {
	body := []byte(`{"version":"historical-v1","billed_model":"alias-a","resolved_model":"model-base","reference":{"cache_read":0.000001},"effective":{"cache_read":0},"reference_total_cost":0.002}`)
	values := []any{int64(1), int64(2), int64(3), int64(4), sql.NullString{}, sql.NullString{}, "alias-a", sql.NullString{}, sql.NullInt64{}, sql.NullInt64{}, 1, 2, 3, 4, 5, 6, 0.1, 0.2, 0.3, 0.35, 0.36, 0.4, 1.0, 0.9, 1.0, sql.NullFloat64{}, int16(service.BillingTypeBalance), int16(service.RequestTypeSync), false, false, sql.NullInt64{}, sql.NullInt64{}, sql.NullString{}, sql.NullString{}, 0, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}, false, time.Now(), 1.0, 1.0, 1.0, body}
	log, err := scanUsageLog(usageLogScannerStub{values: values})
	require.NoError(t, err)
	require.Equal(t, "historical-v1", log.PricingSnapshot.Version)
	require.Equal(t, 1e-6, log.PricingSnapshot.Reference.CacheReadPricePerToken)
	require.Zero(t, log.PricingSnapshot.Effective.CacheReadPricePerToken)
	values[len(values)-1] = []byte(`{"version":`)
	_, err = scanUsageLog(usageLogScannerStub{values: values})
	require.Error(t, err)
}
