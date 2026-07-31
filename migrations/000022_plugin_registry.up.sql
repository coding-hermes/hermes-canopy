-- 000022_plugin_registry.up.sql

-- One row per (name, version) tuple. Full history preserved.
-- The "active" version of a plugin is identified by name + status = 'active'.
-- A given (name) can have at most one row with status = 'active' (enforced via partial unique index).
CREATE TABLE plugin_registry (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    name                text        NOT NULL,           -- e.g. "csv-viewer"
    slug                text        NOT NULL,           -- e.g. "csv-viewer" (derived from name; lowercase, hyphenated)
    version             text        NOT NULL,           -- semver: "1.2.3"
    description         text        NOT NULL DEFAULT '',
    author_profile_id   uuid        NOT NULL REFERENCES profiles(id) ON DELETE RESTRICT,
    permissions         text[]      NOT NULL DEFAULT '{}',  -- subset of canonical permission set
    manifest_json       jsonb       NOT NULL,           -- full parsed manifest (including optional fields)
    source_js           text        NOT NULL,           -- the actual plugin JS (raw UTF-8)
    source_sha256       text        NOT NULL,           -- hex digest of source_js for integrity verification
    source_byte_size    integer     NOT NULL,           -- raw byte size; server enforces <1MB
    icon_url            text        NOT NULL DEFAULT '',
    status              text        NOT NULL DEFAULT 'active',  -- 'active' | 'disabled' | 'archived'
    install_count       integer     NOT NULL DEFAULT 0, -- number of plugin_instances referencing this row
    is_root_version     boolean     NOT NULL DEFAULT false,    -- true if this is the first version of this name
    superseded_by_id    uuid        REFERENCES plugin_registry(id) ON DELETE SET NULL,  -- points to newer active version
    previous_version_id uuid        REFERENCES plugin_registry(id) ON DELETE SET NULL,  -- points to older version (for rollback)
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    archived_at         timestamptz,
    CONSTRAINT chk_plugin_status
        CHECK (status IN ('active', 'disabled', 'archived')),
    CONSTRAINT chk_plugin_name
        CHECK (char_length(name) BETWEEN 1 AND 100),
    CONSTRAINT chk_plugin_version
        CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$'),
    CONSTRAINT chk_plugin_slug
        CHECK (slug ~ '^[a-z]([a-z0-9-]*[a-z0-9])?$'),
    CONSTRAINT chk_source_byte_size
        CHECK (source_byte_size > 0 AND source_byte_size <= 1048576),  -- 1 MB hard limit
    CONSTRAINT uq_plugin_name_version
        UNIQUE (name, version)
);

-- At most one active version per plugin name
CREATE UNIQUE INDEX idx_plugin_registry_name_active
    ON plugin_registry(name)
    WHERE status = 'active';

CREATE INDEX idx_plugin_registry_name            ON plugin_registry(name);
CREATE INDEX idx_plugin_registry_status          ON plugin_registry(status);
CREATE INDEX idx_plugin_registry_author          ON plugin_registry(author_profile_id);
CREATE INDEX idx_plugin_registry_created         ON plugin_registry(created_at DESC);
CREATE INDEX idx_plugin_registry_name_created    ON plugin_registry(name, created_at DESC);
