-- Canonical GitHub/Google login identities and one-time registration handoffs.
-- Provider tokens are never persisted. Existing users are not backfilled or
-- implicitly bound by email.

CREATE TABLE IF NOT EXISTS auth_identities (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider VARCHAR(20) NOT NULL CHECK (provider IN ('github', 'google')),
    subject VARCHAR(255) NOT NULL,
    verified_email VARCHAR(255) NOT NULL,
    verified_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS auth_identities_provider_subject_key
    ON auth_identities(provider, subject);
CREATE UNIQUE INDEX IF NOT EXISTS auth_identities_user_provider_key
    ON auth_identities(user_id, provider);
CREATE INDEX IF NOT EXISTS auth_identities_user_id_idx
    ON auth_identities(user_id);

CREATE TABLE IF NOT EXISTS oauth_registration_sessions (
    id BIGSERIAL PRIMARY KEY,
    token_hash VARCHAR(64) NOT NULL UNIQUE,
    provider VARCHAR(20) NOT NULL CHECK (provider IN ('github', 'google')),
    subject VARCHAR(255) NOT NULL,
    verified_email VARCHAR(255) NOT NULL,
    username VARCHAR(100) NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS oauth_registration_sessions_expires_at_idx
    ON oauth_registration_sessions(expires_at);
CREATE INDEX IF NOT EXISTS oauth_registration_sessions_provider_subject_idx
    ON oauth_registration_sessions(provider, subject);
