-- 000028_reference_resolution_log.up.sql
-- SPEC-TM-04 §3.3: Analytics and audit log for every reference resolution attempt.

CREATE TABLE reference_resolution_log (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL,
    node_id         uuid        REFERENCES nodes(id) ON DELETE SET NULL,
    profile_id      uuid        REFERENCES profiles(id) ON DELETE SET NULL,
    raw_ref         text        NOT NULL,
    slug            text        NOT NULL,
    topic_id        uuid        REFERENCES topics(id) ON DELETE SET NULL,
    status          text        NOT NULL,    -- 'resolved' | 'not_found' | 'ambiguous' | 'too_many' | 'error'
    error_code      text,                    -- Optional error code when status != 'resolved'
    duration_ms     integer     NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX idx_rrl_tree_created   ON reference_resolution_log(tree_id, created_at DESC);
CREATE INDEX idx_rrl_node           ON reference_resolution_log(node_id);
CREATE INDEX idx_rrl_profile        ON reference_resolution_log(profile_id);
CREATE INDEX idx_rrl_status         ON reference_resolution_log(status);
CREATE INDEX idx_rrl_slug           ON reference_resolution_log(tree_id, slug);
