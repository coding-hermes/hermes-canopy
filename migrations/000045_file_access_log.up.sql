-- 000045_file_access_log.up.sql
-- SPEC-PL-02 §3.3
--
-- Append-only audit trail of every viewer render. Used for:
--   - "Recently viewed" lists
--   - Quota / abuse detection
--   - "Who accessed what" compliance queries
--   - Telemetry on viewer popularity
--
-- Append-only enforcement (SPEC-PL-02 §3.6): BEFORE UPDATE/DELETE triggers
-- reject any mutation; only INSERT is allowed. Kept in this migration so
-- the table is immutable from the moment it exists.

CREATE TABLE file_access_log (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    file_id         uuid        NOT NULL REFERENCES file_metadata(id) ON DELETE CASCADE,
    profile_id      uuid        NOT NULL REFERENCES profiles(id) ON DELETE RESTRICT,
    tree_id         uuid,                           -- tree where the view occurred (nullable)
    node_id         uuid,                           -- node where the view was triggered (nullable)
    viewer_slug     text        NOT NULL,           -- which viewer rendered
    action          text        NOT NULL,           -- 'open' | 'download' | 'thumbnail_fetch' | 'preview_text' | 'stream_start' | 'stream_end' | 'error'
    duration_ms     integer,                        -- for stream_end; how long the view lasted
    byte_offset     bigint,                         -- for streaming content; current byte position
    client_info     jsonb       NOT NULL DEFAULT '{}', -- user agent, viewport size, etc.
    error_code      text,                           -- if action = 'error'
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT chk_action
        CHECK (action IN ('open', 'download', 'thumbnail_fetch', 'preview_text', 'stream_start', 'stream_end', 'error')),
    CONSTRAINT chk_viewer_slug_log
        CHECK (viewer_slug ~ '^[a-z][a-z0-9_]*$')
);

-- Append-only enforcement (SPEC-PL-02 §3.6): reject UPDATE and DELETE; only INSERT.
CREATE OR REPLACE FUNCTION reject_file_access_log_mutation()
RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'file_access_log is append-only; UPDATE/DELETE forbidden';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_file_access_log_no_update
    BEFORE UPDATE ON file_access_log
    FOR EACH ROW EXECUTE FUNCTION reject_file_access_log_mutation();

CREATE TRIGGER trg_file_access_log_no_delete
    BEFORE DELETE ON file_access_log
    FOR EACH ROW EXECUTE FUNCTION reject_file_access_log_mutation();

CREATE INDEX idx_file_access_log_file          ON file_access_log(file_id, created_at DESC);
CREATE INDEX idx_file_access_log_profile       ON file_access_log(profile_id, created_at DESC);
CREATE INDEX idx_file_access_log_viewer        ON file_access_log(viewer_slug, created_at DESC);
CREATE INDEX idx_file_access_log_action        ON file_access_log(action);
CREATE INDEX idx_file_access_log_created       ON file_access_log(created_at DESC);
CREATE INDEX idx_file_access_log_tree          ON file_access_log(tree_id) WHERE tree_id IS NOT NULL;
