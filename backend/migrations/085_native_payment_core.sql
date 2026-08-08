-- Native SAIAI payment core. The feature remains disabled until an operator
-- configures an encrypted provider instance and explicitly enables payments.

CREATE TABLE IF NOT EXISTS payment_provider_instances (
    id                  BIGSERIAL PRIMARY KEY,
    provider_key        VARCHAR(30) NOT NULL,
    name                VARCHAR(100) NOT NULL,
    config_encrypted    TEXT NOT NULL,
    supported_types     VARCHAR(200) NOT NULL DEFAULT '',
    balance_credit_rate DECIMAL(20,8) NOT NULL DEFAULT 1 CHECK (balance_credit_rate > 0),
    enabled             BOOLEAN NOT NULL DEFAULT FALSE,
    sort_order          INTEGER NOT NULL DEFAULT 0,
    limits              TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_provider_instances_provider_key
    ON payment_provider_instances(provider_key);
CREATE INDEX IF NOT EXISTS idx_payment_provider_instances_enabled_sort_order
    ON payment_provider_instances(enabled, sort_order);

CREATE TABLE IF NOT EXISTS payment_orders (
    id                          BIGSERIAL PRIMARY KEY,
    user_id                     BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    user_email                  VARCHAR(255) NOT NULL,
    user_name                   VARCHAR(100) NOT NULL DEFAULT '',
    amount                      DECIMAL(20,2) NOT NULL CHECK (amount > 0),
    pay_amount                  DECIMAL(20,2) NOT NULL CHECK (pay_amount > 0),
    currency                    VARCHAR(3) NOT NULL DEFAULT 'CNY' CHECK (currency ~ '^[A-Z]{3}$'),
    balance_credit_rate         DECIMAL(20,8) NOT NULL DEFAULT 1 CHECK (balance_credit_rate > 0),
    fee_rate                    DECIMAL(10,4) NOT NULL DEFAULT 0 CHECK (fee_rate >= 0 AND fee_rate <= 100),
    recharge_code               VARCHAR(32) NOT NULL UNIQUE,
    out_trade_no                VARCHAR(64) NOT NULL UNIQUE,
    payment_type                VARCHAR(30) NOT NULL,
    provider_key                VARCHAR(30) NOT NULL,
    provider_instance_id        BIGINT NOT NULL REFERENCES payment_provider_instances(id) ON DELETE RESTRICT,
    provider_snapshot_encrypted TEXT NOT NULL,
    status                      VARCHAR(30) NOT NULL DEFAULT 'PENDING'
                                CHECK (status IN ('PENDING', 'PAID', 'RECHARGING', 'COMPLETED', 'EXPIRED', 'CANCELLED', 'FAILED', 'REFUND_REQUESTED', 'REFUNDING', 'REFUND_PENDING', 'REFUNDED', 'REFUND_FAILED')),
    payment_trade_no            VARCHAR(128) NOT NULL DEFAULT '',
    pay_url                     TEXT,
    qr_code                     TEXT,
    expires_at                  TIMESTAMPTZ NOT NULL,
    paid_at                     TIMESTAMPTZ,
    completed_at                TIMESTAMPTZ,
    failed_at                   TIMESTAMPTZ,
    failed_reason               TEXT,
    refund_mode                 VARCHAR(20) NOT NULL DEFAULT '' CHECK (refund_mode IN ('', 'automatic', 'manual')),
    refund_amount               DECIMAL(20,2) NOT NULL DEFAULT 0 CHECK (refund_amount >= 0),
    refund_reason               TEXT,
    refund_external_reference   VARCHAR(200),
    refund_requested_by         VARCHAR(100) NOT NULL DEFAULT '',
    refund_requested_at         TIMESTAMPTZ,
    refund_provider_call_started_at TIMESTAMPTZ,
    refunded_at                 TIMESTAMPTZ,
    refund_id                   VARCHAR(200) NOT NULL DEFAULT '',
    refund_entitlement_reversed BOOLEAN NOT NULL DEFAULT FALSE,
    refund_entitlement_snapshot TEXT NOT NULL DEFAULT '',
    refund_force                BOOLEAN NOT NULL DEFAULT FALSE,
    refund_error                TEXT,
    client_ip                   VARCHAR(64) NOT NULL DEFAULT '',
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_orders_user_id ON payment_orders(user_id);
CREATE INDEX IF NOT EXISTS idx_payment_orders_status ON payment_orders(status);
CREATE INDEX IF NOT EXISTS idx_payment_orders_expires_at ON payment_orders(expires_at);
CREATE INDEX IF NOT EXISTS idx_payment_orders_created_at ON payment_orders(created_at);
CREATE INDEX IF NOT EXISTS idx_payment_orders_provider_status
    ON payment_orders(provider_instance_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_orders_provider_trade_unique
    ON payment_orders(provider_key, payment_trade_no)
    WHERE payment_trade_no <> '';

CREATE TABLE IF NOT EXISTS payment_audit_logs (
    id          BIGSERIAL PRIMARY KEY,
    order_id    BIGINT NOT NULL REFERENCES payment_orders(id) ON DELETE CASCADE,
    action      VARCHAR(50) NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    operator    VARCHAR(100) NOT NULL DEFAULT 'system',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_audit_logs_order_created
    ON payment_audit_logs(order_id, created_at);
CREATE INDEX IF NOT EXISTS idx_payment_audit_logs_action ON payment_audit_logs(action);

INSERT INTO settings (key, value, updated_at) VALUES
    ('payment_enabled', 'false', NOW()),
    ('payment_min_amount', '1', NOW()),
    ('payment_max_amount', '1000', NOW()),
    ('payment_order_timeout_minutes', '5', NOW()),
    ('payment_max_pending_orders', '3', NOW()),
    ('payment_recharge_fee_rate', '0', NOW())
ON CONFLICT (key) DO NOTHING;

-- Retire the unused external purchase iframe. Preserve its URL value for
-- forensic/rollback reference, but ensure older frontend bundles hide it
-- during a rolling deployment.
UPDATE settings
SET value = 'false', updated_at = NOW()
WHERE key = 'purchase_subscription_enabled';
