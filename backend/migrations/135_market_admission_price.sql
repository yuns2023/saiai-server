-- Sharing market (P3a): admission-time price snapshot of a market sale.
--
-- usage_settlement_events.admission_price (JSONB): the price the buyer
-- admission was evaluated with (model, price version, group/model rates,
-- user discount, unit prices). Audit only: the charge follows the ordinary
-- recording price, so a snapshot that differs from the final price is
-- normal. The only writer is the shared_sale insert; every other row stays
-- NULL by construction (no CHECK, so no validation scan).
--
-- ADD COLUMN without a default is a catalog-only change (brief ACCESS
-- EXCLUSIVE lock, no table rewrite).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE usage_settlement_events ADD COLUMN IF NOT EXISTS admission_price JSONB;
