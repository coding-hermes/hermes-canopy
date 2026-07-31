-- 000024_plugin_audit_log.up.sql

-- Immutable log of plugin lifecycle events. Used for compliance, debugging,
-- and "what permissions did I grant this plugin" recall.
CREATE TABLE plugin_audit_log (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    plugin_id       uuid        NOT NULL REFERENCES plugin_registry(id) ON DELETE CASCADE,
    instance_id     uuid        REFERENCES plugin_instances(id) ON DELETE SET NULL,
    event_type      text        NOT NULL,  -- 'registered' | 'updated' | 'installed' | 'paused' | 'resumed' | 'uninstalled' | 'rolled_back' | 'permission_changed' | 'hot_reload' | 'sandbox_error'
    actor_profile_id uuid       NOT NULL REFERENCES profiles(id) ON DELETE RESTRICT,
    metadata        jsonb       NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT chk_plugin_audit_event_type
        CHECK (event_type IN (
            'registered', 'updated', 'installed', 'paused', 'resumed',
            'uninstalled', 'rolled_back', 'permission_changed',
            'hot_reload', 'sandbox_error'
        ))
);

CREATE INDEX idx_plugin_audit_plugin  ON plugin_audit_log(plugin_id, created_at DESC);
CREATE INDEX idx_plugin_audit_event   ON plugin_audit_log(event_type);
CREATE INDEX idx_plugin_audit_actor   ON plugin_audit_log(actor_profile_id);
