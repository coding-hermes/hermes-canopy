CREATE TABLE mls_key_packages (
    id                UUID PRIMARY KEY,
    profile_id        UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    key_package_bytes BYTEA NOT NULL,
    hash_unique       BYTEA NOT NULL UNIQUE,
    ciphersuite       TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ NOT NULL,
    CONSTRAINT ck_mls_key_packages_expiry CHECK (expires_at > created_at)
);

CREATE INDEX idx_mls_key_packages_available ON mls_key_packages(profile_id, expires_at)
    WHERE expires_at > now();
