-- 000043_file_metadata.up.sql
-- SPEC-PL-02 §3.1
--
-- One row per unique file content (by SHA-256) per profile.
-- The same bytes uploaded by two profiles = two rows.
-- file_metadata is the canonical "this is what file X looks like" record.

CREATE TABLE file_metadata (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    profile_id          uuid        NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    sha256              text        NOT NULL,            -- hex digest of full file content
    byte_size           bigint      NOT NULL,            -- raw byte count
    mime_type           text        NOT NULL,            -- server-detected MIME (via magic bytes)
    declared_mime       text        NOT NULL DEFAULT '', -- MIME the uploader declared (may differ)
    filename            text        NOT NULL,            -- original filename (preserved exactly)
    extension           text        NOT NULL DEFAULT '', -- lowercase, no dot: 'pdf', 'ts', 'tsx'
    storage_path        text        NOT NULL,            -- relative path in hermes-kb storage root
    storage_kind        text        NOT NULL DEFAULT 'hermes_kb', -- 'hermes_kb' | 'hermes_fs_ref' | 'external'
    source_kind         text        NOT NULL DEFAULT 'upload',  -- 'upload' | 'reference' | 'agent_message' | 'import'
    source_message_id   uuid,                           -- if uploaded via a message attachment
    source_external_url text,                            -- if storage_kind = 'external'
    is_text             boolean     NOT NULL DEFAULT false, -- server classified as text format
    is_binary           boolean     NOT NULL DEFAULT false,
    is_viewable         boolean     NOT NULL DEFAULT true,  -- false = download-only
    viewer_hint         text        NOT NULL DEFAULT '',     -- e.g. 'pdf', 'code', 'image' — server-detected viewer slug
    thumbnail_path      text        NOT NULL DEFAULT '',     -- relative path to generated thumbnail (images only)
    thumbnail_sha256    text        NOT NULL DEFAULT '',
    preview_text        text        NOT NULL DEFAULT '',     -- first 8 KB of text files (server-extracted)
    metadata_json       jsonb       NOT NULL DEFAULT '{}',  -- type-specific (PDF page count, image dims, audio duration, etc.)
    reference_count     integer     NOT NULL DEFAULT 1,     -- number of messages/edges pointing at this row
    last_accessed_at    timestamptz,
    access_count        integer     NOT NULL DEFAULT 0,
    quarantined         boolean     NOT NULL DEFAULT false, -- true if virus scan (future) flagged it
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at          timestamptz,
    CONSTRAINT chk_storage_kind
        CHECK (storage_kind IN ('hermes_kb', 'hermes_fs_ref', 'external')),
    CONSTRAINT chk_source_kind
        CHECK (source_kind IN ('upload', 'reference', 'agent_message', 'import')),
    CONSTRAINT chk_byte_size
        CHECK (byte_size > 0 AND byte_size <= 536870912), -- 500 MB hard limit
    CONSTRAINT chk_sha256
        CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    CONSTRAINT chk_mime_type
        CHECK (char_length(mime_type) BETWEEN 1 AND 200),
    CONSTRAINT chk_filename
        CHECK (char_length(filename) BETWEEN 1 AND 1000)
);

-- One canonical row per (profile, sha256). Different profiles can have the same content.
CREATE UNIQUE INDEX idx_file_metadata_profile_sha
    ON file_metadata(profile_id, sha256)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_file_metadata_sha             ON file_metadata(sha256);
CREATE INDEX idx_file_metadata_profile         ON file_metadata(profile_id);
CREATE INDEX idx_file_metadata_mime            ON file_metadata(mime_type);
CREATE INDEX idx_file_metadata_extension       ON file_metadata(extension);
CREATE INDEX idx_file_metadata_viewer_hint     ON file_metadata(viewer_hint);
CREATE INDEX idx_file_metadata_created         ON file_metadata(created_at DESC);
CREATE INDEX idx_file_metadata_last_accessed   ON file_metadata(last_accessed_at DESC NULLS LAST);
CREATE INDEX idx_file_metadata_source_message  ON file_metadata(source_message_id) WHERE source_message_id IS NOT NULL;
