-- 000044_viewer_registry.up.sql
-- SPEC-PL-02 §3.2
--
-- One row per built-in viewer compiled into canopyd.
-- Seeded on first boot from a static Go map (see internal/fileviewer/builtin_viewers.go).
-- Rows are immutable after install; the only mutator is a canopyd version bump that
-- rewrites the table contents in one transaction.

CREATE TABLE viewer_registry (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    viewer_slug         text        NOT NULL,           -- 'pdf' | 'image' | 'code' | 'csv' | 'markdown' | 'json' | 'audio_video'
    version             text        NOT NULL,           -- semver; pinned to canopyd build
    canopyd_version     text        NOT NULL,           -- e.g. '0.4.2'
    display_name        text        NOT NULL,           -- 'PDF' | 'Code Editor' | 'Markdown' | etc.
    description         text        NOT NULL DEFAULT '',
    icon_url            text        NOT NULL DEFAULT '',
    render_type         text        NOT NULL DEFAULT 'fullscreen',
    supports_mime       text[]      NOT NULL,           -- e.g. {'application/pdf'}
    supports_extensions text[]      NOT NULL DEFAULT '{}', -- e.g. {'pdf'}
    supports_viewer_hint text[]     NOT NULL DEFAULT '{}',
    required_capabilities text[]    NOT NULL DEFAULT '{}', -- empty for built-ins (always allowed)
    bundle_path         text        NOT NULL DEFAULT '', -- relative to canopyd web root; '' means compile-time
    bundle_byte_size    integer     NOT NULL DEFAULT 0,
    bundle_sha256       text        NOT NULL DEFAULT '',
    min_canopyd_version text        NOT NULL,           -- lowest canopyd version that supports this viewer
    deprecation_notice  text        NOT NULL DEFAULT '',
    is_active           boolean     NOT NULL DEFAULT true,
    installed_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT chk_render_type_viewer
        CHECK (render_type IN ('fullscreen', 'embed', 'card')),
    CONSTRAINT chk_viewer_slug
        CHECK (viewer_slug ~ '^[a-z][a-z0-9_]*$'),
    CONSTRAINT chk_viewer_version
        CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$'),
    CONSTRAINT uq_viewer_slug_version
        UNIQUE (viewer_slug, version)
);

CREATE UNIQUE INDEX idx_viewer_registry_slug_active
    ON viewer_registry(viewer_slug)
    WHERE is_active = true;

CREATE INDEX idx_viewer_registry_active          ON viewer_registry(is_active);
CREATE INDEX idx_viewer_registry_canopyd_version ON viewer_registry(canopyd_version);
CREATE INDEX idx_viewer_registry_installed       ON viewer_registry(installed_at DESC);
