package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestGetOpsAdvancedSettings_DefaultHidesOpenAITokenStats(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	cfg, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() error = %v", err)
	}
	if cfg.DisplayOpenAITokenStats {
		t.Fatalf("DisplayOpenAITokenStats = true, want false by default")
	}
	if !cfg.DisplayAlertEvents {
		t.Fatalf("DisplayAlertEvents = false, want true by default")
	}
	if repo.setCalls != 1 {
		t.Fatalf("expected defaults to be persisted once, got %d", repo.setCalls)
	}
}

func TestUpdateOpsAdvancedSettings_PersistsOpenAITokenStatsVisibility(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	cfg := defaultOpsAdvancedSettings()
	cfg.DisplayOpenAITokenStats = true
	cfg.DisplayAlertEvents = false

	updated, err := svc.UpdateOpsAdvancedSettings(context.Background(), cfg)
	if err != nil {
		t.Fatalf("UpdateOpsAdvancedSettings() error = %v", err)
	}
	if !updated.DisplayOpenAITokenStats {
		t.Fatalf("DisplayOpenAITokenStats = false, want true")
	}
	if updated.DisplayAlertEvents {
		t.Fatalf("DisplayAlertEvents = true, want false")
	}

	reloaded, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() after update error = %v", err)
	}
	if !reloaded.DisplayOpenAITokenStats {
		t.Fatalf("reloaded DisplayOpenAITokenStats = false, want true")
	}
	if reloaded.DisplayAlertEvents {
		t.Fatalf("reloaded DisplayAlertEvents = true, want false")
	}
}

func TestGetOpsAdvancedSettings_BackfillsNewDisplayFlagsFromDefaults(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	legacyCfg := map[string]any{
		"data_retention": map[string]any{
			"cleanup_enabled":               false,
			"cleanup_schedule":              "0 2 * * *",
			"error_log_retention_days":      30,
			"minute_metrics_retention_days": 30,
			"hourly_metrics_retention_days": 30,
		},
		"aggregation": map[string]any{
			"aggregation_enabled": false,
		},
		"ignore_count_tokens_errors":    true,
		"ignore_context_canceled":       true,
		"ignore_no_available_accounts":  false,
		"ignore_invalid_api_key_errors": false,
		"auto_refresh_enabled":          false,
		"auto_refresh_interval_seconds": 30,
	}
	raw, err := json.Marshal(legacyCfg)
	if err != nil {
		t.Fatalf("marshal legacy config: %v", err)
	}
	repo.values[SettingKeyOpsAdvancedSettings] = string(raw)

	cfg, err := svc.GetOpsAdvancedSettings(context.Background())
	if err != nil {
		t.Fatalf("GetOpsAdvancedSettings() error = %v", err)
	}
	if cfg.DisplayOpenAITokenStats {
		t.Fatalf("DisplayOpenAITokenStats = true, want false default backfill")
	}
	if !cfg.DisplayAlertEvents {
		t.Fatalf("DisplayAlertEvents = false, want true default backfill")
	}
}

func TestFullRequestBodyLoggingLimit_EnabledForMatchingAPIKeyUntilExpiry(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	raw, err := json.Marshal(OpsFullRequestBodyLoggingSettings{
		Enabled:          true,
		APIKeyIDs:        []int64{249, 249, -1},
		MaxBytes:         64 * 1024,
		ExpiresAtRFC3339: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	repo.values[SettingKeyOpsFullRequestBodyLogging] = string(raw)

	limit, ok := svc.FullRequestBodyLoggingLimit(context.Background(), 249)
	if !ok {
		t.Fatalf("FullRequestBodyLoggingLimit ok=false, want true")
	}
	if limit != 64*1024 {
		t.Fatalf("limit=%d, want %d", limit, 64*1024)
	}
	if _, ok := svc.FullRequestBodyLoggingLimit(context.Background(), 250); ok {
		t.Fatalf("non-matching api key unexpectedly enabled")
	}
}

func TestFullRequestBodyLoggingLimit_ExpiredDisablesCapture(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	raw, err := json.Marshal(OpsFullRequestBodyLoggingSettings{
		Enabled:          true,
		APIKeyIDs:        []int64{249},
		MaxBytes:         64 * 1024,
		ExpiresAtRFC3339: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	repo.values[SettingKeyOpsFullRequestBodyLogging] = string(raw)

	if _, ok := svc.FullRequestBodyLoggingLimit(context.Background(), 249); ok {
		t.Fatalf("expired full request body logging unexpectedly enabled")
	}
}

func TestSuccessRequestBodyCaptureLimit_RequiresSuccessFlag(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	raw, err := json.Marshal(OpsFullRequestBodyLoggingSettings{
		Enabled:          true,
		CaptureSuccess:   false,
		APIKeyIDs:        []int64{249},
		MaxBytes:         64 * 1024,
		ExpiresAtRFC3339: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	repo.values[SettingKeyOpsFullRequestBodyLogging] = string(raw)

	if _, ok := svc.SuccessRequestBodyCaptureLimit(context.Background(), 249); ok {
		t.Fatalf("success capture unexpectedly enabled without capture_success")
	}

	raw, err = json.Marshal(OpsFullRequestBodyLoggingSettings{
		Enabled:          true,
		CaptureSuccess:   true,
		CaptureUpstream:  true,
		APIKeyIDs:        []int64{249},
		MaxBytes:         64 * 1024,
		ExpiresAtRFC3339: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	repo.values[SettingKeyOpsFullRequestBodyLogging] = string(raw)
	svc.fullRequestBodyLoggingCache.Store(&opsFullRequestBodyLoggingCacheEntry{})

	limit, ok := svc.SuccessRequestBodyCaptureLimit(context.Background(), 249)
	if !ok {
		t.Fatalf("SuccessRequestBodyCaptureLimit ok=false, want true")
	}
	if limit != 64*1024 {
		t.Fatalf("limit=%d, want %d", limit, 64*1024)
	}
	opts, ok := svc.SuccessRequestBodyCaptureOptions(context.Background(), 249)
	if !ok {
		t.Fatalf("SuccessRequestBodyCaptureOptions ok=false, want true")
	}
	if !opts.CaptureUpstream {
		t.Fatalf("CaptureUpstream=false, want true")
	}
}
