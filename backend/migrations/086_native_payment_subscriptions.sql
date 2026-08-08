-- Native subscription products and immutable subscription fulfillment
-- snapshots. Existing balance orders retain their original behavior.

CREATE TABLE IF NOT EXISTS subscription_plans (
    id              BIGSERIAL PRIMARY KEY,
    group_id        BIGINT NOT NULL REFERENCES groups(id) ON DELETE RESTRICT,
    name            VARCHAR(100) NOT NULL CHECK (BTRIM(name) <> ''),
    description     TEXT NOT NULL DEFAULT '',
    price           DECIMAL(20,2) NOT NULL CHECK (price > 0),
    original_price  DECIMAL(20,2) CHECK (original_price IS NULL OR original_price >= 0),
    currency        VARCHAR(3) NOT NULL DEFAULT 'CNY' CHECK (currency ~ '^[A-Z]{3}$'),
    validity_days   INTEGER NOT NULL DEFAULT 30 CHECK (validity_days > 0),
    validity_unit   VARCHAR(10) NOT NULL DEFAULT 'day'
                    CHECK (validity_unit IN ('day', 'month', 'year')),
    features        TEXT NOT NULL DEFAULT '',
    product_name    VARCHAR(100) NOT NULL DEFAULT '',
    for_sale        BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order      INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_subscription_plans_group_id
    ON subscription_plans(group_id);
CREATE INDEX IF NOT EXISTS idx_subscription_plans_sale_sort
    ON subscription_plans(for_sale, sort_order);

ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS order_type VARCHAR(20) NOT NULL DEFAULT 'balance',
    ADD COLUMN IF NOT EXISTS plan_id BIGINT,
    ADD COLUMN IF NOT EXISTS subscription_group_id BIGINT,
    ADD COLUMN IF NOT EXISTS subscription_days INTEGER;

ALTER TABLE payment_orders
    DROP CONSTRAINT IF EXISTS payment_orders_order_type_check;
ALTER TABLE payment_orders
    ADD CONSTRAINT payment_orders_order_type_check
    CHECK (order_type IN ('balance', 'subscription'));

ALTER TABLE payment_orders
    DROP CONSTRAINT IF EXISTS payment_orders_subscription_snapshot_check;
ALTER TABLE payment_orders
    ADD CONSTRAINT payment_orders_subscription_snapshot_check CHECK (
        (order_type = 'balance' AND plan_id IS NULL AND subscription_group_id IS NULL AND subscription_days IS NULL)
        OR
        (order_type = 'subscription' AND plan_id IS NOT NULL AND subscription_group_id IS NOT NULL AND subscription_days > 0)
    );

CREATE INDEX IF NOT EXISTS idx_payment_orders_order_type ON payment_orders(order_type);
CREATE INDEX IF NOT EXISTS idx_payment_orders_plan_id ON payment_orders(plan_id);
