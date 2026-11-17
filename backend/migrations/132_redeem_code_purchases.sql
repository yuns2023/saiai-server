-- User-bought balance redeem codes ("buy a code for someone").
--
-- A signed-in user spends balance on an ordinary unused 'balance' redeem code
-- and hands it to anyone; redeeming it goes through the unchanged /redeem
-- flow. Each purchase is one row here, written in the same transaction as the
-- balance deduction, the code and a 'purchase_debit' history record in
-- redeem_codes. Revoking an unused code expires it, refunds the buyer and
-- writes a 'purchase_refund' record, again in one transaction. redeem_codes
-- has no CHECK on type, so the two record types need no change there.
--
-- request_id is the client's idempotency key: one purchase per (buyer,
-- request_id), so a retried request returns the original purchase instead of
-- charging again. Revoked purchases stay (they count toward the rolling 24h
-- caps and keep the code non-deletable); the foreign keys are RESTRICT so a
-- purchased code or its buyer can never be hard-deleted from under the row.
--
-- The table is new and empty, so its indexes are built in this transaction.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS redeem_code_purchases (
    id             BIGSERIAL PRIMARY KEY,
    buyer_user_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    redeem_code_id BIGINT NOT NULL REFERENCES redeem_codes(id) ON DELETE RESTRICT,
    amount         DECIMAL(20,8) NOT NULL,
    request_id     VARCHAR(64) NOT NULL,
    status         VARCHAR(16) NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at     TIMESTAMPTZ,
    CONSTRAINT redeem_code_purchases_redeem_code_key UNIQUE (redeem_code_id),
    CONSTRAINT redeem_code_purchases_buyer_request_key UNIQUE (buyer_user_id, request_id),
    CONSTRAINT redeem_code_purchases_amount_check CHECK (amount > 0),
    CONSTRAINT redeem_code_purchases_request_id_check CHECK (request_id <> ''),
    CONSTRAINT redeem_code_purchases_status_check CHECK (status IN ('active', 'revoked')),
    CONSTRAINT redeem_code_purchases_revoked_at_check CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS redeem_code_purchases_buyer_created_idx
    ON redeem_code_purchases (buyer_user_id, created_at DESC);
