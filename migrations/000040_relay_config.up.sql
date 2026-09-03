CREATE TABLE relay_config (
    instance_id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_mode       TEXT NOT NULL DEFAULT 'self_hosted'
                          CHECK (deployment_mode IN ('self_hosted', 'saas', 'air_gapped')),
    heartbeat_secs        INTEGER NOT NULL DEFAULT 30,
    drain_timeout_secs    INTEGER NOT NULL DEFAULT 30,
    tls_enabled           BOOLEAN NOT NULL DEFAULT false,
    tls_cert_file         TEXT,
    tls_key_file          TEXT,
    tls_ca_file           TEXT,
    tls_mutual            BOOLEAN NOT NULL DEFAULT false,
    hmac_key_rotated_at   TIMESTAMPTZ,
    hmac_key_id           INTEGER NOT NULL DEFAULT 0,
    enabled               BOOLEAN NOT NULL DEFAULT false
);
