-- 000046_viewer_config_overrides.up.sql
-- SPEC-PL-02 §3.4
--
-- Per-profile or per-tree overrides of the default viewer dispatch table.
-- Allows admins to reroute .txt to the code viewer, or disable PDF rendering for a tree.

CREATE TABLE viewer_config_overrides (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    profile_id          uuid        REFERENCES profiles(id) ON DELETE CASCADE,
    tree_id             uuid,                           -- nullable; non-null = per-tree
    mime_pattern        text        NOT NULL DEFAULT '', -- glob, e.g. 'text/*', 'application/pdf'
    extension_pattern   text        NOT NULL DEFAULT '', -- glob, e.g. 'txt', 'md'
    override_viewer     text        NOT NULL,           -- target viewer_slug, or '' to disable
    priority            integer     NOT NULL DEFAULT 100, -- lower = higher priority
    created_by          uuid        NOT NULL REFERENCES profiles(id) ON DELETE RESTRICT,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at          timestamptz,
    CONSTRAINT chk_override_viewer
        CHECK (override_viewer = '' OR override_viewer ~ '^[a-z][a-z0-9_]*$'),
    CONSTRAINT chk_priority
        CHECK (priority BETWEEN 1 AND 1000)
);

CREATE INDEX idx_viewer_config_overrides_profile  ON viewer_config_overrides(profile_id);
CREATE INDEX idx_viewer_config_overrides_tree     ON viewer_config_overrides(tree_id) WHERE tree_id IS NOT NULL;
CREATE INDEX idx_viewer_config_overrides_priority ON viewer_config_overrides(priority);
