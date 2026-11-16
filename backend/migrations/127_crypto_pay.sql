-- Self-hosted stablecoin top-up (USDC/USDT on Arbitrum and Solana).
--
-- Every user gets one fixed deposit address per network; orders are only a
-- suggested amount with a countdown. Inflows are recorded per on-chain event
-- in raw integer token units and credited to users.balance exactly once, in
-- one transaction with a row in crypto_pay_credits (unique deposit_id).
--
-- The server never holds private keys: EVM addresses are derived from an
-- account-level xpub, Solana addresses come from an offline-signed pool.
-- The feature is off until an operator imports keys and turns the switches
-- on; this migration only creates empty tables.

CREATE TABLE IF NOT EXISTS crypto_pay_keysets (
    id            BIGSERIAL PRIMARY KEY,
    network       VARCHAR(32) NOT NULL,
    family        VARCHAR(16) NOT NULL CHECK (family IN ('evm', 'solana')),
    version       INTEGER NOT NULL CHECK (version > 0),
    -- EVM: account-level xpub (m/44'/60'/0'); empty for Solana pools.
    xpub          TEXT NOT NULL DEFAULT '',
    -- e.g. m/44'/60'/0'/0/{i} or m/44'/501'/{i}'/0'
    path_template VARCHAR(100) NOT NULL,
    -- sha256 of the xpub or of the signed manifest; prevents double import.
    fingerprint   VARCHAR(64) NOT NULL,
    signer_pubkey VARCHAR(64) NOT NULL DEFAULT '',
    -- EVM allocation high-water mark: the next index to hand out. Only ever
    -- incremented under the row lock, never recomputed from MAX(index).
    next_index    BIGINT NOT NULL DEFAULT 0 CHECK (next_index >= 0 AND next_index <= 2147483648),
    pool_size     INTEGER NOT NULL DEFAULT 0 CHECK (pool_size >= 0),
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_by    VARCHAR(100) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT crypto_pay_keysets_network_version_key UNIQUE (network, version),
    CONSTRAINT crypto_pay_keysets_network_fingerprint_key UNIQUE (network, fingerprint)
);
-- At most one active xpub per EVM network (rotation deactivates the old one).
CREATE UNIQUE INDEX IF NOT EXISTS crypto_pay_keysets_one_active_evm
    ON crypto_pay_keysets(network) WHERE active AND family = 'evm';

CREATE TABLE IF NOT EXISTS crypto_pay_solana_pool_entries (
    id               BIGSERIAL PRIMARY KEY,
    keyset_id        BIGINT NOT NULL REFERENCES crypto_pay_keysets(id) ON DELETE RESTRICT,
    network          VARCHAR(32) NOT NULL,
    derivation_index BIGINT NOT NULL CHECK (derivation_index >= 0),
    address          VARCHAR(64) NOT NULL,
    assigned_user_id BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    assigned_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT crypto_pay_solana_pool_keyset_index_key UNIQUE (keyset_id, derivation_index),
    CONSTRAINT crypto_pay_solana_pool_network_address_key UNIQUE (network, address)
);
CREATE INDEX IF NOT EXISTS idx_crypto_pay_solana_pool_free
    ON crypto_pay_solana_pool_entries(keyset_id, derivation_index) WHERE assigned_user_id IS NULL;

CREATE TABLE IF NOT EXISTS crypto_pay_addresses (
    id               BIGSERIAL PRIMARY KEY,
    user_id          BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    network          VARCHAR(32) NOT NULL,
    address          VARCHAR(64) NOT NULL,
    keyset_id        BIGINT NOT NULL REFERENCES crypto_pay_keysets(id) ON DELETE RESTRICT,
    keyset_version   INTEGER NOT NULL,
    derivation_index BIGINT NOT NULL CHECK (derivation_index >= 0),
    derivation_path  VARCHAR(100) NOT NULL,
    -- EVM: finalized block height when the address was handed out (scan start);
    -- NULL when no scanner was available at allocation time.
    start_block      BIGINT CHECK (start_block IS NULL OR start_block >= 0),
    last_order_at    TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT crypto_pay_addresses_user_network_key UNIQUE (user_id, network),
    CONSTRAINT crypto_pay_addresses_network_address_key UNIQUE (network, address),
    CONSTRAINT crypto_pay_addresses_keyset_index_key UNIQUE (keyset_id, derivation_index)
);
CREATE INDEX IF NOT EXISTS idx_crypto_pay_addresses_network_last_order
    ON crypto_pay_addresses(network, last_order_at);

CREATE TABLE IF NOT EXISTS crypto_pay_deposits (
    id             BIGSERIAL PRIMARY KEY,
    network        VARCHAR(32) NOT NULL,
    tx_hash        VARCHAR(128) NOT NULL,
    -- EVM "log:<logIndex>", Solana "acct:<tokenAccount>"; unique within the tx.
    event_key      VARCHAR(128) NOT NULL,
    address_id     BIGINT NOT NULL REFERENCES crypto_pay_addresses(id) ON DELETE RESTRICT,
    user_id        BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    address        VARCHAR(64) NOT NULL,
    asset_symbol   VARCHAR(16) NOT NULL,
    asset_contract VARCHAR(64) NOT NULL,
    token_account  VARCHAR(64) NOT NULL DEFAULT '',
    from_address   VARCHAR(64) NOT NULL DEFAULT '',
    units          NUMERIC(78,0) NOT NULL CHECK (units > 0),
    decimals       SMALLINT NOT NULL CHECK (decimals >= 0 AND decimals <= 8),
    -- units / 10^decimals, exact (decimals <= 8 fits the balance scale).
    amount         NUMERIC(20,8) NOT NULL CHECK (amount > 0),
    block_number   BIGINT NOT NULL CHECK (block_number >= 0),
    block_hash     VARCHAR(128) NOT NULL DEFAULT '',
    block_time     TIMESTAMPTZ NOT NULL,
    status         VARCHAR(16) NOT NULL DEFAULT 'observed'
                   CHECK (status IN ('observed', 'finalized', 'credited', 'held', 'orphaned')),
    hold_reason    VARCHAR(64) NOT NULL DEFAULT '',
    observed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finalized_at   TIMESTAMPTZ,
    credited_at    TIMESTAMPTZ,
    orphaned_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT crypto_pay_deposits_network_tx_event_key UNIQUE (network, tx_hash, event_key)
);
CREATE INDEX IF NOT EXISTS idx_crypto_pay_deposits_user_created
    ON crypto_pay_deposits(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_crypto_pay_deposits_address_block_time
    ON crypto_pay_deposits(address_id, block_time);
CREATE INDEX IF NOT EXISTS idx_crypto_pay_deposits_status
    ON crypto_pay_deposits(status, id);
-- Credit backlog sweep: finalized rows, least recently touched first.
CREATE INDEX IF NOT EXISTS idx_crypto_pay_deposits_finalized_backlog
    ON crypto_pay_deposits(updated_at, id) WHERE status = 'finalized';
-- Scan activity check: recent inflows per address.
CREATE INDEX IF NOT EXISTS idx_crypto_pay_deposits_address_observed
    ON crypto_pay_deposits(address_id, observed_at);

-- Orders are hints (suggested amount + countdown); they never own funds.
CREATE TABLE IF NOT EXISTS crypto_pay_orders (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    address_id  BIGINT NOT NULL REFERENCES crypto_pay_addresses(id) ON DELETE RESTRICT,
    network     VARCHAR(32) NOT NULL,
    asset       VARCHAR(16) NOT NULL,
    amount      NUMERIC(20,8) NOT NULL CHECK (amount > 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_crypto_pay_orders_user_created
    ON crypto_pay_orders(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS crypto_pay_scan_cursors (
    id              BIGSERIAL PRIMARY KEY,
    scope_key       VARCHAR(200) NOT NULL,
    network         VARCHAR(32) NOT NULL,
    -- EVM: last fully scanned block number; Solana: newest processed signature.
    position        TEXT NOT NULL DEFAULT '',
    -- Compare-and-swap guard: updates must match the version they read.
    version         BIGINT NOT NULL DEFAULT 0,
    last_scanned_at TIMESTAMPTZ,
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT crypto_pay_scan_cursors_scope_key_key UNIQUE (scope_key)
);

-- Balance ledger: exactly one row per credited deposit.
CREATE TABLE IF NOT EXISTS crypto_pay_credits (
    id            BIGSERIAL PRIMARY KEY,
    deposit_id    BIGINT NOT NULL REFERENCES crypto_pay_deposits(id) ON DELETE RESTRICT,
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    amount        NUMERIC(20,8) NOT NULL CHECK (amount > 0),
    balance_after NUMERIC(20,8),
    operator      VARCHAR(100) NOT NULL DEFAULT 'system',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT crypto_pay_credits_deposit_id_key UNIQUE (deposit_id)
);
CREATE INDEX IF NOT EXISTS idx_crypto_pay_credits_user_created
    ON crypto_pay_credits(user_id, created_at DESC);

-- Written in the same transaction as the action it records.
CREATE TABLE IF NOT EXISTS crypto_pay_audit_logs (
    id         BIGSERIAL PRIMARY KEY,
    action     VARCHAR(50) NOT NULL,
    deposit_id BIGINT,
    user_id    BIGINT,
    keyset_id  BIGINT,
    operator   VARCHAR(100) NOT NULL DEFAULT 'system',
    detail     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_crypto_pay_audit_logs_deposit
    ON crypto_pay_audit_logs(deposit_id, created_at);
CREATE INDEX IF NOT EXISTS idx_crypto_pay_audit_logs_action_created
    ON crypto_pay_audit_logs(action, created_at);
