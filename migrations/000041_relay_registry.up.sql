-- FTR-05 Phase 3 (SPEC-FTR-05 §4.2/§4.3) with one foreman deviation:
-- spec §4.2/§4.3 declare tenant_id UUID NOT NULL REFERENCES tenants(tenant_id),
-- but `tenants` is defined nowhere in the spec or in migrations 000001-000040
-- (Canopy MVP has `users`, migration 000008; tenant infrastructure is Phase 5).
-- Mirroring the FK verbatim makes every fresh database un-migratable, so
-- tenant_id is nullable and FK-free until P5 lands. Registry code treats
-- NULL as self-hosted/unassigned. Revisit in P5.

CREATE TABLE relay_instances (
    instance_id           UUID PRIMARY KEY,
    -- tenant_id: see header note (spec deviation, P5)
    tenant_id             UUID,
    public_key            BYTEA NOT NULL,                 -- Ed25519 public key (32 bytes)
    listen_addr           TEXT NOT NULL,
    tier                  TEXT NOT NULL DEFAULT 'free'
                           CHECK (tier IN ('free', 'pro', 'enterprise')),
    enabled               BOOLEAN NOT NULL DEFAULT true,
    load_factor           REAL NOT NULL DEFAULT 0.0,      -- 0.0 (idle) to 1.0 (full)
    region                TEXT,
    connected_at          TIMESTAMPTZ,
    last_heartbeat_at     TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_relay_instances_tenant ON relay_instances(tenant_id);
CREATE INDEX idx_relay_instances_heartbeat ON relay_instances(last_heartbeat_at);

CREATE TABLE relay_sessions (
    session_id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id           UUID NOT NULL REFERENCES relay_instances(instance_id),
    -- tenant_id: see header note (spec deviation, P5)
    tenant_id             UUID,
    remote_addr           TEXT NOT NULL,
    established_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_activity_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    rx_messages           BIGINT NOT NULL DEFAULT 0,
    tx_messages           BIGINT NOT NULL DEFAULT 0,
    rx_bytes              BIGINT NOT NULL DEFAULT 0,
    tx_bytes              BIGINT NOT NULL DEFAULT 0,
    closed_at             TIMESTAMPTZ,
    close_reason          TEXT,                           -- 'graceful', 'timeout', 'error', 'admin'
    closed_by             TEXT                            -- 'system', 'admin', 'peer'
);

CREATE INDEX idx_relay_sessions_instance ON relay_sessions(instance_id);
CREATE INDEX idx_relay_sessions_active ON relay_sessions(closed_at) WHERE closed_at IS NULL;
