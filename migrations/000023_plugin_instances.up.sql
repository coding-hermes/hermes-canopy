-- 000023_plugin_instances.up.sql

-- Per-tree and per-user configuration for an installed plugin.
-- A single plugin_registry row can be installed into many trees by many users.
CREATE TABLE plugin_instances (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    plugin_id           uuid        NOT NULL REFERENCES plugin_registry(id) ON DELETE CASCADE,
    tree_id             uuid,                           -- NULL = globally available to this user
    profile_id          uuid        NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    instance_name       text        NOT NULL DEFAULT '',-- User-given display name (optional)
    settings            jsonb       NOT NULL DEFAULT '{}',  -- Per-instance settings (API keys, prefs)
    granted_permissions text[]      NOT NULL,           -- Snapshot of permissions user granted (subset of plugin's declared)
    status              text        NOT NULL DEFAULT 'active',  -- 'active' | 'paused' | 'uninstalled'
    last_invoked_at     timestamptz,
    invoke_count        integer     NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    uninstalled_at      timestamptz,
    CONSTRAINT chk_instance_status
        CHECK (status IN ('active', 'paused', 'uninstalled')),
    CONSTRAINT chk_instance_name_length
        CHECK (char_length(instance_name) <= 100)
);

-- At most one install per (plugin, tree, profile) combination
CREATE UNIQUE INDEX idx_plugin_instances_unique_install
    ON plugin_instances(plugin_id, COALESCE(tree_id, '00000000-0000-0000-0000-000000000000'::uuid), profile_id)
    WHERE status != 'uninstalled';

CREATE INDEX idx_plugin_instances_plugin        ON plugin_instances(plugin_id);
CREATE INDEX idx_plugin_instances_tree          ON plugin_instances(tree_id) WHERE tree_id IS NOT NULL;
CREATE INDEX idx_plugin_instances_profile       ON plugin_instances(profile_id);
CREATE INDEX idx_plugin_instances_status        ON plugin_instances(status);
