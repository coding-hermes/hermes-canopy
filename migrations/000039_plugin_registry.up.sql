-- PL-01 Phase 1 registry contract. The tables were introduced by migrations
-- 000022-000024; keep this migration safe for existing databases while
-- applying the PostgreSQL 16 UUID adaptation required by PL-01 Phase 1.
ALTER TABLE plugin_registry
    ALTER COLUMN id SET DEFAULT gen_random_uuid();

ALTER TABLE plugin_instances
    ALTER COLUMN id SET DEFAULT gen_random_uuid();

ALTER TABLE plugin_audit_log
    ALTER COLUMN id SET DEFAULT gen_random_uuid();

-- Reassert the registry invariants in the phase migration. These indexes are
-- idempotent because existing installations already received them in 000022.
CREATE UNIQUE INDEX IF NOT EXISTS idx_plugin_registry_name_active
    ON plugin_registry(name) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_plugin_registry_name ON plugin_registry(name);
CREATE INDEX IF NOT EXISTS idx_plugin_registry_status ON plugin_registry(status);
CREATE INDEX IF NOT EXISTS idx_plugin_registry_author ON plugin_registry(author_profile_id);
CREATE INDEX IF NOT EXISTS idx_plugin_registry_created ON plugin_registry(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_plugin_registry_name_created ON plugin_registry(name, created_at DESC);

