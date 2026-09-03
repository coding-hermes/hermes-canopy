-- FTR-05 Phase 5 tenant infrastructure.
-- SPEC-FTR-05 references tenants(tenant_id), but defines no tenants table or
-- tenant field list. Keep the model deliberately minimal: identity, display
-- name, billing tier, and timestamps. relay tenant_id columns remain nullable
-- so self-hosted/unassigned rows stay valid; ordinary PostgreSQL foreign keys
-- permit NULL while enforcing every non-NULL tenant and cascade SaaS cleanup.

CREATE TABLE tenants (
    tenant_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    tier       TEXT NOT NULL DEFAULT 'free'
               CHECK (tier IN ('free', 'pro', 'enterprise')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE relay_instances
    ADD CONSTRAINT relay_instances_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(tenant_id) ON DELETE CASCADE;

ALTER TABLE relay_sessions
    ADD CONSTRAINT relay_sessions_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(tenant_id) ON DELETE CASCADE;
