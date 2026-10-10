-- NULL preserves historical usage; new token-billed rows retain their prices.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS pricing_snapshot JSONB;
